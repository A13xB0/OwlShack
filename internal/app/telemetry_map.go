package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func (b *backend) TelemetryMap(ctx context.Context) (api.TelemetryMap, error) {
	nodes, err := b.telemetryNodes(ctx)
	if err != nil {
		return api.TelemetryMap{}, err
	}
	out := api.TelemetryMap{
		Nodes:       nodes,
		Entries:     []api.TelemetryMapEntry{},
		Types:       lppTypeInfos(),
		Defaults:    lppDefaults(),
		SelfChannel: sensor.ChannelSelf,
		SelfTypes:   selfTypeCodes(),
		MaxChannel:  sensor.MaxChannel,
		MaxBytes:    sensor.MaxTelemetryPayload,
	}
	for node, entries := range b.telemetry.All() {
		for _, e := range entries {
			out.Entries = append(out.Entries, api.TelemetryMapEntry{
				Node:    api.TelemetryNode{Kind: node.Kind, ID: node.ID},
				Channel: int(e.Channel), Type: int(e.Type),
				SensorID: e.SensorID, Metric: string(e.Metric),
			})
		}
	}
	slices.SortFunc(out.Entries, func(a, b api.TelemetryMapEntry) int {
		return cmp.Or(strings.Compare(a.Node.Kind, b.Node.Kind), cmp.Compare(a.Node.ID, b.Node.ID),
			cmp.Compare(a.Channel, b.Channel), cmp.Compare(a.Type, b.Type))
	})
	return out, nil
}

// telemetryNodes reads configuration, not what is running: a saved map must stay visible with the radio down.
func (b *backend) telemetryNodes(ctx context.Context) ([]api.TelemetryNodeInfo, error) {
	out := []api.TelemetryNodeInfo{}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading the repeater: %w", err)
	}
	if rep != nil && rep.Name != "" {
		out = append(out, api.TelemetryNodeInfo{
			Node:   api.TelemetryNode{Kind: store.NodeKindRepeater, ID: store.RepeaterNodeID},
			Name:   rep.Name,
			Serves: true,
		})
	}
	companions, err := b.db.Companions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the companions: %w", err)
	}
	for _, c := range companions {
		serves, reason := companionServes(c)
		out = append(out, api.TelemetryNodeInfo{
			Node:   api.TelemetryNode{Kind: store.NodeKindCompanion, ID: c.ID},
			Name:   c.Name,
			Serves: serves,
			Reason: reason,
		})
	}
	return out, nil
}

// companionServes reports whether a saved map reaches anyone; one that nothing sends looks identical to one that does.
func companionServes(c store.Companion) (bool, string) {
	if config.TelemetryModeOrDefault(&c.TelemBase) == config.TelemetryDeny {
		return false, "No one may read this companion's telemetry. The firmware answers nothing without Battery and device, so nothing here is sent until you allow that."
	}
	if config.TelemetryModeOrDefault(&c.TelemEnv) == config.TelemetryDeny {
		return false, "Sensor readings are set to deny, so this map is kept but never sent."
	}
	return true, ""
}

func (b *backend) SetTelemetryMap(ctx context.Context, node api.TelemetryNode, in []api.TelemetryMapEntry) error {
	sensorWrites.Lock()
	defer sensorWrites.Unlock()
	target, err := b.telemetryNode(ctx, node)
	if err != nil {
		return err
	}

	entries := make([]sensor.ChannelEntry, 0, len(in))
	for _, e := range in {
		if e.Node != node {
			return api.Invalid(fmt.Errorf("channel %d is for %s %d, not the %s %d being saved", e.Channel, e.Node.Kind, e.Node.ID, node.Kind, node.ID))
		}
		if e.Channel < 0 || e.Channel > 255 {
			return api.Invalid(fmt.Errorf("channel %d is outside the 0 to 255 an LPP channel holds", e.Channel))
		}
		if e.Type < 0 || e.Type > 255 {
			return api.Invalid(fmt.Errorf("%d is not an LPP type", e.Type))
		}
		entries = append(entries, sensor.ChannelEntry{
			Channel: byte(e.Channel), Type: byte(e.Type),
			SensorID: e.SensorID, Metric: sensor.Metric(strings.TrimSpace(e.Metric)),
		})
	}
	if err := sensor.ValidateChannelMap(entries); err != nil {
		return api.Invalid(err)
	}
	if err := b.checkRowsResolve(entries); err != nil {
		return api.Invalid(err)
	}

	rows := make([]store.TelemetryMapEntry, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, store.TelemetryMapEntry{
			Channel: int(e.Channel), LPPType: int(e.Type), SensorID: e.SensorID, Metric: string(e.Metric),
		})
	}
	var writeErr error
	b.db.WriteSync(func() { writeErr = b.db.TelemetryMap.ReplaceForNode(ctx, target, rows) })
	if writeErr != nil {
		return writeErr
	}
	return b.telemetry.Load(context.WithoutCancel(ctx), b.db)
}

// checkRowsResolve refuses a row naming a sensor or reading that is gone, which encoding would skip in silence.
func (b *backend) checkRowsResolve(entries []sensor.ChannelEntry) error {
	specs := map[int64]sensor.Spec{}
	for _, s := range b.sensors.Snapshot() {
		specs[s.Spec.ID] = s.Spec
	}
	for _, e := range entries {
		spec, ok := specs[e.SensorID]
		if !ok {
			return fmt.Errorf("channel %d reads sensor %d, which is not configured", e.Channel, e.SensorID)
		}
		if !b.sensors.Reports(spec)[e.Metric] {
			return fmt.Errorf("channel %d reads %s from %s, which does not report it", e.Channel, e.Metric, spec.Name)
		}
	}
	return nil
}

// telemetryNode resolves what the page named to a node this host actually has.
func (b *backend) telemetryNode(ctx context.Context, node api.TelemetryNode) (store.TelemetryNode, error) {
	nodes, err := b.telemetryNodes(ctx)
	if err != nil {
		return store.TelemetryNode{}, err
	}
	for _, n := range nodes {
		if n.Node == node {
			return store.TelemetryNode{Kind: node.Kind, ID: node.ID}, nil
		}
	}
	return store.TelemetryNode{}, api.Invalid(fmt.Errorf("no %s with id %d is configured on this host", node.Kind, node.ID))
}

func selfTypeCodes() []int {
	out := []int{}
	for _, t := range sensor.SelfTypes() {
		out = append(out, int(t))
	}
	return out
}

func lppTypeInfos() []api.LPPTypeInfo {
	types := sensor.LPPTypes()
	out := make([]api.LPPTypeInfo, 0, len(types))
	for _, t := range types {
		out = append(out, api.LPPTypeInfo{
			Code: int(t.Code), Name: t.Name, Unit: t.Unit, Bytes: 2 + t.Size, Step: t.Step,
		})
	}
	return out
}

func lppDefaults() map[string]int {
	out := map[string]int{}
	for _, m := range sensor.Metrics() {
		if code, ok := sensor.DefaultLPPType(m); ok {
			out[string(m)] = int(code)
		}
	}
	return out
}
