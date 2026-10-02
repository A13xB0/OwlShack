package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/companion"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/appserver"
	"github.com/meshcore-go/OwlShack/internal/buildinfo"
	"github.com/meshcore-go/OwlShack/internal/client/repeater"
	"github.com/meshcore-go/OwlShack/internal/config"
	companionnode "github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/store"
)

const (
	firmwareVerCode = 13 // FIRMWARE_VER_CODE of MeshCore v1.17.1, the protocol this speaks
	appMaxContacts  = 510
	appMaxChannels  = 40
	appModel        = "OwlShack"
	// appRequestFloor is the least time a request waits, whatever airtime says, as the UI's own requests do.
	appRequestFloor = 10 * time.Second
)

// Firmware telemetry modes (TELEM_MODE_*), as SELF_INFO and SET_OTHER_PARAMS carry them.
const (
	telemModeDeny       = 0
	telemModeAllowFlags = 1
	telemModeAllowAll   = 2
)

var started = time.Now()

// appDevice is one running companion as a MeshCore companion app sees it.
type appDevice struct {
	c       *companionnode.Companion
	b       *backend
	cfg     *config.Config
	log     *slog.Logger
	appConn config.AppConnection

	mu   sync.Mutex
	subs map[int]func(appserver.Event)
	next int
}

func newAppDevice(c *companionnode.Companion, b *backend, cfg *config.Config, app config.AppConnection) *appDevice {
	return &appDevice{
		c: c, b: b, cfg: cfg, appConn: app,
		log:  slog.Default().With("component", "appserver", "companion", c.Name()),
		subs: make(map[int]func(appserver.Event)),
	}
}

func (d *appDevice) ctx() context.Context { return context.Background() }
func (d *appDevice) id() int64            { return d.c.ID() }

func (d *appDevice) Subscribe(f func(appserver.Event)) func() {
	d.mu.Lock()
	id := d.next
	d.next++
	d.subs[id] = f
	first := len(d.subs) == 1
	d.mu.Unlock()
	if first {
		d.c.SetAppSink(d)
		d.c.Repeaters().SetAppHooks(repeater.AppHooks{CLIText: d.cliText, PathLearned: d.PathUpdated})
	}
	return func() {
		d.mu.Lock()
		delete(d.subs, id)
		last := len(d.subs) == 0
		d.mu.Unlock()
		if last {
			d.c.SetAppSink(nil)
			d.c.Repeaters().SetAppHooks(repeater.AppHooks{})
		}
	}
}

func (d *appDevice) emit(ev appserver.Event) {
	d.mu.Lock()
	subs := make([]func(appserver.Event), 0, len(d.subs))
	for _, f := range d.subs {
		subs = append(subs, f)
	}
	d.mu.Unlock()
	for _, f := range subs {
		f(ev)
	}
}

func (d *appDevice) push(code byte, data any) {
	d.emit(appserver.Event{Push: &companion.Response{Code: code, Data: data}})
}

func prefix6(key [32]byte) (p [6]byte) {
	copy(p[:], key[:6])
	return p
}

// AppSink: what the companion hears, turned into what the app is told.

func (d *appDevice) Message(m companionnode.AppMessage) {
	msg := &appserver.Message{
		Kind: m.Kind, SenderPrefix: prefix6(m.SenderPubKey), AuthorPrefix: m.SenderPrefix, ChannelIdx: m.ChannelIdx,
		TxtType: m.TxtType, SenderTimestamp: m.SenderTimestamp, Text: m.Text,
		DataType: m.DataType, Data: m.Data, PathLen: m.PathLen, SNR: m.SNR,
	}
	d.emit(appserver.Event{Message: msg})
}

func (d *appDevice) cliText(sender [32]byte, ts uint32, text string, pkt *meshcore.Packet) {
	m := companionnode.AppMessage{Kind: companionnode.AppMessageContact, SenderPubKey: sender, TxtType: companion.TxtTypeCLIData, SenderTimestamp: ts, Text: text, PathLen: 0xff}
	if pkt.IsRouteFlood() {
		m.PathLen = pkt.PathLength
	}
	if pkt.HasSignalInfo {
		m.SNR = pkt.SNR
	}
	d.Message(m)
}

