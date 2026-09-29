package bme680

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"
)

// The factory trim and one measurement read off the RAK1906 on pi-4-2, not numbers chosen to pass.
var (
	realCoeff1 = []byte{
		0x1b, 0x68, 0x03, 0x10, 0xd1, 0x92, 0x25, 0xd8, 0x58, 0x00, 0x95, 0x1e,
		0xb9, 0xff, 0x2f, 0x1e, 0x00, 0x00, 0xfb, 0xf2, 0xf4, 0xf6, 0x1e,
	}
	realCoeff2 = []byte{0x3e, 0x98, 0x33, 0x00, 0x2d, 0x14, 0x78, 0x9c, 0x50, 0x65, 0x8f, 0xdf, 0xcb, 0x12}
	realCoeff3 = []byte{0x27, 0xaa, 0x16, 0xcc, 0xd3}

	realField = []byte{
		0x80, 0xff, 0x4d, 0xd2, 0x80, 0x7d, 0x09, 0x00, 0x4d, 0x27,
		0x80, 0x00, 0x00, 0x00, 0x04, 0x00, 0x04,
	}
)

// openOps is the traffic a successful open expects (identify, reset, three trim blocks, profile); a zero heat means the heater is off.
func openOps(addr uint16, id, variant byte, heat time.Duration) []i2ctest.IO {
	ops := []i2ctest.IO{
		{Addr: addr, W: []byte{regChipID}, R: []byte{id}},
		{Addr: addr, W: []byte{regSoftReset, softResetCmd}},
		{Addr: addr, W: []byte{regVariantID}, R: []byte{variant}},
		{Addr: addr, W: []byte{regCoeff1}, R: realCoeff1},
		{Addr: addr, W: []byte{regCoeff2}, R: realCoeff2},
		{Addr: addr, W: []byte{regCoeff3}, R: realCoeff3},
		{Addr: addr, W: []byte{regCtrlMeas}, R: []byte{0x00}},
		{Addr: addr, W: []byte{regCtrlMeas, 0x00}},
		{Addr: addr, W: []byte{regCtrlHum}, R: []byte{0x00}},
		{Addr: addr, W: []byte{regCtrlHum, 0x02}},
		{Addr: addr, W: []byte{regConfig}, R: []byte{0x00}},
		{Addr: addr, W: []byte{regConfig, 0x00}},
		{Addr: addr, W: []byte{regCtrlMeas}, R: []byte{0x00}},
		{Addr: addr, W: []byte{regCtrlMeas, 0x8C}},
	}
	if heat == 0 {
		return append(ops,
			i2ctest.IO{Addr: addr, W: []byte{regCtrlGas1}, R: []byte{0x00}},
			i2ctest.IO{Addr: addr, W: []byte{regCtrlGas1, 0x00}},
			i2ctest.IO{Addr: addr, W: []byte{regCtrlGas0}, R: []byte{0x00}},
			i2ctest.IO{Addr: addr, W: []byte{regCtrlGas0, maskHeaterOff}},
		)
	}
	return append(ops,
		i2ctest.IO{Addr: addr, W: []byte{regResHeat0, 0x71}},
		i2ctest.IO{Addr: addr, W: []byte{regGasWait0, gasWait(heat)}},
		i2ctest.IO{Addr: addr, W: []byte{regCtrlGas0}, R: []byte{0x00}},
		i2ctest.IO{Addr: addr, W: []byte{regCtrlGas0, 0x00}},
		i2ctest.IO{Addr: addr, W: []byte{regCtrlGas1}, R: []byte{0x00}},
		i2ctest.IO{Addr: addr, W: []byte{regCtrlGas1, runGasFor(variant)}},
	)
}

// runGasFor is the ctrl_gas_1 enable each variant wants: the BME688 runs gas from bit 5.
func runGasFor(variant byte) byte {
	if variant == variantGasHigh {
		return 0x20
	}
	return 0x10
}

