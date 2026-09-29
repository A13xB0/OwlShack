// Package app wires the subsystems together and runs the supervisor loop.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/discover"
	"github.com/meshcore-go/OwlShack/internal/echo"
	"github.com/meshcore-go/OwlShack/internal/logging"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/monitor"
	"github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/node/repeater"
	"github.com/meshcore-go/OwlShack/internal/signaltest"
	"github.com/meshcore-go/OwlShack/internal/store"
	"github.com/meshcore-go/OwlShack/internal/trigger"
	"github.com/meshcore-go/OwlShack/web"
	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

const defaultListenAddr = ":8080"

const (
	initialRetryDelay = 1 * time.Second
	maxRetryDelay     = 30 * time.Second
)

// applyListenEnvOverrides gives HOST/PORT precedence over the stored address; either may be set alone.
func applyListenEnvOverrides(addr string) string {
	envHost, hasHost := os.LookupEnv("HOST")
	envPort, hasPort := os.LookupEnv("PORT")
	if !hasHost && !hasPort {
		return addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not a clean host:port; recover the port from a leading-colon form.
		host, port = "", strings.TrimPrefix(addr, ":")
	}
	if hasHost {
		host = envHost
	}
	if hasPort && envPort != "" {
		port = envPort
	}

	overridden := net.JoinHostPort(host, port)
	slog.Info("web listen address overridden by environment", "from", addr, "to", overridden)
	return overridden
}

// dbPath is the SQLite file, relative to the working directory.
const dbPath = "meshcore.db"

