package companion

import (
	"path/filepath"
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/client/repeater"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// scopedCompanion is a companion on a recording radio with one contact, friend, flooding in scope (nil for unscoped).
func scopedCompanion(t *testing.T, scope string) (*Companion, *recordingRadio, meshcore.LocalIdentity, meshcore.LocalIdentity) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	self := meshcore.NewLocalIdentityFromSeed([32]byte{1})
	friend := meshcore.NewLocalIdentityFromSeed([32]byte{2})
	comp := store.Companion{Name: "home", FloodScope: scope}
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &comp); err == nil {
			err = st.Contacts.Add(ctx, comp.ID, friend.PublicKeyBytes(), "friend", "CHAT")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var opts []node.Option
	if scope != "" {
		opts = append(opts, node.WithFloodScope(config.ScopeRegion(scope)))
	}
	radio := &recordingRadio{}
	n := node.New(self, radio, opts...)
	t.Cleanup(n.Stop)
	c := telemetryCompanion(t, config.CompanionConfig{ID: comp.ID, Name: "home", FloodScope: scope, TelemetryBase: mode(config.TelemetryContacts)}, nil)
	c.node, c.store, c.runCtx = n, st, ctx
	c.repeaters = repeater.NewClient(n, st, comp.ID, c.log, c.stats, c.pathHashSize)
	return c, radio, self, friend
}

// sentOne is the one packet the radio sent, failing if it sent anything else.
func sentOne(t *testing.T, radio *recordingRadio) *meshcore.Packet {
	t.Helper()
	sent := radio.take()
	if len(sent) != 1 {
		t.Fatalf("%d packets sent, want one", len(sent))
	}
	pkt, err := meshcore.PacketFromBytes(sent[0])
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func wantInScope(t *testing.T, what string, pkt *meshcore.Packet, scope string) {
	t.Helper()
	if scope == "" {
		if pkt.RouteType() != meshcore.RouteTypeFlood {
			t.Errorf("%s: route type %d, want a plain flood", what, pkt.RouteType())
		}
		return
	}
	if r := config.ScopeRegion(scope); pkt.RouteType() != meshcore.RouteTypeTransportFlood || !r.MatchesPacket(pkt) {
		t.Errorf("%s: route type %d code %04x, want a flood scoped to %s", what, pkt.RouteType(), pkt.TransportCode1, scope)
	}
}

// Every flood a companion originates goes out in its scope, as the firmware sends them all through sendFloodScoped.
func TestFloodScope_EveryFloodACompanionOriginates(t *testing.T) {
	for _, scope := range []string{"sco", ""} {
		t.Run("scope "+scope, func(t *testing.T) {
			c, radio, self, friend := scopedCompanion(t, scope)
			secret, _ := friend.SharedSecret(self.Identity)

			c.node.SetChannel(0, meshcore.NewChannelFromHashtag("#scotland"))
			if err := c.SendChannelMessage("#scotland", "hello"); err != nil {
				t.Fatal(err)
			}
			wantInScope(t, "channel message", sentOne(t, radio), scope)

			if err := c.SendContactMessage(friend.Identity.String(), "hi"); err != nil {
				t.Fatal(err)
			}
			wantInScope(t, "DM with no route", sentOne(t, radio), scope)

			ack := []byte{1, 2, 3, 4, 0, 9}
			c.sendDMAck(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}, friend.PublicKeyBytes(), secret, ack)
			wantInScope(t, "ACK with no route", sentOne(t, radio), scope)

			flooded := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), PathLength: 1, Path: []byte{0xaa}}
			c.sendDMAck(flooded, friend.PublicKeyBytes(), secret, ack)
			wantInScope(t, "ACK riding a path return", sentOne(t, radio), scope)

			if err := c.sendAdvert(true); err != nil {
				t.Fatal(err)
			}
			wantInScope(t, "flood advert", sentOne(t, radio), scope)
		})
	}
}

func TestFloodScope_LeavesDirectAndZeroHopAlone(t *testing.T) {
	c, radio, self, friend := scopedCompanion(t, "sco")
	secret, _ := friend.SharedSecret(self.Identity)

	if err := c.sendAdvert(false); err != nil {
		t.Fatal(err)
	}
	if pkt := sentOne(t, radio); pkt.RouteType() != meshcore.RouteTypeDirect {
		t.Errorf("zero-hop advert: route type %d, want direct", pkt.RouteType())
	}

	c.node.Peers().Insert(&node.Peer{Identity: friend.Identity, Name: "friend"})
	c.node.Peers().SetOutPath(friend.PublicKey(), []byte{0x7a}, 1)
	c.sendDMAck(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}, friend.PublicKeyBytes(), secret, []byte{1, 2, 3, 4, 0, 9})
	if pkt := sentOne(t, radio); pkt.RouteType() != meshcore.RouteTypeDirect {
		t.Errorf("ACK with a route: route type %d, want direct", pkt.RouteType())
	}
}

// A telemetry reply floods in scope whether it rides a path return or goes as a datagram with no route back.
func TestFloodScope_RequestReplies(t *testing.T) {
	c, radio, self, friend := scopedCompanion(t, "sco")
	secret, _ := friend.SharedSecret(self.Identity)
	enc, err := meshcore.EncryptThenMAC(secret, []byte{7, 0, 0, 0, reqTypeGetTelemetryData, 0})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&meshcore.Request{
		Destination: self.PublicKey()[0], Source: friend.PublicKey()[0], MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:],
	}).ToBytes()
	for name, req := range map[string]*meshcore.Packet{
		"direct request":  {Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0), Payload: payload},
		"flooded request": {Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeReq, 0), PathLength: 1, Path: []byte{0xaa}, Payload: payload},
	} {
		c.handleReq(req)
		wantInScope(t, name, sentOne(t, radio), "sco")
	}
}
