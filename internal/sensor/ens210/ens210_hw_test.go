package ens210

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/host/v3"
)

// Skipped unless ENS210_HW_BUS names a bus ("1" for /dev/i2c-1); cross-compile with `go test -c`, as neither Pi has a toolchain.
func hwBus(t *testing.T) i2c.Bus {
	t.Helper()
	name := os.Getenv("ENS210_HW_BUS")
	if name == "" {
		t.Skip("set ENS210_HW_BUS to the I2C bus to run against real hardware")
	}
	if _, err := host.Init(); err != nil {
		t.Fatalf("periph host init: %v", err)
	}
	bus, err := i2creg.Open(name)
	if err != nil {
		t.Fatalf("opening bus %q: %v", name, err)
	}
	t.Cleanup(func() { bus.Close() })
	return bus
}

func TestHardware_Identity(t *testing.T) {
	bus := hwBus(t)
	if err := Probe(bus, DefaultAddress); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if d.PartID() != partIDValue {
		t.Errorf("PART_ID %#04x, want %#04x", d.PartID(), partIDValue)
	}
	if d.UID() == 0 {
		t.Error("UID is zero, which no genuine part reports")
	}
	t.Logf("part %#04x die rev %#04x uid %#016x", d.PartID(), d.DieRev(), d.UID())
}

// Checked against the registers the part latched: a scale error reads as a plausible room temperature.
func TestHardware_SenseMatchesTheRawRegisters(t *testing.T) {
	bus := hwBus(t)
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}

	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	t.Logf("%s  %s", e.Temperature, e.Humidity)

	// T_VAL and H_VAL hold the last single-shot result until the next one is started.
	dev := i2c.Dev{Bus: bus, Addr: DefaultAddress}
	var raw [6]byte
	if err := dev.Tx([]byte{regTVal}, raw[:]); err != nil {
		t.Fatalf("re-reading the measurement registers: %v", err)
	}
	tRaw := binary.LittleEndian.Uint32(append(raw[0:3:3], 0)) & 0xFFFF
	hRaw := binary.LittleEndian.Uint32(append(raw[3:6:6], 0)) & 0xFFFF

	wantT := physic.Temperature(math.Round(float64(tRaw) / 64.0 * float64(physic.Kelvin)))
	wantH := physic.RelativeHumidity(math.Round(float64(hRaw) / 512.0 * float64(physic.PercentRH)))
	if e.Temperature != wantT {
		t.Errorf("temperature %s from raw %d, want %s", e.Temperature, tRaw, wantT)
	}
	if e.Humidity != wantH {
		t.Errorf("humidity %s from raw %d, want %s", e.Humidity, hRaw, wantH)
	}

	// A part that answers but is not measuring reads as a fixed, plausible-looking value.
	if e.Temperature < -20*physic.Celsius+physic.ZeroCelsius || e.Temperature > 70*physic.Celsius+physic.ZeroCelsius {
		t.Errorf("temperature %s is outside anything an indoor board sees", e.Temperature)
	}
	if e.Humidity <= 0 || e.Humidity > 100*physic.PercentRH {
		t.Errorf("humidity %s is not a relative humidity", e.Humidity)
	}
}

// A read that works only once points at the low-power sequencing; a CRC passing by luck shows as a wild second value.
func TestHardware_RepeatedSense(t *testing.T) {
	bus := hwBus(t)
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	var first physic.Env
	var slowest time.Duration
	for i := range 10 {
		var e physic.Env
		started := time.Now()
		if err := d.Sense(&e); err != nil {
			t.Fatalf("Sense %d: %v", i+1, err)
		}
		took := time.Since(started)
		slowest = max(slowest, took)
		t.Logf("read %d: %s  %s  in %s", i+1, e.Temperature, e.Humidity, took.Round(time.Millisecond))
		if i == 0 {
			first = e
			continue
		}
		if diff := e.Temperature - first.Temperature; diff > 2*physic.Celsius || diff < -2*physic.Celsius {
			t.Errorf("read %d moved %s from the first, which is not a room warming up", i+1, diff)
		}
	}

	// Too little headroom turns a busier bus into a sensor that reports nothing, which reads as a dead one.
	budget := DefaultOpts.MeasurementReadTimeout
	t.Logf("slowest read %s against a %s timeout (%.0f%% of budget)",
		slowest.Round(time.Millisecond), budget, float64(slowest)/float64(budget)*100)
	if slowest > budget*2/3 {
		t.Errorf("slowest read %s uses more than two thirds of the %s timeout", slowest, budget)
	}
}
