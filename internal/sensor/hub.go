package sensor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// Hub owns the configured sensors; consumers pull with Snapshot or subscribe with OnUpdate, and never see a Provider.
type Hub struct {
	log           *slog.Logger
	providers     map[string]Provider
	providerOrder []string

	// pollMu stops Set closing a sensor a pass is mid-read on; ponytail: one lock, split per-sensor if a wedged bus makes adding one feel stuck.
	pollMu sync.Mutex

	// mu guards what a consumer reads and is never held across a sensor read, so Snapshot never waits on hardware.
	mu        sync.RWMutex
	entries   map[int64]*entry
	listeners []func([]Status)
	// order is the poll order, every sensor after the ones it reads; ranging the map would reshuffle it every pass.
	order []int64

	// kick asks for an immediate pass, so a sensor just added reads now; buffered, since one pending kick is enough.
	kick chan struct{}
}

// entry is one configured sensor; sensor stays nil until a read opens it, and a failed open is retried every pass.
type entry struct {
	spec     Spec
	sensor   Sensor
	readings []Reading
	at       time.Time
	err      string
	// openFails and nextOpen back off a sensor that will not open, as each try sends its identify sequence to whatever is there.
	openFails int
	nextOpen  time.Time
}

// openBackoff doubles from one poll to a cap, so a part plugged in late is found within minutes without a write every pass.
const (
	openBackoffFirst = 5 * time.Second
	openBackoffMax   = 5 * time.Minute
	// openSlack lets a pass that lands a moment before its retry time count, or every step would wait an extra poll.
	openSlack = time.Second
)

func openBackoff(fails int) time.Duration {
	return min(openBackoffFirst<<min(fails-1, 16), openBackoffMax)
}

func NewHub(log *slog.Logger, providers ...Provider) *Hub {
	h := &Hub{
		log:       log.With("component", "sensor"),
		providers: make(map[string]Provider, len(providers)),
		entries:   map[int64]*entry{},
		kick:      make(chan struct{}, 1),
	}
	for _, p := range providers {
		h.providers[p.ID()] = p
		h.providerOrder = append(h.providerOrder, p.ID())
		// A provider that derives sensors from other sensors reads them back through the hub.
		if b, ok := p.(Binder); ok {
			b.Bind(h.Snapshot)
		}
	}

	return h
}

// Providers lists every provider with whether it can run here.
func (h *Hub) Providers(ctx context.Context) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(h.providerOrder))
	for _, id := range h.providerOrder {
		p := h.providers[id]
		ok, reason := p.Available(ctx)
		out = append(out, ProviderInfo{ID: id, Label: p.Label(), Available: ok, Reason: reason})
	}
	return out
}

// Discover scans providers, all of them when providerID is empty; one that could not be scanned lands in Problems.
func (h *Hub) Discover(ctx context.Context, providerID string) (DiscoverResult, error) {
	// A scan's reads share the bus with live ones, and a part mid-measurement answers the first read it sees; -race cannot see that.
	h.pollMu.Lock()
	defer h.pollMu.Unlock()

	ids := h.providerOrder
	if providerID != "" {
		if _, ok := h.providers[providerID]; !ok {
			return DiscoverResult{}, fmt.Errorf("unknown provider %q", providerID)
		}
		ids = []string{providerID}
	}

	var res DiscoverResult
	for _, id := range ids {
		p := h.providers[id]
		if ok, reason := p.Available(ctx); !ok {
			res.Problems = append(res.Problems, ProviderProblem{Provider: id, Label: p.Label(), Reason: reason})
			continue
		}
		found, err := p.Discover(ctx)
		if err != nil {
			res.Problems = append(res.Problems, ProviderProblem{Provider: id, Label: p.Label(), Reason: err.Error()})
		}
		for _, c := range found {
			c.Provider = id
			if c.Addable {
				c.UsedBy, _ = h.claimedBy(Spec{Provider: id, Kind: c.Kind, Options: c.Options})
			}
			res.Candidates = append(res.Candidates, c)
		}
	}
	return res, nil
}

