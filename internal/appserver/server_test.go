package appserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/companion"
	"github.com/meshcore-go/meshcore-go/companion/client"
)

// tcpTransport is the smallest client.Transport: frames over one TCP connection.
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
				if f.Type != companion.FrameTypeIncoming {
					continue
				}
				if resp, err := companion.ParseResponse(f.Data); err == nil && t.respH != nil {
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

var (
	friendKey = [32]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 31: 0x99}
	selfKey   = [32]byte{0x04, 0xf1, 0xfe, 0x66, 31: 0x01}
)

// fakeDevice records what the server asks of it and answers from fixed state.
type fakeDevice struct {
	mu        sync.Mutex
	prefs     Prefs
	name      string
	contacts  []companion.ContactResponse
	sentTexts []string
	scopes    []Scope
	signed    []byte
	subs      map[int]func(Event)
	nextSub   int
	cancelled int
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{
		name:  "home",
		prefs: Prefs{AirtimeFactorMs: 1000},
		contacts: []companion.ContactResponse{
			{PublicKey: friendKey, Type: 1, OutPathLen: 0xff, AdvertName: "friend", LastModified: 100},
			{PublicKey: [32]byte{0x77}, Type: 2, OutPathLen: 0xff, AdvertName: "Fife", LastModified: 200},
		},
		subs: map[int]func(Event){},
	}
}

func (d *fakeDevice) emit(ev Event) {
	d.mu.Lock()
	subs := make([]func(Event), 0, len(d.subs))
	for _, f := range d.subs {
		subs = append(subs, f)
	}
	d.mu.Unlock()
	for _, f := range subs {
		f(ev)
	}
}

