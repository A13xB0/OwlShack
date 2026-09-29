package sensor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"

	"github.com/meshcore-go/OwlShack/internal/sensor/ads1x15"
	"github.com/meshcore-go/OwlShack/internal/sensor/bme680"
)

func TestParseAddress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       string
		fallback uint16
		want     uint16
		wantErr  bool
	}{
		{"empty falls back", "", 0x5C, 0x5C, false},
		{"hex", "0x5d", 0x5C, 0x5D, false},
		{"decimal", "92", 0x5C, 0x5C, false},
		{"not a number", "0x5g", 0x5C, 0, true},
		{"below the device range", "0x07", 0x5C, 0, true},
		{"above the device range", "0x78", 0x5C, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAddress(tc.in, tc.fallback)
			if tc.wantErr != (err != nil) {
				t.Fatalf("parseAddress(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseAddress(%q) = %#02x, want %#02x", tc.in, got, tc.want)
			}
		})
	}
}

// An Env field a part does not measure stays zero, which would publish as a real 0 hPa or 0 %RH.
func TestEnvSensor_ReportsOnlyTheMetricsThePartMeasures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		metrics []Metric
		want    map[Metric]float64
	}{
		{"humidity part", []Metric{Temperature, Humidity}, map[Metric]float64{Temperature: 21, Humidity: 55}},
		{"pressure part", []Metric{Pressure, Temperature}, map[Metric]float64{Pressure: 1013.25, Temperature: 21}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &envSensor{dev: fixedEnv{}, metrics: tc.metrics}
			got, err := s.Read(t.Context())
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d readings, want %d: %+v", len(got), len(tc.want), got)
			}
			for _, r := range got {
				want, ok := tc.want[r.Metric]
				if !ok {
					t.Errorf("reported %s, which this part does not measure", r.Metric)
					continue
				}
				if diff := r.Value - want; diff > 0.01 || diff < -0.01 {
					t.Errorf("%s = %v, want %v", r.Metric, r.Value, want)
				}
				if r.Unit == "" {
					t.Errorf("%s has no unit", r.Metric)
				}
			}
		})
	}
}

func TestEnvSensor_ReadErrorIsReported(t *testing.T) {
	s := &envSensor{dev: failingEnv{}, metrics: []Metric{Temperature}}
	if _, err := s.Read(t.Context()); err == nil {
		t.Fatal("Read returned no error although the device failed")
	}
}

// A chip missing its metrics or addresses builds a sensor that reports nothing or reaches a reserved address.
func TestChipTableIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range chips {
		if c.kind == "" || c.label == "" {
			t.Errorf("chip %+v has no kind or label", c)
		}
		if seen[c.kind] {
			t.Errorf("duplicate chip kind %q", c.kind)
		}
		seen[c.kind] = true
		if len(c.metrics) == 0 {
			t.Errorf("chip %q declares no metrics, so it would report nothing", c.kind)
		}
		if len(c.addrs) == 0 {
			t.Errorf("chip %q declares no addresses, so it could never be found", c.kind)
		}
		for _, a := range c.addrs {
			if a < 0x08 || a > 0x77 {
				t.Errorf("chip %q address %#02x is outside the 7-bit device range", c.kind, a)
			}
		}
		if c.open == nil {
			t.Errorf("chip %q has no open function", c.kind)
		}
	}
}

func TestI2CProvider_OpenRejectsUnknownKind(t *testing.T) {
	if _, err := (I2CProvider{}).Open(Spec{Provider: "i2c", Kind: "nonsense"}); err == nil {
		t.Fatal("Open accepted a kind no driver claims")
	}
}

type fixedEnv struct{}

func (fixedEnv) Sense(e *physic.Env) error {
	e.Temperature = physic.ZeroCelsius + 21*physic.Kelvin
	e.Humidity = 55 * physic.PercentRH
	e.Pressure = 101325 * physic.Pascal
	return nil
}
func (fixedEnv) SenseContinuous(time.Duration) (<-chan physic.Env, error) { return nil, nil }
func (fixedEnv) Precision(*physic.Env)                                    {}
func (fixedEnv) Halt() error                                              { return nil }
func (fixedEnv) String() string                                           { return "fixedEnv" }