// senseOps is one forced measurement: set the mode bits, wait for sleep, read the field.
func senseOps(addr uint16, field []byte) []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: addr, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: addr, W: []byte{regCtrlMeas, 0x8D}},
		{Addr: addr, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: addr, W: []byte{regField0}, R: field},
	}
}

// noGasOpts keeps the measurement short and the heater out of the traffic.
func noGasOpts() *Opts {
	o := DefaultOpts
	o.Heater = HeaterOff
	return &o
}

// fixedHeat keeps the gas tests off the warm-up model, which would make each sense sleep two seconds.
const fixedHeat = 150 * time.Millisecond

func gasOpts() *Opts {
	o := DefaultOpts
	o.Heater, o.HeaterDuration = HeaterFixed, fixedHeat
	return &o
}

// The whole sequence is offered, so a driver skipping the check sails through, and one resetting first fails on the reset instead.
func TestNewI2C_RejectsAPartThatIsNotABME680(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, 0x60, variantGasLow, 0), DontPanic: true}
	if _, err := NewI2C(bus, noGasOpts()); err == nil || !strings.Contains(err.Error(), "chip id 0x60") {
		t.Fatalf("NewI2C gave %v; want chip id 0x60 refused before anything is written to it", err)
	}
}

// The trim decides every reading, and the part stores p7 ahead of p6 with h1 and h2 sharing a byte.
func TestNewI2C_ReadsTheFactoryTrim(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, 0), DontPanic: true}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	want := calib{
		t1: 25936, t2: 26651, t3: 3,
		p1: 37585, p2: -10203, p3: 88, p4: 7829, p5: -71, p6: 30, p7: 47,
		p8: -3333, p9: -2316, p10: 30,
		h1: 824, h2: 1001, h3: 0, h4: 45, h5: 20, h6: 120, h7: -100,
		gh1: -53, gh2: -8305, gh3: 18,
		resHeatVal: 39, resHeatRange: 1, rangeSwErr: -3,
	}
	if !reflect.DeepEqual(d.calib, want) {
		t.Errorf("trim parsed as\n%+v\nwant\n%+v", d.calib, want)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// A blank trim converts to a plausible number rather than failing.
func TestNewI2C_RejectsABlankTrim(t *testing.T) {
	ops := openOps(DefaultAddress, chipID, variantGasLow, 0)
	for i := range ops {
		switch ops[i].W[0] {
		case regCoeff1, regCoeff2, regCoeff3:
			ops[i].R = make([]byte, len(ops[i].R))
		}
	}
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	if _, err := NewI2C(bus, noGasOpts()); err == nil {
		t.Fatal("NewI2C accepted an all-zero trim, so every reading would be silently wrong")
	}
}

// Expected values come from an independent transcription of Bosch's float compensation.
func TestSense_ConvertsARealMeasurement(t *testing.T) {
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, 0), senseOps(DefaultAddress, realField)...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}

	if got, want := e.Temperature.Celsius(), 30.8758803734; !near(got, want, 0.0001) {
		t.Errorf("temperature = %.6f C, want %.6f", got, want)
	}
	if got, want := float64(e.Pressure)/float64(physic.Pascal), 101378.7036118366; !near(got, want, 0.01) {
		t.Errorf("pressure = %.4f Pa, want %.4f", got, want)
	}
	if got, want := float64(e.Humidity)/float64(physic.PercentRH), 32.1354164293; !near(got, want, 0.0001) {
		t.Errorf("humidity = %.6f %%, want %.6f", got, want)
	}
	if _, ok := d.SenseGas(); ok {
		t.Error("SenseGas vouched for a reading taken with the heater off")
	}
	if d.IsBME688() {
		t.Error("a part answering variant 0x00 said it is a BME688")
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// A gas reading taken before the plate is hot is wrong, not missing, so the part's own bits decide.
func TestSenseGas_OnlyReportsWhatThePartVouchesFor(t *testing.T) {
	// adc 400 in range 3: msb 0x64, and the low bits sit above the flags in the lsb.
	const gasMSB = 0x64
	for _, tc := range []struct {
		name    string
		lsb     byte
		wantOK  bool
		wantKil float64
	}{
		{"valid and stable", 0x20 | 0x10 | 0x03, true, 1092.333059},
		{"valid but the heater never settled", 0x20 | 0x03, false, 0},
		{"stable but the part marked it invalid", 0x10 | 0x03, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := append([]byte(nil), realField...)
			field[13], field[14] = gasMSB, tc.lsb
			bus := &i2ctest.Playback{
				Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, field)...),
				DontPanic: true,
			}
			d, err := NewI2C(bus, gasOpts())
			if err != nil {
				t.Fatalf("NewI2C: %v", err)
			}
			var e physic.Env
			if err := d.Sense(&e); err != nil {
				t.Fatalf("Sense: %v", err)
			}
			r, ok := d.SenseGas()
			if ok != tc.wantOK {
				t.Fatalf("SenseGas ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got := float64(r) / float64(1000*physic.Ohm); !near(got, tc.wantKil, 0.001) {
				t.Errorf("gas = %.6f kohm, want %.6f", got, tc.wantKil)
			}
			if err := bus.Close(); err != nil {
				t.Errorf("playback not fully consumed: %v", err)
			}
		})
	}
}

