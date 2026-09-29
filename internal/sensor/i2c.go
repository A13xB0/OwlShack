package sensor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/analog"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/host/v3"

	"github.com/meshcore-go/OwlShack/internal/sensor/ads1x15"
	"github.com/meshcore-go/OwlShack/internal/sensor/bme680"
	"github.com/meshcore-go/OwlShack/internal/sensor/ens210"
	"github.com/meshcore-go/OwlShack/internal/sensor/lps22hb"
	"github.com/meshcore-go/OwlShack/internal/sensor/shtc3"
)

// chip is one part this build can drive, and the addresses it can answer on.
type chip struct {
	kind string
	// label is the name printed on the chip; describe is what makes it findable when searching.
	label    string
	describe string
	category string
	// addrs is every address the part's strap pins allow, so a scan finds one wired the non-default way.
	addrs []uint16
	// metrics are the Env fields this part fills; an unmeasured one stays zero and would publish.
	metrics []Metric
	// reportsUnder narrows metrics by the part's options; nil reports them all.
	reportsUnder func(options map[string]string) []Metric
	// fields are the options this part needs beyond the bus and address every part takes.
	fields []Field
	// silent marks a part that answers nothing until woken, so no scan can see it.
	silent bool
	// open returns a periph resource, since an ADC reports volts and has no Env, and takes the sample period a part deriving readings from its rate must be built for.
	open func(bus i2c.Bus, addr uint16, opts map[string]string, period time.Duration) (conn.Resource, error)
}

var chips = []chip{
	{
		kind: "shtc3", label: "SHTC3",
		describe: "Temperature and humidity (a scan cannot see it)", category: "Environment",
		addrs:   []uint16{shtc3.DefaultAddress},
		metrics: []Metric{Temperature, Humidity},
		silent:  true,
		open: func(bus i2c.Bus, addr uint16, _ map[string]string, _ time.Duration) (conn.Resource, error) {
			opts := shtc3.DefaultOpts
			opts.Address = addr
			return shtc3.NewI2C(bus, &opts)
		},
	},
	{
		kind: "lps22hb", label: "LPS22HB",
		describe: "Barometric pressure and temperature", category: "Environment",
		addrs:   []uint16{lps22hb.DefaultAddress, lps22hb.AltAddress},
		metrics: []Metric{Pressure, Temperature},
		open: func(bus i2c.Bus, addr uint16, _ map[string]string, _ time.Duration) (conn.Resource, error) {
			opts := lps22hb.DefaultOpts
			opts.Address = addr
			return lps22hb.NewI2C(bus, &opts)
		},
	},
	{
		kind: "bme680", label: "BME680",
		describe: "Temperature, pressure, humidity, gas resistance and air quality", category: "Environment",
		addrs: []uint16{bme680.DefaultAddress, bme680.AltAddress},
		metrics: []Metric{
			Temperature, Pressure, Humidity, Resistance, GasCompensated,
			IAQ, StaticIAQ, CO2Equivalent, BreathVOC, GasPercentage,
			IAQAccuracy, GasPercentageAccuracy, AirQualityRunIn,
		},
		reportsUnder: func(o map[string]string) []Metric {
			if o["heater"] == "off" {
				return []Metric{Temperature, Pressure, Humidity}
			}
			return nil
		},
		fields: []Field{
			{
				Key: "oversampling", Label: "Oversampling",
				Help:    "Samples averaged into each reading; higher is quieter and slower",
				Choices: []string{"1x", "2x", "4x", "8x", "16x"}, Default: "8x",
			},
			{
				Key: "heater", Label: "Gas heater",
				Help:    "Off leaves the gas plate cold and reports only the other three",
				Choices: []string{"on", "off"}, Default: "on",
			},
			{
				Key: "temperature_offset", Label: "Temperature offset",
				Help:    "Degrees C to subtract for self-heating; with the heater on it samples every 3 s, which warms it about 0.1, so leave it at 0 unless the enclosure runs warm",
				Default: "0",
			},
		},
		open: func(bus i2c.Bus, addr uint16, o map[string]string, period time.Duration) (conn.Resource, error) {
			opts, err := bmeOpts(addr, o, period)
			if err != nil {
				return nil, err
			}
			return bme680.NewI2C(bus, &opts)
		},
	},
	{
		kind: "sgm58031", label: "SGM58031",
		describe: "Four-channel 16-bit ADC, ADS1115 compatible", category: "Analogue",
		addrs:   []uint16{0x48, 0x49, 0x4A, 0x4B},
		metrics: []Metric{Voltage},
		fields:  adcFields,
		open: func(bus i2c.Bus, addr uint16, o map[string]string, _ time.Duration) (conn.Resource, error) {
			return openADC(bus, addr, o, ads1x15.SGM58031)
		},
	},
	{
		kind: "ads1115", label: "ADS1115",
		describe: "Four-channel 16-bit ADC", category: "Analogue",
		addrs:   []uint16{0x48, 0x49, 0x4A, 0x4B},
		metrics: []Metric{Voltage},
		fields:  adcFields,
		open: func(bus i2c.Bus, addr uint16, o map[string]string, _ time.Duration) (conn.Resource, error) {
			return openADC(bus, addr, o, ads1x15.ADS1115)
		},
	},
	{
		kind: "ens210", label: "ENS210",
		describe: "Temperature and humidity", category: "Environment",
		addrs:   []uint16{ens210.DefaultAddress},
		metrics: []Metric{Temperature, Humidity},
		open: func(bus i2c.Bus, addr uint16, _ map[string]string, _ time.Duration) (conn.Resource, error) {
			opts := ens210.DefaultOpts
			opts.Address = addr
			return ens210.NewI2C(bus, &opts)
		},
	},
}

