package sensor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeProvider is driven from the test: every failure mode the hub keeps apart is reachable by setting a field.
type fakeProvider struct {
	mu        sync.Mutex
	available bool
	reason    string
	openErr   error
	readErr   error
	readings  []Reading
	openDelay time.Duration
	// closing, when set, parks Close until it is closed, and entered hears that one has parked: a bus slow to let go.
	closing, entered chan struct{}
	opens            int
	closes           int
	reads            int
	// tries counts every Open, the failed ones too.
	tries int
}

func (p *fakeProvider) ID() string    { return "fake" }
func (p *fakeProvider) Label() string { return "Fake" }

func (p *fakeProvider) Available(context.Context) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.available, p.reason
}

func (p *fakeProvider) Kinds() []KindInfo {
	return []KindInfo{{
		Kind: "thing", Label: "A thing",
		Metrics: []Metric{Temperature, Voltage},
		Fields: []Field{
			{Key: "n", Label: "Number", Required: true, Default: "1"},
			{Key: "mode", Label: "Mode", Choices: []string{"fast", "slow"}},
		},
	}}
}

func (p *fakeProvider) Discover(context.Context) ([]Candidate, error) {
	return []Candidate{{Kind: "thing", Label: "A thing", Options: map[string]string{"n": "1"}}}, nil
}

func (p *fakeProvider) Open(Spec) (Sensor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tries++
	if p.openErr != nil {
		return nil, p.openErr
	}
	time.Sleep(p.openDelay)
	p.opens++
	return &fakeSensor{p: p}, nil
}

func (p *fakeProvider) set(f func(*fakeProvider)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

type fakeSensor struct{ p *fakeProvider }

func (s *fakeSensor) Read(context.Context) ([]Reading, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.readErr != nil {
		return nil, s.p.readErr
	}
	s.p.reads++
	return s.p.readings, nil
}

func (s *fakeSensor) Close() error {
	s.p.mu.Lock()
	wait, entered := s.p.closing, s.p.entered
	s.p.mu.Unlock()
	if wait != nil {
		entered <- struct{}{}
		<-wait
	}
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.closes++
	return nil
}

func newFake() *fakeProvider {
	return &fakeProvider{available: true, readings: []Reading{{Metric: Temperature, Value: 21.5, Unit: "C"}}}
}

func spec(id int64) Spec {
	return Spec{ID: id, Provider: "fake", Kind: "thing", Name: "s", Options: map[string]string{"n": "1"}}
}

// Never polled, read fine and failed are the three states the framework exists to keep apart.
func TestHub_ThreeStatesAreDistinguishable(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})

	got := h.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot has %d entries, want 1", len(got))
	}
	if !got[0].At.IsZero() || got[0].Err != "" || len(got[0].Readings) != 0 {
		t.Fatalf("before any poll: got %+v, want no readings, no timestamp and no error", got[0])
	}

	h.pass(t.Context(), false)
	got = h.Snapshot()
	if got[0].At.IsZero() || got[0].Err != "" || len(got[0].Readings) != 1 {
		t.Fatalf("after a good poll: got %+v, want one reading, a timestamp and no error", got[0])
	}
	goodAt := got[0].At

	p.set(func(p *fakeProvider) { p.readErr = errors.New("bus went away") })
	h.pass(t.Context(), false)
	got = h.Snapshot()
	if got[0].Err == "" {
		t.Fatal("after a failed poll: Err is empty, so a failing sensor is indistinguishable from a working one")
	}
	if !got[0].At.Equal(goodAt) || len(got[0].Readings) != 1 {
		t.Fatalf("after a failed poll: got %+v, want the last good reading and its timestamp kept", got[0])
	}
}