// The register packs a 6-bit count and a 2-bit power-of-four multiplier, so an off-by-one hides at the boundaries.
func TestGasWait_EncodesTheDurationTable(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want byte
	}{
		{1, 0x01}, {63, 0x3F}, {64, 0x50}, {150, 0x65},
		{255, 0x7F}, {1000, 0xBE}, {4031, 0xFE}, {4032, 0xFF}, {5000, 0xFF},
	} {
		if got := gasWait(time.Duration(tc.ms) * time.Millisecond); got != tc.want {
			t.Errorf("gasWait(%d ms) = %#02x, want %#02x", tc.ms, got, tc.want)
		}
	}
}

func TestParseOversampling(t *testing.T) {
	for in, want := range map[string]Oversampling{
		"1x": Oversampling1x, "2X": Oversampling2x, " 4x ": Oversampling4x,
		"8": Oversampling8x, "16x": Oversampling16x, "off": OversamplingOff,
	} {
		got, err := ParseOversampling(in)
		if err != nil {
			t.Errorf("ParseOversampling(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseOversampling(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseOversampling("32x"); err == nil {
		t.Error("ParseOversampling accepted 32x, which the part cannot do")
	}
}

// Oversampling drives the measurement wait; reading too early returns the previous field.
func TestMeasureDuration_GrowsWithOversampling(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, 0), DontPanic: true}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	low := d.measureDuration(0)
	d.opts.Temperature, d.opts.Pressure, d.opts.Humidity = Oversampling16x, Oversampling16x, Oversampling16x
	if high := d.measureDuration(0); high <= low {
		t.Errorf("16x everywhere takes %s, not longer than the default %s", high, low)
	}
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// The part signals done by leaving forced mode; the new-data flag alone converts the previous measurement.
func TestSense_WaitsForThePartToLeaveForcedMode(t *testing.T) {
	ops := append(openOps(DefaultAddress, chipID, variantGasLow, 0), []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas, 0x8D}},
		// Still converting: the mode bits are the only thing that says so.
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8D}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: DefaultAddress, W: []byte{regField0}, R: realField},
	}...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed, so the busy read was skipped: %v", err)
	}
}

// A part asleep without new data has nothing to convert; the field still holds the previous one.
func TestSense_RefusesAFieldWithNoNewData(t *testing.T) {
	stale := append([]byte(nil), realField...)
	stale[0] &^= maskNewData
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, 0), senseOps(DefaultAddress, stale)...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err == nil {
		t.Fatal("Sense converted a field the part had not refreshed")
	}
}

