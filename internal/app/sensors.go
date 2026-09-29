package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// sensorPollInterval is how often every sensor is read; one added from the UI reads at once, so this sets how fast a value goes stale, not how long an add feels.
const sensorPollInterval = 5 * time.Second

// airQualityPeriod is the firmware's BSEC_SAMPLE_RATE_LP; a BME680 with its heater on samples at it on its own clock, whatever the poll.
const airQualityPeriod = 3 * time.Second

// startSensors runs the hub for the life of the process, outside the radio lifecycle.
func startSensors(ctx context.Context, db *store.Store, hub *api.Hub, log *slog.Logger) (*sensor.Hub, error) {
	i2c := sensor.I2CProvider{Period: airQualityPeriod, State: sensorState{db: db}}
	board := sensor.RadioBoardProvider{Board: boardReadings, MaxAge: modem.StaleReadingAfter}
	sh := sensor.NewHub(log, i2c, sensor.PiSugarProvider{}, board, &sensor.VirtualProvider{}, sensor.HTTPProvider{})
	if err := loadSensors(ctx, db, sh); err != nil {
		return nil, err
	}
	sh.OnUpdate(func(st []sensor.Status) {
		hub.Broadcast("sensors", sensorStatusDTOs(sh, st))
	})
	go sh.Poll(ctx, sensorPollInterval)
	return sh, nil
}

// liveRadio is the running radio generation, for the sensor hub, which outlives every reconnect.
var liveRadio atomic.Pointer[modem.State]

// boardReadings is the running board's cached readings; only a transport with a board to answer the probe has any.
func boardReadings() (sensor.BoardReadings, error) {
	ms := liveRadio.Load()
	if ms == nil || ms.Stats == nil {
		return sensor.BoardReadings{}, sensor.ErrNoRadio
	}
	last, silentFrom, ok := radioSeen.answered(ms.Stats)
	if !ok {
		return sensor.BoardReadings{}, sensor.ErrNoBoard
	}
	ds := ms.Stats.CachedStats()
	return sensor.BoardReadings{
		Transport:   ms.Stats.Transport(),
		BatteryV:    float64(ds.BatteryMV) / 1000,
		HaveBattery: ds.HaveBattery,
		MCUTempC:    ds.MCUTempC,
		HaveMCUTemp: ds.HaveMCUTemp,
		At:          last,
		SilentSince: silentFrom,
	}, nil
}

// sensorState is the store behind a sensor's learned calibration; a save waits for the writer, so a reopen reads what its close saved.
type sensorState struct{ db *store.Store }

func (s sensorState) LoadState(sensorID int64) ([]byte, error) {
	return s.db.Sensors.LoadState(context.Background(), sensorID)
}

func (s sensorState) SaveState(sensorID int64, state []byte) error {
	var err error
	s.db.WriteSync(func() { err = s.db.Sensors.SaveState(context.Background(), sensorID, state) })
	return err
}

func loadSensors(ctx context.Context, db *store.Store, sh *sensor.Hub) error {
	rows, err := db.Sensors.List(ctx)
	if err != nil {
		return fmt.Errorf("reading sensors: %w", err)
	}
	specs := make([]sensor.Spec, 0, len(rows))
	for _, r := range rows {
		specs = append(specs, sensor.Spec{
			ID: r.ID, Provider: r.Provider, Kind: r.Kind, Name: r.Name, Options: r.Options,
			Bindings: specBindings(r.Bindings),
		})
	}
	sh.Set(specs)
	return nil
}

func (b *backend) Sensors() []api.SensorStatus {
	return sensorStatusDTOs(b.sensors, b.sensors.Snapshot())
}

func (b *backend) SensorProviders(ctx context.Context) []api.SensorProviderInfo {
	infos := b.sensors.Providers(ctx)
	out := make([]api.SensorProviderInfo, 0, len(infos))
	for _, p := range infos {
		out = append(out, api.SensorProviderInfo{
			ID: p.ID, Label: p.Label, Available: p.Available, Reason: p.Reason,
		})
	}
	return out
}

func (b *backend) DiscoverSensors(ctx context.Context, provider string) (api.SensorScan, error) {
	res, err := b.sensors.Discover(ctx, provider)
	if err != nil {
		return api.SensorScan{}, api.Invalid(err)
	}
	out := api.SensorScan{
		Candidates: make([]api.SensorCandidate, 0, len(res.Candidates)),
		Problems:   make([]api.SensorProviderProblem, 0, len(res.Problems)),
	}
	for _, c := range res.Candidates {
		out.Candidates = append(out.Candidates, api.SensorCandidate{
			Kind: c.Kind, Provider: c.Provider, Label: c.Label, Detail: c.Detail,
			Addable: c.Addable, UsedBy: c.UsedBy, Options: c.Options,
		})
	}
	for _, p := range res.Problems {
		out.Problems = append(out.Problems, api.SensorProviderProblem{
			Provider: p.Provider, Label: p.Label, Reason: p.Reason,
		})
	}
	return out, nil
}