// bmeOpts turns the stored options and the sample period into driver settings; a part told the wrong rate sizes its air-quality filters for one it never sees, and one told none reports no index.
func bmeOpts(addr uint16, o map[string]string, period time.Duration) (bme680.Opts, error) {
	opts := bme680.DefaultOpts
	opts.Address = addr
	opts.SamplePeriod = period
	if v := o["oversampling"]; v != "" {
		os, err := bme680.ParseOversampling(v)
		if err != nil {
			return opts, err
		}
		opts.Temperature, opts.Pressure, opts.Humidity = os, os, os
	}
	if o["heater"] == "off" {
		opts.Heater = bme680.HeaterOff
	}
	opts.AirQuality = opts.Heater != bme680.HeaterOff
	if opts.AirQuality {
		// BSEC's scheduled 3 s measurement; the warm-up model is its on-demand one, for a part read at odd times.
		opts.Heater, opts.HeaterDuration = bme680.HeaterFixed, 197*time.Millisecond
	}
	if v := o["temperature_offset"]; v != "" {
		off, err := strconv.ParseFloat(v, 64)
		// Self-heating is a degree or two, so tens of degrees is a typo; negated so NaN fails too.
		if err != nil || !(math.Abs(off) <= maxTemperatureOffset) {
			return opts, fmt.Errorf("temperature offset %q is not a number of degrees within ±%d", v, maxTemperatureOffset)
		}
		opts.TemperatureOffset = physic.Temperature(off * float64(physic.Kelvin))
	}
	return opts, nil
}

const maxTemperatureOffset = 50

// adcFields are the inputs both ADC kinds take on top of the bus and address.
var adcFields = []Field{
	{
		Key: "channel", Label: "Input", Identifies: true,
		Help:    "A0 to A3 against ground, or a documented differential pair",
		Choices: []string{"A0", "A1", "A2", "A3", "A0-A1", "A0-A3", "A1-A3", "A2-A3"},
		Default: "A0",
	},
	{
		Key: "gain", Label: "Range",
		Help:    "Full scale; a 3.3V signal clips at 2.048V",
		Choices: []string{"6.144V", "4.096V", "2.048V", "1.024V", "0.512V", "0.256V"},
		Default: "4.096V",
	},
}

// openADC refuses a part that is not the one this kind names, so an SGM58031 is never listed as an ADS1115.
func openADC(bus i2c.Bus, addr uint16, o map[string]string, want ads1x15.Variant) (conn.Resource, error) {
	opts := ads1x15.DefaultOpts
	opts.Address = addr
	if v := o["channel"]; v != "" {
		ch, err := ads1x15.ParseChannel(v)
		if err != nil {
			return nil, err
		}
		opts.Channel = ch
	}
	if v := o["gain"]; v != "" {
		g, err := ads1x15.ParseGain(v)
		if err != nil {
			return nil, err
		}
		opts.Gain = g
	}
	d, err := ads1x15.NewI2C(bus, &opts)
	if err != nil {
		return nil, err
	}
	if d.Variant() != want {
		closeDevice(d)
		return nil, fmt.Errorf("the part at %#02x is a %s, not a %s", addr, d.Variant(), want)
	}
	return d, nil
}