// An unavailable provider reports why, beside what the others found; an empty list reads as "nothing is attached".
func TestHub_DiscoverReportsAnUnavailableProviderRatherThanHidingIt(t *testing.T) {
	p := newFake()
	p.set(func(p *fakeProvider) { p.available, p.reason = false, "no bus here" })
	h := NewHub(testLog(), p)

	res, err := h.Discover(t.Context(), "fake")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none from a provider that could not be scanned", res.Candidates)
	}
	if len(res.Problems) != 1 || res.Problems[0].Reason != "no bus here" {
		t.Fatalf("problems = %+v, want the provider's own reason", res.Problems)
	}

	// The positive, so the check above is not passing because Discover never works.
	p.set(func(p *fakeProvider) { p.available, p.reason = true, "" })
	res, err = h.Discover(t.Context(), "fake")
	if err != nil || len(res.Candidates) != 1 {
		t.Fatalf("Discover when available = (%+v, %v), want one candidate and no error", res, err)
	}
	if res.Candidates[0].Provider != "fake" {
		t.Errorf("candidate provider = %q, want it stamped by the hub", res.Candidates[0].Provider)
	}
	if len(res.Problems) != 0 {
		t.Errorf("problems = %+v, want none", res.Problems)
	}
}

// One unreadable bus must not hide what every other provider found.
func TestHub_DiscoverAcrossProvidersKeepsGoingPastAFailure(t *testing.T) {
	broken := newFake()
	broken.set(func(p *fakeProvider) { p.available, p.reason = false, "no bus here" })
	h := NewHub(testLog(), broken, &secondProvider{})

	res, err := h.Discover(t.Context(), "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Provider != "second" {
		t.Errorf("candidates = %+v, want the working provider's one entry", res.Candidates)
	}
	if len(res.Problems) != 1 || res.Problems[0].Provider != "fake" {
		t.Errorf("problems = %+v, want the broken provider named", res.Problems)
	}
}

// The catalogue spans providers, so every entry has to say where it came from.
func TestHub_KindsAcrossProvidersAreTagged(t *testing.T) {
	h := NewHub(testLog(), newFake(), &secondProvider{})
	all, err := h.Kinds("")
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("catalogue has %d entries, want one per provider", len(all))
	}
	for _, k := range all {
		if k.Provider == "" {
			t.Errorf("catalogue entry %q has no provider", k.Kind)
		}
	}
	one, err := h.Kinds("second")
	if err != nil || len(one) != 1 || one[0].Provider != "second" {
		t.Fatalf("Kinds(\"second\") = (%+v, %v), want just that provider's", one, err)
	}
}

type secondProvider struct{}

func (secondProvider) ID() string                               { return "second" }
func (secondProvider) Label() string                            { return "Second" }
func (secondProvider) Available(context.Context) (bool, string) { return true, "" }
func (secondProvider) Kinds() []KindInfo {
	return []KindInfo{{Kind: "other", Label: "Other"}}
}
func (secondProvider) Discover(context.Context) ([]Candidate, error) {
	return []Candidate{{Kind: "other", Label: "Other", Addable: true}}, nil
}
func (secondProvider) Open(Spec) (Sensor, error) { return nil, errors.New("not opened in this test") }

// A failed Open stays in the list with its reason and is retried, so a replugged device recovers without a restart.
func TestHub_FailedOpenIsReportedAndRetried(t *testing.T) {
	p := newFake()
	p.set(func(p *fakeProvider) { p.openErr = errors.New("device busy") })
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})

	h.pass(t.Context(), false)
	got := h.Snapshot()
	if len(got) != 1 {
		t.Fatalf("a sensor that failed to open vanished from the list: %+v", got)
	}
	if got[0].Err != "device busy" {
		t.Fatalf("Err = %q, want the open failure", got[0].Err)
	}

	p.set(func(p *fakeProvider) { p.openErr = nil })
	h.entries[1].nextOpen = time.Time{}
	h.pass(t.Context(), false)
	got = h.Snapshot()
	if got[0].Err != "" || len(got[0].Readings) != 1 {
		t.Fatalf("after the device came back: got %+v, want a clean reading", got[0])
	}
	if !got[0].RetryAt.IsZero() {
		t.Errorf("RetryAt = %v on an open sensor, want zero", got[0].RetryAt)
	}
}