// Correcting temperature alone publishes a pair that disagree about the same air.
func TestSense_OffsetMovesTemperatureAndHumidityTogether(t *testing.T) {
	for _, tc := range []struct {
		offset   float64
		wantTemp float64
		wantHum  float64
	}{
		{0, 30.8758803734, 32.1354164293},
		{1.0, 29.8758803734, 34.0295288317},
		{2.5, 28.3758803734, 37.1114123723},
	} {
		o := *noGasOpts()
		o.TemperatureOffset = physic.Temperature(tc.offset * float64(physic.Kelvin))
		bus := &i2ctest.Playback{
			Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, 0), senseOps(DefaultAddress, realField)...),
			DontPanic: true,
		}
		d, err := NewI2C(bus, &o)
		if err != nil {
			t.Fatalf("NewI2C: %v", err)
		}
		var e physic.Env
		if err := d.Sense(&e); err != nil {
			t.Fatalf("Sense: %v", err)
		}
		if got := e.Temperature.Celsius(); !near(got, tc.wantTemp, 0.0001) {
			t.Errorf("offset %.1f: temperature = %.6f C, want %.6f", tc.offset, got, tc.wantTemp)
		}
		if got := float64(e.Humidity) / float64(physic.PercentRH); !near(got, tc.wantHum, 0.0001) {
			t.Errorf("offset %.1f: humidity = %.6f %%, want %.6f", tc.offset, got, tc.wantHum)
		}
	}
}

// The BME688 enables gas from a different bit and reports it from different bytes.
func TestSense_BME688UsesItsOwnGasPathAndEnableBit(t *testing.T) {
	field := append([]byte(nil), realField...)
	// The gas-low bytes are left saying "nothing here": a driver reading them would refuse outright.
	field[13], field[14] = 0x00, 0x00
	field[15], field[16] = 0x64, 0x20|0x10|0x03

	ops := append(openOps(DefaultAddress, chipID, variantGasHigh, fixedHeat),
		senseOps(DefaultAddress, field)...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, gasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if !d.IsBME688() {
		t.Error("a part answering variant 0x01 did not say it is a BME688")
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	r, ok := d.SenseGas()
	if !ok {
		t.Fatal("SenseGas refused a BME688 reading the part marked valid and stable")
	}
	if got, want := float64(r)/float64(1000*physic.Ohm), 8714.893617; !near(got, want, 0.001) {
		t.Errorf("gas = %.6f kohm, want %.6f", got, want)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// Absolute zero is not "unset": as the zero value it cuts the heater drive by a tenth, unreported.
func TestNewI2C_TreatsAZeroAmbientAsUnset(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), DontPanic: true}
	o := *gasOpts()
	o.Ambient, o.HeaterTarget = 0, 0
	d, err := NewI2C(bus, &o)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if d.opts.Ambient != DefaultOpts.Ambient {
		t.Errorf("Ambient = %s, want the default %s", d.opts.Ambient, DefaultOpts.Ambient)
	}
	// openOps expects the 0x71 the default ambient and target produce, so a wrong default fails the playback above.
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

func TestNewI2C_RejectsSettingsThePartCannotHold(t *testing.T) {
	// Each case is otherwise valid, or the all-channels-off check answers before the one named.
	ok := Opts{Temperature: Oversampling1x, Pressure: Oversampling1x, Humidity: Oversampling1x}
	bad := func(f func(*Opts)) Opts {
		o := ok
		f(&o)
		return o
	}
	for name, o := range map[string]Opts{
		"oversampling past 16x":     bad(func(o *Opts) { o.Temperature = Oversampling16x + 1 }),
		"heater duration under 1ms": bad(func(o *Opts) { o.Heater, o.HeaterDuration = HeaterFixed, 500*time.Microsecond }),
		"heater duration past 4032ms": bad(func(o *Opts) {
			o.Heater, o.HeaterDuration = HeaterFixed, 5*time.Second
		}),
		"a fixed heater with no duration": bad(func(o *Opts) { o.Heater = HeaterFixed }),
		"a duration the profile ignores":  bad(func(o *Opts) { o.HeaterDuration = time.Second }),
		"temperature off under pressure":  bad(func(o *Opts) { o.Temperature = OversamplingOff }),
		"humidity off under the heater": bad(func(o *Opts) {
			o.Humidity = OversamplingOff
		}),
		"filter past 127":   bad(func(o *Opts) { o.Filter = filterCount }),
		"every channel off": {},
	} {
		// An erroring bus fails NewI2C whatever it does, so an untouched bus is the proof these run before any I/O.
		bus := &refusingBus{}
		if _, err := NewI2C(bus, &o); err == nil {
			t.Errorf("%s: NewI2C accepted it", name)
		}
		if bus.touched {
			t.Errorf("%s: reached the bus before rejecting it, so the bus refused it and not the check", name)
		}
	}
}

// A mode is only taken from sleep, so a Sense after an abandoned one must put the part back first.
func TestSense_PutsAPartStillConvertingBackToSleepFirst(t *testing.T) {
	ops := append(openOps(DefaultAddress, chipID, variantGasLow, 0), []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8D}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas, 0x8C}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas, 0x8D}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: DefaultAddress, W: []byte{regField0}, R: realField},
	}...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed, so the part was never forced back to sleep: %v", err)
	}
}

