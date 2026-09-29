package app

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/node/repeater"
	"github.com/meshcore-go/OwlShack/internal/store"
	"github.com/meshcore-go/meshcore-go/node"
)

// At most one repeater runs — a radio hosts one relay identity — so the lifecycle is start/stop, not set reconciliation.

// effectiveRepeaterConfig resolves inherited settings into the block, so a change to a global default shows in the reload diff.
func effectiveRepeaterConfig(cfg *config.Config) *config.RepeaterConfig {
	if cfg.Repeater == nil {
		return nil
	}
	block := *cfg.Repeater
	if block.PathHashSize == nil {
		v := cfg.PathHashSizeOr()
		block.PathHashSize = &v
	}
	return &block
}

func startRepeater(ctx context.Context, cfg *config.Config, mux *node.RadioMux, db *store.Store, hub *api.Hub, stats modem.StatsProvider, reload func() error, telemetry *telemetryPublisher) (*repeater.Repeater, error) {
	if cfg.Repeater == nil {
		return nil, nil
	}
	hooks := repeater.Hooks{
		Reconfigure: repeaterReconfigurer(db, reload),
		PollStats:   statsPoller(stats),
		Telemetry:   telemetry.MapFor(store.TelemetryNode{Kind: store.NodeKindRepeater, ID: store.RepeaterNodeID}),
	}
	rep, err := repeater.NewRepeater(*effectiveRepeaterConfig(cfg), mux, db, hub, hooks)
	if err != nil {
		return nil, fmt.Errorf("creating repeater %q: %w", cfg.Repeater.Name, err)
	}
	if err := rep.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting repeater %q: %w", cfg.Repeater.Name, err)
	}
	slog.Info("started repeater", "repeater", cfg.Repeater.Name)
	return rep, nil
}

func stopRepeater(rep *repeater.Repeater) {
	if rep != nil {
		rep.Stop()
	}
}

// reloadRepeater keeps the running instance on an unchanged block: no relay gap, no re-advert.
func reloadRepeater(ctx context.Context, oldCfg, newCfg *config.Config, running *repeater.Repeater, mux *node.RadioMux, db *store.Store, hub *api.Hub, stats modem.StatsProvider, reload func() error, telemetry *telemetryPublisher) (*repeater.Repeater, error) {
	// reflect.DeepEqual handles nil/one-nil/deep on the two *RepeaterConfig.
	if running != nil && oldCfg != nil && reflect.DeepEqual(effectiveRepeaterConfig(oldCfg), effectiveRepeaterConfig(newCfg)) {
		return running, nil
	}
	// Region-only change: apply it live so the neighbour list, learned routes and relay counters survive.
	if running != nil && oldCfg != nil && onlyRegionsDiffer(effectiveRepeaterConfig(oldCfg), effectiveRepeaterConfig(newCfg)) {
		running.ApplyRegions(newCfg.Repeater.Regions, newCfg.Repeater.DefaultRegion, newCfg.Repeater.HomeRegion)
		return running, nil
	}
	stopRepeater(running)
	return startRepeater(ctx, newCfg, mux, db, hub, stats, reload, telemetry)
}

// onlyRegionsDiffer reports the differences the reload path can apply live, without restarting the node.
func onlyRegionsDiffer(a, b *config.RepeaterConfig) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.Regions, y.Regions = nil, nil
	x.DefaultRegion, y.DefaultRegion = "", ""
	x.HomeRegion, y.HomeRegion = "", ""
	return reflect.DeepEqual(x, y)
}

// statsPoller maps a nil provider to a nil poller, leaving noise floor and battery at 0.
func statsPoller(stats modem.StatsProvider) func(context.Context) repeater.DeviceStats {
	if stats == nil {
		return nil
	}
	return func(ctx context.Context) repeater.DeviceStats {
		ds := stats.Stats(ctx)
		return repeater.DeviceStats{
			NoiseFloor:  ds.NoiseFloor,
			BatteryMV:   ds.BatteryMV,
			HaveBattery: ds.HaveBattery,
			MCUTempC:    ds.MCUTempC,
			HaveMCUTemp: ds.HaveMCUTemp,
		}
	}
}

// repeaterReconfigurer applies an over-mesh CLI change; the node calls it off its dispatch path, so the reload's node restart can't deadlock.
func repeaterReconfigurer(db *store.Store, reload func() error) func(func(*config.RepeaterConfig)) error {
	return func(mutate func(*config.RepeaterConfig)) error {
		ctx := context.Background()
		return writeConfigTx(ctx, db, reload, func(rows *configRows) error {
			if rows.repeater == nil {
				return fmt.Errorf("no repeater configured")
			}
			cfg := assembleFromRows(rows)
			mutate(cfg.Repeater)
			if verr := cfg.Validate(); verr != nil {
				return verr
			}
			return writeRepeater(ctx, db, cfg)
		})
	}
}