// hostInit registers periph's drivers once and logs any that failed; host.Init only errors outright, so a dropped driver looks like no bus.
var hostInit = sync.OnceValue(func() error {
	st, err := host.Init()
	if err != nil {
		return err
	}
	for _, f := range st.Failed {
		slog.Warn("periph driver did not load", "component", "sensor", "driver", f.D, "error", f.Err)
	}
	slog.Info("periph host initialised", "component", "sensor",
		"loaded", len(st.Loaded), "skipped", len(st.Skipped), "failed", len(st.Failed))
	return nil
})

// StateStore is where a sensor's learned calibration lives between runs; a nil one relearns at every start.
type StateStore interface {
	// LoadState returns nil, nil when nothing is stored; an error means a row may exist that could not be read.
	LoadState(sensorID int64) ([]byte, error)
	// SaveState returns once the state is written, so a close that saves on the way out loses nothing.
	SaveState(sensorID int64, state []byte) error
}

// I2CProvider finds and opens the sensors wired to this host's I2C buses.
type I2CProvider struct {
	// Period is how often a part with an air-quality fusion samples itself, which sizes its filters, run-in and promotion count, so a wrong value is wrong readings, not just timing.
	Period time.Duration
	// State persists what a sensor has learned, so an index does not restart at "calibrating" on every reboot.
	State StateStore
}

func (I2CProvider) ID() string    { return "i2c" }
func (I2CProvider) Label() string { return "I2C bus" }

// Available separates no bus at all, a bus that cannot be opened, and a usable one.
func (I2CProvider) Available(context.Context) (bool, string) {
	if err := hostInit(); err != nil {
		return false, fmt.Sprintf("periph host init failed: %v", err)
	}
	refs := i2creg.All()
	if len(refs) == 0 {
		return false, "no I2C bus on this host; on a Pi enable it with raspi-config"
	}
	var lastErr error
	for _, ref := range refs {
		b, err := ref.Open()
		if err != nil {
			lastErr = err
			continue
		}
		b.Close()
		return true, ""
	}
	return false, fmt.Sprintf("found %d I2C bus(es) but none could be opened: %v", len(refs), lastErr)
}

// Kinds declares a bus and an address per part; the address stays free text for a multiplexer or an unlisted strap variant.
func (I2CProvider) Kinds() []KindInfo {
	_ = hostInit()
	buses := busNames()
	out := make([]KindInfo, 0, len(chips))
	for _, c := range chips {
		out = append(out, KindInfo{
			Kind: c.kind, Label: c.label,
			Description: c.describe, Category: c.category,
			Metrics: c.metrics, ReportsUnder: c.reportsUnder,
			Fields: append([]Field{
				{
					Key: "bus", Label: "Bus", Required: true,
					Choices: buses, Default: soleBus(buses),
				},
				{
					Key: "address", Label: "Address", Required: true, Identifies: true,
					Help:    "7-bit, as i2cdetect shows it",
					Default: fmt.Sprintf("%#02x", c.addrs[0]),
				},
			}, c.fields...),
		})
	}
	return out
}

// Validate checks a spec without touching the bus, so a sensor can be configured before its board is plugged in.
func (I2CProvider) Validate(spec Spec) error {
	c, ok := chipByKind(spec.Kind)
	if !ok {
		return fmt.Errorf("no I2C driver for kind %q", spec.Kind)
	}
	addr, err := parseAddress(spec.Options["address"], c.addrs[0])
	if err == nil && spec.Kind == "bme680" {
		_, err = bmeOpts(addr, spec.Options, 0)
	}
	return err
}