// refusingBus separates a setting rejected on its shape from one that died on the first register write.
type refusingBus struct{ touched bool }

func (b *refusingBus) Tx(uint16, []byte, []byte) error {
	b.touched = true
	return errors.New("refusingBus: nothing should have reached the bus")
}
func (b *refusingBus) SetSpeed(physic.Frequency) error { return nil }
func (b *refusingBus) String() string                  { return "refusingBus" }

// Halt has to retire the gas conversion too: leaving run_gas set arms a measurement on a part we are done with.
func TestHalt_StopsTheGasMeasurementAndTheHeater(t *testing.T) {
	ops := append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{regCtrlGas1}, R: []byte{0x10}},
		{Addr: DefaultAddress, W: []byte{regCtrlGas1, 0x00}},
		{Addr: DefaultAddress, W: []byte{regCtrlGas0}, R: []byte{0x00}},
		{Addr: DefaultAddress, W: []byte{regCtrlGas0, maskHeaterOff}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
		{Addr: DefaultAddress, W: []byte{regCtrlMeas, 0x8C}},
	}...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, gasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if err := d.Halt(); err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// The heating time is the whole difference between a gas reading and a number: the plate must reach temperature, which takes longer the longer it was cold.
func TestWarmUp_FollowsTheLibrarysModel(t *testing.T) {
	for _, tc := range []struct {
		off  time.Duration
		want time.Duration
	}{
		{0, 446 * time.Millisecond},
		{3 * time.Second, 495 * time.Millisecond},
		{30 * time.Second, 855 * time.Millisecond},
		{60 * time.Second, 1131 * time.Millisecond},
		// Past the quartic's peak the curve falls away, so a colder plate must not ask for less heat.
		{maxWarmUpOff, 1858 * time.Millisecond},
		{time.Hour, 1858 * time.Millisecond},
	} {
		if got := warmUp(tc.off); got != tc.want {
			t.Errorf("%s cold wants %s of heating, got %s", tc.off, tc.want, got)
		}
	}
	if warmUp(time.Hour) > maxHeaterDuration {
		t.Error("the model asks for longer than the register can encode")
	}
}

