package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/appserver"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/discover"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/node/repeater"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
	"github.com/meshcore-go/OwlShack/internal/trigger"
	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

// repeaterReqTimeout bounds every repeater round-trip initiated from the API.
const repeaterReqTimeout = 10 * time.Second

// backend implements api.Backend for one generation of companions; a reload installs a new instance rather than mutating this one.
type backend struct {
	companions []*companion.Companion
	repeater   *repeater.Repeater // the single running repeater node, or nil
	db         *store.Store
	stats      modem.StatsProvider
	mux        *node.RadioMux
	reload     func() error
	// resetModem asks the supervisor for a reconnect; the same path a vanished serial port takes.
	resetModem func()
	// discover is nil when no node is running to carry a request.
	discover *discover.Service
	// sensors outlives every radio generation: local sensors are not on the mesh.
	sensors *sensor.Hub
	// telemetry is every node's channel map; it outlives a radio generation too.
	telemetry *telemetryPublisher
	// feedPreview keeps what a bot Test fetched across radio generations, so a reconnect mid-edit does not refetch.
	feedPreview *trigger.FeedPreview
	// apps serves companion apps; it outlives every generation, so an app stays connected through a reload.
	apps *appserver.Server
}

func (b *backend) find(name string) (*companion.Companion, bool) {
	for _, c := range b.companions {
		if c.Name() == name {
			return c, true
		}
	}
	return nil, false
}

func (b *backend) Companions() []api.CompanionInfo {
	infos := make([]api.CompanionInfo, 0, len(b.companions))
	for _, c := range b.companions {
		channels := make([]api.ChannelInfo, 0)
		for _, ch := range c.Node().Channels() {
			if ch == nil {
				continue
			}
			channels = append(channels, api.ChannelInfo{Name: ch.Name, PSK: ch.PSK[:]})
		}
		lat, lon := c.LatLon()
		infos = append(infos, api.CompanionInfo{
			ID:        c.ID(),
			Name:      c.Name(),
			PubKey:    hex.EncodeToString(c.Node().Identity().Identity.PublicKeyBytes()),
			PeerCount: c.Node().Peers().Count(),
			Lat:       lat,
			Lon:       lon,
			Channels:  channels,
		})
	}
	return infos
}

// ChannelByHash resolves against live channels, so a new channel is visible without rebuilding the backend.
func (b *backend) ChannelByHash(hash byte) *api.ChannelInfo {
	for _, c := range b.companions {
		for _, ch := range c.Node().Channels() {
			if ch != nil && ch.Hash == hash {
				return &api.ChannelInfo{Name: ch.Name, PSK: ch.PSK[:]}
			}
		}
	}
	return nil
}

// AddPeer registers a contact with every companion's peer table, so it is reachable without waiting for an advert.
func (b *backend) AddPeer(pubkey []byte, name, peerType string) {
	id, err := meshcore.NewIdentityFromBytes(pubkey)
	if err != nil {
		return
	}
	for _, c := range b.companions {
		if c.Node().Peers().Lookup(id.PublicKey()) != nil {
			continue // already known — don't clobber a heard advert
		}
		c.Node().Peers().Insert(&node.Peer{Identity: id, Name: name, Type: peerType, LastSeen: time.Now()})
	}
}

// RemovePeers evicts peers from every companion's in-memory table, not just from the DB.
func (b *backend) RemovePeers(pubkeys [][]byte) {
	for _, pk := range pubkeys {
		if len(pk) != 32 {
			continue
		}
		var key [32]byte
		copy(key[:], pk)
		for _, c := range b.companions {
			c.Node().Peers().Remove(key)
		}
	}
}

func (b *backend) Companion(name string) (api.MessageSender, api.DMSender, bool) {
	c, ok := b.find(name)
	if !ok {
		return nil, nil, false
	}
	return c.SendChannelMessage, c.SendContactMessage, true
}

func (b *backend) ChannelMutator(name string) (api.ChannelAdder, api.ChannelRemover, bool) {
	c, ok := b.find(name)
	if !ok {
		return nil, nil, false
	}
	adder := func(chName, privateKey string) error {
		return c.AddChannel(config.ChannelRef{Name: chName, PrivateKey: privateKey})
	}
	return adder, c.RemoveChannel, true
}