// Each open sends the part's identify sequence to whatever answers there, so a wrong address must not get one every pass.
func TestHub_AFailedOpenBacksOff(t *testing.T) {
	p := newFake()
	p.set(func(p *fakeProvider) { p.openErr = errors.New("wrong chip") })
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})

	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute} {
		before := time.Now()
		h.pass(t.Context(), false)
		h.pass(t.Context(), false)
		if p.tries != i+1 {
			t.Fatalf("after failure %d: %d opens, want %d: a pass inside the backoff opened it again", i+1, p.tries, i+1)
		}
		if got := h.Snapshot()[0].RetryAt.Sub(before); got < want || got > want+time.Second {
			t.Fatalf("after failure %d: next try in %v, want %v", i+1, got, want)
		}
		h.entries[1].nextOpen = time.Now().Add(openSlack / 2)
	}

	// An edit is a new entry, so a fixed address is tried straight away.
	fixed := spec(1)
	fixed.Options = map[string]string{"n": "2"}
	h.Set([]Spec{fixed})
	h.pass(t.Context(), false)
	if p.tries != 9 {
		t.Errorf("%d opens after an edit, want 9: the edit waited out the old backoff", p.tries)
	}
}

// Reconfiguring must not disturb unchanged sensors, or adding one drops every other sensor's readings.
func TestHub_SetKeepsUnchangedSensorsOpen(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})
	h.pass(t.Context(), false)

	p.mu.Lock()
	opensAfterFirst := p.opens
	p.mu.Unlock()
	if opensAfterFirst != 1 {
		t.Fatalf("opens = %d after the first pass, want 1", opensAfterFirst)
	}

	h.Set([]Spec{spec(1), spec(2)})
	h.pass(t.Context(), false)

	p.mu.Lock()
	opens, closes := p.opens, p.closes
	p.mu.Unlock()
	if opens != 2 {
		t.Errorf("opens = %d after adding a second sensor, want 2 (the first must not be reopened)", opens)
	}
	if closes != 0 {
		t.Errorf("closes = %d, want 0: nothing was removed", closes)
	}

	h.Set([]Spec{spec(2)})
	p.mu.Lock()
	closes = p.closes
	p.mu.Unlock()
	if closes != 1 {
		t.Errorf("closes = %d after removing a sensor, want 1", closes)
	}
	if got := h.Snapshot(); len(got) != 1 || got[0].Spec.ID != 2 {
		t.Errorf("snapshot after removal = %+v, want only sensor 2", got)
	}
}

// Changed options must reopen, or the old device is reported under the new configuration.
func TestHub_ChangedOptionsReopen(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})
	h.pass(t.Context(), false)

	changed := spec(1)
	changed.Options = map[string]string{"n": "2"}
	h.Set([]Spec{changed})
	h.pass(t.Context(), false)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opens != 2 || p.closes != 1 {
		t.Errorf("opens = %d, closes = %d after an options change, want 2 and 1", p.opens, p.closes)
	}
}

// A second read seconds after the first is off the sensor's schedule, and a BME680 then reads hot off its own heater.
func TestHub_AnAddReadsOnlyTheSensorsNotYetOpen(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	passes := make(chan struct{}, 8)
	h.OnUpdate(func([]Status) { passes <- struct{}{} })
	reads := func() int {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.reads
	}

	h.Set([]Spec{spec(1)})
	go h.Poll(t.Context(), time.Hour)
	<-passes
	<-passes // the add queued before polling started, which the first pass already covered
	if n := reads(); n != 1 {
		t.Fatalf("read %d times by the end of the startup passes, want once", n)
	}

	h.Set([]Spec{spec(1), spec(2)})
	<-passes
	if n := reads(); n != 2 {
		t.Errorf("%d reads after adding a second sensor, want 2: only the new one is read now", n)
	}
}