// Run blocks until ctx is cancelled; importPath imports a config file into the DB.
func Run(ctx context.Context, importPath string, verbosity int) error {
	// A UI restore is staged beside the DB because the accepting process still held it open.
	if adopted, err := store.AdoptPendingRestore(dbPath); err != nil {
		return fmt.Errorf("restoring database: %w", err)
	} else if adopted {
		slog.Info("adopted restored database", "path", dbPath, "previous", dbPath+".replaced")
	}

	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("database open: %w", err)
	}
	defer db.Close()

	cfg, err := resolveConfig(ctx, db, importPath)
	if err != nil {
		return fmt.Errorf("resolving config: %w", err)
	}

	if err := seedListenAddr(ctx, db, cfg); err != nil {
		return fmt.Errorf("listen address: %w", err)
	}

	logLevel := ""
	if cfg.LogLevel != nil {
		logLevel = *cfg.LogLevel
	}
	logging.Configure(verbosity, logLevel)

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)

	reconnectCh := make(chan struct{}, 1)
	// resetModem lets the API ask for the reconnect a vanished port triggers. The channel is buffered
	// and the send non-blocking, so a reset while one is already running is a no-op rather than a queue.
	resetModem := func() {
		slog.Warn("modem reset requested")
		select {
		case reconnectCh <- struct{}{}:
		default:
		}
	}

	// Not fatal: a KISS node never consults it, and an SPI node naming a board it
	// failed to define fails at LookupBoard with that name in the error.
	if err := modem.LoadBoardOverrides(); err != nil {
		slog.Error("board override file ignored", "component", "modem", "error", err)
	}

	srv := api.NewServer(db, web.Assets(), slog.Default())
	reload := func() error {
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			return err
		}
		return p.Signal(syscall.SIGHUP)
	}

	listenAddr := defaultListenAddr
	if cfg.ListenAddr != nil && *cfg.ListenAddr != "" {
		listenAddr = *cfg.ListenAddr
	}
	listenAddr = applyListenEnvOverrides(listenAddr)
	// Bound before the goroutine: a failed bind must stop the process, not run on with no UI.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("web listen on %s: %w", listenAddr, err)
	}
	httpServer := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("web UI listening", "addr", listenAddr)
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	// Sensors are local hardware, so the hub outlives a radio reconnect; a failed load is fatal, or the page reads as nothing configured.
	sensorHub, err := startSensors(ctx, db, srv.Hub(), slog.Default())
	if err != nil {
		return err
	}
	defer sensorHub.Close()

	// Loaded once here and refreshed on save, so a telemetry reply never waits on the database.
	telemetry := newTelemetryPublisher(sensorHub)
	feedPreview := trigger.NewFeedPreview()
	if err := telemetry.Load(ctx, db); err != nil {
		return err
	}

	echoTracker := echo.NewTracker(db, srv.Hub(), slog.Default())
	go echoTracker.PruneLoop(ctx)

	// Long-lived across reloads: reaches the current companions through compReg, re-pointed on each reload.
	compReg := newCompanionRegistry()

	mon := monitor.New(db, srv.Hub(), newMergedLister(newContactLister(compReg, db), newLinkLister(compReg, db)), slog.Default())
	mon.RegisterCollector("repeater", newRepeaterCollector(compReg, db, slog.Default()))
	mon.RegisterCollector("companion", newCompanionCollector(compReg, slog.Default()))
	mon.RegisterCollector("link", newLinkCollector(compReg, db, slog.Default()))
	mon.Start(ctx)
	srv.SetPoller(mon)

	// Shares the monitor's airtime lock so a test and a scheduled poll interleave per-operation.
	tester := signaltest.New(db, srv.Hub(), newSignalTestTracer(compReg), mon.AirtimeLock(), slog.Default())
	tester.Start(ctx)
	srv.SetSignalTester(tester)

	var (
		ms         *modem.State
		mux        *node.RadioMux
		companions []*companion.Companion
		rep        *repeater.Repeater
		disc       *discover.Service
	)
	// newDiscovery attaches zero-hop discovery to whichever node is running. The request carries no
	// identity, so any node will do and the answers describe our radio's range, not that node's.
	newDiscovery := func() *discover.Service {
		var send discover.Sender
		switch {
		case rep != nil:
			send = rep.Node()
		case len(companions) > 0:
			send = companions[0].Node()
		default:
			return nil
		}
		return discover.New(send, slog.Default().With("component", "discover"), func(r discover.Result) {
			srv.Hub().Broadcast("discovered", api.DiscoveryInfo{
				PubKey: r.PubKey, Name: peerName(ctx, db, r.PubKey), Type: r.Type,
				SNR: r.SNR, ReportedSNR: r.ReportedSNR, Heard: r.Heard.Format(time.RFC3339),
			})
		})
	}
	// startRadio brings up the modem and everything that hangs off it, leaving ms nil if it cannot.
	// Failing is not fatal: exiting here would take away the page an operator uses to fix the connection.
	startRadio := func(c *config.Config) error {
		newMs, newMux, err := reconnectModem(ctx, c, db, srv, reconnectCh, compReg)
		if err != nil {
			return err
		}
		newComps, err := startCompanions(ctx, c, newMs, newMux, db, srv.Hub(), echoTracker, telemetry)
		if err != nil {
			newMs.Close()
			return fmt.Errorf("%w: %w", errCompanionStart, err)
		}
		newRep, err := startRepeater(ctx, c, newMux, db, srv.Hub(), newMs.Stats, reload, telemetry)
		if err != nil {
			stopCompanions(newComps)
			newMs.Close()
			return fmt.Errorf("%w: %w", errRepeaterStart, err)
		}
		ms, mux, companions, rep = newMs, newMux, newComps, newRep
		compReg.set(companions)
		disc = newDiscovery()
		return nil
	}

	// installBackend hands the server a backend over whatever radio generation is running now.
	installBackend := func() {
		liveRadio.Store(ms)
		srv.SetBackend(&backend{
			companions: companions, repeater: rep, db: db, stats: statsOf(ms), mux: mux,
			reload: reload, resetModem: resetModem, discover: disc, sensors: sensorHub, telemetry: telemetry,
			feedPreview: feedPreview,
		})
	}

	// stopRadio tears the stack down and leaves the vars nil, which is the state startRadio recovers from.
	stopRadio := func() {
		stopCompanions(companions)
		stopRepeater(rep)
		if ms != nil {
			liveRadio.Store(nil)
			radioSeen.keepReply(ms.Stats)
			ms.Close()
		}
		ms, mux, companions, rep, disc = nil, nil, nil, nil, nil
	}

	// retryTimer is nil whenever no retry is pending, and a nil channel blocks forever in a select —
	// which is how a retry queued while the radio was down gets cancelled the moment it comes up,
	// instead of firing later and tearing down a working modem.
	var retryTimer <-chan time.Time
	retryDelay := initialRetryDelay
	radioUp := func(err error) {
		radioSeen.started(err)
		if err == nil {
			retryTimer, retryDelay = nil, initialRetryDelay
			return
		}
		retryTimer = time.After(retryDelay)
		slog.Warn("radio unavailable, retrying", "error", err, "retryIn", retryDelay)
		retryDelay = min(retryDelay*2, maxRetryDelay)
	}

	if err := startRadio(cfg); err != nil {
		slog.Error("radio unavailable; serving the web UI so the connection can be corrected in Settings",
			"error", err, "addr", listenAddr)
		radioUp(err)
	}
	installBackend()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down...")
			httpServer.Close()
			stopRadio()
			return nil

		case err := <-httpErr:
			stopRadio()
			return fmt.Errorf("web server on %s: %w", listenAddr, err)

		case <-sighup:
			slog.Info("SIGHUP received, reloading config...")

			newCfg, err := loadConfigFromDB(ctx, db)
			if err != nil {
				slog.Error("config reload failed, keeping current config", "error", err)
				continue
			}
			// Zero companions is allowed (observer-only / wizard skip); reloadCompanions then starts none.

			newLogLevel := ""
			if newCfg.LogLevel != nil {
				newLogLevel = *newCfg.LogLevel
			}
			logging.Configure(verbosity, newLogLevel)

			// Refreshed on every config reload, or a deleted companion leaves its channels listed.
			if err := telemetry.Load(ctx, db); err != nil {
				slog.Error("reloading the telemetry map", "error", err)
			}

			var stats reloadStats
			// A reload with no radio always retries it: the save that just landed is how an operator
			// corrects a connection the node could not open, and this is the only path back.
			if ms == nil || config.ModemSettingsChanged(cfg, newCfg) {
				if ms != nil {
					slog.Info("modem config changed, reconnecting...")
					stats.stopped = len(companions)
					stopRadio()
				}
				// Not fatal, for the same reason startup is not: exiting takes away the page that
				// would fix a wrong connection string.
				radioUp(startRadio(newCfg))
				stats.started = len(companions)
			} else {
				companions, stats, err = reloadCompanions(ctx, cfg, newCfg, companions, ms, mux, db, srv.Hub(), echoTracker, telemetry)
				if err != nil {
					ms.Close()
					return fmt.Errorf("companion restart after reload: %w", err)
				}
				rep, err = reloadRepeater(ctx, cfg, newCfg, rep, mux, db, srv.Hub(), ms.Stats, reload, telemetry)
				if err != nil {
					ms.Close()
					return fmt.Errorf("repeater restart after reload: %w", err)
				}
			}
			cfg = newCfg
			compReg.set(companions)
			disc = newDiscovery()
			installBackend()
			slog.Info("config reloaded", "started", stats.started, "stopped", stats.stopped, "kept", stats.kept, "reloaded", stats.reloaded)

		// One arm for both: the dead-radio watcher (and the UI's reset button) signal reconnectCh, and
		// a failed attempt re-arms retryTimer. Retrying here rather than in a blocking backoff loop is
		// what keeps a SIGHUP from the operator's fix from queueing behind the retries.
		case <-reconnectCh:
			if ms != nil {
				slog.Warn("modem read loop exited, reconnecting...")
				stopRadio()
			}
			if err := startRadio(cfg); err == nil {
				slog.Info("modem connected")
				radioUp(nil)
			} else {
				radioUp(err)
			}
			installBackend()

		case <-retryTimer:
			retryTimer = nil
			if err := startRadio(cfg); err == nil {
				slog.Info("modem connected")
				radioUp(nil)
				installBackend()
			} else {
				radioUp(err)
			}
		}
	}
}

