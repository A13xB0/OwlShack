// Package repeater drives remote repeater nodes over the mesh; it does not emulate one.
package repeater

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/store"
)

const (
	reqTypeGetStatus        = 0x01
	reqTypeKeepAlive        = 0x02 // rooms: resume the post push stream
	reqTypeGetTelemetryData = 0x03
	reqTypeGetAvgMinMax     = 0x04 // sensors only
	reqTypeGetAccessList    = 0x05
	reqTypeGetNeighbors     = 0x06
	reqTypeGetOwnerInfo     = 0x07
	txtTypeCliData          = 1
	cliPrefixLen            = 3
	respServerLoginOK       = 0 // login reply byte 4
)

// isLoginReply: byte 4 alone also matches a status body with a zero batt low byte, so check byte 5's always-zero legacy field too.
func isLoginReply(data []byte) bool {
	return len(data) >= 13 && data[4] == respServerLoginOK && data[5] == 0
}

type Session struct {
	PubKeyHex string `json:"pubkeyHex"`
	IsAdmin   bool   `json:"isAdmin"`
	// Permissions is login reply byte 7: low 2 bits role, and on sensors bits 6-7 the alert subscription.
	Permissions  int       `json:"permissions"`
	Role         string    `json:"role,omitempty"` // room sessions: "admin" | "read-write" | "read-only"
	IsRoom       bool      `json:"isRoom,omitempty"`
	LoggedInAt   time.Time `json:"loggedInAt"`
	sharedSecret []byte
	localPubKey  [32]byte
}

type pendingRequest struct {
	ch      chan []byte
	created time.Time

	// Set for sessionless requests so the response can be matched and decrypted without a session.
	sharedSecret   []byte
	peerPubKeyByte byte
	peerPubKey     [32]byte
}

type pendingLogin struct {
	ch             chan []byte
	created        time.Time
	sharedSecret   []byte
	peerPubKeyByte byte
	peerPubKey     [32]byte
}

// airtimeEstimator is the one method the reply timeout needs off the modem, so it can be tested
// without a radio. modem.StatsProvider satisfies it.
type airtimeEstimator interface {
	EstAirtimeMs(packetLen int) uint32
}

type Client struct {
	node        *node.Node
	store       *store.Store
	companionID int64 // owner of the contact rows that persist learned routes
	log         *slog.Logger
	stats       airtimeEstimator
	// ownHashSize is the companion's bytes per hop, for a node that is not a contact; required.
	ownHashSize func() uint8

	mu       sync.Mutex
	sessions map[string]*Session

	pendingMu sync.Mutex
	pending   map[uint32]*pendingRequest

	loginMu       sync.Mutex
	pendingLogins []*pendingLogin

	cliMu      sync.Mutex
	cliPending map[string]chan string

	tsMu   sync.Mutex
	lastTS uint32
}

// UniqueTimestamp mirrors the firmware's getCurrentTimeUnique(): a remote node drops a timestamp <= the last one it saw.
func (rm *Client) UniqueTimestamp() uint32 {
	rm.tsMu.Lock()
	defer rm.tsMu.Unlock()
	ts := uint32(time.Now().Unix())
	if ts <= rm.lastTS {
		ts = rm.lastTS + 1
	}
	rm.lastTS = ts
	return ts
}

func NewClient(n *node.Node, st *store.Store, companionID int64, log *slog.Logger, stats airtimeEstimator, ownHashSize func() uint8) *Client {
	return &Client{
		ownHashSize: ownHashSize,
		node:        n,
		store:       st,
		companionID: companionID,
		log:         log,
		stats:       stats,
		sessions:    make(map[string]*Session),
		pending:     make(map[uint32]*pendingRequest),
		cliPending:  make(map[string]chan string),
	}
}

func (rm *Client) persistOutPath(pubkey []byte, path []byte, hashSize uint8) {
	rm.store.WriteAsync(func() {
		if err := rm.store.Contacts.UpdateOutPath(context.Background(), rm.companionID, pubkey, path, hashSize); err != nil {
			rm.log.Error("failed to persist out_path", "error", err)
		}
	})
}

// learnedRoute is the peer's learned send-path; hydratePeerTables seeds it from the contact row at start, so nil means none known.
func learnedRoute(peer *node.Peer) (path []byte, hashSize uint8) {
	if peer == nil || peer.OutPath == nil {
		return nil, 0
	}
	return peer.OutPath, max(peer.OutPathHashSize, 1)
}

// bytesPerHop is what requests to a peer go out at: its contact's setting, else the companion's own.
func (rm *Client) bytesPerHop(pubkey []byte) uint8 {
	if ct, err := rm.store.Contacts.Get(context.Background(), rm.companionID, pubkey); err == nil && ct != nil {
		return ct.PathHashSize
	}
	return rm.ownHashSize()
}