// Kinds is the parts catalogue; an empty providerID returns every provider's, so a part can be found without knowing its bus.
func (h *Hub) Kinds(providerID string) ([]KindInfo, error) {
	if providerID != "" {
		p, ok := h.providers[providerID]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q", providerID)
		}
		return tag(p), nil
	}
	var out []KindInfo
	for _, id := range h.providerOrder {
		out = append(out, tag(h.providers[id])...)
	}
	return out, nil
}

// tag stamps each entry with the provider it came from, so nothing downstream has to track that.
func tag(p Provider) []KindInfo {
	kinds := p.Kinds()
	_, testable := p.(Tester)
	for i := range kinds {
		kinds[i].Provider = p.ID()
		kinds[i].Testable = testable
	}
	return kinds
}

// Prepare defaults then validates, so a spec cannot be validated in one shape and stored in another.
func (h *Hub) Prepare(spec Spec) (Spec, error) {
	// Trimmed here, not in the form: a name of spaces passes "is required" and then labels nothing.
	spec.Name = strings.TrimSpace(spec.Name)
	if err := spec.Validate(); err != nil {
		return spec, err
	}
	spec, _, err := h.prepareOptions(spec, false)
	if err != nil {
		return spec, err
	}
	if err := h.checkUnique(spec); err != nil {
		return spec, err
	}
	if err := h.checkBindings(spec); err != nil {
		return spec, err
	}
	return spec, nil
}

// prepareOptions fills in defaults and holds the options to their kind's fields and the provider's own rules; a test may leave empty the lists it is there to help fill, and its provider checks the rest itself.
func (h *Hub) prepareOptions(spec Spec, test bool) (Spec, Provider, error) {
	p, ok := h.providers[spec.Provider]
	if !ok {
		return spec, nil, fmt.Errorf("unknown provider %q", spec.Provider)
	}
	var kind *KindInfo
	for _, k := range p.Kinds() {
		if k.Kind == spec.Kind {
			kind = &k
			break
		}
	}
	if kind == nil {
		return spec, nil, fmt.Errorf("%s has no sensor kind %q", p.Label(), spec.Kind)
	}

	// Only the kind's own options, or an injected "metric" on an SHTC3 lets a binding name a reading that never comes.
	for key := range spec.Options {
		if !slices.ContainsFunc(kind.Fields, func(f Field) bool { return f.Key == key }) {
			return spec, nil, fmt.Errorf("%s takes no option %q", kind.Label, key)
		}
	}
	if len(spec.Bindings) > 0 && !kind.Binds {
		return spec, nil, fmt.Errorf("%s does not read other sensors, so it takes no bindings", kind.Label)
	}
	opts := map[string]string{}
	maps.Copy(opts, spec.Options)
	for _, f := range kind.Fields {
		// Dropped, or a password stays stored under an auth method that no longer uses it.
		if !f.Shown(opts) {
			delete(opts, f.Key)
			continue
		}
		if opts[f.Key] == "" && f.Default != "" {
			opts[f.Key] = f.Default
		}
		v := opts[f.Key]
		if test && f.Tested && (v == "" || v == "[]") {
			continue
		}
		if f.Required && v == "" {
			return spec, nil, fmt.Errorf("%s is required", f.Label)
		}
		if v != "" && len(f.Choices) > 0 && !slices.Contains(f.Choices, v) {
			return spec, nil, fmt.Errorf("%s must be one of %s", f.Label, strings.Join(f.Choices, ", "))
		}
		limit := maxOptionLen
		if f.Type == FieldList {
			limit = maxListLen
		}
		if len(v) > limit {
			return spec, nil, fmt.Errorf("%s is too long: %d bytes, and %d is the most", f.Label, len(v), limit)
		}
		if hasControl(v, f.Multiline) {
			return spec, nil, fmt.Errorf("%s has a control character in it", f.Label)
		}
		if v != "" {
			if err := checkField(f, v, opts, kind.Fields); err != nil {
				return spec, nil, err
			}
		}
	}
	spec.Options = opts

	if v, ok := p.(Validator); ok && !test {
		if err := v.Validate(spec); err != nil {
			return spec, nil, err
		}
	}
	return spec, p, nil
}

