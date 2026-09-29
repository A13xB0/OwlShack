package sensor

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

// expressionSpec is a derived sensor reading one binding.
func expressionSpec(id int64, src string, bindings ...Binding) Spec {
	return Spec{
		ID: id, Provider: "virtual", Kind: KindExpression, Name: "derived",
		Options:  map[string]string{"expression": src, "metric": "moisture", "unit": "%"},
		Bindings: bindings,
	}
}

// staticHub answers with fixed statuses, standing in for the hub a derived sensor reads back through.
func staticHub(sts ...Status) func() []Status { return func() []Status { return sts } }

func reading(id int64, name string, m Metric, v float64) Status {
	return Status{
		Spec:     Spec{ID: id, Name: name},
		Readings: []Reading{{Metric: m, Value: v}},
		At:       time.Now(),
	}
}

func TestVirtual_EvaluatesOverItsBindings(t *testing.T) {
	p := &VirtualProvider{}
	p.Bind(staticHub(reading(1, "ADC A0", Voltage, 1.25)))
	spec := expressionSpec(2, "v * 40", Binding{Name: "v", SensorID: 1, Metric: Voltage})

	s, err := p.Open(spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Value != 50 {
		t.Fatalf("got %+v, want one reading of 50", got)
	}
	if got[0].Metric != "moisture" || got[0].Unit != "%" {
		t.Errorf("got %s in %q, want moisture in %%", got[0].Metric, got[0].Unit)
	}
}

// A source that cannot be trusted has to stop the evaluation rather than feed it.
func TestVirtual_RefusesToDeriveFromASourceItCannotTrust(t *testing.T) {
	failing := reading(1, "ADC A0", Voltage, 1.25)
	failing.Err = "bus error"
	unread := Status{Spec: Spec{ID: 1, Name: "ADC A0"}}
	wrongMetric := reading(1, "ADC A0", Temperature, 20)

	for name, tc := range map[string]struct {
		hub  func() []Status
		want string
	}{
		"source is failing":       {staticHub(failing), "failing"},
		"source never read":       {staticHub(unread), "not been read"},
		"source has no reading":   {staticHub(wrongMetric), "reports no voltage"},
		"source no longer exists": {staticHub(), "no longer configured"},
	} {
		t.Run(name, func(t *testing.T) {
			p := &VirtualProvider{}
			p.Bind(tc.hub)
			s, err := p.Open(expressionSpec(2, "v * 40", Binding{Name: "v", SensorID: 1, Metric: Voltage}))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_, err = s.Read(context.Background())
			if err == nil {
				t.Fatal("Read derived a value from a source it should not have trusted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Dividing by zero is +Inf, which encoding/json refuses: one bad expression would take the whole sensor list off the API.
func TestVirtual_RefusesANonFiniteResult(t *testing.T) {
	p := &VirtualProvider{}
	p.Bind(staticHub(reading(1, "ADC A0", Voltage, 0)))
	s, err := p.Open(expressionSpec(2, "1 / v", Binding{Name: "v", SensorID: 1, Metric: Voltage}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Read(context.Background())
	if err == nil {
		t.Fatalf("Read returned %+v for a division by zero", got)
	}
	// Prove the premise: the expression really does produce a non-finite number.
	zero := 0.0
	if v := 1.0 / zero; !math.IsInf(v, 1) {
		t.Fatal("premise wrong: 1/0 is finite")
	}
}

// The environment is closed, so an undeclared identifier fails at save rather than silently at poll time.
func TestVirtual_ValidateRefusesWhatCannotWork(t *testing.T) {
	ok := Binding{Name: "v", SensorID: 1, Metric: Voltage}
	for name, tc := range map[string]struct {
		spec Spec
		want string
	}{
		"reads an undeclared name": {
			expressionSpec(2, "v * other", ok), "does not compile",
		},
		"is not arithmetic": {
			expressionSpec(2, `"hello"`, ok), "does not compile",
		},
		"binding name is not an identifier": {
			expressionSpec(2, "v", Binding{Name: "a b", SensorID: 1, Metric: Voltage}), "usable name",
		},
		"two bindings share a name": {
			expressionSpec(2, "v", ok, Binding{Name: "v", SensorID: 3, Metric: Voltage}), "both called",
		},
		"binds nothing at all": {
			expressionSpec(2, "1"), "at least one other sensor",
		},
		"expression is empty": {
			expressionSpec(2, "  ", ok), "empty",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (&VirtualProvider{}).Validate(tc.spec); err == nil {
				t.Fatal("Validate accepted it")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestOrderByDependency(t *testing.T) {
	hw := func(id int64) Spec { return Spec{ID: id, Provider: "fake"} }
	derived := func(id int64, srcs ...int64) Spec {
		s := Spec{ID: id, Provider: "virtual"}
		for _, src := range srcs {
			s.Bindings = append(s.Bindings, Binding{Name: "x", SensorID: src, Metric: Voltage})
		}
		return s
	}

	t.Run("a source is always read before what derives from it", func(t *testing.T) {
		// Ids are chosen so plain id order would put the derived sensors first and read stale values.
		specs := map[int64]Spec{1: derived(1, 3), 2: derived(2, 1), 3: hw(3)}
		got, err := orderByDependency(specs)
		if err != nil {
			t.Fatalf("orderByDependency: %v", err)
		}
		at := map[int64]int{}
		for i, id := range got {
			at[id] = i
		}
		if at[3] > at[1] || at[1] > at[2] {
			t.Errorf("order %v reads a sensor before its source", got)
		}
	})

	t.Run("a loop is named rather than ordered", func(t *testing.T) {
		if _, err := orderByDependency(map[int64]Spec{1: derived(1, 2), 2: derived(2, 1)}); err == nil {
			t.Fatal("orderByDependency ordered a loop")
		}
	})

	t.Run("a sensor reading itself is a loop", func(t *testing.T) {
		if _, err := orderByDependency(map[int64]Spec{1: derived(1, 1)}); err == nil {
			t.Fatal("orderByDependency ordered a sensor that reads itself")
		}
	})

	t.Run("a source that is not configured is not an edge", func(t *testing.T) {
		// Missing sources are a read error for that one sensor, not an ordering problem for the rest.
		if _, err := orderByDependency(map[int64]Spec{1: derived(1, 99)}); err != nil {
			t.Errorf("orderByDependency refused a spec whose source is simply gone: %v", err)
		}
	})

	t.Run("the order is the same every time", func(t *testing.T) {
		specs := map[int64]Spec{1: hw(1), 2: hw(2), 3: derived(3, 1), 4: derived(4, 2), 5: hw(5)}
		first, err := orderByDependency(specs)
		if err != nil {
			t.Fatalf("orderByDependency: %v", err)
		}
		for i := 0; i < 20; i++ {
			got, err := orderByDependency(specs)
			if err != nil {
				t.Fatalf("orderByDependency: %v", err)
			}
			for j := range got {
				if got[j] != first[j] {
					t.Fatalf("run %d gave %v, first gave %v", i, got, first)
				}
			}
		}
	})
}

// Why the hub stores a poll order: reading a derived sensor first gives it the previous pass's value or nothing.
func TestHub_ReadsASourceBeforeWhatDerivesFromIt(t *testing.T) {
	hw := &fakeProvider{available: true, readings: []Reading{{Metric: Voltage, Value: 2}}}
	v := &VirtualProvider{}
	h := NewHub(testLog(), hw, v)

	// Id 1 derives from id 2, so ordering by id alone would read the derived sensor first.
	h.Set([]Spec{
		expressionSpec(1, "x * 10", Binding{Name: "x", SensorID: 2, Metric: Voltage}),
		{ID: 2, Provider: "fake", Kind: "thing", Name: "source"},
	})
	h.pass(context.Background(), false)

	var derived Status
	for _, st := range h.Snapshot() {
		if st.Spec.ID == 1 {
			derived = st
		}
	}
	if derived.Err != "" {
		t.Fatalf("the derived sensor failed on the first pass: %s", derived.Err)
	}
	if len(derived.Readings) != 1 || derived.Readings[0].Value != 20 {
		t.Fatalf("got %+v, want one reading of 20 from this pass's source value", derived.Readings)
	}
}

// Prepare is where a loop is refused, so it never reaches the poll order.
func TestHub_PrepareRefusesBindingsThatCannotWork(t *testing.T) {
	hw := &fakeProvider{available: true}
	h := NewHub(testLog(), hw, &VirtualProvider{})
	h.Set([]Spec{
		{ID: 1, Provider: "fake", Kind: "thing", Name: "source"},
		expressionSpec(2, "x", Binding{Name: "x", SensorID: 1, Metric: Voltage}),
	})

	t.Run("a source that is not configured", func(t *testing.T) {
		unsourced := expressionSpec(3, "x", Binding{Name: "x", SensorID: 99, Metric: Voltage})
		unsourced.Name = "unsourced"
		if _, err := h.Prepare(unsourced); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("Prepare gave %v, want the missing source refused", err)
		}
	})

	t.Run("an update that closes a loop", func(t *testing.T) {
		// Sensor 2 already reads 1, so making 1 read 2 closes the loop; the check is on the graph, not the provider.
		loop := Spec{
			ID: 1, Provider: "virtual", Kind: KindExpression, Name: "source",
			Options:  map[string]string{"expression": "x", "metric": "m", "unit": "u"},
			Bindings: []Binding{{Name: "x", SensorID: 2, Metric: "moisture"}},
		}
		if _, err := h.Prepare(loop); err == nil || !strings.Contains(err.Error(), "loop") {
			t.Fatalf("Prepare gave %v, want the loop refused", err)
		}
	})

	t.Run("a new sensor cannot close a loop", func(t *testing.T) {
		// Nothing can point at an id that does not exist yet, so a create is always safe.
		fresh := expressionSpec(0, "x", Binding{Name: "x", SensorID: 1, Metric: Voltage})
		fresh.Name = "another derived" // a name of its own; the collision rule has its own test
		if _, err := h.Prepare(fresh); err != nil {
			t.Fatalf("Prepare refused a new derived sensor: %v", err)
		}
	})
}

// derivedValue is the single reading of sensor id, or a failure naming why there is none.
func derivedValue(t *testing.T, h *Hub, id int64) float64 {
	t.Helper()
	for _, st := range h.Snapshot() {
		if st.Spec.ID != id {
			continue
		}
		if st.Err != "" {
			t.Fatalf("sensor %d is failing: %s", id, st.Err)
		}
		if len(st.Readings) != 1 {
			t.Fatalf("sensor %d has %d readings, want 1", id, len(st.Readings))
		}
		return st.Readings[0].Value
	}
	t.Fatalf("sensor %d is not in the snapshot", id)
	return 0
}

// A changed spec must reopen, or a stale expression reads as a wrong value, not a failed save.
func TestHub_ChangingABindingReopensTheSensor(t *testing.T) {
	hw := &fakeProvider{available: true, readings: []Reading{
		{Metric: Voltage, Value: 2},
		{Metric: Temperature, Value: 5},
	}}
	h := NewHub(testLog(), hw, &VirtualProvider{})
	src := Spec{ID: 2, Provider: "fake", Kind: "thing", Name: "source"}

	h.Set([]Spec{src, expressionSpec(1, "x * 10", Binding{Name: "x", SensorID: 2, Metric: Voltage})})
	h.pass(context.Background(), false)
	if got := derivedValue(t, h, 1); got != 20 {
		t.Fatalf("bound to voltage: got %v, want 20", got)
	}

	// Same expression, same source sensor, different reading of it.
	h.Set([]Spec{src, expressionSpec(1, "x * 10", Binding{Name: "x", SensorID: 2, Metric: Temperature})})
	h.pass(context.Background(), false)
	if got := derivedValue(t, h, 1); got != 50 {
		t.Fatalf("rebound to temperature: got %v, want 50; the sensor was not reopened", got)
	}
}

// expr's builtins stop at abs/ceil/floor/round/min/max; a dew point and a thermistor curve both want a logarithm.
func TestVirtual_MathFunctionsAreAvailable(t *testing.T) {
	p := &VirtualProvider{}
	p.Bind(staticHub(reading(1, "src", Voltage, 100)))
	for src, want := range map[string]float64{
		"log(v)":     math.Log(100),
		"log10(v)":   2,
		"sqrt(v)":    10,
		"exp(0) * v": 100,
		"pow(v, 2)":  10000,
		"abs(0 - v)": 100,
	} {
		t.Run(src, func(t *testing.T) {
			s, err := p.Open(expressionSpec(2, src, Binding{Name: "v", SensorID: 1, Metric: Voltage}))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			got, err := s.Read(context.Background())
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if math.Abs(got[0].Value-want) > 1e-9 {
				t.Errorf("%s = %v, want %v", src, got[0].Value, want)
			}
		})
	}
}

// log(0) is -Inf and sqrt(-1) is NaN, so the guard on the way out has to cover the functions too.
func TestVirtual_MathFunctionsCannotSmuggleOutANonFiniteResult(t *testing.T) {
	p := &VirtualProvider{}
	p.Bind(staticHub(reading(1, "src", Voltage, 0)))
	for _, src := range []string{"log(v)", "sqrt(v - 1)"} {
		s, err := p.Open(expressionSpec(2, src, Binding{Name: "v", SensorID: 1, Metric: Voltage}))
		if err != nil {
			t.Fatalf("Open %s: %v", src, err)
		}
		if got, err := s.Read(context.Background()); err == nil {
			t.Errorf("%s returned %+v instead of refusing a non-finite result", src, got)
		}
	}
}

// Naming intermediate values is what keeps a real formula readable; the Magnus dew point uses one twice.
func TestVirtual_ExpressionsCanNameIntermediateValues(t *testing.T) {
	p := &VirtualProvider{}
	p.Bind(staticHub(Status{
		Spec: Spec{ID: 1, Name: "BME680"},
		Readings: []Reading{
			{Metric: Temperature, Value: 20},
			{Metric: Humidity, Value: 50},
		},
		At: time.Now(),
	}))
	bindings := []Binding{
		{Name: "t", SensorID: 1, Metric: Temperature},
		{Name: "h", SensorID: 1, Metric: Humidity},
	}

	for name, src := range map[string]string{
		"a let binding":   "let g = log(h/100) + (17.62*t)/(243.12+t); 243.12 * g / (17.62 - g)",
		"lets in a chain": "let r = h/100; let g = log(r) + (17.62*t)/(243.12+t); 243.12 * g / (17.62 - g)",
		"spelled out twice": "243.12 * (log(h/100) + (17.62*t)/(243.12+t)) / " +
			"(17.62 - (log(h/100) + (17.62*t)/(243.12+t)))",
	} {
		t.Run(name, func(t *testing.T) {
			s, err := p.Open(expressionSpec(2, src, bindings...))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			got, err := s.Read(context.Background())
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if math.Abs(got[0].Value-9.2551745990) > 1e-9 {
				t.Errorf("got %.10f, want 9.2551745990", got[0].Value)
			}
		})
	}

	// A conditional is the other thing a real formula reaches for.
	s, err := p.Open(expressionSpec(2, "t > 30 ? t : h", bindings...))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got[0].Value != 50 {
		t.Errorf("conditional gave %v, want the humidity branch (50)", got[0].Value)
	}
}