// Timed from before the first pass, whose opens are slow, the first interval is short and run-in finishes a poll late.
func TestHub_TheFirstTickIsAPeriodAfterTheFirstRead(t *testing.T) {
	const period = 150 * time.Millisecond
	p := newFake()
	p.set(func(p *fakeProvider) { p.openDelay = period / 2 })
	h := NewHub(testLog(), p)
	passes := make(chan []Status, 8)
	h.OnUpdate(func(s []Status) { passes <- s })
	h.Set([]Spec{spec(1)})
	go h.Poll(t.Context(), period)

	first := <-passes
	<-passes // the add queued before polling started
	tick := <-passes
	if gap := tick[0].At.Sub(first[0].At); gap < period {
		t.Errorf("the first tick read %s after the first pass, want at least the %s period", gap, period)
	}
}

func TestHub_OnUpdateFiresWithTheSnapshot(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)

	var mu sync.Mutex
	var last []Status
	h.OnUpdate(func(s []Status) {
		mu.Lock()
		defer mu.Unlock()
		last = s
	})

	h.Set([]Spec{spec(1)})
	h.pass(t.Context(), false)

	mu.Lock()
	defer mu.Unlock()
	if len(last) != 1 || len(last[0].Readings) != 1 {
		t.Fatalf("listener got %+v, want one sensor with one reading", last)
	}
}