// Test tries an unsaved spec once, held to the same option rules as a save but not to a unique name, which a form fills in last.
func (h *Hub) Test(ctx context.Context, spec Spec) (TestResult, error) {
	spec, p, err := h.prepareOptions(spec, true)
	if err != nil {
		return TestResult{}, err
	}
	t, ok := p.(Tester)
	if !ok {
		return TestResult{}, fmt.Errorf("%s sensors cannot be tried before they are added", p.Label())
	}
	return t.Test(ctx, spec), nil
}

// checkUnique refuses a collision: a name, which is how other screens tell sensors apart, or a chip, which two sensors would interleave reads on.
func (h *Hub) checkUnique(spec Spec) error {
	h.mu.RLock()
	taken := ""
	for id, e := range h.entries {
		// Editing a sensor never collides with itself.
		if id != spec.ID && strings.EqualFold(e.spec.Name, spec.Name) {
			taken = e.spec.Name
		}
	}
	h.mu.RUnlock()
	if taken != "" {
		return fmt.Errorf("another sensor is already called %q", taken)
	}
	if name, claim := h.claimedBy(spec); name != "" {
		return fmt.Errorf("%s already uses %s", name, claim)
	}
	return nil
}

// claimedBy names the configured sensor, other than the spec's own, already on the part the spec would claim, and that claim.
func (h *Hub) claimedBy(spec Spec) (name, claim string) {
	claimer, ok := h.providers[spec.Provider].(Claimer)
	if !ok {
		return "", ""
	}
	if claim = claimer.Claim(spec); claim == "" {
		return "", ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, e := range h.entries {
		if id != spec.ID && e.spec.Provider == spec.Provider && claimer.Claim(e.spec) == claim {
			return e.spec.Name, claim
		}
	}
	return "", claim
}

// Reports is what a sensor of this spec publishes: its kind's metrics, and the one its operator names where the kind asks for one.
func (h *Hub) Reports(spec Spec) map[Metric]bool {
	out := map[Metric]bool{}
	p, ok := h.providers[spec.Provider]
	if !ok {
		return out
	}
	for _, k := range p.Kinds() {
		if k.Kind != spec.Kind {
			continue
		}
		metrics := k.Metrics
		if k.ReportsUnder != nil {
			if narrowed := k.ReportsUnder(spec.Options); narrowed != nil {
				metrics = narrowed
			}
		}
		for _, m := range metrics {
			out[m] = true
		}
		if slices.ContainsFunc(k.Fields, func(f Field) bool { return f.Key == optMetric }) {
			if m := Metric(strings.TrimSpace(spec.Options[optMetric])); m != "" {
				out[m] = true
			}
		}
	}
	return out
}

// checkBindings refuses an unconfigured source, a reading it does not produce, or a loop.
func (h *Hub) checkBindings(spec Spec) error {
	if len(spec.Bindings) == 0 {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	byID := make(map[int64]Spec, len(h.entries)+1)
	for id, e := range h.entries {
		byID[id] = e.spec
	}
	for _, b := range spec.Bindings {
		src, ok := byID[b.SensorID]
		if !ok {
			return fmt.Errorf("%s reads sensor %d, which is not configured", b.Name, b.SensorID)
		}
		// Skipped at poll time otherwise, which is indistinguishable from a sensor that is merely failing.
		if !h.Reports(src)[b.Metric] {
			return fmt.Errorf("%s reads %s from %s, which does not report it", b.Name, b.Metric, src.Name)
		}
	}
	if spec.ID == 0 {
		return nil
	}
	byID[spec.ID] = spec
	_, err := orderByDependency(byID)
	return err
}

// Set reconciles the live set to the configured one; an unchanged spec keeps its open sensor and last reading.
func (h *Hub) Set(specs []Spec) {
	h.pollMu.Lock()
	defer h.pollMu.Unlock()

	h.mu.Lock()
	next := make(map[int64]*entry, len(specs))
	added := false
	// Closed once mu is released: a close is bus I/O, and Snapshot answers mesh telemetry on the RX goroutine.
	var closing []Sensor
	for _, s := range specs {
		if old, ok := h.entries[s.ID]; ok && specEqual(old.spec, s) {
			// specEqual ignores the name, so without taking the new spec a rename would never reach a consumer.
			old.spec = s
			next[s.ID] = old
			continue
		}
		if old, ok := h.entries[s.ID]; ok {
			closing = append(closing, old.sensor)
		}
		next[s.ID] = &entry{spec: s}
		added = true
	}
	for id, old := range h.entries {
		if _, kept := next[id]; !kept {
			closing = append(closing, old.sensor)
		}
	}
	byID := make(map[int64]Spec, len(next))
	for id, e := range next {
		byID[id] = e.spec
	}
	order, err := orderByDependency(byID)
	if err != nil {
		// Prepare refuses loops, so one stored another way keeps polling on last pass's values.
		h.log.Error("sensor dependency loop, derived sensors in it will read stale values", "error", err)
		order = slices.Sorted(maps.Keys(byID))
	}
	h.entries = next
	h.order = order
	h.mu.Unlock()
	for _, s := range closing {
		closeSensor(s)
	}

	if added {
		h.requestPass()
	} else {
		h.notify()
	}
}

// Snapshot is the pull seam: every configured sensor with its last result, in id order.
func (h *Hub) Snapshot() []Status {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.snapshotLocked()
}

func (h *Hub) snapshotLocked() []Status {
	out := make([]Status, 0, len(h.entries))
	for _, e := range h.entries {
		st := Status{Spec: e.spec, Readings: slices.Clone(e.readings), At: e.at, Err: e.err}
		if e.sensor == nil {
			st.RetryAt = e.nextOpen
		}
		if p, ok := h.providers[e.spec.Provider].(Staler); ok {
			if d, ok := p.StaleAfter(e.spec); ok {
				st.StaleAfter = d
			}
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b Status) int { return cmp.Compare(a.Spec.ID, b.Spec.ID) })
	return out
}

// OnUpdate is the push seam: fn runs after every poll pass and every configuration change.
func (h *Hub) OnUpdate(fn func([]Status)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, fn)
}

// Poll reads every sensor on each tick, and a sensor just added straight away.
func (h *Hub) Poll(ctx context.Context, every time.Duration) {
	h.pass(ctx, false)
	// Started after the first pass, whose opens would otherwise make the first interval short.
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.pass(ctx, false)
		case <-h.kick:
			h.pass(ctx, true)
		}
	}
}