// Advert files the sender as a contact when the app's auto-add rules say so, and tells the app either way.
func (d *appDevice) Advert(pkt *meshcore.Packet, adv *meshcore.Advert) {
	key := adv.PublicKey.PublicKey()
	if bytes.Equal(key[:], d.c.Node().Identity().PublicKeyBytes()) {
		return
	}
	appData := adv.AppData()
	if ct, err := d.b.db.Contacts.Get(d.ctx(), d.id(), key[:]); err == nil && ct != nil {
		d.push(companion.PushAdvert, companion.PushAdvertResponse{PublicKey: key})
		return
	}
	prefs := d.Prefs()
	if !autoAdds(prefs, appData.Type, pkt.PathHashCount()) {
		resp := companion.PushNewAdvertResponse{
			PublicKey: key, Type: advertTypeByte(appData.Type), OutPathLen: companion.OutPathUnknown,
			AdvertName: appData.Name, LastAdvert: adv.Timestamp, AdvertLatitude: appData.Lat, AdvertLongitude: appData.Lon,
			LastModified: uint32(time.Now().Unix()),
		}
		d.push(companion.PushNewAdvert, resp)
		return
	}
	id := d.id()
	d.b.db.WriteAsync(func() {
		if err := d.b.db.Contacts.Add(context.Background(), id, key[:], appData.Name, appData.Type); err != nil {
			d.log.Error("auto-adding a contact for the app", "error", err)
		}
	})
	d.push(companion.PushAdvert, companion.PushAdvertResponse{PublicKey: key})
}

// autoAdds is shouldAutoAddContactType plus the max-hops limit: manual add off takes everyone, on takes only the types the app ticked.
func autoAdds(p appserver.Prefs, advType string, hops uint8) bool {
	if p.AutoAddMaxHops > 0 && hops > p.AutoAddMaxHops {
		return false
	}
	if p.ManualAdd&1 == 0 {
		return true
	}
	bit := map[string]byte{"CHAT": companion.AutoAddChat, "REPEATER": companion.AutoAddRepeater, "ROOM": companion.AutoAddRoomServer, "SENSOR": companion.AutoAddSensor}[advType]
	return p.AutoAddConfig&bit != 0
}

func (d *appDevice) PathUpdated(key [32]byte) {
	d.push(companion.PushPathUpdated, companion.PushPathUpdatedResponse{PublicKey: key})
}

func (d *appDevice) RawRX(data []byte, snr float32, rssi int8) {
	if len(data)+3 > companion.MaxFrameSize {
		return
	}
	d.push(companion.PushLogRxData, companion.PushLogRxDataResponse{LastSNR: snr, LastRSSI: rssi, Raw: append([]byte(nil), data...)})
}

func (d *appDevice) Trace(pkt *meshcore.Packet, tr *meshcore.Trace) {
	d.push(companion.PushTraceData, companion.PushTraceDataResponse{
		PathLen: byte(len(tr.PathHashes)), Flags: tr.Flags, Tag: tr.Tag, AuthCode: tr.AuthCode,
		PathHashes: tr.PathHashes, PathSnrs: append([]byte(nil), pkt.Path...), LastSNR: pkt.SNR,
	})
}

func (d *appDevice) Control(pkt *meshcore.Packet) {
	d.push(companion.PushControlData, companion.PushControlDataResp{SNR: pkt.SNR, RSSI: pkt.RSSI, PathLen: pkt.PathLength, Payload: append([]byte(nil), pkt.Payload...)})
}

func (d *appDevice) RawCustom(pkt *meshcore.Packet) {
	d.push(companion.PushRawData, companion.PushRawDataResponse{LastSNR: pkt.SNR, LastRSSI: pkt.RSSI, Payload: append([]byte(nil), pkt.Payload...)})
}

// The radio, the node and its preferences.

func (d *appDevice) companionRow() store.Companion {
	if c, err := d.b.db.Companions.Get(d.ctx(), d.id()); err == nil && c != nil {
		return *c
	}
	return store.Companion{ID: d.id(), Name: d.c.Name()}
}

func (d *appDevice) SelfInfo() companion.SelfInfoResponse {
	cc := d.c.Config()
	p := d.Prefs()
	r := companion.SelfInfoResponse{
		AdvertType: meshcore.AdvertTypeChat, PublicKey: d.c.Node().Identity().PublicKey(),
		MaxTxPower: 22, ManualAddContacts: p.ManualAdd, Name: cc.Name,
		Reserved: [3]byte{p.MultiAcks, p.AdvLocPolicy, p.TelemetryModes},
	}
	if cc.Latitude != nil && cc.Longitude != nil {
		r.AdvertLatitude = int32(math.Round(*cc.Latitude * 1e6))
		r.AdvertLongitude = int32(math.Round(*cc.Longitude * 1e6))
	}
	if d.cfg.TX != nil {
		r.TxPower = *d.cfg.TX
	}
	if d.cfg.Freq != nil {
		r.RadioFrequency = uint32(math.Round(*d.cfg.Freq * 1000))
	}
	if d.cfg.Bw != nil {
		r.RadioBandwidth = uint32(math.Round(*d.cfg.Bw * 1000))
	}
	if d.cfg.SF != nil {
		r.RadioSpreadFactor = *d.cfg.SF
	}
	if d.cfg.CR != nil {
		r.RadioCodingRate = *d.cfg.CR
	}
	return r
}