// Snapshot must answer mid-read; the read is held in flight or the snapshot can win the race.
func TestHub_SnapshotDoesNotWaitOnASlowRead(t *testing.T) {
	p := &blockingProvider{entered: make(chan struct{}), block: make(chan struct{})}
	h := NewHub(testLog(), p)
	h.Set([]Spec{{ID: 1, Provider: "blocking", Kind: "slow", Name: "slow"}})

	done := make(chan struct{})
	go func() {
		h.pass(context.Background(), false)
		close(done)
	}()
	<-p.entered

	got := make(chan int, 1)
	go func() { got <- len(h.Snapshot()) }()
	select {
	case n := <-got:
		if n != 1 {
			t.Errorf("snapshot has %d entries, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot blocked behind an in-flight sensor read")
	}

	close(p.block)
	<-done
}

// blockingProvider's Read parks until the test releases it, announcing on entered that it has.
type blockingProvider struct {
	entered chan struct{}
	block   chan struct{}
}

func (p *blockingProvider) ID() string                               { return "blocking" }
func (p *blockingProvider) Label() string                            { return "Blocking" }
func (p *blockingProvider) Available(context.Context) (bool, string) { return true, "" }
func (p *blockingProvider) Kinds() []KindInfo {
	return []KindInfo{{Kind: "slow", Label: "Slow"}}
}
func (p *blockingProvider) Discover(context.Context) ([]Candidate, error) {
	return nil, nil
}
func (p *blockingProvider) Open(Spec) (Sensor, error) { return blockingSensor{p}, nil }

type blockingSensor struct{ p *blockingProvider }

func (s blockingSensor) Read(ctx context.Context) ([]Reading, error) {
	close(s.p.entered)
	select {
	case <-s.p.block:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSpec_Validate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"complete", Spec{Provider: "host", Kind: "load-average", Name: "load"}, false},
		{"no provider", Spec{Kind: "load-average", Name: "load"}, true},
		{"no kind", Spec{Provider: "host", Name: "load"}, true},
		{"no name", Spec{Provider: "host", Kind: "load-average"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); tc.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Prepare is the only gate between a hand-typed form and the database.
func TestHub_PrepareFillsDefaultsAndValidates(t *testing.T) {
	h := NewHub(testLog(), newFake())
	base := func(opts map[string]string) Spec {
		return Spec{Provider: "fake", Kind: "thing", Name: "s", Options: opts}
	}

	got, err := h.Prepare(base(nil))
	if err != nil {
		t.Fatalf("Prepare with no options: %v", err)
	}
	if got.Options["n"] != "1" {
		t.Errorf("n = %q, want the declared default 1", got.Options["n"])
	}

	got, err = h.Prepare(base(map[string]string{"n": "7"}))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got.Options["n"] != "7" {
		t.Errorf("n = %q, want the value given to override the default", got.Options["n"])
	}

	if _, err := h.Prepare(base(map[string]string{"mode": "sideways"})); err == nil {
		t.Error("Prepare accepted a value outside the field's declared choices")
	}
	if _, err := h.Prepare(base(map[string]string{"mode": "fast"})); err != nil {
		t.Errorf("Prepare rejected a declared choice: %v", err)
	}

	bad := base(nil)
	bad.Kind = "nonsense"
	if _, err := h.Prepare(bad); err == nil {
		t.Error("Prepare accepted a kind the provider does not offer")
	}
}

// A required field with no default must be refused, or the sensor stores and then fails every read.
func TestHub_PrepareRejectsAMissingRequiredField(t *testing.T) {
	h := NewHub(testLog(), &noDefaultProvider{})
	_, err := h.Prepare(Spec{Provider: "nodefault", Kind: "thing", Name: "s"})
	if err == nil {
		t.Fatal("Prepare accepted a spec missing a required field with no default")
	}
}

// Prepare must run the provider's own rules too: a field declaration cannot express a range.
func TestHub_PrepareRunsTheProvidersOwnRules(t *testing.T) {
	p := &noDefaultProvider{reject: errors.New("address out of range")}
	h := NewHub(testLog(), p)
	_, err := h.Prepare(Spec{
		Provider: "nodefault", Kind: "thing", Name: "s", Options: map[string]string{"needed": "x"},
	})
	if err == nil {
		t.Fatal("Prepare ignored the provider's Validate")
	}
}

type noDefaultProvider struct{ reject error }

func (p *noDefaultProvider) ID() string                               { return "nodefault" }
func (p *noDefaultProvider) Label() string                            { return "No default" }
func (p *noDefaultProvider) Available(context.Context) (bool, string) { return true, "" }
func (p *noDefaultProvider) Kinds() []KindInfo {
	return []KindInfo{{Kind: "thing", Fields: []Field{{Key: "needed", Label: "Needed", Required: true}}}}
}
func (p *noDefaultProvider) Discover(context.Context) ([]Candidate, error) { return nil, nil }
func (p *noDefaultProvider) Open(Spec) (Sensor, error)                     { return nil, nil }
func (p *noDefaultProvider) Validate(Spec) error                           { return p.reject }

// A rename keeps the device open and still reaches consumers, which specEqual ignoring the name could break.
func TestHub_RenameReachesTheSnapshotWithoutReopening(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})
	h.pass(t.Context(), false)

	renamed := spec(1)
	renamed.Name = "new name"
	h.Set([]Spec{renamed})

	got := h.Snapshot()
	if len(got) != 1 || got[0].Spec.Name != "new name" {
		t.Fatalf("snapshot = %+v, want the new name", got)
	}
	if len(got[0].Readings) != 1 {
		t.Errorf("readings = %+v, want the last reading kept across a rename", got[0].Readings)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opens != 1 || p.closes != 0 {
		t.Errorf("opens = %d, closes = %d after a rename, want 1 and 0", p.opens, p.closes)
	}
}

// claimingProvider is a fake with hardware to fight over, standing in for two sensors on one I2C address.
type claimingProvider struct{ fakeProvider }

func (*claimingProvider) ID() string { return "claiming" }

func (*claimingProvider) Kinds() []KindInfo {
	return []KindInfo{{Kind: "chip", Label: "Chip", Metrics: []Metric{Temperature}, Fields: []Field{{Key: "address", Label: "Address", Required: true}}}}
}

func (*claimingProvider) Claim(s Spec) string { return s.Options["address"] }

func claimSpec(id int64, name, addr string) Spec {
	return Spec{
		ID: id, Provider: "claiming", Kind: "chip", Name: name,
		Options: map[string]string{"address": addr},
	}
}

// The API is reachable without the form, and a sensor called "   " labels nothing on any screen.
func TestHub_PrepareRefusesANameOfSpaces(t *testing.T) {
	h := NewHub(testLog(), newFake())
	if _, err := h.Prepare(Spec{Provider: "fake", Kind: "thing", Name: "   "}); err == nil {
		t.Fatal("Prepare accepted a name of spaces")
	}
	got, err := h.Prepare(Spec{Provider: "fake", Kind: "thing", Name: "  padded  "})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got.Name != "padded" {
		t.Errorf("name %q, want it trimmed", got.Name)
	}
}

// Two sensors on one chip interleave their read sequences, and a stateful part then swaps their measurements.
func TestHub_PrepareRefusesTwoSensorsOnOneChip(t *testing.T) {
	h := NewHub(testLog(), &claimingProvider{})
	h.Set([]Spec{claimSpec(1, "first", "0x43")})

	if _, err := h.Prepare(claimSpec(0, "second", "0x43")); err == nil {
		t.Fatal("Prepare accepted a second sensor on an address already driven")
	}
	// Provokes the negative above: a free address is accepted, so the refusal is the address.
	if _, err := h.Prepare(claimSpec(0, "second", "0x44")); err != nil {
		t.Fatalf("Prepare refused a free address: %v", err)
	}
	// Editing a sensor must not collide with itself.
	if _, err := h.Prepare(claimSpec(1, "first renamed", "0x43")); err != nil {
		t.Fatalf("Prepare refused a sensor its own address: %v", err)
	}
}

// The name is how every other screen tells two sensors apart, which two sensors sharing one cannot.
func TestHub_PrepareRefusesADuplicateName(t *testing.T) {
	h := NewHub(testLog(), &claimingProvider{})
	h.Set([]Spec{claimSpec(1, "Air", "0x43")})

	for _, name := range []string{"Air", "air", " Air "} {
		if _, err := h.Prepare(claimSpec(0, name, "0x44")); err == nil {
			t.Errorf("Prepare accepted %q beside the existing \"Air\"", name)
		}
	}
	if _, err := h.Prepare(claimSpec(0, "Airflow", "0x44")); err != nil {
		t.Fatalf("Prepare refused a name that merely starts the same: %v", err)
	}
}

// A binding the source cannot serve is dropped at poll time, which reads as a sensor that is failing.
func TestHub_PrepareRefusesABindingTheSourceCannotServe(t *testing.T) {
	h := NewHub(testLog(), &claimingProvider{}, &VirtualProvider{})
	h.Set([]Spec{claimSpec(1, "chip", "0x43")})

	derived := func(m Metric) Spec {
		return Spec{
			Provider: "virtual", Kind: KindExpression, Name: "derived",
			Options:  map[string]string{"expression": "x + 1", "metric": "m", "unit": "u"},
			Bindings: []Binding{{Name: "x", SensorID: 1, Metric: m}},
		}
	}
	if _, err := h.Prepare(derived("nonesuch")); err == nil {
		t.Fatal("Prepare accepted a binding onto a metric the source does not report")
	}
	if _, err := h.Prepare(derived(Temperature)); err != nil {
		t.Fatalf("Prepare refused a metric the source's kind declares: %v", err)
	}
}

// JSON has no NaN, so one would blank /api/sensors and every push, taking every other sensor with it.
func TestHub_ANonFiniteReadingFailsTheRead(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})
	h.pass(t.Context(), false)

	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		p.set(func(p *fakeProvider) { p.readings = []Reading{{Metric: Temperature, Value: v, Unit: "C"}} })
		h.pass(t.Context(), false)
		got := h.Snapshot()
		if got[0].Err == "" || got[0].Readings[0].Value != 21.5 {
			t.Errorf("a %v reading gave %+v, want an error and the last good 21.5 kept", v, got[0])
		}
		if _, err := json.Marshal(got); err != nil {
			t.Errorf("the snapshot no longer encodes: %v", err)
		}
	}
}