func (d *fakeDevice) SelfInfo() companion.SelfInfoResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return companion.SelfInfoResponse{AdvertType: 1, TxPower: 22, MaxTxPower: 22, PublicKey: selfKey, RadioFrequency: 869618, RadioBandwidth: 62500, RadioSpreadFactor: 8, RadioCodingRate: 8, Name: d.name}
}
func (d *fakeDevice) DeviceInfo() companion.DeviceInfoResponse {
	return companion.DeviceInfoResponse{FirmwareVersion: 13, MaxContacts: 510, MaxChannels: 40, Model: "OwlShack", FirmwareVersionStr: "v1.5.0"}
}
func (d *fakeDevice) BattAndStorage() companion.BattAndStorageResponse {
	return companion.BattAndStorageResponse{BatteryMilliVolts: 4100}
}
func (d *fakeDevice) Stats(kind byte) companion.StatsResponse {
	return companion.StatsResponse{StatsType: kind, Core: &companion.CoreStats{UptimeSecs: 9}}
}
func (d *fakeDevice) Prefs() Prefs { d.mu.Lock(); defer d.mu.Unlock(); return d.prefs }
func (d *fakeDevice) SetPrefs(p Prefs) error {
	d.mu.Lock()
	d.prefs = p
	d.mu.Unlock()
	return nil
}
func (d *fakeDevice) SetName(n string) error       { d.mu.Lock(); d.name = n; d.mu.Unlock(); return nil }
func (d *fakeDevice) SetLatLon(int32, int32) error { return nil }
func (d *fakeDevice) SetPathHashMode(byte) error   { return nil }
func (d *fakeDevice) DefaultScope() (string, []byte) {
	return "sco", meshcore.NewRegionFromHashtag("sco").Key[:]
}
func (d *fakeDevice) SetDefaultScope(string, []byte) error { return nil }
func (d *fakeDevice) SendSelfAdvert(bool) error            { return nil }
func (d *fakeDevice) SelfAdvert() ([]byte, error)          { return []byte{0x11, 0x00, 0xaa}, nil }
func (d *fakeDevice) SelfTelemetry() []byte                { return []byte{1, 0x74, 0x01, 0x9a} }
func (d *fakeDevice) PrivateKey() ([64]byte, bool)         { return [64]byte{}, false }
func (d *fakeDevice) Sign(data []byte) ([64]byte, error) {
	d.mu.Lock()
	d.signed = append([]byte(nil), data...)
	d.mu.Unlock()
	return [64]byte{0x5e}, nil
}
func (d *fakeDevice) Contacts() []companion.ContactResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]companion.ContactResponse(nil), d.contacts...)
}
func (d *fakeDevice) Contact(k [32]byte) (companion.ContactResponse, bool) {
	for _, c := range d.Contacts() {
		if c.PublicKey == k {
			return c, true
		}
	}
	return companion.ContactResponse{}, false
}
func (d *fakeDevice) FindContact(prefix []byte) ([32]byte, bool) {
	for _, c := range d.Contacts() {
		if bytes.HasPrefix(c.PublicKey[:], prefix) {
			return c.PublicKey, true
		}
	}
	return [32]byte{}, false
}
func (d *fakeDevice) PutContact(companion.AddUpdateContactCommand) error { return nil }
func (d *fakeDevice) RemoveContact(k [32]byte) error {
	if _, ok := d.Contact(k); !ok {
		return ErrNotFound
	}
	return nil
}
func (d *fakeDevice) ResetPath([32]byte) error               { return nil }
func (d *fakeDevice) ShareContact([32]byte) error            { return nil }
func (d *fakeDevice) ExportContact([32]byte) ([]byte, error) { return nil, ErrNotFound }
func (d *fakeDevice) ImportContact([]byte) error             { return nil }
func (d *fakeDevice) AdvertPath([32]byte) (companion.AdvertPathResponse, bool) {
	return companion.AdvertPathResponse{}, false
}
func (d *fakeDevice) Channel(idx byte) (companion.ChannelInfoResponse, bool) {
	if idx == 0 {
		return companion.ChannelInfoResponse{ChannelIdx: 0, Name: "Public"}, true
	}
	return companion.ChannelInfoResponse{}, false
}
func (d *fakeDevice) SetChannel(byte, string, [16]byte) error { return nil }
func (d *fakeDevice) SendText(k [32]byte, txtType, attempt byte, ts uint32, text string, scope Scope, onAck func(uint32, uint32)) (companion.SentResponse, error) {
	d.mu.Lock()
	d.sentTexts = append(d.sentTexts, text)
	d.scopes = append(d.scopes, scope)
	d.mu.Unlock()
	go onAck(0xa1b2c3d4, 1234)
	return companion.SentResponse{IsFlood: true, Tag: 0xa1b2c3d4, EstTimeout: 7900, HasExtended: true}, nil
}
func (d *fakeDevice) SendChannelText(idx byte, ts uint32, text string, scope Scope) error {
	d.mu.Lock()
	d.scopes = append(d.scopes, scope)
	d.mu.Unlock()
	if idx != 0 {
		return ErrNotFound
	}
	return nil
}
func (d *fakeDevice) SendChannelData(byte, []byte, bool, uint16, []byte, Scope) error { return nil }
func (d *fakeDevice) SendRawData([]byte, []byte) error                                { return nil }
func (d *fakeDevice) SendRawPacket(byte, []byte) error                                { return nil }
func (d *fakeDevice) SendControlData([]byte) error                                    { return nil }
func (d *fakeDevice) SendTrace(tag, auth uint32, flags byte, path []byte) (companion.SentResponse, error) {
	return companion.SentResponse{Tag: tag, EstTimeout: 3000, HasExtended: true}, nil
}
func (d *fakeDevice) Login([32]byte, string) (companion.SentResponse, error) {
	return companion.SentResponse{IsFlood: true, Tag: 0x5152, EstTimeout: 9000, HasExtended: true}, nil
}
func (d *fakeDevice) Request(k [32]byte, kind RequestKind, data []byte, scope Scope) (companion.SentResponse, error) {
	return companion.SentResponse{IsFlood: true, Tag: uint32(kind) + 1, EstTimeout: 9000, HasExtended: true}, nil
}
func (d *fakeDevice) HasConnection([32]byte) bool { return false }
func (d *fakeDevice) Logout([32]byte)             {}
func (d *fakeDevice) Subscribe(f func(Event)) func() {
	d.mu.Lock()
	id := d.nextSub
	d.nextSub++
	d.subs[id] = f
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.subs, id)
		d.cancelled++
		d.mu.Unlock()
	}
}

func newServer(t *testing.T, d Device) (*Server, string) {
	t.Helper()
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(srv.Close)
	srv.Apply([]Port{{CompanionID: 1, Addr: "127.0.0.1:0"}}, map[int64]Device{1: d})
	st := srv.Status()
	if len(st) != 1 || st[0].Error != "" {
		t.Fatalf("port did not open: %+v", st)
	}
	return srv, st[0].Addr
}

