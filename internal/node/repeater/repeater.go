// Package repeater is the repeater personality we run: relay policy, adverts and neighbours, with meshcore-go's router doing the forwarding.
package repeater

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/hardware"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// neighbor is a directly-heard zero-hop repeater (firmware NeighbourInfo), in memory only and lost on restart.
type neighbor struct {
	pubkey [32]byte
	name   string
	snr    float64
	heard  time.Time
}

type Repeater struct {
	cfg config.RepeaterConfig

	node  *node.Node
	radio node.MuxRadio
	log   *slog.Logger

	store *store.Store
	hub   *api.Hub

	startedAt time.Time
	recvCount atomic.Uint64 // raw packets received (radio raw handler)
	fwdCount  atomic.Uint64 // packets we relayed (allowForward returned true)

	// TX counters; RX / dup counters come from the router, rebased at `clear stats` via statsBase.
	sentFlood  atomic.Uint64
	sentDirect atomic.Uint64
	routeStats func() node.RouteStats
	statsBase  node.RouteStats
	sf         uint8 // spreading factor, for the rx-delay packet score (0 = unknown)

	// Last heard signal and accumulated airtime; airtime is nil when the radio params are unknown, and is set before the node is live.
	lastRSSI    atomic.Int32
	lastSNRx4   atomic.Int32
	haveSignal  atomic.Bool
	rxAirtimeMs atomic.Uint64
	txAirtimeMs atomic.Uint64
	airtime     func(packetLen int) uint32
	logging     atomic.Bool // `log start/stop` — per-packet trace to the bot log

	// Cached modem readings, refreshed by deviceStatsLoop.
	noiseFloor      atomic.Int32
	batteryMV       atomic.Uint32
	haveBattery     atomic.Bool
	haveDeviceStats atomic.Bool
	pollStats       func(ctx context.Context) DeviceStats
	// telemetry is the operator's channel map and the readings behind it; nil publishes no sensors.
	telemetry   func() ([]sensor.ChannelEntry, []sensor.Status)
	mcuTempC    atomic.Int32 // tenths of a degree C
	haveMCUTemp atomic.Bool

	// reconfigure persists + validates + reloads a config change; nil disables config writes, and it restarts this node so it must stay off the dispatch path.
	reconfigure func(mutate func(*config.RepeaterConfig)) error

	// discover tracks an in-flight `discover.neighbors` request; responses matching the tag inside the window become neighbours.
	discover struct {
		sync.Mutex
		tag   uint32
		until time.Time
	}

	// Gate unauthenticated replies (firmware anon_limiter / discover_limiter) so a spammer can't turn us into a beacon.
	anonLimiter     *rateLimiter
	discoverLimiter *rateLimiter

	neighbors struct {
		sync.Mutex
		m map[[32]byte]*neighbor
	}

	// routes caches each admin client's return path from its flood login; an unknown client gets a flooded reply, so a restart just relearns.
	routes struct {
		sync.Mutex
		m map[[32]byte]clientRoute
	}

	// acl mirrors the persisted ACL so the packet path never hits the DB; aclPut/aclDelete write through and persist async.
	acl struct {
		sync.RWMutex
		m map[string]*store.RepeaterACLEntry // keyed by full pubkey hex
	}

	lastTS atomic.Uint32 // last timestamp we stamped (firmware getCurrentTimeUnique)

	mu     sync.Mutex
	cancel context.CancelFunc
	runCtx context.Context
}

// uniqueTimestamp mirrors firmware getCurrentTimeUnique: strictly increasing across every timestamp we stamp on outgoing traffic.
func (r *Repeater) uniqueTimestamp() uint32 {
	for {
		last := r.lastTS.Load()
		ts := max(uint32(time.Now().Unix()), last+1)
		if r.lastTS.CompareAndSwap(last, ts) {
			return ts
		}
	}
}

// reverseHops flips a received path into send order: a flood request accumulates the client's neighbour first.
func reverseHops(path []byte, hashSize uint8) []byte {
	hs := int(hashSize)
	if hs == 0 {
		hs = int(meshcore.PathHashSize)
	}
	n := len(path) / hs * hs
	out := make([]byte, n)
	for i := 0; i < n; i += hs {
		copy(out[n-hs-i:], path[i:i+hs])
	}
	return out
}