// Prepare takes only what the kind declares, so an option or a binding it never reads cannot vouch for a reading.
func TestHub_PrepareRefusesWhatTheKindDoesNotTake(t *testing.T) {
	h := NewHub(testLog(), newFake(), &VirtualProvider{})
	h.Set([]Spec{spec(1)})
	fresh := func(edit func(*Spec)) Spec {
		s := spec(0)
		s.Name = "fresh"
		edit(&s)
		return s
	}
	for _, tc := range []struct {
		name string
		spec Spec
		want string
	}{
		{"an option the kind has no field for", fresh(func(s *Spec) { s.Options["metric"] = "bogus" }), `no option "metric"`},
		{"bindings on a kind that reads nothing", fresh(func(s *Spec) { s.Bindings = []Binding{{Name: "x", SensorID: 1, Metric: Temperature}} }), "takes no bindings"},
		{"a name too long to label anything", fresh(func(s *Spec) { s.Name = strings.Repeat("n", 65) }), "the most"},
		{"a control character in the name", fresh(func(s *Spec) { s.Name = "air\x1b[31m" }), "control character"},
		{"a bidi override in the name", fresh(func(s *Spec) { s.Name = "air\u202e" }), "control character"},
		{"an option value past any real one", fresh(func(s *Spec) { s.Options["n"] = strings.Repeat("9", 1025) }), "too long"},
		{"a newline in a one-line option", fresh(func(s *Spec) { s.Options["n"] = "1\n2" }), "control character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Prepare(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Prepare gave %v, want an error mentioning %q", err, tc.want)
			}
		})
	}

	// Provokes the negatives: the same sensor saves, and a multiline option may hold a newline.
	if _, err := h.Prepare(fresh(func(*Spec) {})); err != nil {
		t.Fatalf("refused a plain sensor: %v", err)
	}
	multi := expressionSpec(0, "x +\n 1", Binding{Name: "x", SensorID: 1, Metric: Temperature})
	if _, err := h.Prepare(multi); err != nil {
		t.Fatalf("refused a newline in a multiline expression: %v", err)
	}
}