func dial(t *testing.T, addr string) *client.Client {
	t.Helper()
	c := client.New(&tcpTransport{addr: addr})
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestServer_HandshakeAndStatus(t *testing.T) {
	srv, addr := newServer(t, newFakeDevice())
	c := dial(t, addr)

	info, err := c.DeviceQuery(ctx(t))
	if err != nil || info.FirmwareVersion != 13 || info.Model != "OwlShack" || info.MaxContacts != 510 {
		t.Fatalf("DeviceQuery = %+v, %v", info, err)
	}
	self, err := c.AppStart(ctx(t), 1, "RemoteTerm")
	if err != nil || self.PublicKey != selfKey || self.Name != "home" || self.RadioFrequency != 869618 {
		t.Fatalf("AppStart = %+v, %v", self, err)
	}
	st := srv.Status()[0]
	if st.AppName != "RemoteTerm" || st.Client == "" || st.Since.IsZero() {
		t.Errorf("status %+v, want the app named and its address", st)
	}
}

func TestServer_ContactsSince(t *testing.T) {
	_, addr := newServer(t, newFakeDevice())
	c := dial(t, addr)
	all, err := c.GetContacts(ctx(t))
	if err != nil || len(all) != 2 {
		t.Fatalf("GetContacts = %d contacts, %v", len(all), err)
	}
	newer, err := c.GetContactsSince(ctx(t), 150, true)
	if err != nil || len(newer) != 1 || newer[0].AdvertName != "Fife" {
		t.Fatalf("GetContactsSince(150) = %+v, %v; want Fife alone", newer, err)
	}
}

func TestServer_SendTextScopesAndConfirms(t *testing.T) {
	d := newFakeDevice()
	_, addr := newServer(t, d)
	c := dial(t, addr)

	confirmed := make(chan companion.PushSendConfirmedResponse, 1)
	c.OnPush(companion.PushSendConfirmed, func(r companion.Response) {
		confirmed <- r.Data.(companion.PushSendConfirmedResponse)
	})
	fif := meshcore.NewRegionFromHashtag("fif")
	if err := c.SetFloodScope(ctx(t), fif.Key[:]); err != nil {
		t.Fatal(err)
	}
	sent, err := c.SendTextMessage(ctx(t), meshcore.NewIdentity(friendKey), "hello", companion.TxtTypePlain)
	if err != nil || !sent.IsFlood || sent.Tag != 0xa1b2c3d4 || sent.EstTimeout != 7900 {
		t.Fatalf("SendTextMessage = %+v, %v", sent, err)
	}
	select {
	case got := <-confirmed:
		if got.AckCode != 0xa1b2c3d4 || got.RoundTrip != 1234 {
			t.Errorf("confirmed %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no PUSH_SEND_CONFIRMED")
	}
	d.mu.Lock()
	scope := d.scopes[0]
	d.mu.Unlock()
	if !scope.Set || scope.Unscoped || scope.Region == nil || scope.Region.Key != fif.Key {
		t.Errorf("the send went out with scope %+v, want the session's fif override", scope)
	}

	if _, err := c.SendTextMessage(ctx(t), meshcore.NewIdentity([32]byte{0xee}), "nobody", companion.TxtTypePlain); err == nil {
		t.Error("a DM to someone not in contacts was sent")
	}
}

func TestServer_QueueHoldsMessagesUntilTheAppSyncs(t *testing.T) {
	d := newFakeDevice()
	_, addr := newServer(t, d)

	// Arrives while no app is connected, as at the firmware.
	d.emit(Event{Message: &Message{Kind: MessageContact, SenderPrefix: [6]byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56}, TxtType: 0, SenderTimestamp: 1790897222, Text: "while you were out", PathLen: 0xff, SNR: 6.25}})
	d.emit(Event{Message: &Message{Kind: MessageChannel, ChannelIdx: 1, SenderTimestamp: 1790897300, Text: "Scot: dry today", PathLen: 0x42}})

	c := dial(t, addr)
	if _, err := c.DeviceQuery(ctx(t)); err != nil { // asks for v3 frames
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 4)
	c.OnPush(companion.PushMsgWaiting, func(companion.Response) { waiting <- struct{}{} })

	msgs, err := c.GetWaitingMessages(ctx(t))
	if err != nil || len(msgs) != 2 {
		t.Fatalf("GetWaitingMessages = %+v, %v", msgs, err)
	}
	if dm := msgs[0].Contact; dm == nil || dm.Text != "while you were out" || dm.SenderTimestamp != 1790897222 || dm.PathLen != 0xff {
		t.Errorf("first message %+v", msgs[0])
	}
	if ch := msgs[1].Channel; ch == nil || ch.ChannelIdx != 1 || ch.Text != "Scot: dry today" {
		t.Errorf("second message %+v", msgs[1])
	}

	d.emit(Event{Message: &Message{Kind: MessageContact, Text: "now"}})
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("a message arriving with an app connected sent no PUSH_MSG_WAITING")
	}
}