func (d *appDevice) DeviceInfo() companion.DeviceInfoResponse {
	return companion.DeviceInfoResponse{
		FirmwareVersion: firmwareVerCode, MaxContacts: appMaxContacts, MaxChannels: appMaxChannels,
		BLEPin: d.Prefs().BLEPin, FirmwareBuildDate: buildinfo.Date, Model: appModel,
		FirmwareVersionStr: buildinfo.Version, PathHashMode: d.c.PathHashSize() - 1,
	}
}

func (d *appDevice) BattAndStorage() companion.BattAndStorageResponse {
	var r companion.BattAndStorageResponse
	if d.b.stats != nil {
		if ds := d.b.stats.CachedStats(); ds.HaveBattery {
			r.BatteryMilliVolts = ds.BatteryMV
		}
	}
	return r
}

func (d *appDevice) Stats(kind byte) companion.StatsResponse {
	r := companion.StatsResponse{StatsType: kind}
	var ds struct {
		battery uint16
		noise   int16
	}
	if d.b.stats != nil {
		cs := d.b.stats.CachedStats()
		if cs.HaveBattery {
			ds.battery = cs.BatteryMV
		}
		ds.noise = cs.NoiseFloor
	}
	tx := d.c.Node().TxStats()
	switch kind {
	case companion.StatsTypeCore:
		r.Core = &companion.CoreStats{BatteryMV: ds.battery, UptimeSecs: uint32(time.Since(started).Seconds()), QueueLen: byte(min(d.c.Node().TxQueueLen(), 255))}
	case companion.StatsTypeRadio:
		r.Radio = &companion.RadioStats{NoiseFloor: ds.noise}
	case companion.StatsTypePackets:
		r.Packets = &companion.PacketStats{PacketsSent: uint32(tx.Sent)}
	}
	return r
}

func telemModeByte(m string) byte {
	switch config.TelemetryModeOrDefault(&m) {
	case config.TelemetrySelected:
		return telemModeAllowFlags
	case config.TelemetryContacts:
		return telemModeAllowAll
	}
	return telemModeDeny
}

func telemModeName(b byte) string {
	switch b & 3 {
	case telemModeAllowFlags:
		return config.TelemetrySelected
	case telemModeAllowAll:
		return config.TelemetryContacts
	}
	return config.TelemetryDeny
}

func (d *appDevice) Prefs() appserver.Prefs {
	a, err := d.b.db.CompanionApps.Get(d.ctx(), d.id())
	if err != nil {
		a = store.DefaultCompanionApp(d.id())
	}
	row := d.companionRow()
	return appserver.Prefs{
		ManualAdd: a.ManualAdd, AdvLocPolicy: a.AdvLocPolicy, MultiAcks: a.MultiAcks,
		AutoAddConfig: a.AutoAddConfig, AutoAddMaxHops: a.AutoAddMaxHops,
		RxDelayBaseMs: a.RxDelayBaseMs, AirtimeFactorMs: a.AirtimeFactorMs, BLEPin: a.BLEPin,
		TelemetryModes: telemModeByte(row.TelemEnv)<<4 | telemModeByte(row.TelemLoc)<<2 | telemModeByte(row.TelemBase),
	}
}

func (d *appDevice) SetPrefs(p appserver.Prefs) error {
	old := d.Prefs()
	a := store.CompanionApp{
		CompanionID: d.id(), ManualAdd: p.ManualAdd, AutoAddConfig: p.AutoAddConfig, AutoAddMaxHops: p.AutoAddMaxHops,
		AdvLocPolicy: p.AdvLocPolicy, MultiAcks: p.MultiAcks, RxDelayBaseMs: p.RxDelayBaseMs, AirtimeFactorMs: p.AirtimeFactorMs, BLEPin: p.BLEPin,
	}
	var err error
	d.b.db.WriteSync(func() { err = d.b.db.CompanionApps.SetPrefs(d.ctx(), a) })
	if err != nil {
		return appserver.ErrFileIO
	}
	if p.TelemetryModes != old.TelemetryModes {
		in := api.CompanionTelemetryInput{Base: telemModeName(p.TelemetryModes), Location: telemModeName(p.TelemetryModes >> 2), Environment: telemModeName(p.TelemetryModes >> 4)}
		if err := d.b.SetCompanionTelemetry(d.ctx(), d.id(), in); err != nil {
			return appserver.ErrFileIO
		}
	}
	return nil
}

// saveCompanion edits the companion as the Companions page does, carrying every field the edit does not touch.
func (d *appDevice) saveCompanion(edit func(*api.CompanionInput)) error {
	row := d.companionRow()
	scope := row.FloodScope
	in := api.CompanionInput{
		ID: row.ID, Name: row.Name, DMPolicy: row.DMPolicy, DMAllow: row.DMAllow,
		Latitude: row.Latitude, Longitude: row.Longitude, AdvertInterval: row.AdvertInterval,
		PathHashSize: row.PathHashSize, FloodScope: &scope,
	}
	edit(&in)
	if _, err := d.b.SaveCompanion(d.ctx(), in); err != nil {
		d.log.Info("an app's settings change was refused", "error", err)
		return appserver.ErrIllegalArg
	}
	return nil
}

