package companion

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/client/repeater"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/echo"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/mqtt"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
	"github.com/meshcore-go/OwlShack/internal/trigger"
)

// modemReadings is the slice of the modem a companion needs, so a send and a telemetry reply test without a radio.
type modemReadings interface {
	EstAirtimeMs(packetLen int) uint32
	// CachedStats is the last poll, never a fresh one: this is read on the packet path.
	CachedStats() modem.DeviceStats
}

type triggerEntry struct {
	trigger  trigger.Trigger
	config   config.TriggerConfig
	channels []*meshcore.ChannelEntry
}

// groupTextHandler is implemented by triggers that react to group-text packets; non-implementers (e.g. cron) are skipped.
type groupTextHandler interface {
	HandleGroupText(*meshcore.Packet)
}

// dmTextHandler is implemented by triggers that react to accepted plain DMs.
type dmTextHandler interface {
	HandleDirectMessage(trigger.DirectMessage, *meshcore.Packet)
}

type Companion struct {
	cfg config.CompanionConfig

	node      *node.Node
	radio     *node.MuxRadio
	mux       *node.RadioMux
	templater *trigger.Templater
	log       *slog.Logger

	store *store.Store
	hub   *api.Hub

	echoTracker *echo.Tracker
	repeaters   *repeater.Client
	// stats is narrowed to what a send and a telemetry reply need: modem.StatsProvider satisfies it.
	stats modemReadings

	// telemetry is the operator's channel map and the readings behind it; nil publishes no sensors.
	telemetry func() ([]sensor.ChannelEntry, []sensor.Status)

	pendingOutbound struct {
		sync.Mutex
		msgID   int64
		channel string
	}

	traceWaiters traceWaiters

	// dmSeen collapses a sender's retransmissions of one message; see recentDM.
	dmSeen dmSeen

	triggers []triggerEntry

	// MQTT: outbound only
	obs *mqtt.Observer

	// appSink is the companion app connection listening to this companion, if any.
	appSink atomic.Pointer[appSinkHolder]

	mu     sync.Mutex
	cancel context.CancelFunc
	// runCtx is kept so ReloadTriggers can start new triggers without a full restart.
	runCtx context.Context
}

func NewCompanion(cfg config.CompanionConfig, mux *node.RadioMux, st *store.Store, hub *api.Hub, echoTracker *echo.Tracker, stats modem.StatsProvider, parseErrors *atomic.Uint64, nodeOpts ...node.Option) (*Companion, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return nil, fmt.Errorf("companion name is required")
	}

	radio := mux.NewRadio()
	log := slog.Default().With("component", "companion", "name", name)
	opts := append([]node.Option{
		node.WithMaxPeers(100_000),
		node.WithErrorHandler(func(err error) {
			log.Error("node error", "error", err)
		}),
	}, nodeOpts...)
	// Out-paths learned via path-returns only; appended last to set on the final table.
	opts = append(opts, node.WithLearnedPathsOnly())
	if cfg.FloodScope != "" {
		opts = append(opts, node.WithFloodScope(config.ScopeRegion(cfg.FloodScope)))
	}

	// Identities are pinned in the config; EnsureCompanionKeys fills empty ones before it is persisted.
	id, err := identityFromHexSeed(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("companion identity: %w", err)
	}

	n := node.New(id, radio, opts...)

	companion := &Companion{
		// RX handlers are live before Start sets the real one, and a DM in between must not query with a nil context.
		runCtx:      context.Background(),
		cfg:         cfg,
		node:        n,
		radio:       &radio,
		mux:         mux,
		templater:   trigger.NewTemplater(),
		log:         log,
		store:       st,
		hub:         hub,
		echoTracker: echoTracker,
		stats:       stats,
	}
	companion.repeaters = repeater.NewClient(n, st, cfg.ID, log, stats, companion.pathHashSize)

	// The companion's channels are the only ones this node listens on; triggers reference them by name and register none of their own.
	if cfg.Channels != nil {
		for i, chRef := range *cfg.Channels {
			ch, err := channelFromRef(chRef)
			if err != nil {
				return nil, fmt.Errorf("channel %q: %w", chRef.Name, err)
			}
			n.SetChannel(i, ch)
		}
	}

	companion.triggers, err = companion.buildTriggers(cfg)
	if err != nil {
		return nil, err
	}

	if companion.cfg.Mqtt != nil {
		mqttCfg := *companion.cfg.Mqtt

		obs, err := mqtt.NewObserver(mqttCfg, name, mux, companion.node.Identity(), stats, parseErrors)
		if err != nil {
			return nil, fmt.Errorf("creating mqtt observer: %w", err)
		}
		companion.obs = obs
	}

	companion.registerPacketHandlers()

	return companion, nil
}

// MqttStatus reports this companion's MQTT broker state; ok=false when it isn't the node feeding MQTT.
func (c *Companion) MqttStatus() ([]mqtt.BrokerStatus, bool) {
	if c.obs == nil {
		return nil, false
	}
	return c.obs.BrokerStatuses(), true
}

func (c *Companion) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.runCtx = ctx
	c.mu.Unlock()

	for i, entry := range c.triggers {
		e := entry
		if err := e.trigger.Start(ctx, c.makeCallback(ctx, e)); err != nil {
			cancel()
			return fmt.Errorf("starting trigger %d (%s): %w", i, e.config.Type, err)
		}
	}

	if c.obs != nil {
		obsErr := c.obs.Start(ctx)
		if obsErr != nil {
			return fmt.Errorf("starting mqtt observer: %w", obsErr)
		}
	}

	if c.cfg.AdvertInterval == nil || *c.cfg.AdvertInterval != 0 {
		go c.advertLoop(ctx)
	}

	return nil
}

func (c *Companion) Stop() error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()

	for _, trig := range c.triggers {
		trig.trigger.Stop()
	}

	if c.obs != nil {
		c.obs.Stop()
	}

	c.node.Stop()
	return nil
}

func (c *Companion) Name() string {
	return c.cfg.Name
}

// ID returns the surrogate primary key all of this companion's history is stored under, so a rename keeps it attached.
func (c *Companion) ID() int64 {
	return c.cfg.ID
}

// SetTelemetry binds this companion's channel map hook; the app owns the map across reloads.
func (c *Companion) SetTelemetry(fn func() ([]sensor.ChannelEntry, []sensor.Status)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.telemetry = fn
}

// LatLon returns the companion's configured position (nil if unset).
func (c *Companion) LatLon() (*float64, *float64) {
	return c.cfg.Latitude, c.cfg.Longitude
}

func (c *Companion) Node() *node.Node {
	return c.node
}

func (c *Companion) Repeaters() *repeater.Client {
	return c.repeaters
}

// Observer returns this companion's MQTT observer, or nil when it isn't the feeding node.
func (c *Companion) Observer() *mqtt.Observer { return c.obs }