// A kind that lets the operator name its metric reports that one too; any other reports only what it declares.
func TestHub_ReportsCountsANamedMetricOnlyWhereTheKindTakesOne(t *testing.T) {
	h := NewHub(testLog(), newFake(), &VirtualProvider{})
	if !h.Reports(expressionSpec(1, "x"))["moisture"] {
		t.Error("an expression's own metric is not counted, so a map row reading it looks removed")
	}
	injected := spec(2)
	injected.Options["metric"] = "bogus"
	if got := h.Reports(injected); got["bogus"] || !got[Temperature] {
		t.Errorf("reports %v, want the kind's declared metrics and not an injected one", got)
	}
}

// A mesh telemetry reply reads Snapshot on the RX goroutine, so closing a removed sensor must not hold it while the bus lets go.
func TestHub_SnapshotDoesNotWaitOnASensorClosing(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1), spec(2)})
	h.pass(t.Context(), false)

	for _, tc := range []struct {
		name   string
		remove func()
	}{
		{"removed by Set", func() { h.Set([]Spec{spec(2)}) }},
		{"closed by Close", h.Close},
	} {
		release, entered := make(chan struct{}), make(chan struct{}, 1)
		p.set(func(p *fakeProvider) { p.closing, p.entered = release, entered })
		done := make(chan struct{})
		go func() { tc.remove(); close(done) }()
		<-entered
		got := make(chan []Status, 1)
		go func() { got <- h.Snapshot() }()
		select {
		case <-got:
		case <-time.After(500 * time.Millisecond):
			t.Errorf("%s: Snapshot waited on the sensor being closed", tc.name)
		}
		p.set(func(p *fakeProvider) { p.closing = nil })
		close(release)
		<-done
	}
}