func (d *appDevice) SetName(name string) error {
	if strings.TrimSpace(name) == "" {
		return appserver.ErrIllegalArg
	}
	return d.saveCompanion(func(in *api.CompanionInput) { in.Name = name })
}

func (d *appDevice) SetLatLon(lat, lon int32) error {
	la, lo := float64(lat)/1e6, float64(lon)/1e6
	return d.saveCompanion(func(in *api.CompanionInput) { in.Latitude, in.Longitude = &la, &lo })
}

func (d *appDevice) SetPathHashMode(mode byte) error {
	size := int(mode) + 1
	return d.saveCompanion(func(in *api.CompanionInput) { in.PathHashSize = &size })
}

func (d *appDevice) DefaultScope() (string, []byte) {
	name := d.c.Config().FloodScope
	if name == "" {
		return "", nil
	}
	key := config.ScopeRegion(name).Key
	return strings.TrimPrefix(name, "#"), key[:]
}

// SetDefaultScope takes a hashtag region, whose key its name derives; a private region's key is refused, as OwlShack keeps none.
func (d *appDevice) SetDefaultScope(name string, key []byte) error {
	if name != "" {
		want := config.ScopeRegion(name).Key
		if !bytes.Equal(key, want[:]) {
			return appserver.ErrIllegalArg
		}
	}
	return d.saveCompanion(func(in *api.CompanionInput) { in.FloodScope = &name })
}

func (d *appDevice) SendSelfAdvert(flood bool) error {
	if err := d.c.SendAdvert(flood); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) SelfAdvert() ([]byte, error) {
	pkt, err := d.c.SelfAdvertPacket()
	if err != nil {
		return nil, appserver.ErrTableFull
	}
	return pkt.ToBytes()
}

func (d *appDevice) SelfTelemetry() []byte { return d.c.SelfTelemetry() }

// PrivateKey is the firmware's 64-byte prv.key form, the clamped scalar and prefix, when the operator allowed export.
func (d *appDevice) PrivateKey() ([64]byte, bool) {
	var out [64]byte
	if !d.appConn.AllowKeyExport {
		return out, false
	}
	raw, err := hex.DecodeString(d.c.Config().PrivateKey)
	switch {
	case err != nil:
		return out, false
	case len(raw) == 64:
		copy(out[:], raw)
	case len(raw) == 32:
		h := sha512.Sum512(raw)
		h[0] &= 248
		h[31] &= 127
		h[31] |= 64
		copy(out[:], h[:])
	default:
		return out, false
	}
	return out, true
}

func (d *appDevice) Sign(data []byte) ([64]byte, error) {
	var sig [64]byte
	copy(sig[:], d.c.Node().Identity().Sign(data))
	return sig, nil
}

// Contacts.

func advertTypeByte(t string) byte {
	switch t {
	case "CHAT":
		return meshcore.AdvertTypeChat
	case "REPEATER":
		return meshcore.AdvertTypeRepeater
	case "ROOM":
		return meshcore.AdvertTypeRoom
	case "SENSOR":
		return meshcore.AdvertTypeSensor
	}
	return meshcore.AdvertTypeNone
}

func advertTypeName(b byte) string {
	return map[byte]string{meshcore.AdvertTypeChat: "CHAT", meshcore.AdvertTypeRepeater: "REPEATER", meshcore.AdvertTypeRoom: "ROOM", meshcore.AdvertTypeSensor: "SENSOR"}[b]
}

func pathLenByte(path []byte, hashSize uint8) byte {
	if path == nil {
		return companion.OutPathUnknown
	}
	hs := max(hashSize, 1)
	return (hs-1)<<6 | byte(len(path)/int(hs))
}

func (d *appDevice) contactResponse(ct store.Contact) companion.ContactResponse {
	var key [32]byte
	copy(key[:], ct.PeerPubKey)
	path, hs := ct.OutPath, ct.OutPathHashSize
	if p := d.c.Node().Peers().Lookup(key); p != nil {
		path, hs = p.OutPath, p.OutPathHashSize
	}
	r := companion.ContactResponse{
		PublicKey: key, Type: advertTypeByte(ct.Type), Flags: ct.Flags, OutPathLen: pathLenByte(path, hs),
		AdvertName: ct.Name, LastAdvert: ct.LastAdvertTS, AdvertLatitude: ct.Lat, AdvertLongitude: ct.Lon, LastModified: ct.LastMod,
	}
	copy(r.OutPath[:], path)
	return r
}

func (d *appDevice) Contacts() []companion.ContactResponse {
	list, err := d.b.db.Contacts.List(d.ctx(), d.id())
	if err != nil {
		d.log.Error("listing contacts for an app", "error", err)
		return nil
	}
	out := make([]companion.ContactResponse, 0, len(list))
	for _, ct := range list {
		if len(ct.PeerPubKey) == 32 {
			out = append(out, d.contactResponse(ct))
		}
	}
	return out
}