// A fixed pulse is the same whatever the poll interval, which makes readings an hour apart incomparable, so the register is rewritten as the gap changes.
func TestSense_SizesTheHeatingFromHowLongThePlateHasBeenCold(t *testing.T) {
	// 601 ms after ten seconds cold, then 855 ms after thirty, each written between the part reaching sleep and the forced measurement starting.
	senseResizing := func(reg byte) []i2ctest.IO {
		return []i2ctest.IO{
			{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
			{Addr: DefaultAddress, W: []byte{regGasWait0, reg}},
			{Addr: DefaultAddress, W: []byte{regCtrlMeas, 0x8D}},
			{Addr: DefaultAddress, W: []byte{regCtrlMeas}, R: []byte{0x8C}},
			{Addr: DefaultAddress, W: []byte{regField0}, R: realField},
		}
	}
	ops := openOps(DefaultAddress, chipID, variantGasLow, warmUp(maxWarmUpOff))
	ops = append(ops, senseResizing(0xA5)...)
	ops = append(ops, senseResizing(0xB5)...)

	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	d.lastGas = time.Now().Add(-10 * time.Second)
	if err := d.Sense(&e); err != nil {
		t.Fatalf("first Sense: %v", err)
	}
	d.lastGas = time.Now().Add(-30 * time.Second)
	if err := d.Sense(&e); err != nil {
		t.Fatalf("second Sense: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("the heater was not reprogrammed as the gap changed: %v", err)
	}
}

// The part's own conversion runs past what the plate can read, and 13 MΩ from an empty reading would publish as very clean air.
func TestSenseGas_RefusesAConversionOutsideThePlatesRange(t *testing.T) {
	field := append([]byte(nil), realField...)
	// adc 0 in range 0, marked valid and stable, converts to about 13Mohm.
	field[13], field[14] = 0x00, maskGasValid|maskHeatStab
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, field)...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, gasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if ohms := d.calib.gasLow(0, 0); ohms <= maxGasOhms {
		t.Fatalf("this field converts to %.0f ohms, inside the envelope, so it tests nothing", ohms)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if r, ok := d.SenseGas(); ok {
		t.Errorf("published %s, which is past what the plate can read", r)
	}
}

// The raw resistance is what the part measured; only the compensated one is comparable between reads.
func TestSenseGasCompensated_MovesWithTheAirTheReadingWasTakenIn(t *testing.T) {
	gas := func(humADC uint16) (physic.ElectricResistance, physic.ElectricResistance, float64) {
		t.Helper()
		field := append([]byte(nil), realField...)
		field[8], field[9] = byte(humADC>>8), byte(humADC)
		field[13], field[14] = 0x80, maskGasValid|maskHeatStab|0x05
		bus := &i2ctest.Playback{
			Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, field)...),
			DontPanic: true,
		}
		d, err := NewI2C(bus, gasOpts())
		if err != nil {
			t.Fatalf("NewI2C: %v", err)
		}
		var e physic.Env
		if err := d.Sense(&e); err != nil {
			t.Fatalf("Sense: %v", err)
		}
		raw, ok := d.SenseGas()
		if !ok {
			t.Fatal("the part did not vouch for the reading, so this proves nothing")
		}
		comp, _ := d.SenseGasCompensated()
		return raw, comp, float64(e.Humidity) / float64(physic.PercentRH)
	}

	dryRaw, dryComp, dryRH := gas(0x2000)
	wetRaw, wetComp, wetRH := gas(0x7000)
	if wetRH <= dryRH {
		t.Fatalf("both fields read %.1f and %.1f %%RH, so there is no humidity difference to compensate", dryRH, wetRH)
	}
	if dryRaw != wetRaw {
		t.Fatalf("the raw resistance moved from %s to %s, but only the humidity changed", dryRaw, wetRaw)
	}
	if wetComp <= dryComp {
		t.Errorf("compensated %s in wetter air against %s in drier, want the wetter one higher", wetComp, dryComp)
	}
}

// airOpts runs the fusion, which needs both a gas plate and the rate the host will read at.
func airOpts() *Opts {
	o := *gasOpts()
	o.AirQuality, o.SamplePeriod = true, 30*time.Second
	return &o
}

// validGasField is a field the part vouches for: adc 400 in range 3, valid and heat-stable.
func validGasField() []byte {
	f := append([]byte(nil), realField...)
	f[13], f[14] = 0x64, maskGasValid|maskHeatStab|0x03
	return f
}

