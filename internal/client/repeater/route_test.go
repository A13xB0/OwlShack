package repeater

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/store"
)

type silentRadio struct{}

func (silentRadio) SendData([]byte) error                               { return nil }
func (silentRadio) SetDataHandler(func(*meshcore.Packet))               {}
func (silentRadio) SetRawDataHandler(func([]byte, float32, int8, bool)) {}
func (silentRadio) AddOutboundHandler(func([]byte))                     {}
func (silentRadio) Close() error                                        { return nil }
func (silentRadio) Enqueue([]byte, uint8, time.Duration) bool           { return true }
func (silentRadio) TxQueueLen() int                                     { return 0 }

// routeTestClient is a client whose node knows one repeater, with a contact row to persist its route to.
func routeTestClient(t *testing.T) (*Client, int64, []byte) {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	comp := &store.Companion{Name: "test"}
	if err := st.Companions.Create(t.Context(), comp); err != nil {
		t.Fatalf("Companions.Create: %v", err)
	}

	peer := meshcore.NewLocalIdentityFromSeed([32]byte{0x8d})
	pubkey := peer.PublicKeyBytes()
	if err := st.Contacts.Add(t.Context(), comp.ID, pubkey, "JKSparrOwl", "REPEATER"); err != nil {
		t.Fatalf("Contacts.Add: %v", err)
	}

	n := node.New(meshcore.NewLocalIdentityFromSeed([32]byte{1}), silentRadio{})
	t.Cleanup(n.Stop)
	n.Peers().Insert(&node.Peer{Identity: peer.Identity, Name: "JKSparrOwl"})
	own := func() uint8 { return 2 }
	return NewClient(n, st, comp.ID, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, own), comp.ID, pubkey
}

func pubkeyArray(b []byte) [meshcore.PubKeySize]byte {
	var k [meshcore.PubKeySize]byte
	copy(k[:], b)
	return k
}

// A direct neighbour is an empty-but-not-nil path; collapsing the two floods at the one peer that needs no path.
func TestLearnedRoute_ZeroHopIsARouteNotAnAbsence(t *testing.T) {
	t.Parallel()

	if path, _ := learnedRoute(nil); path != nil {
		t.Errorf("unknown peer: got %x, want nil (flood)", path)
	}
	path, hs := learnedRoute(&node.Peer{OutPath: []byte{}})
	if path == nil || len(path) != 0 || hs != 1 {
		t.Fatalf("0-hop: got %x/%d, want empty non-nil at hash size 1", path, hs)
	}
	if rt, pathLen := routeForPeer(path, hs, 1); rt != meshcore.RouteTypeDirect || pathLen != 0 {
		t.Errorf("routeType/pathLen = 0x%02x/%d, want direct/0", rt, pathLen)
	}
}