func (b *backend) RenameChannel(companionName, oldName, newName string) error {
	c, ok := b.find(companionName)
	if !ok {
		return fmt.Errorf("companion %q not found", companionName)
	}
	return c.RenameChannel(oldName, newName)
}

func (b *backend) TraceSender(name string) (api.TraceSender, bool) {
	c, ok := b.find(name)
	if !ok {
		return nil, false
	}
	return c.SendTrace, true
}

func (b *backend) AdvertSender(name string) (api.AdvertSender, bool) {
	c, ok := b.find(name)
	if !ok {
		return nil, false
	}
	return c.SendAdvert, true
}

func (b *backend) Repeater(name string) (*api.RepeaterOps, bool) {
	c, ok := b.find(name)
	if !ok {
		return nil, false
	}
	rm := c.Repeaters()
	return &api.RepeaterOps{
		Login: func(pubkeyHex, password string) (any, error) {
			return rm.SendLogin(pubkeyHex, password, repeaterReqTimeout)
		},
		RoomLogin: func(pubkeyHex, password string, syncSince uint32) (any, error) {
			return rm.SendRoomLogin(pubkeyHex, password, syncSince, repeaterReqTimeout)
		},
		StatusReq: func(pubkeyHex string) (any, error) {
			return rm.SendStatusReq(pubkeyHex, repeaterReqTimeout)
		},
		CLI: func(pubkeyHex, command string) (string, error) {
			return rm.SendCLI(pubkeyHex, command, repeaterReqTimeout)
		},
		Session: func(pubkeyHex string) any {
			return rm.Session(pubkeyHex)
		},
		Logout: func(pubkeyHex string) {
			rm.Logout(pubkeyHex)
		},
		PathGet: func(pubkeyHex string) (any, error) {
			return rm.GetPeerPath(pubkeyHex)
		},
		PathReset: func(pubkeyHex string) error {
			return rm.ResetPeerPath(pubkeyHex)
		},
		PathSet: rm.SetPeerPath,
		NeighborsReq: func(pubkeyHex string, count uint8, offset uint16) (any, error) {
			return rm.SendNeighborsReq(pubkeyHex, count, offset, repeaterReqTimeout)
		},
		OwnerInfoReq: func(pubkeyHex string) (any, error) {
			return rm.SendOwnerInfoReq(pubkeyHex, repeaterReqTimeout)
		},
		TelemetryReq: func(pubkeyHex string) (any, error) {
			return rm.SendTelemetryReq(pubkeyHex, repeaterReqTimeout)
		},
		RoomStatusReq: func(pubkeyHex string) (any, error) {
			return rm.SendRoomStatusReq(pubkeyHex, repeaterReqTimeout)
		},
		RoomKeepAlive: func(pubkeyHex string, since uint32) error {
			return rm.SendRoomKeepAlive(pubkeyHex, since)
		},
		SeriesReq: func(pubkeyHex string, startSecsAgo, endSecsAgo uint32) (any, error) {
			return rm.SendSeriesReq(pubkeyHex, startSecsAgo, endSecsAgo, repeaterReqTimeout)
		},
		ContactTelemetryReq: func(pubkeyHex string) (any, error) {
			return rm.SendContactTelemetryReq(pubkeyHex, repeaterReqTimeout)
		},
		AccessList: func(pubkeyHex string) (any, error) {
			return rm.SendAccessListReq(pubkeyHex, repeaterReqTimeout)
		},
		SetPerm: func(pubkeyHex, targetPubkeyHex string, perms uint8) error {
			return rm.SetAccessPerm(pubkeyHex, targetPubkeyHex, perms, repeaterReqTimeout)
		},
	}, true
}