// Hooks are the app-provided callbacks the repeater can't build itself.
type Hooks struct {
	// Reconfigure persists + validates + reloads a config change; nil disables config writes.
	Reconfigure func(mutate func(*config.RepeaterConfig)) error
	// PollStats reads the shared modem's device stats; nil leaves those fields 0.
	PollStats func(ctx context.Context) DeviceStats
	// Telemetry is the operator's channel map and the readings behind it; nil publishes no sensors.
	Telemetry func() ([]sensor.ChannelEntry, []sensor.Status)
}

func NewRepeater(cfg config.RepeaterConfig, mux *node.RadioMux, st *store.Store, hub *api.Hub, hooks Hooks) (*Repeater, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return nil, fmt.Errorf("repeater name is required")
	}

	id, err := config.LocalIdentityFromHex(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("repeater identity: %w", err)
	}

	radio := mux.NewRadio()
	log := slog.Default().With("component", "repeater", "name", name)

	r := &Repeater{
		cfg:         cfg,
		radio:       radio,
		log:         log,
		store:       st,
		hub:         hub,
		reconfigure: hooks.Reconfigure,
		pollStats:   hooks.PollStats,
		telemetry:   hooks.Telemetry,
	}
	r.neighbors.m = make(map[[32]byte]*neighbor)
	r.routes.m = make(map[[32]byte]clientRoute)
	r.acl.m = make(map[string]*store.RepeaterACLEntry)
	r.anonLimiter = newRateLimiter(4, 3*time.Minute)     // firmware anon_limiter(4, 180)
	r.discoverLimiter = newRateLimiter(4, 2*time.Minute) // firmware discover_limiter(4, 120)
	r.aclLoad()                                          // seed the ACL cache from the DB before handlers register
	// Set before the node goes live so neither airtime accumulator sees a torn value.
	r.airtime, r.sf = buildAirtimeEstimator(st)

	opts := []node.Option{
		node.WithMaxPeers(100_000),
		node.WithErrorHandler(func(err error) { log.Error("node error", "error", err) }),
		node.WithFloodRetransmitDelay(r.floodRelayDelay),
		node.WithDirectRetransmitDelay(r.directRelayDelay),
		node.WithRxDelay(r.rxDelay),
		node.WithExtraAckTransmitCount(r.extraAcks),
		// A node with no allowForward handler never relays — this is what makes it a repeater.
		node.WithAllowForwardHandler(r.allowForward),
	}
	// Registering named scopes is what lets FindFloodMatch relay their transport-flood packets.
	named, wildcardFlags := regionsFromConfig(cfg.Regions)
	if len(named) > 0 {
		opts = append(opts, node.WithRegions(named...))
	}
	r.node = node.New(id, radio, opts...)
	r.routeStats = r.node.RouteStats
	r.node.Regions().SetWildcardFlags(wildcardFlags) // "*" entry ⇒ relay unscoped flood; absent ⇒ don't

	r.registerHandlers()

	return r, nil
}

// buildAirtimeEstimator returns a nil estimator and SF 0 when the radio params aren't set.
func buildAirtimeEstimator(st *store.Store) (func(int) uint32, uint8) {
	s, err := st.Settings.Get(context.Background())
	if err != nil || s.Freq == nil || s.BW == nil || s.SF == nil || s.CR == nil {
		return nil, 0
	}
	return hardware.LoRaAirtimeEstimator(&hardware.RadioConfig{
		FreqHz: uint32(*s.Freq * 1_000_000),
		BwHz:   uint32(*s.BW * 1000),
		SF:     uint8(*s.SF),
		CR:     uint8(*s.CR),
	}), uint8(*s.SF)
}

// runContext guards the nil r.runCtx window: node.New registers the radio handler in NewRepeater, so traffic can arrive before Start.
func (r *Repeater) runContext() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runCtx == nil {
		return context.Background()
	}
	return r.runCtx
}

// Node exposes the running node so callers that only need to send or observe packets - node
// discovery, for one - do not have to be part of the repeater personality.
func (r *Repeater) Node() *node.Node { return r.node }