func (b *backend) SensorKinds(provider string) ([]api.SensorKindInfo, error) {
	kinds, err := b.sensors.Kinds(provider)
	if err != nil {
		return nil, api.Invalid(err)
	}
	out := make([]api.SensorKindInfo, 0, len(kinds))
	for _, k := range kinds {
		fields := make([]api.SensorField, 0, len(k.Fields))
		for _, f := range k.Fields {
			field := api.SensorField{
				Key: f.Key, Label: f.Label, Help: f.Help,
				Default: f.Default, Choices: f.Choices, Required: f.Required,
				Multiline: f.Multiline, Identifies: f.Identifies, Secret: f.Secret,
			}
			if f.When != nil {
				field.When = &api.SensorFieldWhen{Key: f.When.Key, Values: f.When.Values}
			}
			field.Type, field.AtLeast, field.Tested = f.Type, f.AtLeast, f.Tested
			field.MinSecs, field.MaxSecs = f.Min.Seconds(), f.Max.Seconds()
			for _, c := range f.Columns {
				field.Columns = append(field.Columns, api.SensorColumn{Key: c.Key, Label: c.Label, Placeholder: c.Placeholder, Required: c.Required})
			}
			fields = append(fields, field)
		}
		metrics := make([]string, 0, len(k.Metrics))
		for _, m := range k.Metrics {
			metrics = append(metrics, string(m))
		}
		out = append(out, api.SensorKindInfo{
			Kind: k.Kind, Provider: k.Provider, Label: k.Label,
			Description: k.Description, Category: k.Category,
			Metrics: metrics, Binds: k.Binds, Fields: fields, Testable: k.Testable,
		})
	}
	return out, nil
}

// sensorWrites holds each sensor or map change from its checks to its reload; ponytail: one process-wide lock, per-sensor if edits queue behind a slow reload.
var sensorWrites sync.Mutex

func (b *backend) CreateSensor(ctx context.Context, in api.SensorInput) (int64, error) {
	sensorWrites.Lock()
	defer sensorWrites.Unlock()
	if len(in.KeepSecrets) > 0 {
		return 0, api.Invalid(errors.New("a new sensor has no stored secrets to keep"))
	}
	spec, err := b.prepareSensor(0, in)
	if err != nil {
		return 0, err
	}
	row := store.Sensor{
		Provider: spec.Provider, Kind: spec.Kind, Name: spec.Name, Options: spec.Options,
		Bindings: rowBindings(spec.Bindings),
	}
	b.db.WriteSync(func() { err = b.db.Sensors.Create(ctx, &row) })
	if err != nil {
		return 0, err
	}
	// The row is written, so a client leaving now must not turn the reload into a failure.
	return row.ID, loadSensors(context.WithoutCancel(ctx), b.db, b.sensors)
}

func (b *backend) UpdateSensor(ctx context.Context, id int64, in api.SensorInput) error {
	sensorWrites.Lock()
	defer sensorWrites.Unlock()
	if err := b.keepSecrets(id, &in); err != nil {
		return err
	}
	spec, err := b.prepareSensor(id, in)
	if err != nil {
		return err
	}
	if err := b.checkDependents(ctx, spec); err != nil {
		return err
	}
	row := store.Sensor{
		ID: id, Provider: spec.Provider, Kind: spec.Kind, Name: spec.Name, Options: spec.Options,
		Bindings: rowBindings(spec.Bindings),
	}
	b.db.WriteSync(func() { err = b.db.Sensors.Update(ctx, &row) })
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	if err := loadSensors(ctx, b.db, b.sensors); err != nil {
		return err
	}
	return b.telemetry.Load(ctx, b.db)
}