// MqttStatus finds the one companion running the MQTT observer.
func (b *backend) MqttStatus() ([]api.MqttBrokerStatus, bool) {
	for _, c := range b.companions {
		sts, ok := c.MqttStatus()
		if !ok {
			continue
		}
		out := make([]api.MqttBrokerStatus, 0, len(sts))
		for _, s := range sts {
			st := api.MqttBrokerStatus{
				Name:        s.Name,
				Host:        s.Host,
				Port:        s.Port,
				Transport:   s.Transport,
				TLS:         s.TLS,
				AuthType:    s.AuthType,
				Enabled:     s.Enabled,
				Connected:   s.Connected,
				LastError:   s.LastError,
				Published:   s.Published,
				Dropped:     s.Dropped,
				StatusTopic: s.StatusTopic,
			}
			if !s.LastErrorAt.IsZero() {
				st.LastErrorTs = s.LastErrorAt.Unix()
			}
			if !s.ConnectedAt.IsZero() {
				st.ConnectedTs = s.ConnectedAt.Unix()
			}
			out = append(out, st)
		}
		return out, true
	}
	return nil, false
}

// RepeaterNode returns ok=false when no repeater is configured or running.
func (b *backend) RepeaterNode() (*api.RepeaterNodeOps, bool) {
	if b.repeater == nil {
		return nil, false
	}
	rep := b.repeater
	return &api.RepeaterNodeOps{
		Name:       rep.Name(),
		Stats:      func() any { return rep.Stats() },
		Neighbors:  func() any { return rep.Neighbors() },
		Advert:     rep.SendAdvert,
		Discover:   rep.SendDiscover,
		ACL:        func() any { return rep.ACLList() },
		RevokeACL:  rep.RevokeACL,
		SetACL:     rep.SetACL,
		ClearStats: rep.ClearStats,
	}, true
}

// PersistChannels writes each companion's standalone channels back, leaving the rest of the config intact.
func (b *backend) PersistChannels(ctx context.Context) error {
	cfg, err := loadConfigFromDB(ctx, b.db)
	if err != nil {
		return fmt.Errorf("reading config for persist: %w", err)
	}

	byName := make(map[string]int, len(cfg.Companions))
	for i, cc := range cfg.Companions {
		byName[cc.Name] = i
	}
	for _, comp := range b.companions {
		i, ok := byName[comp.Name()]
		if !ok {
			continue
		}
		channels := comp.StandaloneChannels()
		if len(channels) > 0 {
			cl := config.ChannelList(channels)
			cfg.Companions[i].Channels = &cl
		} else {
			cfg.Companions[i].Channels = nil
		}
	}

	if err := saveConfig(ctx, b.db, cfg); err != nil {
		return err
	}

	slog.Info("config persisted with channel changes")
	return nil
}

var _ api.Backend = (*backend)(nil)

// SPIBoards lists the radio hats this build can wire, so the settings UI never
// has to hardcode a board name the binary does not actually support.
func (b *backend) SPIBoards() []api.SPIBoardInfo {
	boards := modem.Boards()
	out := make([]api.SPIBoardInfo, 0, len(boards))
	for _, bd := range boards {
		out = append(out, api.SPIBoardInfo{
			Name:       bd.Name,
			Label:      bd.Label,
			Chip:       bd.Chip,
			SPIPort:    bd.SPIPort,
			MaxTxPower: int(bd.MaxTxPower),
			Verified:   bd.Verified,
			Notes:      bd.Notes,
			HasLEDs:    bd.HasLEDs(),
		})
	}
	return out
}

// RadioStats reports the modem's link counters, so the SPI path's fault counters are readable with MQTT off.
func (b *backend) RadioStats() (api.RadioStatsInfo, bool) {
	return b.radioStats(true)
}