// statsOf keeps the nil modem out of every call site: a nil provider is what the backend reads as
// "there is no radio", rather than a zeroed one it would report as healthy.
func statsOf(ms *modem.State) modem.StatsProvider {
	if ms == nil {
		return nil
	}
	return ms.Stats
}

func startCompanions(ctx context.Context, cfg *config.Config, ms *modem.State, mux *node.RadioMux, db *store.Store, hub *api.Hub, echoTracker *echo.Tracker, telemetry *telemetryPublisher) ([]*companion.Companion, error) {
	companions, _, err := reloadCompanions(ctx, nil, cfg, nil, ms, mux, db, hub, echoTracker, telemetry)
	return companions, err
}

type reloadStats struct {
	started, stopped, kept, reloaded int
}

// reloadCompanions reuses running instances whose block is unchanged; a nil oldCfg/running builds everything.
func reloadCompanions(ctx context.Context, oldCfg, newCfg *config.Config, running []*companion.Companion, ms *modem.State, mux *node.RadioMux, db *store.Store, hub *api.Hub, echoTracker *echo.Tracker, telemetry *telemetryPublisher) ([]*companion.Companion, reloadStats, error) {
	// Must reach the observer before Start: it publishes its first status the instant a broker connects.
	relaying := newCfg.Repeater != nil && !newCfg.Repeater.IsFwdDisabled()

	oldBlocks := make(map[string]config.CompanionConfig)
	if oldCfg != nil {
		for _, b := range effectiveCompanionConfigs(oldCfg) {
			oldBlocks[b.Name] = b
		}
	}

	runningByName := make(map[string]*companion.Companion, len(running))
	for _, c := range running {
		runningByName[c.Name()] = c
	}

	type plan struct {
		block      config.CompanionConfig
		reuse      *companion.Companion // nil = build fresh
		reloadTrig bool
	}
	newBlocks := effectiveCompanionConfigs(newCfg)
	plans := make([]plan, 0, len(newBlocks))
	for _, nb := range newBlocks {
		inst, isRunning := runningByName[nb.Name]
		ob, hadOld := oldBlocks[nb.Name]
		switch {
		case isRunning && hadOld && blocksEqual(ob, nb):
			plans = append(plans, plan{block: nb, reuse: inst})
		case isRunning && hadOld && triggersOnlyChange(ob, nb):
			plans = append(plans, plan{block: nb, reuse: inst, reloadTrig: true})
		default:
			plans = append(plans, plan{block: nb})
		}
	}

	// Instances we keep; every other running instance is stopped.
	keep := make(map[*companion.Companion]bool)
	for _, p := range plans {
		if p.reuse != nil {
			keep[p.reuse] = true
		}
	}

	var stats reloadStats
	for _, c := range running {
		if !keep[c] {
			c.Stop()
			stats.stopped++
		}
	}

	var companions, fresh []*companion.Companion
	stopAll := func() {
		stopCompanions(fresh)
		for c := range keep {
			c.Stop()
		}
	}

	for _, p := range plans {
		if p.reuse != nil {
			if p.reloadTrig {
				if err := p.reuse.ReloadTriggers(p.block); err != nil {
					stopAll()
					return nil, stats, fmt.Errorf("reloading triggers for %q: %w", p.block.Name, err)
				}
				stats.reloaded++
			} else {
				stats.kept++
			}
			companions = append(companions, p.reuse)
			continue
		}
		c, err := companion.NewCompanion(p.block, mux, db, hub, echoTracker, ms.Stats, ms.ParseErrors)
		if err != nil {
			stopAll()
			return nil, stats, fmt.Errorf("creating companion %q: %w", p.block.Name, err)
		}
		if obs := c.Observer(); obs != nil {
			obs.SetRelaying(relaying)
		}
		// Bound to this companion's id, so it can never answer with another node's channels.
		c.SetTelemetry(telemetry.MapFor(store.TelemetryNode{Kind: store.NodeKindCompanion, ID: p.block.ID}))
		// RX is live from NewCompanion, so seed at once: later, a route learned in between would be overwritten.
		hydratePeerTables(ctx, db, []*companion.Companion{c})
		if err := c.Start(ctx); err != nil {
			stopAll()
			return nil, stats, fmt.Errorf("starting companion %q: %w", p.block.Name, err)
		}
		companions = append(companions, c)
		fresh = append(fresh, c)
		stats.started++
		slog.Info("started companion", "companion", p.block.Name)
	}

	// Reused instances kept their observer across the reload, so they need the current value too.
	for _, c := range companions {
		if obs := c.Observer(); obs != nil {
			obs.SetRelaying(relaying)
		}
	}

	return companions, stats, nil
}