func TestServer_ANewConnectionReplacesTheOld(t *testing.T) {
	srv, addr := newServer(t, newFakeDevice())
	first := dial(t, addr)
	if _, err := first.DeviceQuery(ctx(t)); err != nil {
		t.Fatal(err)
	}
	second := dial(t, addr)
	if _, err := second.DeviceQuery(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DeviceQuery(ctx(t)); err == nil {
		t.Error("the replaced connection still got an answer")
	}
	if st := srv.Status()[0]; st.Replaced != 1 {
		t.Errorf("replaced = %d, want 1", st.Replaced)
	}
}

func TestServer_SharedRadioSettingsAreAcknowledgedOnly(t *testing.T) {
	_, addr := newServer(t, newFakeDevice())
	c := dial(t, addr)
	if err := c.SetRadioParams(ctx(t), 869618, 62500, 8, 8); err != nil {
		t.Errorf("valid radio params refused: %v", err)
	}
	if err := c.SetRadioParams(ctx(t), 869618, 62500, 13, 8); err == nil {
		t.Error("SF13 accepted")
	}
	if err := c.SetTxPower(ctx(t), 22); err != nil {
		t.Errorf("22 dBm refused: %v", err)
	}
	if err := c.SetTxPower(ctx(t), 30); err == nil {
		t.Error("30 dBm accepted above the device's maximum")
	}
}

func TestServer_PrefsRoundTrip(t *testing.T) {
	d := newFakeDevice()
	_, addr := newServer(t, d)
	c := dial(t, addr)
	if err := c.SetAutoAddConfig(ctx(t), companion.AutoAddChat|companion.AutoAddRepeater, 3); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAutoAddConfig(ctx(t))
	if err != nil || got.Config != 0x06 || got.MaxHops != 3 {
		t.Errorf("auto-add = %+v, %v", got, err)
	}
	if err := c.SetTuningParams(ctx(t), 0.5, 1.25); err != nil {
		t.Fatal(err)
	}
	tp, err := c.GetTuningParams(ctx(t))
	if err != nil || tp.RxDelayBase != 0.5 || tp.AirtimeFactor != 1.25 {
		t.Errorf("tuning = %+v, %v", tp, err)
	}
	if err := c.SetOtherParams(ctx(t), 1); err != nil {
		t.Fatal(err)
	}
	if d.Prefs().ManualAdd != 1 {
		t.Error("manual add was not stored")
	}
}

func TestServer_SigningSession(t *testing.T) {
	d := newFakeDevice()
	_, addr := newServer(t, d)
	c := dial(t, addr)
	if err := c.SignData(ctx(t), []byte("early")); err == nil {
		t.Error("SIGN_DATA before SIGN_START accepted")
	}
	if st, err := c.SignStart(ctx(t)); err != nil || st.MaxSignDataLen != 8192 {
		t.Fatalf("SignStart = %+v, %v", st, err)
	}
	for _, part := range []string{"hello ", "mesh"} {
		if err := c.SignData(ctx(t), []byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	sig, err := c.SignFinish(ctx(t))
	if err != nil || sig.Signature[0] != 0x5e {
		t.Fatalf("SignFinish = %+v, %v", sig, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if string(d.signed) != "hello mesh" {
		t.Errorf("signed %q, want both parts in order", d.signed)
	}
}

func TestServer_Refusals(t *testing.T) {
	_, addr := newServer(t, newFakeDevice())
	c := dial(t, addr)
	if _, err := c.ExportPrivateKey(ctx(t)); !errors.Is(err, client.ErrDisabled) {
		t.Errorf("key export = %v, want RESP_CODE_DISABLED", err)
	}
	if err := c.ImportPrivateKey(ctx(t), [64]byte{1}); !errors.Is(err, client.ErrDisabled) {
		t.Errorf("key import = %v, want RESP_CODE_DISABLED", err)
	}
	wantCode := func(what string, err error, code byte) {
		t.Helper()
		var de *client.DeviceError
		if !errors.As(err, &de) || de.Code != code {
			t.Errorf("%s = %v, want error code %d", what, err, code)
		}
	}
	wantCode("removing an unknown contact", c.RemoveContact(ctx(t), meshcore.NewIdentity([32]byte{0xee})), companion.ErrCodeNotFound)
	_, err := c.GetChannel(ctx(t), 5)
	wantCode("an empty channel slot", err, companion.ErrCodeNotFound)
	wantCode("a login to a stranger", c.SendLogin(ctx(t), meshcore.NewIdentity([32]byte{0xee}), "x"), companion.ErrCodeNotFound)
	wantCode("a custom var", c.SetCustomVar(ctx(t), "gps", "1"), companion.ErrCodeIllegalArg)
}

func TestServer_DeviceSwapKeepsTheSession(t *testing.T) {
	first := newFakeDevice()
	srv, addr := newServer(t, first)
	c := dial(t, addr)
	if _, err := c.AppStart(ctx(t), 1, "app"); err != nil {
		t.Fatal(err)
	}

	second := newFakeDevice()
	second.name = "home renamed"
	srv.Apply([]Port{{CompanionID: 1, Addr: srv.Status()[0].Addr}}, map[int64]Device{1: second})

	self, err := c.AppStart(ctx(t), 1, "app")
	if err != nil || self.Name != "home renamed" {
		t.Fatalf("after the swap AppStart = %+v, %v; want the new device on the same connection", self, err)
	}
	first.mu.Lock()
	cancelled := first.cancelled
	first.mu.Unlock()
	if cancelled != 1 {
		t.Errorf("the old device's subscription was cancelled %d times, want 1", cancelled)
	}
}

func TestServer_NoDeviceIsBadState(t *testing.T) {
	srv, addr := newServer(t, newFakeDevice())
	srv.Apply([]Port{{CompanionID: 1, Addr: srv.Status()[0].Addr}}, nil)
	c := dial(t, addr)
	if _, err := c.DeviceQuery(ctx(t)); err == nil {
		t.Error("a port with no running companion answered a query")
	}
}

func TestServer_PortThatCannotOpenIsReported(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer srv.Close()
	srv.Apply([]Port{{CompanionID: 7, Addr: ln.Addr().String()}}, nil)
	if st := srv.Status(); len(st) != 1 || st[0].Error == "" {
		t.Errorf("status %+v, want the bind failure reported", st)
	}
}

// The frame a message syncs in follows the version the app asked for, as queueMessage checks app_target_ver.
func TestServer_SyncFrameFollowsTheAppsVersion(t *testing.T) {
	for _, tt := range []struct {
		ver  byte
		code byte
	}{{1, companion.RespContactMsgRecv}, {3, companion.RespContactMsgRecvV3}} {
		d := newFakeDevice()
		_, addr := newServer(t, d)
		d.emit(Event{Message: &Message{Kind: MessageContact, Text: "hi", SNR: -2.5}})

		got := make(chan companion.Response, 8)
		tr := &tcpTransport{addr: addr}
		tr.SetResponseHandler(func(r companion.Response) { got <- r })
		if err := tr.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer tr.Close()
		tr.Send(companion.DeviceQueryCommand{AppTargetVersion: tt.ver}.ToBytes())
		tr.Send(companion.SyncNextMessageCommand{}.ToBytes())
		for {
			select {
			case r := <-got:
				if r.Code == companion.RespDeviceInfo {
					continue
				}
				if r.Code != tt.code {
					t.Errorf("version %d synced code %d, want %d", tt.ver, r.Code, tt.code)
				}
				if v3, ok := r.Data.(companion.ContactMsgRecvV3Response); ok && v3.SNR != -2.5 {
					t.Errorf("v3 frame SNR %v, want -2.5", v3.SNR)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("version %d: no message synced", tt.ver)
			}
			break
		}
	}
}