// radioStats builds the radio snapshot. poll asks the board for fresh readings over the wire, which
// costs a 500ms round trip and is right for a diagnostics page a person is looking at; the health
// endpoint passes false so that a monitor scraping it every few seconds cannot put that much
// traffic on the link.
func (b *backend) radioStats(poll bool) (api.RadioStatsInfo, bool) {
	// No provider means the modem never came up. Reporting a zeroed struct here would draw a page of
	// healthy-looking counters for a radio that is not there.
	if b.stats == nil {
		return api.RadioStatsInfo{}, false
	}
	rc := b.stats.RadioConfig()
	ls := b.stats.LinkStats()
	out := api.RadioStatsInfo{
		FreqHz:               rc.FreqHz,
		BwHz:                 rc.BwHz,
		SF:                   rc.SF,
		CR:                   rc.CR,
		TxPower:              rc.TxPower,
		InboundDroppedNew:    ls.InboundDroppedNew,
		HandlerSlow:          ls.HandlerSlow,
		HwDecodeErrors:       ls.HwDecodeErrors,
		InboundDroppedOldest: ls.InboundDroppedOldest,
		RxMetaTimeouts:       ls.RxMetaTimeouts,
		RxMetaMisattributed:  ls.RxMetaMisattributed,
		HwErrors:             ls.HwErrors,
		TxOutcomeLost:        ls.TxOutcomeLost,
		PacketsRecv:          ls.PacketsRecv,
		PacketsSent:          ls.PacketsSent,
		CRCErrors:            ls.CRCErrors,
		RecvErrors:           ls.RecvErrors,
		DriverErrors:         ls.DriverErrors,
		RecvRecoveries:       ls.RecvRecoveries,
		Transport:            b.stats.Transport(),
	}

	// The readings drop out on their own once the modem stops answering, rather than reporting the
	// last known values, on both paths.
	ds := b.stats.CachedStats()
	if poll {
		ds = b.stats.Stats(context.Background())
	}
	out.UptimeSecs = ds.UptimeSecs
	if ds.HaveBattery {
		mv := ds.BatteryMV
		out.BatteryMV = &mv
	}
	if ds.HaveMCUTemp {
		c := ds.MCUTempC
		out.MCUTempC = &c
	}
	if nf := ds.NoiseFloor; nf != 0 {
		out.NoiseFloor = &nf
	}

	if b.mux != nil {
		tx := b.mux.TxStats()
		out.TxSent = tx.Sent
		out.TxFailed = tx.Failed
		out.TxRequeued = tx.BusyRequeued
		out.TxDroppedBusy = tx.BusyDropped
		out.TxDroppedQueue = tx.QueueRejected
	}
	if r, ok := b.stats.(interface{ TxQueueLen() int }); ok {
		out.TxQueueLen = r.TxQueueLen()
	}
	return out, true
}

func (b *backend) ResetModem() {
	if b.resetModem != nil {
		b.resetModem()
	}
}

// StartDiscovery broadcasts a zero-hop discovery. It needs any running node to carry the request,
// not a repeater: the firmware's request is anonymous, so what answers is whatever hears our radio.
func (b *backend) StartDiscovery(types []int) (api.DiscoveryState, bool) {
	if b.discover == nil {
		return api.DiscoveryState{}, false
	}
	if err := b.discover.Start(discover.FilterFor(types...), time.Time{}); err != nil {
		slog.Error("discovery scan failed to send", "error", err)
		return api.DiscoveryState{}, false
	}
	return b.discoveryState(), true
}

func (b *backend) DiscoveryState() (api.DiscoveryState, bool) {
	if b.discover == nil {
		return api.DiscoveryState{}, false
	}
	return b.discoveryState(), true
}

func (b *backend) discoveryState() api.DiscoveryState {
	running, endsAt, results := b.discover.State()
	out := api.DiscoveryState{Running: running, Results: make([]api.DiscoveryInfo, 0, len(results))}
	if running {
		out.SecsLeft = int(time.Until(endsAt).Seconds())
	}
	if !endsAt.IsZero() {
		out.ScanStartedAt = endsAt.Add(-discover.Window).Format(time.RFC3339)
	}
	for _, r := range results {
		out.Results = append(out.Results, api.DiscoveryInfo{
			PubKey:      r.PubKey,
			Name:        b.peerName(r.PubKey),
			Type:        r.Type,
			SNR:         r.SNR,
			ReportedSNR: r.ReportedSNR,
			Heard:       r.Heard.Format(time.RFC3339),
		})
	}
	return out
}

func (b *backend) peerName(pubkeyHex string) string {
	return peerName(context.Background(), b.db, pubkeyHex)
}

// peerName resolves a key against peers we have heard advert from; a node we have never heard from
// stays nameless rather than borrowing one.
func peerName(ctx context.Context, db *store.Store, pubkeyHex string) string {
	raw, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return ""
	}
	p, err := db.Peers.GetByPubKey(ctx, raw)
	if err != nil || p == nil {
		return ""
	}
	return p.Name
}
