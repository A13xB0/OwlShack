package repeater

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/telemetry"
)

func (rm *Client) SendStatusReq(pubkeyHex string, timeout time.Duration) (*Status, error) {
	body := make([]byte, 5)
	body[0] = reqTypeGetStatus
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "status")
	if err != nil {
		return nil, err
	}
	return parseRepeaterStatus(data)
}

// SendRoomKeepAlive is fire-and-forget: the firmware honours only a direct keep-alive and streams posts over the DM path.
func (rm *Client) SendRoomKeepAlive(pubkeyHex string, since uint32) error {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}
	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("invalid pubkey: %w", err)
	}
	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return fmt.Errorf("peer not found in peer table")
	}
	rm.mu.Lock()
	sess := rm.sessions[pubkeyHex]
	rm.mu.Unlock()
	if sess == nil || sess.sharedSecret == nil {
		return fmt.Errorf("not logged in to this room")
	}
	if outPath, _ := learnedRoute(peer); outPath == nil {
		return fmt.Errorf("no direct route to the room yet — it ignores flooded keep-alives; log in (flood) to learn one")
	}

	// [tag:4][0x02][since:4] — exactly the 9 bytes the room hashes for its ACK.
	plaintext := make([]byte, 9)
	binary.LittleEndian.PutUint32(plaintext[:4], rm.UniqueTimestamp())
	plaintext[4] = reqTypeKeepAlive
	binary.LittleEndian.PutUint32(plaintext[5:9], since)

	encrypted, err := meshcore.EncryptThenMAC(sess.sharedSecret, plaintext)
	if err != nil {
		return fmt.Errorf("encrypting keep-alive: %w", err)
	}
	var mac [2]byte
	copy(mac[:], encrypted[:2])
	peerPub := peerIdentity.PublicKey()
	reqBytes, err := (&meshcore.Request{
		Destination:      peerPub[0],
		Source:           sess.localPubKey[0],
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}).ToBytes()
	if err != nil {
		return fmt.Errorf("encoding keep-alive: %w", err)
	}
	pkt, _, _ := rm.routedPacket(peer, meshcore.PayloadTypeReq, reqBytes)
	return rm.node.SendPacket(pkt)
}

// SendRoomStatusReq is SendStatusReq for a room server, whose ServerStats trailer differs.
func (rm *Client) SendRoomStatusReq(pubkeyHex string, timeout time.Duration) (*Status, error) {
	body := make([]byte, 5)
	body[0] = reqTypeGetStatus
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "room status")
	if err != nil {
		return nil, err
	}
	return parseRoomStatus(data)
}

func (rm *Client) SendNeighborsReq(pubkeyHex string, count uint8, offset uint16, timeout time.Duration) (*Neighbors, error) {
	// payload: type(1) request_version(1) count(1) offset(2) order_by(1) prefix_len(1) random(4)
	body := make([]byte, 11)
	body[0] = reqTypeGetNeighbors
	body[1] = 0 // request version
	body[2] = count
	binary.LittleEndian.PutUint16(body[3:5], offset)
	body[5] = 2 // order_by: strongest_to_weakest
	body[6] = 6 // 6-byte pubkey prefix (matches CLI `neighbors` output)
	if _, err := rand.Read(body[7:11]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "neighbors")
	if err != nil {
		return nil, err
	}
	return parseRepeaterNeighbors(data, 6)
}

func (rm *Client) SendOwnerInfoReq(pubkeyHex string, timeout time.Duration) (*OwnerInfo, error) {
	body := make([]byte, 5)
	body[0] = reqTypeGetOwnerInfo
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "owner")
	if err != nil {
		return nil, err
	}
	return parseRepeaterOwnerInfo(data), nil
}

func (rm *Client) SendAccessListReq(pubkeyHex string, timeout time.Duration) (*AccessList, error) {
	// payload: type(1) reserved(2) reserved(2) random(4)
	body := make([]byte, 9)
	body[0] = reqTypeGetAccessList
	if _, err := rand.Read(body[5:9]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "access list")
	if err != nil {
		return nil, err
	}
	return parseRepeaterAccessList(data), nil
}

// SetAccessPerm needs an admin session; perms 0 revokes and accepts a prefix, granting needs the full key.
func (rm *Client) SetAccessPerm(pubkeyHex, targetPubkeyHex string, perms uint8, timeout time.Duration) error {
	cmd := fmt.Sprintf("setperm %s %d", targetPubkeyHex, perms)
	resp, err := rm.SendCLI(pubkeyHex, cmd, timeout)
	if err != nil {
		return err
	}
	resp = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(resp), ">"))
	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("setperm rejected: %s", resp)
	}
	return nil
}