type failingEnv struct{ fixedEnv }

func (failingEnv) Sense(*physic.Env) error { return errors.New("bus fault") }

// readBus answers a one-byte read at the addresses it lists and keeps every write, so a test sees whether a scan wrote at all.
type readBus struct {
	answers map[uint16]bool
	writes  []string
}

func (b *readBus) Tx(addr uint16, w, r []byte) error {
	if len(w) > 0 {
		b.writes = append(b.writes, fmt.Sprintf("% x to %#02x", w, addr))
	}
	if !b.answers[addr] {
		return errors.New("nack")
	}
	clear(r)
	return nil
}
func (b *readBus) SetSpeed(physic.Frequency) error { return nil }
func (b *readBus) String() string                  { return "readBus" }
func (b *readBus) Close() error                    { return nil }

// A write is a command to some parts, even a register address: a multiplexer at 0x70 takes it as its channel mask.
func TestScanBus_OnlyReads(t *testing.T) {
	bus := &readBus{answers: map[uint16]bool{0x21: true, 0x48: true, 0x70: true, 0x76: true}}
	got := scanBus(t.Context(), "/dev/i2c-1", bus)
	if len(bus.writes) != 0 {
		t.Fatalf("the scan wrote %v", bus.writes)
	}

	fits := map[string][]string{}
	var unknown []string
	for _, c := range got {
		if !c.Addable {
			unknown = append(unknown, c.Label)
			continue
		}
		if c.Options["bus"] != "/dev/i2c-1" {
			t.Errorf("%s carries bus %q, want the one scanned", c.Kind, c.Options["bus"])
		}
		fits[c.Options["address"]] = append(fits[c.Options["address"]], c.Kind)
	}
	if !slices.Equal(fits["0x76"], []string{"bme680"}) {
		t.Errorf("0x76 offered %v, want the bme680", fits["0x76"])
	}
	// Nothing is identified, so every part that could sit at an address is offered.
	if !slices.Equal(fits["0x48"], []string{"sgm58031", "ads1115"}) {
		t.Errorf("0x48 offered %v, want both ADCs", fits["0x48"])
	}
	// An SHTC3 answers no plain read, so whatever did at 0x70 is something else, as likely a multiplexer as anything.
	if len(fits["0x70"]) != 0 {
		t.Errorf("0x70 offered %v for a device that answered a read", fits["0x70"])
	}
	if !slices.Equal(unknown, []string{"Unknown device at 0x21", "Unknown device at 0x70"}) {
		t.Errorf("unknown = %v, want 0x21 and 0x70 listed, or the bus looks empty there", unknown)
	}
}

// An empty bus and an unreadable one must not give the same answer.
func TestScanBus_EmptyBusYieldsNothing(t *testing.T) {
	if got := scanBus(t.Context(), "fake", &readBus{}); len(got) != 0 {
		t.Fatalf("scanBus on an empty bus = %+v, want nothing", got)
	}
}

func TestScanBus_HonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := scanBus(ctx, "fake", &readBus{answers: map[uint16]bool{0x76: true}}); len(got) != 0 {
		t.Fatalf("scanBus with a cancelled context = %+v, want nothing", got)
	}
}

// The address is free text for a part behind a multiplexer, so the provider is what refuses a reserved one.
func TestI2CProvider_ValidateChecksTheAddressWithoutTouchingTheBus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"declared default", "", false},
		{"the other strap", "0x5d", false},
		{"an address no chip table lists", "0x21", false},
		{"reserved low", "0x03", true},
		{"reserved high", "0x7f", true},
		{"not a number", "twelve", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := I2CProvider{}.Validate(Spec{
				Provider: "i2c", Kind: "lps22hb", Name: "p",
				Options: map[string]string{"address": tc.addr},
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate(%q) = %v, wantErr %v", tc.addr, err, tc.wantErr)
			}
		})
	}
}