// A dead sensor logged a warning every pass, 2,880 a day, and one that would not open logged nothing; the log says when either starts and ends.
func TestHub_LogsAFailureWhenItStartsNotEveryPass(t *testing.T) {
	var buf bytes.Buffer
	p := newFake()
	h := NewHub(slog.New(slog.NewTextHandler(&buf, nil)), p)
	h.Set([]Spec{spec(1)})

	p.set(func(p *fakeProvider) { p.openErr = errors.New("no ACK at 0x70") })
	for range 3 {
		h.pass(t.Context(), false)
	}
	p.set(func(p *fakeProvider) { p.openErr, p.readErr = nil, errors.New("i2c: remote I/O error") })
	h.entries[1].nextOpen = time.Time{}
	for range 3 {
		h.pass(t.Context(), false)
	}
	p.set(func(p *fakeProvider) { p.readErr = nil })
	h.pass(t.Context(), false)

	out := buf.String()
	for _, msg := range []string{"no ACK at 0x70", "remote I/O error"} {
		if n := strings.Count(out, msg); n != 1 {
			t.Errorf("%q logged %d times over three passes, want once", msg, n)
		}
	}
	if !strings.Contains(out, "reading again") {
		t.Errorf("the recovery was not logged:\n%s", out)
	}
}

// Shutdown cancels the context before it closes the hub, so a pass that starts late must not reopen what Close shut.
func TestHub_APassAfterShutdownOpensNothing(t *testing.T) {
	p := newFake()
	h := NewHub(testLog(), p)
	h.Set([]Spec{spec(1)})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.pass(ctx, false)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opens != 0 {
		t.Errorf("a pass on a cancelled context opened %d sensors", p.opens)
	}
}

// A yes or no carried as 1 or 0, or a whole count, has to say so, or the page shows "1.000" for charging.
func TestFormatOf_MarksWhatIsNotAMeasurement(t *testing.T) {
	for m, want := range map[Metric]string{
		Charging: "flag", Plugged: "flag", AirQualityRunIn: "flag",
		IAQAccuracy: "count", GasPercentageAccuracy: "count",
		Temperature: "number", "moisture": "number",
	} {
		if got := FormatOf(m); got != want {
			t.Errorf("%s reads as %q, want %q", m, got, want)
		}
	}
}

// scanningProvider finds a chip at 0x43 and one at 0x44, so a test sees which of them a configured sensor holds.
type scanningProvider struct{ claimingProvider }

func (*scanningProvider) Discover(context.Context) ([]Candidate, error) {
	return []Candidate{
		{Kind: "chip", Label: "Chip", Addable: true, Options: map[string]string{"address": "0x43"}},
		{Kind: "chip", Label: "Chip", Addable: true, Options: map[string]string{"address": "0x44"}},
	}, nil
}

// A scan names the sensor already on a part it finds, or the page offers a chip that saving then refuses.
func TestHub_DiscoverNamesTheSensorAlreadyOnAPart(t *testing.T) {
	p := &scanningProvider{}
	p.available = true
	h := NewHub(testLog(), p)
	h.Set([]Spec{claimSpec(1, "first", "0x43")})

	res, err := h.Discover(t.Context(), "claiming")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]string{}
	for _, c := range res.Candidates {
		used[c.Options["address"]] = c.UsedBy
	}
	if used["0x43"] != "first" || used["0x44"] != "" {
		t.Fatalf("used by %v, want 0x43 held by first and 0x44 free", used)
	}
}
