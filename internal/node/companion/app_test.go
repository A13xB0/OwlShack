package companion

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// airModem hands the test the mux's receive handler, so packets can be put on the air to the companion.
type airModem struct {
	mu   sync.Mutex
	recv func([]byte, float32, int8, bool)
}

func (m *airModem) SendData([]byte) error { return nil }
func (m *airModem) SetDataHandler(h func([]byte, float32, int8, bool)) {
	m.mu.Lock()
	m.recv = h
	m.mu.Unlock()
}
func (m *airModem) AddOutboundHandler(func([]byte)) {}

func (m *airModem) hear(t *testing.T, pkt *meshcore.Packet) {
	t.Helper()
	b, err := pkt.ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	h := m.recv
	m.mu.Unlock()
	h(b, 6.25, -80, true)
}

type recordingSink struct {
	mu       sync.Mutex
	messages []AppMessage
	adverts  int
	rawRX    int
	paths    [][32]byte
}

func (s *recordingSink) Message(m AppMessage) {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	s.mu.Unlock()
}
func (s *recordingSink) Advert(*meshcore.Packet, *meshcore.Advert) {
	s.mu.Lock()
	s.adverts++
	s.mu.Unlock()
}
func (s *recordingSink) PathUpdated(k [32]byte) {
	s.mu.Lock()
	s.paths = append(s.paths, k)
	s.mu.Unlock()
}
func (s *recordingSink) RawRX([]byte, float32, int8) {
	s.mu.Lock()
	s.rawRX++
	s.mu.Unlock()
}
func (s *recordingSink) Trace(*meshcore.Packet, *meshcore.Trace) {}
func (s *recordingSink) Control(*meshcore.Packet)                {}
func (s *recordingSink) RawCustom(*meshcore.Packet)              {}

func (s *recordingSink) waitMessages(t *testing.T, n int) []AppMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := append([]AppMessage(nil), s.messages...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sink heard fewer than %d messages", n)
	return nil
}

// appCompanion is a running companion on a mux whose air the test controls, with one contact, friend.
func appCompanion(t *testing.T) (*Companion, *airModem, *recordingSink, meshcore.LocalIdentity, meshcore.LocalIdentity) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	friend := meshcore.NewLocalIdentityFromSeed([32]byte{2})
	comp := store.Companion{Name: "home"}
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &comp); err == nil {
			err = st.Contacts.Add(ctx, comp.ID, friend.PublicKeyBytes(), "friend", "CHAT")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	air := &airModem{}
	mux := node.NewRadioMux(air)
	t.Cleanup(mux.Stop)
	channels := config.ChannelList{{Name: "Public"}, {Name: "#scotland"}}
	c, err := NewCompanion(config.CompanionConfig{ID: comp.ID, Name: "home", PrivateKey: strings.Repeat("11", 32), Channels: &channels},
		mux, st, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop() })
	sink := &recordingSink{}
	c.SetAppSink(sink)
	return c, air, sink, c.node.Identity(), friend
}

func TestAppSink_HearsADMWithTheSendersTimestamp(t *testing.T) {
	_, air, sink, self, friend := appCompanion(t)
	secret, _ := friend.SharedSecret(self.Identity)
	plain := meshcore.BuildTextPlaintext(time.Unix(1790897222, 0), 0, []byte("hello home"))
	msg, err := meshcore.NewTextMessage(friend, self.Identity, plain, secret)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := msg.ToBytes()
	air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), PathLength: 0x42, Path: []byte{1, 2, 3, 4}, Payload: payload})

	m := sink.waitMessages(t, 1)[0]
	if m.Kind != AppMessageContact || m.SenderPubKey != friend.PublicKey() || m.TxtType != txtTypePlain ||
		m.SenderTimestamp != 1790897222 || m.Text != "hello home" || m.PathLen != 0x42 || m.SNR != 6.25 {
		t.Errorf("heard %+v", m)
	}
}

func TestAppSink_HearsAChannelMessageWithItsSlotAndSender(t *testing.T) {
	_, air, sink, _, _ := appCompanion(t)
	ch := meshcore.NewChannelFromHashtag("#scotland")
	grp, err := (&meshcore.GroupTextPayload{Timestamp: 1790897300, Sender: "Scot", Text: "dry today"}).Encrypt(ch.Hash, ch.PSK[:])
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := grp.ToBytes()
	air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeGrpTxt, 0), Payload: payload})

	m := sink.waitMessages(t, 1)[0]
	if m.Kind != AppMessageChannel || m.ChannelIdx != 1 || m.Text != "Scot: dry today" || m.SenderTimestamp != 1790897300 || m.PathLen != 0xff {
		t.Errorf("heard %+v", m)
	}
}

func TestAppSink_HearsChannelData(t *testing.T) {
	_, air, sink, _, _ := appCompanion(t)
	ch := meshcore.NewChannelFromHashtag("#scotland")
	gd, err := meshcore.NewGroupData(ch.Hash, ch.PSK[:], 0x0102, []byte("rns"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := gd.ToBytes()
	air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpData, 0), Payload: payload})

	m := sink.waitMessages(t, 1)[0]
	if m.Kind != AppMessageChannelData || m.ChannelIdx != 1 || m.DataType != 0x0102 || string(m.Data) != "rns" {
		t.Errorf("heard %+v", m)
	}
}

func TestAppSink_HearsAdvertsAndKeepsThePayload(t *testing.T) {
	c, air, sink, _, _ := appCompanion(t)
	rpt := meshcore.NewLocalIdentityFromSeed([32]byte{9})
	app, _ := (&meshcore.AdvertAppData{Type: "REPEATER", Name: "Fife"}).ToBytes()
	adv := &meshcore.Advert{PublicKey: rpt.Identity, Timestamp: 1790897000, RawAppData: app}
	adv.SignWith(rpt)
	payload, _ := adv.ToBytes()
	air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), Payload: payload})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if kept, _ := c.store.Peers.Advert(t.Context(), rpt.PublicKeyBytes()); len(kept) > 0 {
			sink.mu.Lock()
			adverts, raw := sink.adverts, sink.rawRX
			sink.mu.Unlock()
			if adverts != 1 || raw != 1 {
				t.Errorf("sink heard %d adverts and %d raw packets, want 1 of each", adverts, raw)
			}
			if string(kept) != string(payload) {
				t.Error("kept advert differs from the one heard")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the advert payload was never kept")
}

func TestAppSink_DetachedHearsNothing(t *testing.T) {
	c, air, sink, self, friend := appCompanion(t)
	c.SetAppSink(nil)
	secret, _ := friend.SharedSecret(self.Identity)
	plain := meshcore.BuildTextPlaintext(time.Unix(1790897222, 0), 0, []byte("anyone?"))
	msg, _ := meshcore.NewTextMessage(friend, self.Identity, plain, secret)
	payload, _ := msg.ToBytes()
	air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), Payload: payload})
	time.Sleep(50 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.messages) != 0 {
		t.Errorf("a detached sink heard %d messages", len(sink.messages))
	}
}