func (d *appDevice) Contact(key [32]byte) (companion.ContactResponse, bool) {
	ct, err := d.b.db.Contacts.Get(d.ctx(), d.id(), key[:])
	if err != nil || ct == nil {
		return companion.ContactResponse{}, false
	}
	return d.contactResponse(*ct), true
}

func (d *appDevice) FindContact(prefix []byte) ([32]byte, bool) {
	list, err := d.b.db.Contacts.List(d.ctx(), d.id())
	if err != nil {
		return [32]byte{}, false
	}
	for _, ct := range list {
		if len(ct.PeerPubKey) == 32 && bytes.HasPrefix(ct.PeerPubKey, prefix) {
			return [32]byte(ct.PeerPubKey), true
		}
	}
	return [32]byte{}, false
}

// ensurePeer puts a contact in the node's peer table, which every send to it routes from.
func (d *appDevice) ensurePeer(key [32]byte, name string) {
	peers := d.c.Node().Peers()
	if peers.Lookup(key) == nil {
		peers.Insert(&node.Peer{Identity: meshcore.NewIdentity(key), Name: name})
	}
}

func (d *appDevice) PutContact(c companion.AddUpdateContactCommand) error {
	id, key := d.id(), c.PublicKey
	var err error
	d.b.db.WriteSync(func() {
		ctx := context.Background()
		if err = d.b.db.Contacts.Add(ctx, id, key[:], c.Name, advertTypeName(c.Type)); err != nil {
			return
		}
		if err = d.b.db.Contacts.SetFlags(ctx, id, key[:], c.Flags); err != nil {
			return
		}
		if !c.OmitLocation {
			err = d.b.db.Contacts.SetLocation(ctx, id, key[:], c.Latitude, c.Longitude)
		}
	})
	if err != nil {
		return appserver.ErrTableFull
	}
	d.ensurePeer(key, c.Name)
	if c.OutPathLen != companion.OutPathUnknown && meshcore.IsValidPathLen(c.OutPathLen) {
		hs := c.OutPathLen>>6 + 1
		path := append([]byte{}, c.OutPath[:int(hs)*int(c.OutPathLen&63)]...)
		return d.setRoute(key, path, hs)
	}
	return nil
}

func (d *appDevice) setRoute(key [32]byte, path []byte, hs uint8) error {
	ct, err := d.b.db.Contacts.Get(d.ctx(), d.id(), key[:])
	if err != nil || ct == nil {
		return appserver.ErrNotFound
	}
	if path == nil {
		d.c.Node().Peers().ResetOutPath(key)
	} else {
		d.c.Node().Peers().SetOutPath(key, path, hs)
	}
	bytesPerHop := ct.PathHashSize
	if path != nil {
		bytesPerHop = hs
	}
	d.b.db.WriteSync(func() { err = d.b.db.Contacts.SetRoute(context.Background(), d.id(), key[:], path, bytesPerHop) })
	return err
}

func (d *appDevice) RemoveContact(key [32]byte) error {
	if _, ok := d.Contact(key); !ok {
		return appserver.ErrNotFound
	}
	var err error
	d.b.db.WriteSync(func() { err = d.b.db.Contacts.Delete(context.Background(), d.id(), key[:]) })
	if err != nil {
		return appserver.ErrNotFound
	}
	return nil
}

func (d *appDevice) ResetPath(key [32]byte) error { return d.setRoute(key, nil, 0) }

func (d *appDevice) advertPacket(key [32]byte) (*meshcore.Packet, error) {
	if _, ok := d.Contact(key); !ok {
		return nil, appserver.ErrNotFound
	}
	payload, err := d.b.db.Peers.Advert(d.ctx(), key[:])
	if err != nil || len(payload) == 0 {
		return nil, appserver.ErrNotFound
	}
	return &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), Payload: payload}, nil
}

// ShareContact re-sends the contact's own advert zero-hop, so neighbours hear it as if from them.
func (d *appDevice) ShareContact(key [32]byte) error {
	pkt, err := d.advertPacket(key)
	if err != nil {
		return err
	}
	pkt.Header = meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeAdvert, 0)
	if err := d.c.Node().SendPacket(pkt); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) ExportContact(key [32]byte) ([]byte, error) {
	pkt, err := d.advertPacket(key)
	if err != nil {
		return nil, err
	}
	return pkt.ToBytes()
}