// With no rate to size the filters, or no plate to feed them, a fusion asked for is refused at open rather than left off in silence.
func TestNewI2C_FusionNeedsAPlateAndASamplePeriod(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts func(*Opts)
		heat time.Duration
	}{
		{"no sample period", func(o *Opts) { o.SamplePeriod = 0 }, fixedHeat},
		{"heater off", func(o *Opts) { o.Heater, o.HeaterDuration = HeaterOff, 0 }, 0},
	} {
		o := airOpts()
		tc.opts(o)
		bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, tc.heat), DontPanic: true}
		if _, err := NewI2C(bus, o); err == nil || !strings.Contains(err.Error(), "fusion") {
			t.Errorf("%s: NewI2C gave %v, want the fusion refused", tc.name, err)
		}
	}

	for _, tc := range []struct {
		name string
		opts *Opts
		heat time.Duration
		want bool
	}{
		{"asked for, with a plate and a period", airOpts(), fixedHeat, true},
		{"not asked for", gasOpts(), fixedHeat, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &i2ctest.Playback{
				Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, tc.heat), senseOps(DefaultAddress, validGasField())...),
				DontPanic: true,
			}
			d, err := NewI2C(bus, tc.opts)
			if err != nil {
				t.Fatalf("NewI2C: %v", err)
			}
			var e physic.Env
			if err := d.Sense(&e); err != nil {
				t.Fatalf("Sense: %v", err)
			}
			if _, ok := d.SenseAirQuality(); ok != tc.want {
				t.Errorf("SenseAirQuality ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

// A part that has just been switched on has learned nothing, and its first reading must say so.
func TestSenseAirQuality_FirstSampleIsNotCalibrated(t *testing.T) {
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, validGasField())...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, airOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	v, ok := d.SenseAirQuality()
	if !ok {
		t.Fatal("the part vouched for the gas reading but reported no index")
	}
	if v.Accuracy != 0 || v.RunIn {
		t.Errorf("accuracy %d and run-in %v on the first sample, want 0 and false", v.Accuracy, v.RunIn)
	}
	if !near(v.IAQ, 50, 1e-9) || !near(v.CO2Equivalent, 600, 1e-9) {
		t.Errorf("index %v and CO2 %v, want the 50 and 600 a pipeline starts from", v.IAQ, v.CO2Equivalent)
	}
	if !v.Stabilised {
		t.Error("the part reported itself unstabilised, which ships disabled")
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// A gas reading the part will not stand behind must not reach the fusion at all.
func TestSenseAirQuality_IgnoresAReadingThePartRefuses(t *testing.T) {
	field := validGasField()
	field[14] &^= maskHeatStab
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, field)...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, airOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if _, ok := d.SenseAirQuality(); ok {
		t.Error("the fusion published an index built on a reading the plate would not vouch for")
	}
}

// The host can only carry calibration across a restart if it can take it out and put it back.
func TestAirQualityState_RoundTripsThroughTheDriver(t *testing.T) {
	open := func(t *testing.T, opts *Opts, heat time.Duration) *BME680 {
		t.Helper()
		bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, heat), DontPanic: true}
		d, err := NewI2C(bus, opts)
		if err != nil {
			t.Fatalf("NewI2C: %v", err)
		}
		return d
	}

	d := open(t, airOpts(), fixedHeat)
	saved, err := d.AirQualityState()
	if err != nil || len(saved) == 0 {
		t.Fatalf("AirQualityState = %q, %v; want state and no error", saved, err)
	}

	other := open(t, airOpts(), fixedHeat)
	if err := other.RestoreAirQuality(saved); err != nil {
		t.Fatalf("RestoreAirQuality: %v", err)
	}
	back, err := other.AirQualityState()
	if err != nil {
		t.Fatalf("AirQualityState: %v", err)
	}
	if string(back) != string(saved) {
		t.Errorf("restored state reads\n%s\nwant\n%s", back, saved)
	}
	if err := other.RestoreAirQuality([]byte(`{"version":404}`)); err == nil {
		t.Error("a state from another build was accepted")
	}

	// Another part, told apart by its factory trim, starts over rather than take extremes it never saw.
	ops := openOps(DefaultAddress, chipID, variantGasLow, fixedHeat)
	for i, op := range ops {
		if len(op.W) == 1 && op.W[0] == regCoeff1 {
			trim := bytes.Clone(op.R)
			trim[0]++
			ops[i].R = trim
		}
	}
	swapped, err := NewI2C(&i2ctest.Playback{Ops: ops, DontPanic: true}, airOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if err := swapped.RestoreAirQuality(saved); err == nil || !strings.Contains(err.Error(), "another part") {
		t.Errorf("a part with another trim took the state with %v", err)
	}

	// A part with the heater off has nothing to restore into, and must not report success.
	cold := open(t, noGasOpts(), 0)
	if b, err := cold.AirQualityState(); b != nil || err != nil {
		t.Errorf("a part with no fusion returned %q, %v; want nil, nil", b, err)
	}
	if err := cold.RestoreAirQuality(saved); err == nil {
		t.Error("a part with no fusion reported a successful restore")
	}
}

// A reopened part reads off for minutes, so a restore runs it in again, withholding the index, but keeps what it learned.
func TestRestoreAirQuality_RunsInAgainButKeepsWhatItLearned(t *testing.T) {
	learned := newTracker(pollPeriod)
	run(learned, 50_000, runInSamples+10, epoch)
	if !learned.st.RunIn {
		t.Fatal("run-in never completed, so this proves nothing")
	}
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), DontPanic: true}
	d, err := NewI2C(bus, airOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	saved, err := learned.marshalState(d.chip)
	if err != nil {
		t.Fatalf("marshalState: %v", err)
	}
	if err := d.RestoreAirQuality(saved); err != nil {
		t.Fatalf("RestoreAirQuality: %v", err)
	}
	if st := d.air.st; st.RunIn || st.RunInAccumSec != 0 || !d.air.last.IsZero() {
		t.Errorf("restored with run-in %v, %v s accrued and a last sample at %v, want it starting over", st.RunIn, st.RunInAccumSec, d.air.last)
	}
	if st := d.air.st; st.BandMax != learned.st.BandMax || st.MatureAccumSec != learned.st.MatureAccumSec {
		t.Error("the restore dropped what the part had learned")
	}
}

