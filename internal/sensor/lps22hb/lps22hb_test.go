package lps22hb

import (
	"math"
	"strings"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"
)

// coldFirst is the first conversion pi-4-2's LPS22HB gave after a full reboot: 760.31 hPa, against 1026.57 thirty seconds later.
const coldFirst = 3114230

// openOps is the traffic a successful open expects, as the datasheet's bytes so a wrong driver constant fails here: WHO_AM_I, reset, poll the reset and boot bits, configure, one conversion thrown away.
func openOps(addr uint16, who byte) []i2ctest.IO {
	ops := []i2ctest.IO{
		{Addr: addr, W: []byte{0x0F}, R: []byte{who}},
		{Addr: addr, W: []byte{0x11, 0x84}},
		{Addr: addr, W: []byte{0x11}, R: []byte{0x00}},
		{Addr: addr, W: []byte{0x25}, R: []byte{0x00}},
		{Addr: addr, W: []byte{0x11, 0x10}},
		{Addr: addr, W: []byte{0x10, 0x02}},
	}
	if who != whoAmIValue {
		return ops
	}
	return append(ops, measureOps(addr, coldFirst, 2150)...)
}

// measureOps is one Sense: trigger, poll status, read the five result bytes.
func measureOps(addr uint16, p int32, t int16) []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: addr, W: []byte{0x11, 0x11}},
		{Addr: addr, W: []byte{0x27}, R: []byte{0x03}},
		{Addr: addr, W: []byte{0x28}, R: []byte{
			byte(p), byte(p >> 8), byte(p >> 16),
			byte(t), byte(t >> 8),
		}},
	}
}

func TestNewI2C_DefaultsToTheSA0LowAddress(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, 0xB1), DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if d.Address() != 0x5C {
		t.Errorf("Address() = %#02x, want 0x5C", d.Address())
	}
	if d.WhoAmI() != 0xB1 {
		t.Errorf("WhoAmI() = %#02x, want 0xb1", d.WhoAmI())
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// A non-default address has to reach the bus, not just be stored; playback asserts every address.
func TestNewI2C_HonoursANonDefaultAddress(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(AltAddress, 0xB1), DontPanic: true}
	opts := DefaultOpts
	opts.Address = AltAddress
	d, err := NewI2C(bus, &opts)
	if err != nil {
		t.Fatalf("NewI2C at %#02x: %v", AltAddress, err)
	}
	if d.Address() != 0x5D {
		t.Errorf("Address() = %#02x, want 0x5D", d.Address())
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

// A zero address means "unset", not address zero, which is the I2C general call.
func TestNewI2C_ZeroAddressFallsBackToTheDefault(t *testing.T) {
	bus := &i2ctest.Playback{Ops: openOps(DefaultAddress, 0xB1), DontPanic: true}
	d, err := NewI2C(bus, &Opts{MeasurementReadTimeout: DefaultOpts.MeasurementReadTimeout})
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if d.Address() != DefaultAddress {
		t.Errorf("Address() = %#02x, want %#02x", d.Address(), DefaultAddress)
	}
}

// A cold part's first conversion is wrong but plausible, so the open spends it and the first Sense is the second.
func TestNewI2C_DiscardsTheFirstConversion(t *testing.T) {
	e := senseAt(t, 4096*1026, 2150)
	if hPa := float64(e.Pressure) / float64(100*physic.Pascal); math.Abs(hPa-1026) > hPaTolerance {
		t.Errorf("first Sense = %.2f hPa, want 1026: the open's throwaway conversion reached the caller", hPa)
	}
}

// WHO_AM_I before the reset: resetting whatever is there would touch someone else's chip.
func TestNewI2C_RejectsWrongDeviceBeforeResettingIt(t *testing.T) {
	ops := openOps(DefaultAddress, 0xBD) // an LPS25HB, say
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	if _, err := NewI2C(bus, nil); err == nil {
		t.Fatal("NewI2C accepted a device whose WHO_AM_I is not an LPS22HB's")
	}
	if bus.Count != 1 {
		t.Errorf("%d transactions before rejecting, want 1: nothing may be written to an unknown device", bus.Count)
	}
}

// One tenth of an LSB each: 1/4096 hPa and 1/100 C.
const (
	hPaTolerance = 0.00002
	cTolerance   = 0.001
)

func senseAt(t *testing.T, p int32, tRaw int16) physic.Env {
	t.Helper()
	ops := append(openOps(DefaultAddress, 0xB1), measureOps(DefaultAddress, p, tRaw)...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
	return e
}

// Raw words are asymmetric across their bytes, so an endianness swap cannot slip through.
func TestSense_ConvertsRawWords(t *testing.T) {
	// 0x3EA1B4 / 4096 = 1002.106445 hPa; 0x0929 = 2345 -> 23.45 C.
	e := senseAt(t, 0x3EA1B4, 0x0929)

	hPa := float64(e.Pressure) / float64(100*physic.Pascal)
	if math.Abs(hPa-1002.106445) > hPaTolerance {
		t.Errorf("pressure = %.6f hPa, want 1002.106445", hPa)
	}
	if c := e.Temperature.Celsius(); math.Abs(c-23.45) > cTolerance {
		t.Errorf("temperature = %.4f C, want 23.4500", c)
	}
}

// Unsigned decoding of -1.0 hPa gives 4095.0, which still looks like a real reading.
func TestSense_HandlesNegativeValues(t *testing.T) {
	e := senseAt(t, -4096, -1234) // -1.0 hPa and -12.34 C

	hPa := float64(e.Pressure) / float64(100*physic.Pascal)
	if math.Abs(hPa-(-1)) > hPaTolerance {
		t.Errorf("pressure = %.6f hPa, want -1.000000: bit 23 was not sign-extended", hPa)
	}
	if c := e.Temperature.Celsius(); math.Abs(c-(-12.34)) > cTolerance {
		t.Errorf("temperature = %.4f C, want -12.3400", c)
	}
}

// Reading on the first flag would pair a fresh half with the previous conversion's other half.
func TestSense_WaitsForBothValues(t *testing.T) {
	ops := append(openOps(DefaultAddress, 0xB1),
		// Pressure ready, temperature not: this must not be taken as done.
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x11, 0x11}},
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x27}, R: []byte{0x01}},
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x27}, R: []byte{0x03}},
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x28}, R: []byte{0xB4, 0xA1, 0x3E, 0x29, 0x09}},
	)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	// Close fails unless both status polls were consumed, which is the assertion.
	if err := bus.Close(); err != nil {
		t.Errorf("Sense did not wait for both data-available flags: %v", err)
	}
}