// Every kind has to be addable by hand, so each needs a bus and an address field.
func TestI2CProvider_KindsDeclareTheFieldsOpenNeeds(t *testing.T) {
	kinds := I2CProvider{}.Kinds()
	if len(kinds) != len(chips) {
		t.Fatalf("Kinds() has %d entries, want one per chip (%d)", len(kinds), len(chips))
	}
	for _, k := range kinds {
		keys := map[string]Field{}
		for _, f := range k.Fields {
			keys[f.Key] = f
		}
		for _, want := range []string{"bus", "address"} {
			f, ok := keys[want]
			if !ok {
				t.Errorf("kind %q declares no %q field", k.Kind, want)
				continue
			}
			if !f.Required {
				t.Errorf("kind %q field %q is not required, so it could be left empty", k.Kind, want)
			}
		}
		if keys["address"].Default == "" {
			t.Errorf("kind %q has no default address, so the form opens blank", k.Kind)
		}
		if len(keys["address"].Choices) != 0 {
			t.Errorf("kind %q pins the address to a picker; a part behind a mux could not be reached", k.Kind)
		}
	}
}

// Defaulting with several buses picks by sort order, and a Pi lists HDMI channels beside the header bus.
func TestSoleBus_OnlyDefaultsWhenThereIsNoChoiceToMake(t *testing.T) {
	if got := soleBus([]string{"/dev/i2c-1"}); got != "/dev/i2c-1" {
		t.Errorf("one bus: got %q, want it defaulted", got)
	}
	if got := soleBus([]string{"/dev/i2c-1", "/dev/i2c-20"}); got != "" {
		t.Errorf("two buses: got %q, want no default so the operator chooses", got)
	}
	if got := soleBus(nil); got != "" {
		t.Errorf("no buses: got %q, want empty", got)
	}
}

// A catalogue row the operator cannot search is a part they cannot find at sixty entries.
func TestI2CProvider_KindsAreSearchable(t *testing.T) {
	for _, k := range (I2CProvider{}).Kinds() {
		if k.Description == "" {
			t.Errorf("kind %q has no description, so a catalogue row shows only a part number", k.Kind)
		}
		if k.Category == "" {
			t.Errorf("kind %q has no category, so it cannot be grouped", k.Kind)
		}
		if len(k.Metrics) == 0 {
			t.Errorf("kind %q lists no metrics, so it cannot be found by what it measures", k.Kind)
		}
	}
}

// A part's own extra fields have to reach the catalogue without colliding with the bus and address the provider adds.
func TestI2CProvider_KindsCarryTheirOwnFields(t *testing.T) {
	var bme KindInfo
	for _, k := range (I2CProvider{}).Kinds() {
		seen := map[string]bool{}
		for _, f := range k.Fields {
			if seen[f.Key] {
				t.Errorf("kind %q declares %q twice, so one of them is unreachable", k.Kind, f.Key)
			}
			seen[f.Key] = true
		}
		for _, want := range []string{"bus", "address"} {
			if !seen[want] {
				t.Errorf("kind %q has no %q field, so it cannot be opened", k.Kind, want)
			}
		}
		if k.Kind == "bme680" {
			bme = k
		}
	}
	if bme.Kind == "" {
		t.Fatal("no bme680 in the catalogue")
	}
	for _, want := range []string{"oversampling", "heater"} {
		if !slices.ContainsFunc(bme.Fields, func(f Field) bool { return f.Key == want }) {
			t.Errorf("bme680 does not offer %q, so its per-kind fields are not reaching the catalogue", want)
		}
	}
}

// An ADC is set up by its input and range, so both have to reach the catalogue for either part.
func TestI2CProvider_ADCsOfferTheirInputAndRange(t *testing.T) {
	for _, kind := range []string{"sgm58031", "ads1115"} {
		var adc KindInfo
		for _, k := range (I2CProvider{}).Kinds() {
			if k.Kind == kind {
				adc = k
			}
		}
		if adc.Kind == "" {
			t.Fatalf("%s is not in the catalogue", kind)
		}
		for _, want := range []string{"channel", "gain"} {
			if !slices.ContainsFunc(adc.Fields, func(f Field) bool { return f.Key == want }) {
				t.Errorf("%s does not offer %q", kind, want)
			}
		}
	}
}