// routeForPeer floods only on nil; a flood or 0-hop send carries bytesPerHop, since the far end replies at the request's size.
func routeForPeer(path []byte, routeHashSize, bytesPerHop uint8) (routeType byte, pathLen uint8) {
	if len(path) == 0 {
		routeType = meshcore.RouteTypeDirect
		if path == nil {
			routeType = meshcore.RouteTypeFlood
		}
		return routeType, (max(bytesPerHop, 1) - 1) << 6
	}
	hs := max(routeHashSize, 1)
	return meshcore.RouteTypeDirect, (hs-1)<<6 | uint8(len(path)/int(hs))
}

func (rm *Client) Session(pubkeyHex string) *Session {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.sessions[pubkeyHex]
}

func (rm *Client) Logout(pubkeyHex string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.sessions, pubkeyHex)
}

// sendBinaryRequest prepends the tag, placing body at offset 4, and returns the response without its tag.
func (rm *Client) sendBinaryRequest(pubkeyHex string, body []byte, timeout time.Duration, label string) ([]byte, error) {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid pubkey: %w", err)
	}

	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return nil, fmt.Errorf("peer not found in peer table")
	}

	rm.mu.Lock()
	sess := rm.sessions[pubkeyHex]
	rm.mu.Unlock()
	if sess == nil || sess.sharedSecret == nil {
		return nil, fmt.Errorf("not logged in to this repeater")
	}

	// The session decrypts the response, so the pending entry needn't carry the secret.
	return rm.roundtripRequest(peerIdentity.PublicKey(), peer, sess.sharedSecret, sess.localPubKey[0], body, timeout, label, false)
}

// replyTimeout sizes the wait on a repeater's reply from airtime, as the firmware sizes its ACK
// waits (MyMesh.cpp:851-858), treating the caller's value as a floor. The reply sets the pace, not
// the request: a telemetry body runs to a full packet, and over two hops that alone outruns the
// flat 10s every command used to get.
func (rm *Client) replyTimeout(reqLen int, path []byte, hashSize uint8, floor time.Duration) time.Duration {
	if rm.stats == nil {
		return floor
	}
	airtime := rm.stats.EstAirtimeMs(max(reqLen, meshcore.MaxPacketPayload))
	if airtime == 0 {
		return floor // radio params unknown; a guess here would be worse than the caller's value
	}
	if path == nil {
		return max(node.CalcFloodTimeout(airtime), floor)
	}
	hops := len(path) / int(max(hashSize, 1))
	return max(node.CalcDirectTimeout(airtime, uint8(min(hops, 255))), floor)
}

// routedPacket builds the length byte and the hops from one route, so the receiver never reads payload as path; it returns the route to size the reply wait.
func (rm *Client) routedPacket(peer *node.Peer, payloadType byte, payload []byte) (*meshcore.Packet, []byte, uint8) {
	outPath, hashSize := learnedRoute(peer)
	pub := peer.Identity.PublicKey()
	routeType, pathLen := routeForPeer(outPath, hashSize, rm.bytesPerHop(pub[:]))
	return &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, payloadType, 0),
		PathLength: pathLen,
		Path:       outPath,
		Payload:    payload,
	}, outPath, hashSize
}

// roundtripRequest awaits the tagged response; storeSecret puts the secret on the pending entry for sessionless matching.
func (rm *Client) roundtripRequest(peerPub [32]byte, peer *node.Peer, sharedSecret []byte, localPubByte byte, body []byte, timeout time.Duration, label string, storeSecret bool) ([]byte, error) {
	tag := rm.UniqueTimestamp()

	plaintext := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(plaintext[:4], tag)
	copy(plaintext[4:], body)

	encrypted, err := meshcore.EncryptThenMAC(sharedSecret, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting %s req: %w", label, err)
	}

	var mac [2]byte
	copy(mac[:], encrypted[:2])

	req := &meshcore.Request{
		Destination:      peerPub[0],
		Source:           localPubByte,
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}

	reqBytes, err := req.ToBytes()
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	resultCh := make(chan []byte, 1)
	pr := &pendingRequest{ch: resultCh, created: time.Now()}
	if storeSecret {
		pr.sharedSecret = sharedSecret
		pr.peerPubKeyByte = peerPub[0]
		pr.peerPubKey = peerPub
	}
	rm.pendingMu.Lock()
	rm.pending[tag] = pr
	rm.pendingMu.Unlock()

	defer func() {
		rm.pendingMu.Lock()
		delete(rm.pending, tag)
		rm.pendingMu.Unlock()
	}()

	pkt, outPath, hashSize := rm.routedPacket(peer, meshcore.PayloadTypeReq, reqBytes)

	if err := rm.node.SendPacket(pkt); err != nil {
		return nil, fmt.Errorf("sending %s req: %w", label, err)
	}

	wait := rm.replyTimeout(len(reqBytes), outPath, hashSize, timeout)
	rm.log.Debug(label+" req sent", "peer", fmt.Sprintf("%x", peerPub[:6]), "tag", fmt.Sprintf("%08x", tag), "wait", wait)

	select {
	case data := <-resultCh:
		return data, nil
	case <-time.After(wait):
		return nil, fmt.Errorf("%s request timed out after %s", label, wait)
	}
}