// SendSeriesReq bounds are seconds before the sensor's now, start being the older edge; needs read-only or above.
func (rm *Client) SendSeriesReq(pubkeyHex string, startSecsAgo, endSecsAgo uint32, timeout time.Duration) (*telemetry.Series, error) {
	// payload: type(1) start(4) end(4) reserved(2)
	body := make([]byte, 11)
	body[0] = reqTypeGetAvgMinMax
	binary.LittleEndian.PutUint32(body[1:5], startSecsAgo)
	binary.LittleEndian.PutUint32(body[5:9], endSecsAgo)
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "series")
	if err != nil {
		return nil, err
	}
	return telemetry.ParseSeries(data)
}

// telemetryReqBody: type(1) mask(1) reserved(3) random(4); mask 0x00 asks for all and the firmware filters by ACL.
func telemetryReqBody() ([]byte, error) {
	body := make([]byte, 9)
	body[0] = reqTypeGetTelemetryData
	if _, err := rand.Read(body[5:9]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	return body, nil
}

func (rm *Client) SendTelemetryReq(pubkeyHex string, timeout time.Duration) (*telemetry.Telemetry, error) {
	body, err := telemetryReqBody()
	if err != nil {
		return nil, err
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "telemetry")
	if err != nil {
		return nil, err
	}
	return telemetry.Parse(data)
}

// SendContactTelemetryReq needs no login: it encrypts with the ECDH secret shared with the contact.
func (rm *Client) SendContactTelemetryReq(pubkeyHex string, timeout time.Duration) (*telemetry.Telemetry, error) {
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

	self := rm.node.Identity()
	selfSeed := self.Seed()
	sharedSecret, err := meshcore.DeriveSharedSecret(selfSeed[:], peerIdentity.PublicKeyBytes())
	if err != nil {
		return nil, fmt.Errorf("deriving shared secret: %w", err)
	}

	body, err := telemetryReqBody()
	if err != nil {
		return nil, err
	}
	data, err := rm.roundtripRequest(peerIdentity.PublicKey(), peer, sharedSecret, self.PublicKey()[0], body, timeout, "contact-telemetry", true)
	if err != nil {
		return nil, err
	}
	return telemetry.Parse(data)
}

func (rm *Client) SendCLI(pubkeyHex, command string, timeout time.Duration) (string, error) {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return "", fmt.Errorf("invalid pubkey: %w", err)
	}

	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return "", fmt.Errorf("peer not found in peer table")
	}

	rm.mu.Lock()
	sess := rm.sessions[pubkeyHex]
	rm.mu.Unlock()
	if sess == nil || sess.sharedSecret == nil {
		return "", fmt.Errorf("not logged in to this repeater")
	}
	if !sess.IsAdmin {
		return "", fmt.Errorf("CLI requires an admin session; the node ignores commands from other roles")
	}

	resultCh := make(chan string, 1)
	var prefix string
	rm.cliMu.Lock()
	// The prefix is one hex byte, so a full map would spin the random search below forever holding cliMu.
	if len(rm.cliPending) >= 256 {
		rm.cliMu.Unlock()
		return "", fmt.Errorf("too many CLI commands in flight")
	}
	for {
		var b [1]byte
		rand.Read(b[:])
		prefix = fmt.Sprintf("%02X", b[0])
		if _, taken := rm.cliPending[prefix]; !taken {
			break
		}
	}
	rm.cliPending[prefix] = resultCh
	rm.cliMu.Unlock()
	framedCommand := prefix + "|" + command

	defer func() {
		rm.cliMu.Lock()
		delete(rm.cliPending, prefix)
		rm.cliMu.Unlock()
	}()

	plaintext := meshcore.BuildTextPlaintext(time.Unix(int64(rm.UniqueTimestamp()), 0), txtTypeCliData<<2, []byte(framedCommand))

	encrypted, err := meshcore.EncryptThenMAC(sess.sharedSecret, plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypting CLI: %w", err)
	}

	var mac [2]byte
	copy(mac[:], encrypted[:2])

	txtMsg := &meshcore.TextMessage{
		Destination:      peerIdentity.PublicKey()[0],
		Source:           sess.localPubKey[0],
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}

	msgBytes, err := txtMsg.ToBytes()
	if err != nil {
		return "", fmt.Errorf("encoding text message: %w", err)
	}

	pkt, outPath, hashSize := rm.routedPacket(peer, meshcore.PayloadTypeTxtMsg, msgBytes)

	if err := rm.node.SendPacket(pkt); err != nil {
		return "", fmt.Errorf("sending CLI: %w", err)
	}

	wait := rm.replyTimeout(len(msgBytes), outPath, hashSize, timeout)
	rm.log.Debug("CLI sent", "peer", pubkeyHex[:12], "prefix", prefix, "command", command, "wait", wait)

	select {
	case response := <-resultCh:
		return response, nil
	case <-time.After(wait):
		return "", fmt.Errorf("CLI command timed out after %s", wait)
	}
}