// One wedged sensor must not hold up the whole poll pass.
func TestSense_TimesOutWhenNoValueArrives(t *testing.T) {
	ops := openOps(DefaultAddress, 0xB1)
	ops = append(ops, i2ctest.IO{Addr: DefaultAddress, W: []byte{0x11, 0x11}})
	for range 40 {
		ops = append(ops, i2ctest.IO{Addr: DefaultAddress, W: []byte{0x27}, R: []byte{0x00}})
	}
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	opts := DefaultOpts
	opts.MeasurementReadTimeout = 20 * time.Millisecond
	d, err := NewI2C(bus, &opts)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var e physic.Env
	if err := d.Sense(&e); err == nil {
		t.Fatal("Sense returned no error although the sensor never reported a value")
	}
}

func TestPrecision(t *testing.T) {
	var e physic.Env
	(&LPS22HB{}).Precision(&e)
	if want := physic.Pressure(math.Round(float64(100*physic.Pascal) / 4096)); e.Pressure != want {
		t.Errorf("pressure precision = %v, want %v", e.Pressure, want)
	}
	if want := physic.Temperature(math.Round(float64(physic.Kelvin) / 100)); e.Temperature != want {
		t.Errorf("temperature precision = %v, want %v", e.Temperature, want)
	}
	if e.Humidity != 0 {
		t.Errorf("humidity precision = %v, want 0: the LPS22HB has no hygrometer", e.Humidity)
	}
}

// The boot bit clears before the trim reload; configuring in that window read 250 hPa out.
func TestNewI2C_WaitsForTheMemoryRebootNotJustTheResetBit(t *testing.T) {
	ops := []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{0x0F}, R: []byte{0xB1}},
		{Addr: DefaultAddress, W: []byte{0x11, 0x84}},
		{Addr: DefaultAddress, W: []byte{0x11}, R: []byte{0x00}},
		// The command bit has cleared, but the reboot is still running.
		{Addr: DefaultAddress, W: []byte{0x25}, R: []byte{0x80}},
		{Addr: DefaultAddress, W: []byte{0x25}, R: []byte{0x00}},
		{Addr: DefaultAddress, W: []byte{0x11, 0x10}},
		{Addr: DefaultAddress, W: []byte{0x10, 0x02}},
	}
	ops = append(ops, measureOps(DefaultAddress, coldFirst, 2150)...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	if _, err := NewI2C(bus, nil); err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	// Close fails unless both boot-status polls were consumed, which is the assertion.
	if err := bus.Close(); err != nil {
		t.Errorf("open did not wait out the memory reboot: %v", err)
	}
}

// neverReadyBus is an LPS22HB whose conversion never lands: WHO_AM_I answers and every other register reads zero, STATUS included.
type neverReadyBus struct{}

func (neverReadyBus) String() string                  { return "neverReadyBus" }
func (neverReadyBus) SetSpeed(physic.Frequency) error { return nil }

func (neverReadyBus) Tx(addr uint16, w, r []byte) error {
	if len(w) == 1 && w[0] == 0x0F {
		r[0] = 0xB1
	}
	return nil
}

// Zero is unset, as in every driver here, not "wait for ever": a part that stopped converting would hold its caller for good.
func TestSense_TakesTheDefaultTimeoutForZero(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := NewI2C(neverReadyBus{}, &Opts{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("NewI2C gave %v, want its first conversion to time out", err)
		}
	case <-time.After(10 * DefaultOpts.MeasurementReadTimeout):
		t.Fatal("a zero timeout waited for ever on a part that never finished")
	}
}

// A stream that carried on past a failed read would go quiet for an unplugged part, which reads as a long interval, and a zero interval would panic its ticker.
func TestSenseContinuous_EndsAtAFailedReadAndRefusesNoInterval(t *testing.T) {
	bus := &i2ctest.Playback{Ops: append(openOps(DefaultAddress, 0xB1), measureOps(DefaultAddress, 4096*1000, 2150)...), DontPanic: true}
	d, err := NewI2C(bus, nil)
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
		case <-time.After(10 * DefaultOpts.MeasurementReadTimeout):
			t.Fatalf("receive %d: the stream went quiet instead of ending", i)
		}
	}
}