// BSEC's host stamps a sample when it triggers it, before a heat and a wait whose length varies.
func TestSenseAirQuality_StampsTheSampleWhenTheMeasurementStarted(t *testing.T) {
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, fixedHeat), senseOps(DefaultAddress, validGasField())...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, airOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	before := time.Now()
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	after := time.Now()
	// Sense is almost all heater wait, so a stamp taken at the trigger falls in its first half and one taken after the wait in its last.
	stamp := d.air.last
	if stamp.Sub(before) >= after.Sub(stamp) {
		t.Errorf("the sample is stamped %s into a %s Sense, want at the trigger", stamp.Sub(before), after.Sub(before))
	}
}

// A stream that carried on past a failed read would go quiet for an unplugged part, which reads as a long interval, and a zero interval would panic its ticker.
func TestSenseContinuous_EndsAtAFailedReadAndRefusesNoInterval(t *testing.T) {
	bus := &i2ctest.Playback{
		Ops:       append(openOps(DefaultAddress, chipID, variantGasLow, 0), senseOps(DefaultAddress, realField)...),
		DontPanic: true,
	}
	d, err := NewI2C(bus, noGasOpts())
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if _, err := d.SenseContinuous(0); err == nil {
		t.Fatal("a zero interval was taken")
	}
	ch, err := d.SenseContinuous(time.Millisecond)
	if err != nil {
		t.Fatalf("SenseContinuous: %v", err)
	}
	defer d.Halt()
	// The one measurement played back, then the read that finds the part gone.
	for i, want := range []bool{true, false} {
		select {
		case _, ok := <-ch:
			if ok != want {
				t.Fatalf("receive %d: open = %v, want %v", i, ok, want)
			}
		case <-time.After(3 * DefaultOpts.MeasurementReadTimeout):
			t.Fatalf("receive %d: the stream went quiet instead of ending", i)
		}
	}
}