// An ADC takes the other adapter; volts and raw counts differ only by gain and both look plausible.
func TestNewSensor_RoutesAnADCToTheAnalogueAdapterAndReportsVolts(t *testing.T) {
	const cfg = 0xC383 // AIN0 vs ground, +-4.096V, single-shot, ~128Hz, comparator off
	r16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	bus := &i2ctest.Playback{
		Ops: []i2ctest.IO{
			{Addr: 0x48, W: []byte{0x01}, R: r16(0x8583)},
			{Addr: 0x48, W: []byte{0x05}, R: r16(0x0080)}, // an SGM58031's own chip id
			{Addr: 0x48, W: []byte{0x01, 0xC3, 0x83}},
			{Addr: 0x48, W: []byte{0x01}, R: r16(cfg &^ 0x8000)}, // still converting
			{Addr: 0x48, W: []byte{0x01}, R: r16(cfg)},
			{Addr: 0x48, W: []byte{0x01}, R: r16(cfg)},
			{Addr: 0x48, W: []byte{0x00}, R: r16(0x4000)}, // half of full scale
		},
		DontPanic: true,
	}
	dev, err := ads1x15.NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	s, err := newSensor(dev, bus, []Metric{Voltage}, nil)
	if err != nil {
		t.Fatalf("newSensor: %v", err)
	}
	readings, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("got %d readings, want 1", len(readings))
	}
	if readings[0].Metric != Voltage || readings[0].Unit != "V" {
		t.Errorf("got %s in %q, want voltage in V", readings[0].Metric, readings[0].Unit)
	}
	// 0x4000 of 0x8000 at the 4.096V range.
	if got := readings[0].Value; got < 2.0479 || got > 2.0481 {
		t.Errorf("value = %v V, want 2.048", got)
	}
}

// gasEnv is a part with a gas plate and a fusion over it, as the BME680 presents them.
type gasEnv struct {
	fixedEnv
	ohms     physic.ElectricResistance
	gasValid bool
	air      bme680.AirQuality
	airValid bool
	// restored is what the provider handed back at open; refuse makes the part reject it.
	restored []byte
	refuse   bool
	state    []byte
	// period is the rate its fusion asks to be sampled at; zero leaves it on the hub's poll.
	period time.Duration
}

func (g *gasEnv) SamplePeriod() time.Duration { return g.period }

func (g *gasEnv) SenseGas() (physic.ElectricResistance, bool)            { return g.ohms, g.gasValid }
func (g *gasEnv) SenseGasCompensated() (physic.ElectricResistance, bool) { return g.ohms, g.gasValid }
func (g *gasEnv) SenseAirQuality() (bme680.AirQuality, bool)             { return g.air, g.airValid }
func (g *gasEnv) AirQualityState() ([]byte, error)                       { return g.state, nil }

func (g *gasEnv) RestoreAirQuality(state []byte) error {
	if g.refuse {
		return errors.New("state from another build")
	}
	g.restored = state
	return nil
}

func bmeChip(t *testing.T) chip {
	t.Helper()
	c, ok := chipByKind("bme680")
	if !ok {
		t.Fatal("no bme680 in the chip table")
	}
	return c
}

