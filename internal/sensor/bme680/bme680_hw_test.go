package bme680

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/host/v3"
)

// Skipped unless BME680_HW_BUS names a bus ("1" for /dev/i2c-1; BME680_HW_ADDR overrides 0x76); build with `go test -c`, as the Pis have no toolchain.
func hwBus(t *testing.T) (i2c.Bus, uint16) {
	t.Helper()
	name := os.Getenv("BME680_HW_BUS")
	if name == "" {
		t.Skip("set BME680_HW_BUS to the I2C bus to run against real hardware")
	}
	if _, err := host.Init(); err != nil {
		t.Fatalf("periph host init: %v", err)
	}
	bus, err := i2creg.Open(name)
	if err != nil {
		t.Fatalf("opening bus %q: %v", name, err)
	}
	t.Cleanup(func() { bus.Close() })

	addr := uint16(DefaultAddress)
	if s := os.Getenv("BME680_HW_ADDR"); s != "" {
		v, err := strconv.ParseUint(s, 0, 16)
		if err != nil {
			t.Fatalf("BME680_HW_ADDR %q is not an address: %v", s, err)
		}
		addr = uint16(v)
	}
	return bus, addr
}

func TestHardware_Identity(t *testing.T) {
	bus, addr := hwBus(t)
	if err := Probe(bus, addr); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	d, err := NewI2C(bus, hwOpts(addr))
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	defer d.Halt()
	t.Logf("%s, BME688 %v", d, d.IsBME688())
	c := d.calib
	t.Logf("trim t1=%d t2=%d t3=%d p1=%d gh1=%d gh2=%d gh3=%d resHeatRange=%d resHeatVal=%d rangeSwErr=%d",
		c.t1, c.t2, c.t3, c.p1, c.gh1, c.gh2, c.gh3, c.resHeatRange, c.resHeatVal, c.rangeSwErr)
	// The drive byte is what the heater actually gets; a clamp firing here would mean a trim we misread.
	t.Logf("res_heat for 320C at 25C ambient = %d", c.resHeat(320, 25))
}

func hwOpts(addr uint16) *Opts {
	o := DefaultOpts
	o.Address = addr
	return &o
}

func TestHardware_SenseIsPlausible(t *testing.T) {
	bus, addr := hwBus(t)
	d, err := NewI2C(bus, hwOpts(addr))
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	defer d.Halt()

	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	gas, ok := d.SenseGas()
	comp, _ := d.SenseGasCompensated()
	t.Logf("%s  %s  %s  gas=%s compensated=%s valid=%v", e.Temperature, e.Pressure, e.Humidity, gas, comp, ok)

	if c := e.Temperature.Celsius(); c < -20 || c > 60 {
		t.Errorf("temperature %s is outside what a room or a shed reads", e.Temperature)
	}
	if hpa := float64(e.Pressure) / float64(100*physic.Pascal); hpa < 800 || hpa > 1100 {
		t.Errorf("pressure %.1f hPa is outside sea-level weather", hpa)
	}
	if rh := float64(e.Humidity) / float64(physic.PercentRH); rh < 0 || rh > 100 {
		t.Errorf("humidity %.1f %% is not a fraction", rh)
	}
	if !ok {
		t.Error("the part did not vouch for the gas reading on the default profile")
	}
}

// Holds conditions still and starts each sample from the same cold plate: a resistance still climbing at the longest heat means shorter heats read the plate on its way up.
func TestHardware_HeaterSweep(t *testing.T) {
	bus, addr := hwBus(t)
	settle := 30 * time.Second
	if testing.Short() {
		t.Skip("the sweep spends half a minute per sample letting the plate go cold")
	}

	o := hwOpts(addr)
	o.Heater, o.HeaterDuration = HeaterFixed, 150*time.Millisecond
	d, err := NewI2C(bus, o)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	defer d.Halt()

	// BME680_HW_SWEEP overrides the durations so a run can be repeated in reverse: a trend that survives it is the heating, one that does not is the air drifting.
	heats := []time.Duration{
		150 * time.Millisecond, // what this driver used to send
		warmUp(0),              // the model's floor, from a hot plate
		warmUp(settle),         // what the default profile now asks for at this gap
		warmUp(maxWarmUpOff),   // fully cold
	}
	if list := os.Getenv("BME680_HW_SWEEP"); list != "" {
		heats = nil
		for _, f := range strings.Split(list, ",") {
			ms, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				t.Fatalf("BME680_HW_SWEEP %q is not a list of milliseconds: %v", list, err)
			}
			heats = append(heats, time.Duration(ms)*time.Millisecond)
		}
	}
	for _, heat := range heats {
		// Every sample starts from the same off-time, or the sweep measures the gap and not the heating.
		time.Sleep(settle)
		d.opts.HeaterDuration = heat
		var e physic.Env
		if err := d.Sense(&e); err != nil {
			t.Fatalf("Sense at %s: %v", heat, err)
		}
		gas, ok := d.SenseGas()
		comp, _ := d.SenseGasCompensated()
		t.Logf("heat=%-8s gas=%-12s compensated=%-12s valid=%-5v  %s %s",
			heat, gas, comp, ok, e.Temperature, e.Humidity)
	}
}

// The node's default profile: two reads a known gap apart must differ in heating and both come back stable.
func TestHardware_WarmUpProfileHoldsStable(t *testing.T) {
	bus, addr := hwBus(t)
	d, err := NewI2C(bus, hwOpts(addr))
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	defer d.Halt()

	for i, gap := range []time.Duration{0, 5 * time.Second, 30 * time.Second} {
		time.Sleep(gap)
		var e physic.Env
		start := time.Now()
		if err := d.Sense(&e); err != nil {
			t.Fatalf("sense %d: %v", i+1, err)
		}
		gas, ok := d.SenseGas()
		comp, _ := d.SenseGasCompensated()
		t.Logf("gap=%-8s heat=%-8s took=%-12s gas=%-12s compensated=%-12s valid=%v",
			gap, gasWaitDuration(d.heatReg), time.Since(start).Round(time.Millisecond), gas, comp, ok)
		if !ok {
			t.Errorf("read %d came back without the part vouching for it", i+1)
		}
	}
}