// The badge shows the route a request takes, Set and Reset are on disk when they return, and bytes per hop rides every send.
func TestPeerPath_BadgeIsTheRouteSent(t *testing.T) {
	t.Parallel()

	rm, companionID, pubkey := routeTestClient(t)
	pkHex := hex.EncodeToString(pubkey)
	key := pubkeyArray(pubkey)
	row := func() *store.Contact {
		ct, err := rm.store.Contacts.Get(t.Context(), companionID, pubkey)
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	sent := func() *meshcore.Packet {
		pkt, _, _ := rm.routedPacket(rm.node.Peers().Lookup(key), meshcore.PayloadTypeReq, nil)
		return pkt
	}

	for _, tt := range []struct {
		name    string
		path    []byte
		hs      uint8
		route   byte
		pathLen uint8
		hasPath bool
		direct  bool
		hops    int
	}{
		{"flood at 2 bytes", nil, 2, meshcore.RouteTypeFlood, 0x40, false, false, 0},
		{"direct at 3 bytes", []byte{}, 3, meshcore.RouteTypeDirect, 0x80, true, true, 0},
		{"two 2-byte hops", []byte{0xaa, 0xbb, 0xcc, 0xdd}, 2, meshcore.RouteTypeDirect, 0x42, true, false, 2},
	} {
		if err := rm.SetPeerPath(pkHex, tt.path, tt.hs); err != nil {
			t.Fatalf("%s: SetPeerPath: %v", tt.name, err)
		}
		info, err := rm.GetPeerPath(pkHex)
		if err != nil {
			t.Fatal(err)
		}
		if info.HasPath != tt.hasPath || info.DirectNeighbor != tt.direct || info.Hops != tt.hops || info.BytesPerHop != int(tt.hs) {
			t.Errorf("%s: badge %+v", tt.name, info)
		}
		if pkt := sent(); pkt.RouteType() != tt.route || pkt.PathLength != tt.pathLen {
			t.Errorf("%s: sent as route 0x%02x length byte 0x%02x, want 0x%02x 0x%02x", tt.name, pkt.RouteType(), pkt.PathLength, tt.route, tt.pathLen)
		}
		if ct := row(); (ct.OutPath == nil) != (tt.path == nil) || !bytes.Equal(ct.OutPath, tt.path) || ct.PathHashSize != tt.hs {
			t.Errorf("%s: contact row %x at %d bytes per hop, want %x at %d saved for the next start", tt.name, ct.OutPath, ct.PathHashSize, tt.path, tt.hs)
		}
	}

	if err := rm.ResetPeerPath(pkHex); err != nil {
		t.Fatal(err)
	}
	if info, _ := rm.GetPeerPath(pkHex); info.HasPath || info.BytesPerHop != 2 {
		t.Errorf("after reset the badge says %+v, want flood at the 2 bytes per hop still set", info)
	}
	if pkt := sent(); pkt.RouteType() != meshcore.RouteTypeFlood || pkt.PathLength != 0x40 {
		t.Errorf("after reset sent as 0x%02x/0x%02x, want a flood at 2 bytes per hop", pkt.RouteType(), pkt.PathLength)
	}
	if ct := row(); ct.OutPath != nil || ct.PathHashSize != 2 {
		t.Errorf("after reset the contact row holds %x at %d bytes per hop, want no route at 2", ct.OutPath, ct.PathHashSize)
	}
}

// A length byte that does not describe the hops makes the receiver read payload as path, a send that logs well-formed.
func TestRoutedPacket_LengthAlwaysDescribesTheBytesItCarries(t *testing.T) {
	t.Parallel()
	rm, _, _ := routeTestClient(t)

	for _, tt := range []struct {
		name string
		peer *node.Peer
	}{
		{"no route floods", &node.Peer{}},
		{"three 1-byte hops", &node.Peer{OutPath: []byte{0xe6, 0x07, 0x1a}, OutPathHashSize: 1}},
		{"two 2-byte hops", &node.Peer{OutPath: []byte{0xe6, 0x07, 0x1a, 0x45}, OutPathHashSize: 2}},
		{"zero-hop", &node.Peer{OutPath: []byte{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pkt, outPath, _ := rm.routedPacket(tt.peer, meshcore.PayloadTypeReq, []byte{1, 2, 3, 4})

			hs := int((pkt.PathLength>>6)&3) + 1
			if got, want := int(pkt.PathLength&63)*hs, len(pkt.Path); got != want {
				t.Errorf("PathLength describes %d bytes, Path carries %d", got, want)
			}
			if !bytes.Equal(pkt.Path, outPath) {
				t.Errorf("Path %x is not the route that sized it (%x)", pkt.Path, outPath)
			}
		})
	}
}

// A route survives a restart, so a login that times out on a stale one must drop it or the peer is unreachable for good.
func TestLoginTimeout_ClearsTheRouteItFailedOn(t *testing.T) {
	t.Parallel()

	rm, companionID, pubkey := routeTestClient(t)
	pkHex := hex.EncodeToString(pubkey)
	if err := rm.SetPeerPath(pkHex, []byte{0xbc, 0x8d}, 1); err != nil {
		t.Fatal(err)
	}
	if p := rm.node.Peers().Lookup(pubkeyArray(pubkey)); !bytes.Equal(p.OutPath, []byte{0xbc, 0x8d}) {
		t.Fatalf("precondition: route %x, want bc8d set before the login", p.OutPath)
	}

	if _, err := rm.SendLogin(pkHex, "password", 20*time.Millisecond); err == nil {
		t.Fatal("login answered with nothing on the air")
	}

	if p := rm.node.Peers().Lookup(pubkeyArray(pubkey)); p.OutPath != nil {
		t.Errorf("route still %x after a failed login; the retry would repeat it", p.OutPath)
	}
	rm.store.WriteSync(func() {}) // drain the async writer
	if ct, _ := rm.store.Contacts.Get(t.Context(), companionID, pubkey); ct.OutPath != nil {
		t.Errorf("contact row still %x, so the next start would route down it again", ct.OutPath)
	}
}

// A node that is not a contact has no setting of its own, so it gets the companion's, and a path for it is refused rather than lost.
func TestBytesPerHop_ANonContactGetsTheCompanionsOwn(t *testing.T) {
	t.Parallel()
	rm, _, _ := routeTestClient(t)
	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{0x44})
	rm.node.Peers().Insert(&node.Peer{Identity: stranger.Identity, Name: "stranger"})
	pkt, _, _ := rm.routedPacket(rm.node.Peers().Lookup(stranger.PublicKey()), meshcore.PayloadTypeReq, nil)
	if pkt.RouteType() != meshcore.RouteTypeFlood || pkt.PathLength != 0x40 {
		t.Errorf("sent as 0x%02x/0x%02x, want a flood at the companion's 2 bytes per hop", pkt.RouteType(), pkt.PathLength)
	}
	err := rm.SetPeerPath(hex.EncodeToString(stranger.PublicKeyBytes()), []byte{0xaa}, 1)
	if !errors.Is(err, store.ErrNotContact) {
		t.Errorf("SetPeerPath on a stranger: %v, want ErrNotContact", err)
	}
	if p := rm.node.Peers().Lookup(stranger.PublicKey()); p.OutPath != nil {
		t.Errorf("a refused save still changed the live route to %x", p.OutPath)
	}
}