// The derived values ride on the part's own Status, so the page and the channel map need no special case; a part with no fusion reports none.
func TestEnvSensor_ReportsTheDerivedAirQuality(t *testing.T) {
	c := bmeChip(t)
	dev := &gasEnv{
		ohms: 50_000 * physic.Ohm, gasValid: true, airValid: true,
		air: bme680.AirQuality{
			IAQ: 87, StaticIAQ: 91, CO2Equivalent: 910, BreathVOCEquiv: 1.4,
			GasPercentage: 62, Accuracy: 3, GasAccuracy: 2, RunIn: true,
		},
	}
	s := &envSensor{dev: dev, metrics: c.metrics}
	got, err := s.Read(t.Context())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	by := map[Metric]Reading{}
	for _, r := range got {
		by[r.Metric] = r
	}
	for _, m := range c.metrics {
		if _, ok := by[m]; !ok {
			t.Errorf("%s is declared by the chip but was not reported", m)
		}
	}
	// Gas resistance is in ohms, as the firmware's BME680 publishes it (EnvironmentSensorManager.cpp).
	if got := by[Resistance]; got.Value != 50_000 || got.Unit != "Ω" {
		t.Errorf("resistance = %v %s, want 50000 Ω", got.Value, got.Unit)
	}
	// The same units as the rest of the app, which writes "°C" everywhere else.
	if got := by[Temperature].Unit; got != "°C" {
		t.Errorf("temperature is in %q, want °C", got)
	}
	for m, want := range map[Metric]float64{
		IAQ: 87, StaticIAQ: 91, CO2Equivalent: 910, BreathVOC: 1.4, GasPercentage: 62,
		IAQAccuracy: 3, GasPercentageAccuracy: 2, AirQualityRunIn: 1,
	} {
		if got := by[m].Value; got != want {
			t.Errorf("%s = %v, want %v", m, got, want)
		}
		if by[m].Label == "" {
			t.Errorf("%s has no label, so the page would show its metric name", m)
		}
	}

	dev.airValid = false
	if got, err = s.Read(t.Context()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, r := range got {
		if r.Metric != Temperature && r.Metric != Humidity && r.Metric != Pressure &&
			r.Metric != Resistance && r.Metric != GasCompensated {
			t.Errorf("reported %s from a part whose fusion is not running", r.Metric)
		}
	}
}

// At accuracy 0 the index reads as real air when it is not, so only the accuracies and run-in go out.
func TestEnvSensor_HoldsTheIndexBackUntilRunInEnds(t *testing.T) {
	dev := &gasEnv{
		ohms: 50_000 * physic.Ohm, gasValid: true, airValid: true,
		air: bme680.AirQuality{IAQ: 50, StaticIAQ: 50, CO2Equivalent: 600, BreathVOCEquiv: 0.5, Stabilised: true},
	}
	s := &envSensor{dev: dev, metrics: bmeChip(t).metrics}
	got, err := s.Read(t.Context())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	reported := map[Metric]bool{}
	for _, r := range got {
		reported[r.Metric] = true
	}
	for _, m := range []Metric{IAQ, StaticIAQ, CO2Equivalent, BreathVOC, GasPercentage} {
		if reported[m] {
			t.Errorf("%s was reported at accuracy 0", m)
		}
	}
	for _, m := range []Metric{IAQAccuracy, GasPercentageAccuracy, AirQualityRunIn, AirQualityRunInLeft} {
		if !reported[m] {
			t.Errorf("%s was held back too, so nothing says the index is still calibrating", m)
		}
	}

	// Once run in, there is no countdown left to show.
	dev.air.RunIn, dev.air.Accuracy = true, 1
	got, err = s.Read(t.Context())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if slices.ContainsFunc(got, func(r Reading) bool { return r.Metric == AirQualityRunInLeft }) {
		t.Error("a part that has run in still reports run-in left")
	}
}

// fakeState is the store seam, kept in memory; loadErr and saveErr stand in for a database that failed.
type fakeState struct {
	loaded           map[int64][]byte
	saved            map[int64][]byte
	saves            int
	loadErr, saveErr error
}

func (f *fakeState) LoadState(id int64) ([]byte, error) { return f.loaded[id], f.loadErr }

func (f *fakeState) SaveState(id int64, b []byte) error {
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.saved == nil {
		f.saved = map[int64][]byte{}
	}
	f.saved[id] = b
	return nil
}

// Without this a restart puts the index back to "calibrating" every time, whatever it had learned.
func TestAirQualityState_RestoresAtOpenAndSavesOnItsOwnCadence(t *testing.T) {
	store := &fakeState{loaded: map[int64][]byte{7: []byte("learned")}}
	p := I2CProvider{Period: 30 * time.Second, State: store}
	dev := &gasEnv{gasValid: true, airValid: true, state: []byte("fresh")}

	air := p.restoreAirQuality(dev, 7)
	if string(dev.restored) != "learned" {
		t.Fatalf("the part was handed %q at open, want the stored state", dev.restored)
	}

	s := &envSensor{dev: dev, metrics: bmeChip(t).metrics, air: air}
	if _, err := s.Read(t.Context()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	// A part that refused the stored state would otherwise write a fresh one over it on its first read.
	if store.saves != 0 {
		t.Errorf("the first read saved %q, before a period of learning", store.saved[7])
	}
	air.saved = time.Now().Add(-stateSaveInterval)
	if _, err := s.Read(t.Context()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(store.saved[7]) != "fresh" {
		t.Errorf("a period on, the read saved %q, want what the part handed back", store.saved[7])
	}
	// The row is replaced on a slow cadence, not written on every pass.
	store.saved = nil
	if _, err := s.Read(t.Context()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if store.saved != nil {
		t.Errorf("state was saved again within %s of the last one", stateSaveInterval)
	}
	// Closing saves whatever the cadence, or a restart loses what was learned since the last save.
	dev.state = []byte("at close")
	s.bus = &readBus{}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(store.saved[7]) != "at close" {
		t.Errorf("closing saved %q, want the part's state as it closed", store.saved[7])
	}

	// A refused blob leaves the part relearning, and still reading.
	refusing := &gasEnv{gasValid: true, airValid: true, refuse: true}
	s = &envSensor{dev: refusing, metrics: bmeChip(t).metrics, air: p.restoreAirQuality(refusing, 7)}
	if refusing.restored != nil {
		t.Error("a refused state still reached the part")
	}
	if got, err := s.Read(t.Context()); err != nil || len(got) == 0 {
		t.Errorf("after refusing a stored state the part read %d values, %v", len(got), err)
	}
}

// A load that failed says nothing about the row, which may hold days of learning, so nothing this run learns replaces it.
func TestAirQualityState_AFailedLoadKeepsWhatIsStored(t *testing.T) {
	store := &fakeState{loadErr: errors.New("database is locked")}
	p := I2CProvider{Period: 30 * time.Second, State: store}
	dev := &gasEnv{gasValid: true, airValid: true, state: []byte("fresh")}

	s := &envSensor{dev: dev, metrics: bmeChip(t).metrics, air: p.restoreAirQuality(dev, 7), bus: &readBus{}}
	s.air.saved = time.Now().Add(-stateSaveInterval)
	if _, err := s.Read(t.Context()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if store.saves != 0 {
		t.Errorf("%d saves after a failed load, want none", store.saves)
	}
}

// A save that failed is not done, so the next read tries again rather than waiting out another period.
func TestAirQualityState_AFailedSaveIsRetried(t *testing.T) {
	store := &fakeState{saveErr: errors.New("disk I/O error")}
	p := I2CProvider{Period: 30 * time.Second, State: store}
	dev := &gasEnv{gasValid: true, airValid: true, state: []byte("fresh")}

	s := &envSensor{dev: dev, metrics: bmeChip(t).metrics, air: p.restoreAirQuality(dev, 7)}
	s.air.saved = time.Now().Add(-stateSaveInterval)
	for range 2 {
		if _, err := s.Read(t.Context()); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if store.saves != 2 {
		t.Errorf("%d save attempts over two reads after a failure, want 2", store.saves)
	}
}

// Nothing stores a sensor with no id, and a part with no fusion is never asked for one.
func TestAirQualityState_SkipsWhatItCannotKey(t *testing.T) {
	store := &fakeState{loaded: map[int64][]byte{7: []byte("learned")}}
	p := I2CProvider{Period: 30 * time.Second, State: store}
	dev := &gasEnv{gasValid: true, airValid: true}

	if a := p.restoreAirQuality(dev, 0); a != nil {
		t.Error("a sensor with no id was given somewhere to save")
	}
	if a := (I2CProvider{Period: 30 * time.Second}).restoreAirQuality(dev, 7); a != nil {
		t.Error("a provider with no store was given somewhere to save")
	}
	if a := p.restoreAirQuality(fixedEnv{}, 7); a != nil {
		t.Error("a part with no fusion was given somewhere to save")
	}
	if a := p.restoreAirQuality(dev, 7); a == nil {
		t.Error("a stored sensor was given nowhere to save, so this proves nothing")
	}
}

// The poll period has to reach the part, or its fusion is sized for a rate it never sees; the rest is what an operator can get wrong in the form.
func TestBMEOpts_CarriesThePollPeriodAndTheForm(t *testing.T) {
	got, err := bmeOpts(0x77, map[string]string{
		"oversampling": "4x", "heater": "on", "temperature_offset": "1.5",
	}, 30*time.Second)
	if err != nil {
		t.Fatalf("bmeOpts: %v", err)
	}
	if got.SamplePeriod != 30*time.Second || !got.AirQuality {
		t.Errorf("sample period = %s with the fusion %v, want 30s and on", got.SamplePeriod, got.AirQuality)
	}
	if got.Address != 0x77 {
		t.Errorf("address = %#02x, want 0x77", got.Address)
	}
	if got.Temperature != bme680.Oversampling4x || got.Humidity != bme680.Oversampling4x {
		t.Errorf("oversampling = %s/%s, want 4x on every channel", got.Temperature, got.Humidity)
	}
	// BSEC's own 3 s profile, which the fusion's constants were fitted to.
	if got.Heater != bme680.HeaterFixed || got.HeaterDuration != 197*time.Millisecond {
		t.Errorf("heater = %v for %s, want fixed at 197ms", got.Heater, got.HeaterDuration)
	}
	// The offset is a difference, not a temperature, so it is read in kelvin rather than through Celsius.
	if k := float64(got.TemperatureOffset) / float64(physic.Kelvin); !(k > 1.49 && k < 1.51) {
		t.Errorf("temperature offset = %v K, want 1.5", k)
	}

	// A cold plate has nothing to fuse, and asking for the fusion anyway would refuse the part at open.
	if off, err := bmeOpts(0x76, map[string]string{"heater": "off"}, 30*time.Second); err != nil || off.Heater != bme680.HeaterOff || off.AirQuality {
		t.Errorf("heater off gave %v with the fusion %v, %v", off.Heater, off.AirQuality, err)
	}
	for _, bad := range []map[string]string{{"oversampling": "3x"}, {"temperature_offset": "warm"}} {
		if _, err := bmeOpts(0x76, bad, 30*time.Second); err == nil {
			t.Errorf("bmeOpts accepted %v", bad)
		}
	}
}

// Checked at save, or "abc" saves and then fails every poll, and "NaN" or 1e300 turn into a garbage offset.
func TestI2CValidate_RefusesAnOffsetThatIsNotADegreeCount(t *testing.T) {
	spec := func(off string) Spec {
		return Spec{Provider: "i2c", Kind: "bme680", Name: "air", Options: map[string]string{
			"bus": "/dev/i2c-1", "address": "0x76", "temperature_offset": off,
		}}
	}
	for _, bad := range []string{"abc", "NaN", "Inf", "1e300", "-60"} {
		if err := (I2CProvider{}).Validate(spec(bad)); err == nil {
			t.Errorf("an offset of %q was accepted", bad)
		}
	}
	if err := (I2CProvider{}).Validate(spec("2.5")); err != nil {
		t.Errorf("a 2.5 C offset was refused: %v", err)
	}
}

// A four-channel ADC is four sensors, and the rig's A0/A1 pair on one chip refused every edit while the claim ignored the input.
func TestI2CClaim_AnADCsInputsAreSeparateSensors(t *testing.T) {
	adc := func(ch string) Spec {
		return Spec{Provider: "i2c", Kind: "sgm58031", Options: map[string]string{"bus": "/dev/i2c-1", "address": "0x48", "channel": ch}}
	}
	p := I2CProvider{}
	if p.Claim(adc("A0")) == p.Claim(adc("A1")) {
		t.Error("A0 and A1 claim the same thing, so the second cannot be added")
	}
	if p.Claim(adc("A0")) != p.Claim(adc("A0")) {
		t.Error("two sensors on A0 do not collide")
	}
}

// A bus that will not open is worth showing even when another found parts; dropped, it reads as a bus with nothing on it.
func TestI2CDiscover_ReportsABusThatWouldNotOpen(t *testing.T) {
	if err := hostInit(); err != nil {
		t.Skipf("periph host init: %v", err)
	}
	for name, open := range map[string]i2creg.Opener{
		"zz-dead": func() (i2c.BusCloser, error) { return nil, errors.New("permission denied") },
		"zz-live": func() (i2c.BusCloser, error) { return &readBus{answers: map[uint16]bool{0x76: true}}, nil },
	} {
		if err := i2creg.Register(name, nil, -1, open); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = i2creg.Unregister(name) })
	}
	found, err := (I2CProvider{}).Discover(t.Context())
	if len(found) == 0 {
		t.Fatal("the live bus found nothing, so this proves nothing")
	}
	if err == nil || !strings.Contains(err.Error(), "zz-dead") {
		t.Errorf("the scan reported %v, want the bus that would not open", err)
	}
}

// The bus picker lists buses by number; as text, /dev/i2c-10 to -19 sat between -1 and -2.
func TestBusNames_ByNumber(t *testing.T) {
	if err := hostInit(); err != nil {
		t.Skipf("periph host init: %v", err)
	}
	for name, n := range map[string]int{"zz-a": 1010, "zz-b": 1002} {
		if err := i2creg.Register(name, nil, n, func() (i2c.BusCloser, error) { return &readBus{}, nil }); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = i2creg.Unregister(name) })
	}
	names := busNames()
	if a, b := slices.Index(names, "zz-a"), slices.Index(names, "zz-b"); a < 0 || b < 0 || b > a {
		t.Errorf("bus 1002 listed at %d and bus 1010 at %d in %v, want 1002 first", b, a, names)
	}
}

// A BME680 with its heater off takes no gas reading, so a channel or binding on its index would read nothing.
func TestReports_ABME680WithItsHeaterOffReportsNoGas(t *testing.T) {
	h := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)), I2CProvider{})
	spec := func(heater string) Spec {
		return Spec{Provider: "i2c", Kind: "bme680", Options: map[string]string{"heater": heater}}
	}
	off := h.Reports(spec("off"))
	if off[IAQ] || off[Resistance] || off[AirQualityRunIn] {
		t.Errorf("heater off reports %v, want no gas or air quality", off)
	}
	if !off[Temperature] || !off[Pressure] || !off[Humidity] {
		t.Errorf("heater off reports %v, want temperature, pressure and humidity", off)
	}
	if on := h.Reports(spec("on")); !on[IAQ] || !on[Resistance] {
		t.Errorf("heater on reports %v, want the gas and the index, so this proves nothing", on)
	}
}