func (b *backend) TestSensor(ctx context.Context, id int64, in api.SensorInput) (api.SensorTest, error) {
	if id == 0 && len(in.KeepSecrets) > 0 {
		return api.SensorTest{}, api.Invalid(errors.New("a new sensor has no stored secrets to keep"))
	}
	if err := b.keepSecrets(id, &in); err != nil {
		return api.SensorTest{}, err
	}
	res, err := b.sensors.Test(ctx, sensor.Spec{
		ID: id, Provider: in.Provider, Kind: in.Kind, Name: in.Name, Options: in.Options,
		Bindings: apiToSpecBindings(in.Bindings),
	})
	if err != nil {
		return api.SensorTest{}, api.Invalid(err)
	}
	out := api.SensorTest{
		Error: res.Err, Status: res.Status, ContentType: res.ContentType,
		Body: res.Body, Truncated: res.Truncated, Values: []api.SensorTestValue{},
	}
	for _, v := range res.Values {
		tv := api.SensorTestValue{Metric: string(v.Metric), Unit: v.Unit, Error: v.Err}
		if v.Err == "" {
			tv.Value = &v.Value
		}
		out.Values = append(out.Values, tv)
	}
	return out, nil
}

// keepSecrets carries each named secret over from the stored sensor; naming one that is not a secret of its kind, or also sending it, is refused.
func (b *backend) keepSecrets(id int64, in *api.SensorInput) error {
	if len(in.KeepSecrets) == 0 {
		return nil
	}
	var stored *sensor.Spec
	for _, s := range b.sensors.Snapshot() {
		if s.Spec.ID == id {
			stored = &s.Spec
		}
	}
	if stored == nil {
		return api.Invalid(fmt.Errorf("no sensor %d to keep secrets from", id))
	}
	if stored.Provider != in.Provider || stored.Kind != in.Kind {
		return api.Invalid(errors.New("a sensor changed to another kind has no stored secrets to keep"))
	}
	kinds, err := b.sensors.Kinds(in.Provider)
	if err != nil {
		return api.Invalid(err)
	}
	secret := map[string]bool{}
	for _, k := range kinds {
		if k.Kind == in.Kind {
			for _, f := range k.Fields {
				secret[f.Key] = f.Secret
			}
		}
	}
	opts := maps.Clone(in.Options)
	if opts == nil {
		opts = map[string]string{}
	}
	for _, key := range in.KeepSecrets {
		if !secret[key] {
			return api.Invalid(fmt.Errorf("%q is not a secret of this kind, so there is nothing to keep", key))
		}
		if opts[key] != "" {
			return api.Invalid(fmt.Errorf("%q is both sent and kept; send one or the other", key))
		}
		opts[key] = stored.Options[key]
	}
	in.Options = opts
	return nil
}

// checkDependents refuses an edit that would leave a published channel or a derived sensor reading something this one stops reporting.
func (b *backend) checkDependents(ctx context.Context, spec sensor.Spec) error {
	reports := b.sensors.Reports(spec)
	nodes, err := b.telemetryNodes(ctx)
	if err != nil {
		return err
	}
	names := map[api.TelemetryNode]string{}
	for _, n := range nodes {
		names[n.Node] = n.Name
	}
	for node, entries := range b.telemetry.All() {
		for _, e := range entries {
			if e.SensorID == spec.ID && !reports[e.Metric] {
				return api.Invalid(fmt.Errorf("channel %d of %s publishes %s from this sensor, which it would no longer report; take it off that map first",
					e.Channel, names[api.TelemetryNode{Kind: node.Kind, ID: node.ID}], e.Metric))
			}
		}
	}
	for _, s := range b.sensors.Snapshot() {
		for _, bd := range s.Spec.Bindings {
			if bd.SensorID == spec.ID && !reports[bd.Metric] {
				return api.Invalid(fmt.Errorf("%s reads %s from this sensor, which it would no longer report", s.Spec.Name, bd.Metric))
			}
		}
	}
	return nil
}

// prepareSensor defaults then validates, and the id lets the hub refuse an edit that makes two sensors read each other.
func (b *backend) prepareSensor(id int64, in api.SensorInput) (sensor.Spec, error) {
	spec, err := b.sensors.Prepare(sensor.Spec{
		ID: id, Provider: in.Provider, Kind: in.Kind, Name: in.Name, Options: in.Options,
		Bindings: apiToSpecBindings(in.Bindings),
	})
	if err != nil {
		return spec, api.Invalid(err)
	}
	return spec, nil
}

func specBindings(bs []store.SensorBinding) []sensor.Binding {
	out := make([]sensor.Binding, 0, len(bs))
	for _, b := range bs {
		out = append(out, sensor.Binding{Name: b.Name, SensorID: b.SensorID, Metric: sensor.Metric(b.Metric)})
	}
	return out
}