// effectiveCompanionConfigs injects the single top-level mqtt block into the companion named by mqtt.node (the first when unset).
func effectiveCompanionConfigs(cfg *config.Config) []config.CompanionConfig {
	mqttNode := ""
	if cfg.Mqtt.IsEnabled() && len(cfg.Mqtt.Brokers) > 0 {
		if cfg.Mqtt.Node != nil && *cfg.Mqtt.Node != "" {
			mqttNode = *cfg.Mqtt.Node
		} else if len(cfg.Companions) > 0 {
			mqttNode = cfg.Companions[0].Name
		}
	}

	pathHash := cfg.PathHashSizeOr()
	blocks := make([]config.CompanionConfig, len(cfg.Companions))
	copy(blocks, cfg.Companions)
	for i := range blocks {
		if blocks[i].Name == mqttNode && mqttNode != "" {
			blocks[i].Mqtt = cfg.Mqtt
		}
		if blocks[i].PathHashSize == nil {
			blocks[i].PathHashSize = &pathHash
		}
	}
	return blocks
}

// blocksEqual compares blocks as JSON; a marshal error reports "not equal", erring towards a restart.
func blocksEqual(a, b config.CompanionConfig) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(aj, bj)
}

// triggersOnlyChange reports whether only Triggers differ — the case ReloadTriggers applies in place.
func triggersOnlyChange(a, b config.CompanionConfig) bool {
	a.Triggers = nil
	b.Triggers = nil
	return blocksEqual(a, b)
}