// countingGas counts the samples a fusing part is asked for.
type countingGas struct {
	gasEnv
	mu     sync.Mutex
	senses int
}

func (c *countingGas) Sense(e *physic.Env) error {
	c.mu.Lock()
	c.senses++
	c.mu.Unlock()
	return c.gasEnv.Sense(e)
}

func (c *countingGas) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.senses
}

// The fusion's filters assume its rate, so a part with one is read on its own clock and a slow pass cannot open a gap in it.
func TestNewSensor_AFusingPartSamplesOnItsOwnClock(t *testing.T) {
	dev := &countingGas{gasEnv: gasEnv{gasValid: true, airValid: true, period: 10 * time.Millisecond}}
	s, err := newSensor(dev, &readBus{}, bmeChip(t).metrics, nil)
	if err != nil {
		t.Fatalf("newSensor: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); dev.count() < 3; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d samples in 2s at a 10ms period, with nothing calling Read", dev.count())
		}
	}
	if got, err := s.Read(t.Context()); err != nil || len(got) == 0 {
		t.Fatalf("Read gave %d values, %v; want the latest sample", len(got), err)
	}
	if err := s.(io.Closer).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	n := dev.count()
	time.Sleep(50 * time.Millisecond)
	if dev.count() != n {
		t.Errorf("%d samples after Close, want none", dev.count()-n)
	}

	// A part with no fusion takes any rate, so it stays on the hub's poll.
	plain := &countingGas{gasEnv: gasEnv{gasValid: true}}
	if _, err := newSensor(plain, &readBus{}, bmeChip(t).metrics, nil); err != nil {
		t.Fatalf("newSensor: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if plain.count() != 0 {
		t.Errorf("a part with no fusion was sampled %d times on its own", plain.count())
	}
}

// A read wedged in the driver stops the samples, and the hub would stamp the last one as new on every pass.
func TestSampledSensor_RefusesASampleOlderThanTwoPeriods(t *testing.T) {
	s := &sampledSensor{period: 3 * time.Second, readings: []Reading{{Metric: Temperature, Value: 20}}}
	s.at = time.Now().Add(-5 * time.Second)
	if _, err := s.Read(t.Context()); err != nil {
		t.Fatalf("a sample under two periods old was refused: %v", err)
	}
	s.at = time.Now().Add(-7 * time.Second)
	if _, err := s.Read(t.Context()); err == nil {
		t.Error("a sample over two periods old was handed back as current")
	}
}