func (r *Repeater) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.runCtx = ctx
	r.startedAt = time.Now()
	r.mu.Unlock()

	go r.advertLoop(ctx)
	if r.pollStats != nil {
		go r.deviceStatsLoop(ctx)
	}

	r.log.Info("repeater started", "pubkey", hex.EncodeToString(r.node.Identity().PublicKeyBytes()[:8]))
	return nil
}

// deviceStatsLoop refreshes the cached readings because pollStats blocks ~500ms and can't run on the packet path.
func (r *Repeater) deviceStatsLoop(ctx context.Context) {
	const interval = 60 * time.Second
	refresh := func() {
		ds := r.pollStats(ctx)
		if ctx.Err() != nil {
			return
		}
		r.noiseFloor.Store(int32(ds.NoiseFloor))
		r.batteryMV.Store(uint32(ds.BatteryMV))
		r.haveBattery.Store(ds.HaveBattery)
		if ds.HaveMCUTemp {
			r.mcuTempC.Store(int32(ds.MCUTempC * 10))
			r.haveMCUTemp.Store(true)
		}
		r.haveDeviceStats.Store(true)
	}
	refresh()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

func (r *Repeater) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.node.Stop()
	return nil
}

// regionsFromConfig derives each named scope's key from its name (SHA256(name)[:16], firmware getAutoKeyFor); "*" is never a named region.
func regionsFromConfig(cfg []config.RepeaterRegion) (named []*meshcore.Region, wildcardFlags uint8) {
	wildcardFlags = meshcore.RegionDenyFlood // no "*" entry ⇒ don't relay unscoped flood
	for _, rg := range cfg {
		if rg.Name == config.WildcardRegion {
			if rg.DenyFlood {
				wildcardFlags = meshcore.RegionDenyFlood
			} else {
				wildcardFlags = 0
			}
			continue
		}
		reg := meshcore.NewRegionFromKey(rg.Name, meshcore.DeriveRegionKey(rg.Name))
		if rg.DenyFlood {
			reg.Flags |= meshcore.RegionDenyFlood
		}
		named = append(named, reg)
	}
	return named, wildcardFlags
}

// ApplyRegions updates regions in place so a region-only edit doesn't restart the node and wipe neighbours, routes and counters.
func (r *Repeater) ApplyRegions(regions []config.RepeaterRegion, defaultRegion, homeRegion string) {
	named, wildcardFlags := regionsFromConfig(regions)
	rm := r.node.Regions()
	rm.SetWildcardFlags(wildcardFlags)

	want := make(map[string]*meshcore.Region, len(named))
	for _, rg := range named {
		want[rg.Name] = rg
	}
	// A deny-flood change is remove+re-add: mutating a returned *Region would race FindFloodMatch on the packet path.
	for _, cur := range rm.All() {
		w, keep := want[cur.Name]
		if !keep {
			rm.Remove(cur.Name)
			continue
		}
		if cur.Flags == w.Flags {
			delete(want, cur.Name) // unchanged — leave in place
		} else {
			rm.Remove(cur.Name) // re-added below with the new flags
		}
	}
	for _, rg := range want {
		rm.Add(rg)
	}

	r.mu.Lock()
	r.cfg.Regions = regions
	r.cfg.DefaultRegion = defaultRegion
	r.cfg.HomeRegion = homeRegion
	r.mu.Unlock()
}

func (r *Repeater) Name() string { return r.cfg.Name }

// rateLimiter mirrors the firmware Limiter: max allows per sliding window, and a nil limiter never blocks.
type rateLimiter struct {
	mu     sync.Mutex
	stamps []time.Time
	max    int
	window time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window}
}

func (l *rateLimiter) allow() bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.stamps[:0]
	for _, t := range l.stamps {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.stamps = kept
		return false
	}
	l.stamps = append(kept, now)
	return true
}

// DeviceStats are the shared modem's board readings, polled for the over-mesh
// STATUS and telemetry replies. HaveMCUTemp is false when the board can't
// measure one, and HaveBattery false when there is no cell, so 0 is never mistaken for a reading.
type DeviceStats struct {
	NoiseFloor  int16
	BatteryMV   uint16
	HaveBattery bool
	MCUTempC    float64
	HaveMCUTemp bool
}