// busNames lists the buses by number, or /dev/i2c-10 to -19 sit between -1 and -2.
func busNames() []string {
	refs := i2creg.All()
	slices.SortFunc(refs, func(a, b *i2creg.Ref) int {
		return cmp.Or(cmp.Compare(a.Number, b.Number), strings.Compare(a.Name, b.Name))
	})
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

// soleBus defaults the bus only when the host has one: a Pi's HDMI channels sit on the same list as the header bus.
func soleBus(buses []string) string {
	if len(buses) == 1 {
		return buses[0]
	}
	return ""
}

// Discover only reads: a write, even of a register address, is a command to some parts, and what answers is unknown until the operator says.
func (p I2CProvider) Discover(ctx context.Context) ([]Candidate, error) {
	if err := hostInit(); err != nil {
		return nil, fmt.Errorf("periph host init failed: %w", err)
	}
	var out []Candidate
	var errs []error
	for _, ref := range i2creg.All() {
		bus, err := ref.Open()
		if err != nil {
			errs = append(errs, fmt.Errorf("opening %s: %w", ref.Name, err))
			continue
		}
		out = append(out, scanBus(ctx, ref.Name, bus)...)
		bus.Close()
	}
	// Returned beside what was found, since a bus that would not open otherwise reads as one with nothing on it.
	return out, errors.Join(errs...)
}

// scanBus reads a byte at every address and offers each part that could sit where something answered; nothing is identified until it is added.
func scanBus(ctx context.Context, name string, bus i2c.Bus) []Candidate {
	var out []Candidate
	for addr := uint16(0x08); addr <= 0x77; addr++ {
		if ctx.Err() != nil {
			return out
		}
		var b [1]byte
		if bus.Tx(addr, nil, b[:]) != nil {
			continue
		}
		fits := false
		for _, c := range chips {
			if c.silent || !slices.Contains(c.addrs, addr) {
				continue
			}
			fits = true
			out = append(out, Candidate{
				Kind:    c.kind,
				Label:   c.label,
				Detail:  fmt.Sprintf("%s at %#02x", name, addr),
				Addable: true,
				Options: map[string]string{"bus": name, "address": fmt.Sprintf("%#02x", addr)},
			})
		}
		// Listed unaddable, or a bus carrying a part we cannot drive looks like an empty bus.
		if !fits {
			out = append(out, Candidate{
				Label:   fmt.Sprintf("Unknown device at %#02x", addr),
				Detail:  fmt.Sprintf("%s has no driver for this part", name),
				Options: map[string]string{},
			})
		}
	}
	return out
}

func (p I2CProvider) Open(spec Spec) (Sensor, error) {
	c, ok := chipByKind(spec.Kind)
	if !ok {
		return nil, fmt.Errorf("no I2C driver for kind %q", spec.Kind)
	}
	if err := hostInit(); err != nil {
		return nil, fmt.Errorf("periph host init failed: %w", err)
	}
	addr, err := parseAddress(spec.Options["address"], c.addrs[0])
	if err != nil {
		return nil, err
	}
	bus, err := i2creg.Open(spec.Options["bus"])
	if err != nil {
		return nil, fmt.Errorf("opening I2C bus %q: %w", spec.Options["bus"], err)
	}
	dev, err := c.open(bus, addr, spec.Options, p.Period)
	if err != nil {
		bus.Close()
		return nil, err
	}
	s, err := newSensor(dev, bus, c.metrics, p.restoreAirQuality(dev, spec.ID))
	if err != nil {
		closeDevice(dev)
		bus.Close()
		return nil, err
	}
	return s, nil
}

// newSensor adapts a driver to the framework; periph measures through physic.SenseEnv or analog.PinADC, and a part is one or the other.
func newSensor(dev conn.Resource, bus i2c.BusCloser, metrics []Metric, air *airQualityState) (Sensor, error) {
	switch v := dev.(type) {
	case physic.SenseEnv:
		env := &envSensor{dev: v, bus: bus, metrics: metrics, air: air}
		if aq, ok := v.(airQualitySenser); ok && aq.SamplePeriod() > 0 {
			return sample(env, aq.SamplePeriod()), nil
		}
		return env, nil
	case analog.PinADC:
		return &adcSensor{pin: v, bus: bus}, nil
	}
	return nil, fmt.Errorf("%T is neither an environment sensor nor an ADC", dev)
}

// adcSensor reports one reading, because one configured sensor is one channel; a four-channel part is four sensors.
type adcSensor struct {
	pin analog.PinADC
	bus i2c.BusCloser
}

func (s *adcSensor) Read(context.Context) ([]Reading, error) {
	smp, err := s.pin.Read()
	if err != nil {
		return nil, err
	}
	return []Reading{{Metric: Voltage, Value: float64(smp.V) / float64(physic.Volt), Unit: "V"}}, nil
}

func (s *adcSensor) Close() error {
	closeDevice(s.pin)
	return s.bus.Close()
}

func chipByKind(kind string) (chip, bool) {
	for _, c := range chips {
		if c.kind == kind {
			return c, true
		}
	}
	return chip{}, false
}

// Claim is the chip this spec drives; two sensors on one address interleave a stateful read sequence.
func (I2CProvider) Claim(spec Spec) string {
	addr, err := parseAddress(spec.Options["address"], 0)
	if err != nil || addr == 0 {
		return "" // a spec that will not validate anyway, or a kind with no fixed address
	}
	claim := fmt.Sprintf("%s@%#02x", spec.Options["bus"], addr)
	// An ADC read sets its own input first, so each input is a sensor of its own.
	if ch := spec.Options["channel"]; ch != "" {
		claim += "/" + ch
	}
	return claim
}

// parseAddress takes the 0x70 form or a decimal, and rejects anything outside the 7-bit range.
func parseAddress(s string, fallback uint16) (uint16, error) {
	if s == "" {
		return fallback, nil
	}
	v, err := strconv.ParseUint(s, 0, 16)
	if err != nil {
		return 0, fmt.Errorf("I2C address %q is not a number", s)
	}
	if v < 0x08 || v > 0x77 {
		return 0, fmt.Errorf("I2C address %#02x is outside the 0x08..0x77 device range", v)
	}
	return uint16(v), nil
}

// gasSenser is a part measuring what physic.Env has no field for, from the Sense already taken; trend the compensated reading, as the raw one moves with the water in the air.
type gasSenser interface {
	SenseGas() (physic.ElectricResistance, bool)
	SenseGasCompensated() (physic.ElectricResistance, bool)
}

// envSensor reads out only the metrics the part measures, since physic.Env carries every quantity at once.
type envSensor struct {
	dev     physic.SenseEnv
	bus     i2c.BusCloser
	metrics []Metric
	// air persists what the part's fusion has learned; nil when nothing stores it.
	air *airQualityState
}

func (s *envSensor) Read(context.Context) ([]Reading, error) {
	var e physic.Env
	if err := s.dev.Sense(&e); err != nil {
		return nil, err
	}
	gas, _ := s.dev.(gasSenser)
	derived := s.readAirQuality()

	out := make([]Reading, 0, len(s.metrics))
	for _, m := range s.metrics {
		switch m {
		case Temperature:
			out = append(out, Reading{Metric: m, Value: e.Temperature.Celsius(), Unit: "°C"})
		case Humidity:
			out = append(out, Reading{Metric: m, Value: float64(e.Humidity) / float64(physic.PercentRH), Unit: "%"})
		case Pressure:
			out = append(out, Reading{Metric: m, Value: float64(e.Pressure) / float64(100*physic.Pascal), Unit: "hPa"})
		case Resistance, GasCompensated:
			// A part that cannot vouch for the reading reports nothing rather than a plausible number.
			if gas == nil {
				continue
			}
			r, valid := gas.SenseGas()
			label := "gas"
			if m == GasCompensated {
				r, valid = gas.SenseGasCompensated()
				label = "gas at 20 °C, 40 %RH"
			}
			if valid {
				out = append(out, Reading{Metric: m, Label: label, Value: float64(r) / float64(physic.Ohm), Unit: "Ω"})
			}
		default:
			if i := slices.IndexFunc(derived, func(r Reading) bool { return r.Metric == m }); i >= 0 {
				out = append(out, derived[i])
			}
		}
	}
	// Outside the metrics loop, as no part declares the countdown.
	if i := slices.IndexFunc(derived, func(r Reading) bool { return r.Metric == AirQualityRunInLeft }); i >= 0 {
		out = append(out, derived[i])
	}
	return out, nil
}

func (s *envSensor) Close() error {
	// Saved at close too, or a restart loses up to half an hour of what the part learned.
	if aq, ok := s.dev.(airQualitySenser); ok {
		s.air.save(aq)
	}
	closeDevice(s.dev)
	return s.bus.Close()
}

// stateSaveInterval is how often the learned calibration is written back; the bands move over hours, so an unclean shutdown loses little.
const stateSaveInterval = 30 * time.Minute

// airQualitySenser is a part that derives an air-quality index from its own gas readings and hands its calibration back for the host to keep.
type airQualitySenser interface {
	SamplePeriod() time.Duration
	SenseAirQuality() (bme680.AirQuality, bool)
	AirQualityState() ([]byte, error)
	RestoreAirQuality(state []byte) error
}

// airQualityState is where a part's calibration is kept between runs, and how often it is written.
type airQualityState struct {
	store    StateStore
	sensorID int64
	saved    time.Time
	// held stops every save after a failed load, since the row it would replace may still be good.
	held bool
}

// restoreAirQuality hands a part back what it learned before the last restart, and says where to keep what it learns next.
func (p I2CProvider) restoreAirQuality(dev conn.Resource, sensorID int64) *airQualityState {
	a, ok := dev.(airQualitySenser)
	if !ok || p.State == nil || sensorID == 0 {
		return nil
	}
	// First saved a period from now, or a part that refused the stored state writes a fresh one over it on its first read.
	st := &airQualityState{store: p.State, sensorID: sensorID, saved: time.Now()}
	b, err := p.State.LoadState(sensorID)
	if err != nil {
		st.held = true
		slog.Warn("reading a sensor's stored state; relearning, and leaving the stored state alone",
			"component", "sensor", "sensor", sensorID, "error", err)
		return st
	}
	if len(b) == 0 {
		return st
	}
	// A blob the part refuses leaves it relearning rather than trusting a number that would never look wrong again.
	if err := a.RestoreAirQuality(b); err != nil {
		slog.Warn("stored air-quality state refused, relearning from scratch",
			"component", "sensor", "sensor", sensorID, "error", err)
	}
	return st
}

// readAirQuality holds the index back at accuracy 0, where it is a placeholder or a settling plate that reads as real air.
func (s *envSensor) readAirQuality() []Reading {
	aq, ok := s.dev.(airQualitySenser)
	if !ok {
		return nil
	}
	v, valid := aq.SenseAirQuality()
	if !valid {
		return nil
	}
	s.air.persist(aq)

	runIn := 0.0
	if v.RunIn {
		runIn = 1
	}
	out := []Reading{
		{Metric: IAQAccuracy, Label: "index accuracy", Value: float64(v.Accuracy)},
		{Metric: GasPercentageAccuracy, Label: "gas percentage accuracy", Value: float64(v.GasAccuracy)},
		{Metric: AirQualityRunIn, Label: "run-in complete", Value: runIn},
	}
	if !v.RunIn {
		out = append(out, Reading{Metric: AirQualityRunInLeft, Label: "run-in left", Value: v.RunInLeft.Seconds(), Unit: "s"})
	}
	if v.Accuracy > 0 {
		out = append(out,
			Reading{Metric: IAQ, Label: "air quality index", Value: v.IAQ},
			Reading{Metric: StaticIAQ, Label: "static air quality index", Value: v.StaticIAQ},
			Reading{Metric: CO2Equivalent, Label: "CO2 equivalent", Value: v.CO2Equivalent, Unit: "ppm"},
			Reading{Metric: BreathVOC, Label: "breath VOC equivalent", Value: v.BreathVOCEquiv, Unit: "ppm"},
		)
	}
	if v.GasAccuracy > 0 {
		out = append(out, Reading{Metric: GasPercentage, Label: "gas percentage", Value: v.GasPercentage, Unit: "%"})
	}
	return out
}

// persist writes the calibration back on a slow cadence; a nil keeper means nothing stores it.
func (a *airQualityState) persist(aq airQualitySenser) {
	if a != nil && time.Since(a.saved) >= stateSaveInterval {
		a.save(aq)
	}
}

func (a *airQualityState) save(aq airQualitySenser) {
	if a == nil || a.held {
		return
	}
	b, err := aq.AirQualityState()
	if err != nil {
		slog.Warn("encoding air-quality state", "component", "sensor", "sensor", a.sensorID, "error", err)
		return
	}
	if len(b) == 0 {
		return
	}
	if err := a.store.SaveState(a.sensorID, b); err != nil {
		slog.Warn("saving a sensor's state", "component", "sensor", "sensor", a.sensorID, "error", err)
		return
	}
	a.saved = time.Now()
}

// closeDevice is where the framework's Close meets periph's Halt.
func closeDevice(dev conn.Resource) {
	if err := dev.Halt(); err != nil {
		slog.Warn("halting sensor", "component", "sensor", "error", err)
	}
}