func (d *appDevice) ImportContact(raw []byte) error {
	pkt, err := meshcore.PacketFromBytes(raw)
	if err != nil || pkt.PayloadType() != meshcore.PayloadTypeAdvert {
		return appserver.ErrIllegalArg
	}
	adv, err := meshcore.AdvertFromBytes(pkt.Payload)
	if err != nil || !adv.Verify() {
		return appserver.ErrIllegalArg
	}
	key := adv.PublicKey.PublicKey()
	appData := adv.AppData()
	peer := &store.Peer{PubKey: key[:], Name: appData.Name, Type: appData.Type, Lat: appData.Lat, Lon: appData.Lon, LastAdvertTS: adv.Timestamp, LastSeen: time.Now()}
	id := d.id()
	d.b.db.WriteSync(func() {
		ctx := context.Background()
		if err = d.b.db.Peers.Upsert(ctx, peer); err == nil {
			if err = d.b.db.Peers.SetAdvert(ctx, key[:], pkt.Payload); err == nil {
				err = d.b.db.Contacts.Add(ctx, id, key[:], appData.Name, appData.Type)
			}
		}
	})
	if err != nil {
		return appserver.ErrIllegalArg
	}
	d.ensurePeer(key, appData.Name)
	return nil
}

func (d *appDevice) AdvertPath(key [32]byte) (companion.AdvertPathResponse, bool) {
	p, err := d.b.db.Peers.GetByPubKey(d.ctx(), key[:])
	if err != nil || p == nil || p.OutPath == nil {
		return companion.AdvertPathResponse{}, false
	}
	return companion.AdvertPathResponse{RecvTimestamp: uint32(p.LastSeen.Unix()), PathLen: pathLenByte(p.OutPath, p.OutPathHashSize), Path: p.OutPath}, true
}

// Channels: an app's slot is the channel's place in the companion's list.

func (d *appDevice) Channel(idx byte) (companion.ChannelInfoResponse, bool) {
	ch := d.c.Node().Channel(int(idx))
	if ch == nil {
		// Firmware answers every slot below MAX_GROUP_CHANNELS, an unused one with no name and a
		// zero key; apps read them all and skip the blank ones.
		return companion.ChannelInfoResponse{ChannelIdx: idx}, int(idx) < appMaxChannels
	}
	return companion.ChannelInfoResponse{ChannelIdx: idx, Name: ch.Name, Secret: ch.PSK}, true
}

// SetChannel writes slot idx: an empty name clears it, the next free slot adds one, and an occupied slot is renamed or rekeyed in place.
func (d *appDevice) SetChannel(idx byte, name string, secret [16]byte) error {
	rows, err := d.b.db.Channels.ListByCompanion(d.ctx(), d.id())
	if err != nil {
		return appserver.ErrFileIO
	}
	if int(idx) >= appMaxChannels {
		return appserver.ErrNotFound
	}
	if name == "" && int(idx) >= len(rows) {
		// Clearing a slot that holds nothing succeeds, as on firmware: RemoteTerm clears every slot it reads.
		return nil
	}
	if int(idx) > len(rows) {
		return appserver.ErrNotFound
	}
	key := channelKey(name, secret)
	if name != "" && int(idx) < len(rows) && rows[idx].Name == name && rows[idx].PrivateKey == key {
		// RemoteTerm loads its channel into the same slot before every post; an unchanged slot writes nothing.
		return nil
	}
	if name == "" {
		if err := d.b.DeleteChannel(d.ctx(), rows[idx].ID); err != nil {
			return appserver.ErrNotFound
		}
	} else {
		in := api.ChannelInput{CompanionID: d.id(), Name: name, PrivateKey: &key}
		if int(idx) < len(rows) {
			in.ID = rows[idx].ID
		}
		if _, err := d.b.SaveChannel(d.ctx(), in); err != nil {
			d.log.Info("an app's channel change was refused", "error", err)
			return appserver.ErrIllegalArg
		}
	}
	// The reload the save starts applies the change too, but later: an app sends on the slot straight after OK.
	if err := d.c.SetChannels(d.channelList()); err != nil {
		d.log.Warn("applying an app's channel change to the running companion", "error", err)
	}
	return nil
}

// channelList is the companion's stored channels in slot order, as config carries them.
func (d *appDevice) channelList() config.ChannelList {
	rows, err := d.b.db.Channels.ListByCompanion(d.ctx(), d.id())
	if err != nil {
		return nil
	}
	list := make(config.ChannelList, len(rows))
	for i, r := range rows {
		list[i] = config.ChannelRef{Name: r.Name, PrivateKey: r.PrivateKey}
	}
	return list
}

// channelKey stores no key for a channel whose name derives it, as the Channels page does, and the hex key otherwise.
func channelKey(name string, secret [16]byte) string {
	if strings.EqualFold(name, "Public") {
		if pub, err := meshcore.NewChannelFromBase64("Public", "izOH6cXN6mrJ5e26oRXNcg=="); err == nil && pub.PSK == secret {
			return ""
		}
	}
	if strings.HasPrefix(name, "#") && meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag(name)).PSK == secret {
		return ""
	}
	return hex.EncodeToString(secret[:])
}

// Sending.

func sendOpts(s appserver.Scope) []node.SendOption {
	switch {
	case !s.Set:
		return nil
	case s.Unscoped:
		return []node.SendOption{node.Unscoped()}
	default:
		return []node.SendOption{node.InScope(s.Region)}
	}
}