func stopCompanions(companions []*companion.Companion) {
	for _, comp := range companions {
		comp.Stop()
	}
}

// hydratePeerTables seeds a new companion's peer table from the DB before it starts, with its contacts' saved send-paths; it never overwrites what RX already learned.
func hydratePeerTables(ctx context.Context, db *store.Store, companions []*companion.Companion) {
	if len(companions) == 0 {
		return
	}
	peers, err := db.Peers.LoadAll(ctx)
	if err != nil {
		slog.Error("failed to load peers for hydration", "error", err)
		return
	}
	// A companion rebuilt on reload may have left a route write queued; the writer is FIFO, so this drains it.
	db.WriteSync(func() {})

	for _, c := range companions {
		table := c.Node().Peers()
		for _, sp := range peers {
			id, err := meshcore.NewIdentityFromBytes(sp.PubKey)
			if err != nil {
				slog.Debug("skipping peer with invalid pubkey", "error", err)
				continue
			}
			if table.Lookup(id.PublicKey()) != nil {
				continue
			}
			table.Insert(&node.Peer{
				Identity:            id,
				Name:                sp.Name,
				Type:                sp.Type,
				Lat:                 sp.Lat,
				Lon:                 sp.Lon,
				Feat1:               sp.Feat1,
				Feat2:               sp.Feat2,
				LastAdvertTimestamp: sp.LastAdvertTS,
				LastSeen:            sp.LastSeen,
				SNR:                 derefFloat32(sp.SNR),
				RSSI:                derefInt8(sp.RSSI),
			})
		}

		contacts, err := db.Contacts.List(ctx, c.ID())
		if err != nil {
			slog.Error("failed to load contact routes for hydration", "companion", c.Name(), "error", err)
			continue
		}
		routes := 0
		for _, ct := range contacts {
			id, err := meshcore.NewIdentityFromBytes(ct.PeerPubKey)
			if err != nil {
				continue
			}
			// A contact never heard advertising (added by key, or its peer deleted) still needs a peer to carry its route.
			p := table.Lookup(id.PublicKey())
			if p == nil {
				table.Insert(&node.Peer{Identity: id, Name: ct.Name, Type: ct.Type})
			}
			if ct.OutPath != nil && (p == nil || p.OutPath == nil) && table.SetOutPath(id.PublicKey(), ct.OutPath, max(ct.OutPathHashSize, 1)) {
				routes++
			}
		}
		slog.Info("hydrated peer table from database", "companion", c.Name(), "peers", len(peers), "contacts", len(contacts), "routes", routes)
	}
}

// reconnectModem performs a single modem.Setup attempt and rebuilds the mux, dead-watcher and packet logger.
func reconnectModem(ctx context.Context, cfg *config.Config, db *store.Store, srv *api.Server, reconnectCh chan struct{}, compReg *companionRegistry) (*modem.State, *node.RadioMux, error) {
	ms, err := modem.Setup(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	ms.StartDeadWatcher(reconnectCh)
	mux := node.NewRadioMux(ms.Modem, modem.MuxOptions(ms)...)
	wirePacketLogger(mux, ms.Modem, db, srv, compReg)
	return ms, mux, nil
}

func derefInt8(p *int8) int8 {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat32(p *float64) float32 {
	if p == nil {
		return 0
	}
	return float32(*p)
}