// Close releases every open sensor; the configured set is left alone.
func (h *Hub) Close() {
	h.pollMu.Lock()
	defer h.pollMu.Unlock()

	h.mu.Lock()
	var closing []Sensor
	for _, e := range h.entries {
		closing = append(closing, e.sensor)
		e.sensor = nil
	}
	h.mu.Unlock()
	for _, s := range closing {
		closeSensor(s)
	}
}

// pass reads each entry in turn, as they share buses; unopened keeps the rest on their tick, or a BME680 reads hot off its heater.
func (h *Hub) pass(ctx context.Context, unopened bool) {
	h.pollMu.Lock()
	defer h.pollMu.Unlock()

	h.mu.RLock()
	todo := make([]*entry, 0, len(h.entries))
	for _, id := range h.order {
		if e, ok := h.entries[id]; ok && (!unopened || e.sensor == nil) {
			todo = append(todo, e)
		}
	}
	h.mu.RUnlock()

	for _, e := range todo {
		// Shutdown cancels before it closes the hub, so a late pass stops rather than reopening what Close shut.
		if ctx.Err() != nil {
			return
		}
		h.read(ctx, e)
	}
	h.notify()
}

// read opens then reads outside h.mu; pollMu already excludes Set, so the lock only publishes.
func (h *Hub) read(ctx context.Context, e *entry) {
	if e.sensor == nil {
		p, ok := h.providers[e.spec.Provider]
		if !ok {
			h.fail(e, fmt.Sprintf("unknown provider %q", e.spec.Provider))
			return
		}
		if time.Until(e.nextOpen) > openSlack {
			return
		}
		s, err := p.Open(e.spec)
		if err != nil {
			h.mu.Lock()
			e.openFails++
			e.nextOpen = time.Now().Add(openBackoff(e.openFails))
			h.mu.Unlock()
			h.fail(e, err.Error())
			return
		}
		h.mu.Lock()
		e.sensor, e.openFails, e.nextOpen = s, 0, time.Time{}
		h.mu.Unlock()
	}

	// ponytail: no per-read deadline, since periph's Sense takes no context; a wedged ioctl holds pollMu until it returns.
	readings, err := e.sensor.Read(ctx)
	// JSON has no NaN or infinity, so one would blank every sensor's status, not just this one's.
	if i := slices.IndexFunc(readings, func(r Reading) bool { return math.IsNaN(r.Value) || math.IsInf(r.Value, 0) }); err == nil && i >= 0 {
		err = fmt.Errorf("%s read %v, which is not a number", readings[i].Metric, readings[i].Value)
	}
	// Waiting on a source's first sample is not a failure, and would flash red on every start.
	if errors.Is(err, errNotYet) && e.at.IsZero() {
		return
	}
	if err != nil {
		h.fail(e, err.Error())
		return
	}
	at := time.Now()
	// A part sampled on its own clock is as old as its sample, not as this read.
	if s, ok := e.sensor.(sampler); ok {
		if at = s.SampledAt(); at.IsZero() {
			return
		}
	}
	h.mu.Lock()
	recovered := e.err != ""
	e.readings, e.at, e.err = readings, at, ""
	h.mu.Unlock()
	if recovered {
		h.log.Info("sensor reading again", "sensor", e.spec.Name, "provider", e.spec.Provider)
	}
}