func ms(d time.Duration) uint32 { return uint32(d / time.Millisecond) }

func (d *appDevice) SendText(key [32]byte, txtType, attempt byte, ts uint32, text string, scope appserver.Scope, onAck func(ack, rtt uint32)) (companion.SentResponse, error) {
	if txtType == companion.TxtTypeCLIData {
		ts = d.c.Repeaters().UniqueTimestamp() // the node's clock, as the firmware sends CLI data, so a repeater never sees a replay
	}
	var ack uint32
	sent, err := d.c.SendAppText(meshcore.NewIdentity(key), txtType, int(attempt), ts, text, func(r node.DMSendResult) {
		if r.Confirmed {
			onAck(ack, ms(r.RoundTrip))
		}
	}, sendOpts(scope)...)
	if err != nil {
		d.log.Info("an app's DM could not be sent", "error", err)
		return companion.SentResponse{}, appserver.ErrTableFull
	}
	ack = sent.AckCRC
	return companion.SentResponse{IsFlood: sent.Flood, Tag: sent.AckCRC, EstTimeout: ms(sent.Timeout), HasExtended: true}, nil
}

func (d *appDevice) SendChannelText(idx byte, ts uint32, text string, scope appserver.Scope) error {
	if d.c.Node().Channel(int(idx)) == nil {
		return appserver.ErrNotFound
	}
	if err := d.c.SendAppChannelText(int(idx), ts, text, sendOpts(scope)...); err != nil {
		return appserver.ErrNotFound
	}
	return nil
}

func (d *appDevice) SendChannelData(idx byte, path []byte, flood bool, dataType uint16, data []byte, scope appserver.Scope) error {
	ch := d.c.Node().Channel(int(idx))
	if ch == nil {
		return appserver.ErrNotFound
	}
	hs := d.c.PathHashSize()
	if flood {
		path = nil
	} else if path == nil {
		path = []byte{}
	}
	if err := d.c.Node().SendGroupData(ch, dataType, data, path, hs, sendOpts(scope)...); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) SendRawData(path, data []byte) error {
	pkt := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeRawCustom, 0), PathLength: byte(len(path)), Path: path, Payload: data}
	if err := d.c.Node().SendPacket(pkt); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) SendRawPacket(priority byte, raw []byte) error {
	pkt, err := meshcore.PacketFromBytes(raw)
	if err != nil {
		return appserver.ErrIllegalArg
	}
	if err := d.c.Node().SendPacketDelayed(pkt, priority, 0); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) SendControlData(data []byte) error {
	pkt := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0), Payload: data}
	if err := d.c.Node().SendPacket(pkt); err != nil {
		return appserver.ErrTableFull
	}
	return nil
}

func (d *appDevice) SendTrace(tag, auth uint32, flags byte, path []byte) (companion.SentResponse, error) {
	hs := uint8(1) << (flags & 3)
	if err := d.c.SendAppTrace(tag, auth, path, hs); err != nil {
		return companion.SentResponse{}, appserver.ErrTableFull
	}
	airtime := uint32(0)
	if d.b.stats != nil {
		airtime = d.b.stats.EstAirtimeMs(len(path) + 11)
	}
	return companion.SentResponse{Tag: tag, EstTimeout: ms(node.CalcDirectTimeout(airtime, uint8(len(path)/int(hs)))), HasExtended: true}, nil
}

// Requests: answered now with the tag and timeout, and the reply pushed when it comes.

func (d *appDevice) roomSyncSince(key [32]byte) *uint32 {
	ct, err := d.b.db.Contacts.Get(d.ctx(), d.id(), key[:])
	if err != nil || ct == nil || ct.Type != "ROOM" {
		return nil
	}
	since := uint32(0)
	if msgs, err := d.b.db.Messages.List(d.ctx(), d.id(), "dm:"+hex.EncodeToString(key[:]), 1, 0); err == nil && len(msgs) > 0 {
		since = uint32(msgs[0].Timestamp.Unix())
	}
	return &since
}

func (d *appDevice) Login(key [32]byte, password string) (companion.SentResponse, error) {
	d.ensurePeer(key, "")
	p, err := d.c.Repeaters().StartLogin(hex.EncodeToString(key[:]), password, d.roomSyncSince(key), appRequestFloor)
	if err != nil {
		d.log.Info("an app's login could not be sent", "error", err)
		return companion.SentResponse{}, appserver.ErrTableFull
	}
	go func() {
		data, err := p.Wait(context.Background())
		if err != nil {
			return // the firmware stays silent on a timeout too
		}
		r := companion.PushLoginSuccessResponse{PubKeyPrefix: prefix6(key), HasServerInfo: len(data) >= 13}
		if len(data) > 6 {
			r.Permissions = data[6]
		}
		if r.HasServerInfo {
			r.ServerTime, r.ACL, r.FirmwareLevel = binary.LittleEndian.Uint32(data[:4]), data[7], data[12]
		}
		d.push(companion.PushLoginSuccess, r)
	}()
	return companion.SentResponse{IsFlood: p.Flood, Tag: binary.LittleEndian.Uint32(key[:4]), EstTimeout: ms(p.Timeout), HasExtended: true}, nil
}

