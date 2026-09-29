package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// telemetryPublisher outlives a radio generation, so the app owns it and hands each node a hook bound to its id.
type telemetryPublisher struct {
	sensors *sensor.Hub

	mu     sync.RWMutex
	byNode map[store.TelemetryNode][]sensor.ChannelEntry
}

func newTelemetryPublisher(sensors *sensor.Hub) *telemetryPublisher {
	return &telemetryPublisher{sensors: sensors, byNode: map[store.TelemetryNode][]sensor.ChannelEntry{}}
}

func (p *telemetryPublisher) Load(ctx context.Context, db *store.Store) error {
	rows, err := db.TelemetryMap.List(ctx)
	if err != nil {
		return fmt.Errorf("reading the telemetry map: %w", err)
	}
	byNode := map[store.TelemetryNode][]sensor.ChannelEntry{}
	for _, r := range rows {
		byNode[r.Node] = append(byNode[r.Node], sensor.ChannelEntry{
			ID: r.ID, Channel: byte(r.Channel), Type: byte(r.LPPType),
			SensorID: r.SensorID, Metric: sensor.Metric(r.Metric),
		})
	}
	p.mu.Lock()
	p.byNode = byNode
	p.mu.Unlock()
	return nil
}

func (p *telemetryPublisher) Entries(node store.TelemetryNode) []sensor.ChannelEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.byNode[node])
}

func (p *telemetryPublisher) All() map[store.TelemetryNode][]sensor.ChannelEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[store.TelemetryNode][]sensor.ChannelEntry, len(p.byNode))
	for node, entries := range p.byNode {
		out[node] = slices.Clone(entries)
	}
	return out
}

// MapFor returns one node's map and the readings behind it, so no node answers with another's channels; the node builds the reply and applies the permissions.
func (p *telemetryPublisher) MapFor(node store.TelemetryNode) func() ([]sensor.ChannelEntry, []sensor.Status) {
	return func() ([]sensor.ChannelEntry, []sensor.Status) {
		if p == nil || p.sensors == nil {
			return nil, nil
		}
		return p.Entries(node), freshOnly(p.sensors.Snapshot(), time.Now())
	}
}

// freshOnly drops a reading past its sensor's stale time, which a wedged bus leaves in place and a requester would take as current.
func freshOnly(sts []sensor.Status, now time.Time) []sensor.Status {
	return slices.DeleteFunc(sts, func(s sensor.Status) bool { return now.Sub(s.At) > staleAfter(s) })
}
