package app

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/companion"
	"github.com/meshcore-go/meshcore-go/companion/client"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/appserver"
	"github.com/meshcore-go/OwlShack/internal/config"
	companionnode "github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// airModem is a radio the test listens to and talks through.
type airModem struct {
	mu   sync.Mutex
	recv func([]byte, float32, int8, bool)
	sent [][]byte
}

func (m *airModem) SendData(b []byte) error {
	m.mu.Lock()
	m.sent = append(m.sent, append([]byte(nil), b...))
	m.mu.Unlock()
	return nil
}
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
	h(b, 7.5, -70, true)
}

// waitSent returns the first packet of payloadType the companion put on air.
func (m *airModem) waitSent(t *testing.T, payloadType byte) *meshcore.Packet {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		sent := append([][]byte(nil), m.sent...)
		m.mu.Unlock()
		for _, b := range sent {
			if pkt, err := meshcore.PacketFromBytes(b); err == nil && pkt.PayloadType() == payloadType {
				return pkt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no packet of type %d went on air", payloadType)
	return nil
}

type tcpTransport struct {
	addr  string
	conn  net.Conn
	respH func(companion.Response)
}

func (t *tcpTransport) Connect(context.Context) error {
	conn, err := net.Dial("tcp", t.addr)
	if err != nil {
		return err
	}
	t.conn = conn
	go func() {
		p := companion.NewFrameParser()
		buf := make([]byte, 512)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			for _, f := range p.Feed(buf[:n]) {
				if resp, err := companion.ParseResponse(f.Data); err == nil && f.Type == companion.FrameTypeIncoming && t.respH != nil {
					t.respH(resp)
				}
			}
		}
	}()
	return nil
}
func (t *tcpTransport) Close() error { return t.conn.Close() }
func (t *tcpTransport) Send(cmd []byte) error {
	raw, err := companion.FrameEncode(companion.FrameTypeOutgoing, cmd)
	if err != nil {
		return err
	}
	_, err = t.conn.Write(raw)
	return err
}
func (t *tcpTransport) SetResponseHandler(h func(companion.Response)) { t.respH = h }
func (t *tcpTransport) SetErrorHandler(func(error))                   {}

type appRig struct {
	st     *store.Store
	air    *airModem
	comp   *companionnode.Companion
	b      *backend
	cli    *client.Client
	friend meshcore.LocalIdentity
	rpt    meshcore.LocalIdentity
}

// newAppRig is a running companion "home" with a friend and a repeater in its contacts, served to an app over TCP.
func newAppRig(t *testing.T) *appRig {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.DefaultConfig()
	lat, lon := 56.205568, -3.161287
	public := config.ChannelList{{Name: "Public"}}
	cfg.Companions = []config.CompanionConfig{{Name: "home", PrivateKey: strings.Repeat("11", 32), Latitude: &lat, Longitude: &lon, Channels: &public}}
	if err := persistToTables(ctx, st, &cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := readConfigFromTables(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	cc := loaded.Companions[0]

	r := &appRig{st: st, air: &airModem{},
		friend: meshcore.NewLocalIdentityFromSeed([32]byte{2}), rpt: meshcore.NewLocalIdentityFromSeed([32]byte{3})}
	st.WriteSync(func() {
		if err = st.Contacts.Add(ctx, cc.ID, r.friend.PublicKeyBytes(), "friend", "CHAT"); err == nil {
			err = st.Contacts.Add(ctx, cc.ID, r.rpt.PublicKeyBytes(), "Cadham", "REPEATER")
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := node.NewRadioMux(r.air)
	t.Cleanup(mux.Stop)
	r.comp, err = companionnode.NewCompanion(cc, mux, st, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.comp.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.comp.Stop() })
	for _, k := range []meshcore.LocalIdentity{r.friend, r.rpt} {
		r.comp.Node().Peers().Insert(&node.Peer{Identity: k.Identity})
	}

	r.b = &backend{companions: []*companionnode.Companion{r.comp}, db: st, reload: func() error { return nil }}
	apps := appserver.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(apps.Close)
	apps.Apply([]appserver.Port{{CompanionID: cc.ID, Addr: "127.0.0.1:0"}},
		map[int64]appserver.Device{cc.ID: newAppDevice(r.comp, r.b, loaded, config.AppConnection{})})

	r.cli = client.New(&tcpTransport{addr: apps.Status()[0].Addr})
	if err := r.cli.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.cli.Close() })
	if _, err := r.cli.DeviceQuery(r.ctx(t)); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *appRig) ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestAppDevice_HandshakeAndContacts(t *testing.T) {
	r := newAppRig(t)
	self, err := r.cli.AppStart(r.ctx(t), 1, "RemoteTerm")
	if err != nil {
		t.Fatal(err)
	}
	if self.Name != "home" || self.PublicKey != r.comp.Node().Identity().PublicKey() || self.AdvertLatitude != 56205568 || self.AdvertLongitude != -3161287 {
		t.Errorf("self info %+v", self)
	}
	contacts, err := r.cli.GetContacts(r.ctx(t))
	if err != nil || len(contacts) != 2 {
		t.Fatalf("contacts %+v, %v", contacts, err)
	}
	for _, ct := range contacts {
		if ct.PublicKey == r.rpt.PublicKey() && (ct.Type != meshcore.AdvertTypeRepeater || ct.AdvertName != "Cadham" || ct.OutPathLen != companion.OutPathUnknown) {
			t.Errorf("repeater contact %+v", ct)
		}
	}
}

func TestAppDevice_DMOutAndItsAck(t *testing.T) {
	r := newAppRig(t)
	confirmed := make(chan companion.PushSendConfirmedResponse, 1)
	r.cli.OnPush(companion.PushSendConfirmed, func(resp companion.Response) {
		confirmed <- resp.Data.(companion.PushSendConfirmedResponse)
	})
	sent, err := r.cli.SendTextMessage(r.ctx(t), r.friend.Identity, "hello friend", companion.TxtTypePlain)
	if err != nil || !sent.IsFlood || sent.Tag == 0 || sent.EstTimeout == 0 {
		t.Fatalf("SendTextMessage = %+v, %v", sent, err)
	}
	r.air.waitSent(t, meshcore.PayloadTypeTxtMsg)

	ack := binary.LittleEndian.AppendUint32(nil, sent.Tag)
	r.air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAck, 0), Payload: ack})
	select {
	case got := <-confirmed:
		if got.AckCode != sent.Tag {
			t.Errorf("confirmed ack %08x, want %08x", got.AckCode, sent.Tag)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the ACK never reached the app")
	}
}

func TestAppDevice_DMInReachesTheQueue(t *testing.T) {
	r := newAppRig(t)
	self := r.comp.Node().Identity()
	secret, _ := r.friend.SharedSecret(self.Identity)
	plain := meshcore.BuildTextPlaintext(time.Unix(1790897222, 0), 0, []byte("are you there"))
	msg, _ := meshcore.NewTextMessage(r.friend, self.Identity, plain, secret)
	payload, _ := msg.ToBytes()
	r.air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), Payload: payload})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := r.cli.GetWaitingMessages(r.ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 1 {
			c := msgs[0].Contact
			fp := r.friend.PublicKey()
			if c == nil || c.Text != "are you there" || c.SenderTimestamp != 1790897222 || string(c.PubKeyPrefix[:]) != string(fp[:6]) {
				t.Errorf("synced %+v", msgs[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the DM never reached the app's queue")
}

func TestAppDevice_ChannelsAndRename(t *testing.T) {
	r := newAppRig(t)
	if ch, err := r.cli.GetChannel(r.ctx(t), 0); err != nil || ch.Name != "Public" {
		t.Fatalf("slot 0 = %+v, %v", ch, err)
	}
	test := meshcore.NewChannelFromHashtag("#test")
	if err := r.cli.SetChannel(r.ctx(t), 1, "#test", test.PSK); err != nil {
		t.Fatal(err)
	}
	chans, err := r.st.Channels.ListByCompanion(t.Context(), r.comp.ID())
	if err != nil || len(chans) != 2 || chans[1].Name != "#test" || chans[1].PrivateKey != "" {
		t.Errorf("channels %+v, %v; want #test added with its key derived from the name", chans, err)
	}
	if err := r.cli.SetChannel(r.ctx(t), 7, "#far", test.PSK); err == nil {
		t.Error("a channel past the next free slot was accepted")
	}
	// An unused slot reads blank and clears without complaint, as on firmware; past the last slot is not found.
	if ch, err := r.cli.GetChannel(r.ctx(t), 5); err != nil || ch.Name != "" || ch.Secret != ([16]byte{}) {
		t.Errorf("unused slot 5 = %+v, %v; want a blank channel", ch, err)
	}
	if err := r.cli.SetChannel(r.ctx(t), 5, "", [16]byte{}); err != nil {
		t.Errorf("clearing unused slot 5: %v", err)
	}
	if _, err := r.cli.GetChannel(r.ctx(t), appMaxChannels); err == nil {
		t.Error("a slot past MAX_GROUP_CHANNELS was answered")
	}
	if chans, _ := r.st.Channels.ListByCompanion(t.Context(), r.comp.ID()); len(chans) != 2 {
		t.Errorf("after the unused-slot calls there are %d channels, want 2", len(chans))
	}

	if err := r.cli.SetAdvertName(r.ctx(t), "home 2"); err != nil {
		t.Fatal(err)
	}
	row, err := r.st.Companions.Get(t.Context(), r.comp.ID())
	if err != nil || row.Name != "home 2" || row.Latitude == nil || *row.Latitude != 56.205568 {
		t.Errorf("after the rename the row is %+v, %v; want the name changed and the position kept", row, err)
	}
}

func advertPacket(t *testing.T, id meshcore.LocalIdentity, advType, name string) *meshcore.Packet {
	t.Helper()
	app, _ := (&meshcore.AdvertAppData{Type: advType, Name: name}).ToBytes()
	adv := &meshcore.Advert{PublicKey: id.Identity, Timestamp: uint32(time.Now().Unix()), RawAppData: app}
	adv.SignWith(id)
	payload, _ := adv.ToBytes()
	return &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), Payload: payload}
}

func TestAppDevice_AutoAdd(t *testing.T) {
	r := newAppRig(t)
	pushes := make(chan companion.Response, 8)
	r.cli.OnPush(companion.PushAdvert, func(resp companion.Response) { pushes <- resp })
	r.cli.OnPush(companion.PushNewAdvert, func(resp companion.Response) { pushes <- resp })

	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{8})
	r.air.hear(t, advertPacket(t, stranger, "CHAT", "Amy"))
	select {
	case p := <-pushes:
		if p.Code != companion.PushAdvert {
			t.Fatalf("with auto-add on the app got push 0x%02x, want PUSH_ADVERT", p.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no advert push")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if ct, _ := r.st.Contacts.Get(t.Context(), r.comp.ID(), stranger.PublicKeyBytes()); ct != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("auto-add never filed the contact")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.cli.SetOtherParams(r.ctx(t), 1); err != nil {
		t.Fatal(err)
	}
	other := meshcore.NewLocalIdentityFromSeed([32]byte{9})
	r.air.hear(t, advertPacket(t, other, "REPEATER", "Fife"))
	select {
	case p := <-pushes:
		na, ok := p.Data.(companion.PushNewAdvertResponse)
		if p.Code != companion.PushNewAdvert || !ok || na.AdvertName != "Fife" || na.Type != meshcore.AdvertTypeRepeater {
			t.Fatalf("with manual add on the app got %+v, want PUSH_NEW_ADVERT for Fife", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no new-advert push")
	}
	time.Sleep(100 * time.Millisecond)
	if ct, _ := r.st.Contacts.Get(t.Context(), r.comp.ID(), other.PublicKeyBytes()); ct != nil {
		t.Error("manual add still filed the contact")
	}
}

func TestAppDevice_LoginReplyIsPushed(t *testing.T) {
	r := newAppRig(t)
	pushed := make(chan companion.PushLoginSuccessResponse, 1)
	r.cli.OnPush(companion.PushLoginSuccess, func(resp companion.Response) {
		pushed <- resp.Data.(companion.PushLoginSuccessResponse)
	})
	if err := r.cli.SendLogin(r.ctx(t), r.rpt.Identity, "secret"); err != nil {
		t.Fatal(err)
	}
	r.air.waitSent(t, meshcore.PayloadTypeAnonReq)

	self := r.comp.Node().Identity()
	secret, _ := r.rpt.SharedSecret(self.Identity)
	reply := []byte{0x0d, 0x0c, 0x0b, 0x0a, 0, 0, 1, 3, 0, 0, 0, 0, 2}
	enc, _ := meshcore.EncryptThenMAC(secret, reply)
	payload, _ := (&meshcore.Response{Destination: self.PublicKey()[0], Source: r.rpt.PublicKey()[0], MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:]}).ToBytes()
	r.air.hear(t, &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 0), Payload: payload})

	select {
	case got := <-pushed:
		rp := r.rpt.PublicKey()
		if got.Permissions != 1 || got.ACL != 3 || got.FirmwareLevel != 2 || got.ServerTime != 0x0a0b0c0d || string(got.PubKeyPrefix[:]) != string(rp[:6]) {
			t.Errorf("login push %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the login reply never reached the app")
	}
	if err := r.cli.HasConnection(r.ctx(t), r.rpt.Identity); err != nil {
		t.Errorf("no session after the login: %v", err)
	}
}

// RemoteTerm loads its channel into slot 0 and posts straight after the OK, so the slot is live before the reply, and loading it again writes nothing.
func TestAppDevice_ChannelIsLiveBeforeTheReply(t *testing.T) {
	r := newAppRig(t)
	if err := r.cli.SetChannel(r.ctx(t), 0, "", [16]byte{}); err != nil {
		t.Fatalf("clearing slot 0: %v", err)
	}
	if ch, err := r.cli.GetChannel(r.ctx(t), 0); err != nil || ch.Name != "" {
		t.Fatalf("slot 0 after clearing = %+v, %v; want blank", ch, err)
	}
	traffic := meshcore.NewChannelFromHashtag("#traffic")
	if err := r.cli.SetChannel(r.ctx(t), 0, "#traffic", traffic.PSK); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cli.SendChannelTextMessage(r.ctx(t), 0, "road closed", companion.TxtTypePlain); err != nil {
		t.Fatalf("posting straight after loading the channel: %v", err)
	}
	r.air.waitSent(t, meshcore.PayloadTypeGrpTxt)

	before, _ := r.st.Channels.ListByCompanion(t.Context(), r.comp.ID())
	if err := r.cli.SetChannel(r.ctx(t), 0, "#traffic", traffic.PSK); err != nil {
		t.Fatal(err)
	}
	after, _ := r.st.Channels.ListByCompanion(t.Context(), r.comp.ID())
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID {
		t.Errorf("reloading the same channel rewrote it: %+v -> %+v", before, after)
	}
}

func TestInPlaceChange(t *testing.T) {
	a := config.ChannelList{{Name: "Public"}}
	b := config.ChannelList{{Name: "#traffic"}}
	base := config.CompanionConfig{Name: "bot", Channels: &a}
	chans := base
	chans.Channels = &b
	renamed := base
	renamed.Name = "bot2"
	if !inPlaceChange(base, chans) || channelsEqual(base, chans) || !triggersEqual(base, chans) {
		t.Error("a channel-only change is not applied in place")
	}
	if inPlaceChange(base, renamed) {
		t.Error("a rename was planned in place")
	}
}