// requestBody is the firmware's 9-byte typed request: the type, four reserved bytes, and four random ones that keep the packet hash unique.
func requestBody(reqType, perms byte) []byte {
	body := make([]byte, 9)
	body[0], body[1] = reqType, perms
	_, _ = rand.Read(body[5:])
	return body
}

const (
	reqTypeGetStatus        = 0x01
	reqTypeGetTelemetryData = 0x03
	telemPermBase           = 0x01
)

func (d *appDevice) Request(key [32]byte, kind appserver.RequestKind, data []byte, scope appserver.Scope) (companion.SentResponse, error) {
	d.ensurePeer(key, "")
	opts := repeater.RequestOptions{Floor: appRequestFloor}
	body := data
	switch kind {
	case appserver.RequestStatus:
		body = requestBody(reqTypeGetStatus, 0)
	case appserver.RequestTelemetry:
		body = requestBody(reqTypeGetTelemetryData, 0)
	case appserver.RequestPathDiscovery:
		body, opts.ForceFlood = requestBody(reqTypeGetTelemetryData, ^byte(telemPermBase)), true
	case appserver.RequestAnon:
		opts.Anon = true
	}
	p, err := d.c.Repeaters().StartRequest(key, body, opts)
	if err != nil {
		d.log.Info("an app's request could not be sent", "error", err)
		return companion.SentResponse{}, appserver.ErrTableFull
	}
	go d.awaitReply(key, kind, p)
	return companion.SentResponse{IsFlood: p.Flood, Tag: p.Tag, EstTimeout: ms(p.Timeout), HasExtended: true}, nil
}

func (d *appDevice) awaitReply(key [32]byte, kind appserver.RequestKind, p *repeater.Pending) {
	data, err := p.Wait(context.Background())
	if err != nil {
		return
	}
	switch kind {
	case appserver.RequestStatus:
		d.push(companion.PushStatusResponse, companion.PushStatusResp{PubKeyPrefix: prefix6(key), StatusData: data})
	case appserver.RequestTelemetry:
		d.push(companion.PushTelemetryResponse, companion.PushTelemetryResp{PubKeyPrefix: prefix6(key), LPPData: data})
	case appserver.RequestPathDiscovery:
		pr, ok := p.Path()
		if !ok {
			return // the reply came back without the route that is the whole point
		}
		d.push(companion.PushPathDiscoveryResponse, companion.PushPathDiscoveryResp{
			PubKeyPrefix: prefix6(key), OutPathLen: pr.OutPathLen, OutPath: pr.OutPath, InPathLen: pr.InPathLen, InPath: pr.InPath,
		})
	default:
		d.push(companion.PushBinaryResponse, companion.PushBinaryResp{Tag: p.Tag, ResponseData: data})
	}
}

func (d *appDevice) HasConnection(key [32]byte) bool {
	return d.c.Repeaters().Session(hex.EncodeToString(key[:])) != nil
}

func (d *appDevice) Logout(key [32]byte) { d.c.Repeaters().Logout(hex.EncodeToString(key[:])) }

var _ appserver.Device = (*appDevice)(nil)
var _ companionnode.AppSink = (*appDevice)(nil)

func appPortStatus(s appserver.PortStatus) api.AppPortStatus {
	out := api.AppPortStatus{CompanionID: s.CompanionID, Addr: s.Addr, Error: s.Error, Client: s.Client, AppName: s.AppName, Replaced: s.Replaced}
	if !s.Since.IsZero() {
		out.Since = s.Since.UTC().Format(time.RFC3339)
	}
	return out
}

func (b *backend) AppServerStatus() []api.AppPortStatus {
	if b.apps == nil {
		return nil
	}
	var out []api.AppPortStatus
	for _, s := range b.apps.Status() {
		out = append(out, appPortStatus(s))
	}
	return out
}

// applyApps opens the app ports this config asks for and points each at its running companion.
func applyApps(apps *appserver.Server, b *backend, cfg *config.Config) {
	if apps == nil || cfg == nil {
		return
	}
	running := make(map[int64]*companionnode.Companion, len(b.companions))
	for _, c := range b.companions {
		running[c.ID()] = c
	}
	var ports []appserver.Port
	devices := make(map[int64]appserver.Device)
	for _, cc := range cfg.Companions {
		if cc.App == nil || cc.App.Port == 0 {
			continue
		}
		ports = append(ports, appserver.Port{CompanionID: cc.ID, Addr: cc.App.Addr()})
		if c := running[cc.ID]; c != nil {
			devices[cc.ID] = newAppDevice(c, b, cfg, *cc.App)
		}
	}
	apps.Apply(ports, devices)
}