func rowBindings(bs []sensor.Binding) []store.SensorBinding {
	out := make([]store.SensorBinding, 0, len(bs))
	for _, b := range bs {
		out = append(out, store.SensorBinding{Name: b.Name, SensorID: b.SensorID, Metric: string(b.Metric)})
	}
	return out
}

func apiToSpecBindings(bs []api.SensorBinding) []sensor.Binding {
	out := make([]sensor.Binding, 0, len(bs))
	for _, b := range bs {
		out = append(out, sensor.Binding{Name: b.Name, SensorID: b.SensorID, Metric: sensor.Metric(b.Metric)})
	}
	return out
}

func apiBindings(bs []sensor.Binding) []api.SensorBinding {
	out := make([]api.SensorBinding, 0, len(bs))
	for _, b := range bs {
		out = append(out, api.SensorBinding{Name: b.Name, SensorID: b.SensorID, Metric: string(b.Metric)})
	}
	return out
}

func (b *backend) DeleteSensor(ctx context.Context, id int64) error {
	sensorWrites.Lock()
	defer sensorWrites.Unlock()
	// Refused rather than left dangling: a derived sensor reading this one would fail every poll from then on.
	for _, s := range b.sensors.Snapshot() {
		for _, bd := range s.Spec.Bindings {
			if bd.SensorID == id {
				return api.Invalid(fmt.Errorf("%s reads this sensor; remove it, or its binding, first", s.Spec.Name))
			}
		}
	}
	var err error
	b.db.WriteSync(func() { err = b.db.Sensors.Delete(ctx, id) })
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	if err := loadSensors(ctx, b.db, b.sensors); err != nil {
		return err
	}
	// A deleted sensor takes its map rows with it, so the publisher has to be told.
	return b.telemetry.Load(ctx, b.db)
}

// staleAfter is how old a reading may be before it is out of date: the sensor's own where it has one, three polls where the poll decides.
func staleAfter(s sensor.Status) time.Duration {
	if s.StaleAfter > 0 {
		return s.StaleAfter
	}
	return 3 * sensorPollInterval
}

func sensorStatusDTOs(sh *sensor.Hub, in []sensor.Status) []api.SensorStatus {
	kinds, _ := sh.Kinds("")
	category := make(map[[2]string]string, len(kinds))
	secret := map[[3]string]bool{}
	for _, k := range kinds {
		category[[2]string{k.Provider, k.Kind}] = k.Category
		for _, f := range k.Fields {
			secret[[3]string{k.Provider, k.Kind, f.Key}] = f.Secret
		}
	}
	now := time.Now()
	out := make([]api.SensorStatus, 0, len(in))
	for _, s := range in {
		row := api.SensorStatus{
			ID:             s.Spec.ID,
			Provider:       s.Spec.Provider,
			Kind:           s.Spec.Kind,
			Name:           s.Spec.Name,
			Bindings:       apiBindings(s.Spec.Bindings),
			Reports:        []string{},
			Readings:       make([]api.SensorReading, 0, len(s.Readings)),
			Category:       category[[2]string{s.Spec.Provider, s.Spec.Kind}],
			SecretsSet:     []string{},
			StaleAfterSecs: staleAfter(s).Seconds(),
			Error:          s.Err,
		}
		// Options is shared with the hub's spec, so the secrets come off a copy.
		row.Options = map[string]string{}
		for k, v := range s.Spec.Options {
			if !secret[[3]string{s.Spec.Provider, s.Spec.Kind, k}] {
				row.Options[k] = v
			} else if v != "" {
				row.SecretsSet = append(row.SecretsSet, k)
			}
		}
		slices.Sort(row.SecretsSet)
		for m := range sh.Reports(s.Spec) {
			row.Reports = append(row.Reports, string(m))
		}
		slices.Sort(row.Reports)
		for _, r := range s.Readings {
			row.Readings = append(row.Readings, api.SensorReading{
				Metric: string(r.Metric), Label: r.Label, Value: r.Value, Unit: r.Unit,
				Format: sensor.FormatOf(r.Metric), Role: sensor.RoleOf(r.Metric),
			})
		}
		// Null until the sensor is first read, which tells the page that apart from a read that reported nothing.
		if !s.At.IsZero() {
			at := s.At.UTC().Format(time.RFC3339)
			row.At = &at
			age := math.Round(max(0, now.Sub(s.At).Seconds())*10) / 10
			row.AgeSecs = &age
		}
		if !s.RetryAt.IsZero() && s.Err != "" {
			in := math.Round(max(0, s.RetryAt.Sub(now).Seconds())*10) / 10
			row.RetryInSecs = &in
		}
		out = append(out, row)
	}
	return out
}