// errNotYet is a read that has nothing to give until something else has been read once.
var errNotYet = errors.New("waiting for a first reading")

// sampler is a sensor that reads on its own clock and hands back its latest sample; a zero time is none taken yet.
type sampler interface {
	SampledAt() time.Time
}

// fail leaves the last good readings alone, so a consumer can show them with their age; it logs a change, not every pass.
func (h *Hub) fail(e *entry, msg string) {
	h.mu.Lock()
	changed := e.err != msg
	e.err = msg
	h.mu.Unlock()
	if changed {
		h.log.Warn("sensor failing", "sensor", e.spec.Name, "provider", e.spec.Provider, "error", msg)
	}
}

func (h *Hub) notify() {
	h.mu.RLock()
	snap := h.snapshotLocked()
	listeners := slices.Clone(h.listeners)
	h.mu.RUnlock()
	for _, fn := range listeners {
		fn(snap)
	}
}

func (h *Hub) requestPass() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func specEqual(a, b Spec) bool {
	return a.Provider == b.Provider && a.Kind == b.Kind && maps.Equal(a.Options, b.Options) &&
		slices.Equal(a.Bindings, b.Bindings)
}

// orderByDependency puts every sensor after the ones it reads; an unconfigured source is one sensor's read error, not an ordering problem.
func orderByDependency(specs map[int64]Spec) ([]int64, error) {
	ids := slices.Sorted(maps.Keys(specs))
	deps := map[int64][]int64{}
	for _, id := range ids {
		for _, b := range specs[id].Bindings {
			if _, ok := specs[b.SensorID]; !ok {
				continue
			}
			if !slices.Contains(deps[id], b.SensorID) {
				deps[id] = append(deps[id], b.SensorID)
			}
		}
	}

	done := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for len(out) < len(ids) {
		progressed := false
		for _, id := range ids {
			if done[id] {
				continue
			}
			ready := true
			for _, d := range deps[id] {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				done[id] = true
				out = append(out, id)
				progressed = true
			}
		}
		// Nothing became ready, so what is left waits on itself, directly or round a loop.
		if !progressed {
			var stuck []int64
			for _, id := range ids {
				if !done[id] {
					stuck = append(stuck, id)
				}
			}
			return nil, fmt.Errorf("sensors %v read each other in a loop", stuck)
		}
	}
	return out, nil
}

func closeSensor(s Sensor) {
	if c, ok := s.(io.Closer); ok {
		_ = c.Close()
	}
}
