// Package bme680 drives the Bosch BME680 and BME688 (datasheet bst-bme680-ds001; conversions from github.com/boschsensortec/BME68x_SensorAPI): temperature, pressure, humidity and a heated gas resistance.
package bme680

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
)

// The SDO pin picks the address; a RAK1906 straps it low.
const (
	DefaultAddress = 0x76
	AltAddress     = 0x77
)

const (
	regCoeff3    = 0x00
	regField0    = 0x1D
	regResHeat0  = 0x5A
	regGasWait0  = 0x64
	regCtrlGas0  = 0x70
	regCtrlGas1  = 0x71
	regCtrlHum   = 0x72
	regCtrlMeas  = 0x74
	regConfig    = 0x75
	regCoeff1    = 0x8A
	regChipID    = 0xD0
	regSoftReset = 0xE0
	regCoeff2    = 0xE1
	regVariantID = 0xF0
)

const (
	chipID       = 0x61
	softResetCmd = 0xB6

	// variantGasHigh is the BME688, which converts gas resistance differently to the BME680.
	variantGasLow  = 0x00
	variantGasHigh = 0x01
)

const (
	maskMode      = 0x03
	maskOSTemp    = 0xE0
	maskOSPres    = 0x1C
	maskOSHum     = 0x07
	maskFilter    = 0x1C
	maskRunGas    = 0x30
	maskNBConv    = 0x0F
	maskHeaterOff = 0x08
	maskNewData   = 0x80
	maskGasRange  = 0x0F
	maskGasValid  = 0x20
	maskHeatStab  = 0x10
	maskResHeatRg = 0x30
	maskRangeErr  = 0xF0
	maskH1Nibble  = 0x0F

	posOSTemp  = 5
	posOSPres  = 2
	posFilter  = 2
	posRunGas  = 4
	modeSleep  = 0x00
	modeForced = 0x01

	// The two variants run their gas conversion from different bits of ctrl_gas_1.
	enableGasLow  = 0x01
	enableGasHigh = 0x02
)

const (
	lenCoeff1   = 23
	lenCoeff2   = 14
	lenCoeff3   = 5
	lenCoeffAll = lenCoeff1 + lenCoeff2 + lenCoeff3
	lenField    = 17
)

const (
	// resetDelay is the datasheet's start-up time after a soft reset.
	resetDelay = 10 * time.Millisecond
	// pollInterval paces the wait for a conversion, matching the reference driver.
	pollInterval = 10 * time.Millisecond
	// maxHeaterDuration is the longest the gas_wait register can encode.
	maxHeaterDuration = 4032 * time.Millisecond
	// maxHeaterTarget is where the heater calculation saturates.
	maxHeaterTarget = 400.0
	// maxWarmUpOff is where the warm-up quartic peaks; past it the curve falls, so a colder plate is treated as fully cold, not as needing less heat.
	maxWarmUpOff = 287 * time.Second
)

// The gas plate's operating envelope.
const (
	minGasOhms = 170.0
	maxGasOhms = 12_800_000.0
)

// HeaterProfile is how long the plate is heated before each gas reading.
type HeaterProfile uint8

const (
	// HeaterWarmUp sizes each heat from how long the plate has been cold: a short pulse from cold never reaches plate temperature, and reads high by an amount that moves with the gap.
	HeaterWarmUp HeaterProfile = iota
	// HeaterFixed holds the plate for HeaterDuration however long it has been off.
	HeaterFixed
	// HeaterOff takes no gas reading at all.
	HeaterOff
)

// Oversampling is how many samples the part averages into one reading.
type Oversampling uint8

const (
	OversamplingOff Oversampling = 0
	Oversampling1x  Oversampling = 1
	Oversampling2x  Oversampling = 2
	Oversampling4x  Oversampling = 3
	Oversampling8x  Oversampling = 4
	Oversampling16x Oversampling = 5
)

// samples is how many conversions the setting costs, which is what sets the measurement time.
func (o Oversampling) samples() int {
	switch o {
	case Oversampling1x:
		return 1
	case Oversampling2x:
		return 2
	case Oversampling4x:
		return 4
	case Oversampling8x:
		return 8
	case Oversampling16x:
		return 16
	default:
		return 0
	}
}

func (o Oversampling) String() string {
	if o == OversamplingOff {
		return "off"
	}
	return fmt.Sprintf("%dx", o.samples())
}

// ParseOversampling reads the forms the UI offers, so a stored option is never a raw register value.
func ParseOversampling(s string) (Oversampling, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "0":
		return OversamplingOff, nil
	case "1x", "1":
		return Oversampling1x, nil
	case "2x", "2":
		return Oversampling2x, nil
	case "4x", "4":
		return Oversampling4x, nil
	case "8x", "8":
		return Oversampling8x, nil
	case "16x", "16":
		return Oversampling16x, nil
	}
	return 0, fmt.Errorf("bme680: %q is not an oversampling setting", s)
}

// Filter is the IIR filter length, smoothing temperature and pressure only.
type Filter uint8

const (
	FilterOff   Filter = 0
	Filter1     Filter = 1
	Filter3     Filter = 2
	Filter7     Filter = 3
	Filter15    Filter = 4
	Filter31    Filter = 5
	Filter63    Filter = 6
	Filter127   Filter = 7
	filterCount        = 8
)

// Opts configures a part. The zero value is not usable; start from DefaultOpts.
type Opts struct {
	Address uint16

	Temperature Oversampling
	Pressure    Oversampling
	Humidity    Oversampling
	Filter      Filter

	// HeaterTarget is the plate temperature the gas measurement heats to, saturating at 400C.
	HeaterTarget physic.Temperature
	// Heater picks how the heating time is chosen.
	Heater HeaterProfile
	// HeaterDuration is the plate time under HeaterFixed, and must be zero under the other profiles.
	HeaterDuration time.Duration
	// TemperatureOffset is subtracted from every reading; self-heating is about 4 °C at a sample a second, 0.1 °C at one every 3 s and under 0.01 °C at one every thirty.
	TemperatureOffset physic.Temperature
	// Ambient is what the heater resistance is calculated against; nothing on the bus can tell us it.
	Ambient physic.Temperature
	// AirQuality runs the air-quality fusion over the gas readings, which needs the heater and SamplePeriod.
	AirQuality bool
	// SamplePeriod is how often the host will call Sense, which sizes the fusion's filters, run-in and promotion count.
	SamplePeriod time.Duration

	// MeasurementReadTimeout bounds the wait for a conversion to land.
	MeasurementReadTimeout time.Duration
}

// DefaultOpts is Bosch's indoor-air profile; the I2C provider overrides the oversampling.
var DefaultOpts = Opts{
	Address:     DefaultAddress,
	Temperature: Oversampling8x,
	Pressure:    Oversampling4x,
	Humidity:    Oversampling2x,
	// Off as Bosch does in forced mode, where it would average in air several samples old.
	Filter:                 FilterOff,
	HeaterTarget:           physic.ZeroCelsius + 320*physic.Kelvin,
	Heater:                 HeaterWarmUp,
	Ambient:                physic.ZeroCelsius + 25*physic.Kelvin,
	MeasurementReadTimeout: time.Second,
}

// calib is the part's factory trim, read once at start-up. Every reading is meaningless without it.
type calib struct {
	t1         uint16
	t2         int16
	t3         int8
	p1         uint16
	p2         int16
	p3         int8
	p4, p5     int16
	p6, p7     int8
	p8, p9     int16
	p10        uint8
	h1, h2     uint16
	h3, h4, h5 int8
	h6         uint8
	h7         int8
	gh1        int8
	gh2        int16
	gh3        int8

	resHeatRange uint8
	resHeatVal   int8
	rangeSwErr   int8

	// tFine carries temperature into the pressure and humidity maths, so temperature converts first.
	tFine float64
}

// BME680 is an open connection to the part.
type BME680 struct {
	dev     *i2c.Dev
	opts    Opts
	variant byte
	calib   calib
	// chip fingerprints the factory trim, which differs per part, so a learned state is only restored onto the part that learned it.
	chip string

	mu      sync.Mutex
	gas     physic.ElectricResistance
	gasComp physic.ElectricResistance
	// gasValid covers both readings: they come from the same conversion.
	gasValid bool
	// air is the fusion over the gas readings, nil with the heater off or no period given; airValid covers its outputs as gasValid covers the gas.
	air      *tracker
	airOut   AirQuality
	airValid bool
	// lastGas is when the plate was last heated, which is what the warm-up profile measures from.
	lastGas time.Time
	// heatReg is what gas_wait holds, so a warm-up that rounds to the same register is not rewritten.
	heatReg    byte
	heatRegSet bool
	stop       chan struct{}
	wg         sync.WaitGroup
}

// NewI2C opens a BME680 and verifies it really is one before returning it.
func NewI2C(bus i2c.Bus, opts *Opts) (*BME680, error) {
	o := DefaultOpts
	if opts != nil {
		o = *opts
	}
	if o.Address == 0 {
		o.Address = DefaultAddress
	}
	// Zero is absolute zero, not "unset", and would cut the heater drive by a tenth.
	if o.Ambient == 0 {
		o.Ambient = DefaultOpts.Ambient
	}
	if o.HeaterTarget == 0 {
		o.HeaterTarget = DefaultOpts.HeaterTarget
	}
	for _, os := range []Oversampling{o.Temperature, o.Pressure, o.Humidity} {
		if os > Oversampling16x {
			return nil, fmt.Errorf("bme680: oversampling %d is not one the part has", os)
		}
	}
	// All three off measures nothing and still converts, publishing compensated nonsense.
	if o.Temperature == OversamplingOff && o.Pressure == OversamplingOff && o.Humidity == OversamplingOff {
		return nil, errors.New("bme680: every channel is oversampled off, so the part would measure nothing")
	}
	// A skipped temperature compensates to a plausible room temperature and pressure and humidity derive from it, wrong in a way nothing downstream could see.
	if o.Temperature == OversamplingOff && (o.Pressure != OversamplingOff || o.Humidity != OversamplingOff) {
		return nil, errors.New("bme680: pressure and humidity are compensated through the temperature reading, which is oversampled off")
	}
	if o.Heater != HeaterOff && o.Humidity == OversamplingOff {
		return nil, errors.New("bme680: the gas reading is compensated for the humidity it was taken in, which is oversampled off")
	}
	if o.Heater > HeaterOff {
		return nil, fmt.Errorf("bme680: heater profile %d is not one this driver has", o.Heater)
	}
	switch {
	case o.Heater == HeaterFixed && o.HeaterDuration < time.Millisecond:
		return nil, fmt.Errorf("bme680: a fixed heater needs a duration of at least a millisecond, not %s", o.HeaterDuration)
	case o.Heater == HeaterFixed && o.HeaterDuration > maxHeaterDuration:
		return nil, fmt.Errorf("bme680: heater duration %s is longer than the %s the part can encode",
			o.HeaterDuration, maxHeaterDuration)
	case o.Heater != HeaterFixed && o.HeaterDuration != 0:
		return nil, fmt.Errorf("bme680: heater duration %s is set but the profile does not use it", o.HeaterDuration)
	}
	if o.Filter >= filterCount {
		return nil, fmt.Errorf("bme680: filter %d is not one of the eight the part has", o.Filter)
	}
	switch {
	case o.AirQuality && o.Heater == HeaterOff:
		return nil, errors.New("bme680: the air-quality fusion reads the gas plate, whose heater is off")
	case o.AirQuality && o.SamplePeriod <= 0:
		return nil, fmt.Errorf("bme680: the air-quality fusion is sized for the period Sense is called at, not %s", o.SamplePeriod)
	}
	if o.MeasurementReadTimeout <= 0 {
		o.MeasurementReadTimeout = DefaultOpts.MeasurementReadTimeout
	}
	d := &BME680{dev: &i2c.Dev{Bus: bus, Addr: o.Address}, opts: o}
	if o.AirQuality {
		d.air = newTracker(o.SamplePeriod)
	}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *BME680) init() error {
	// The chip id first: resetting whatever else answers here would be a write to someone else's part.
	var b [1]byte
	if err := d.read(regChipID, b[:]); err != nil {
		return fmt.Errorf("bme680: reading the chip id: %w", err)
	}
	if b[0] != chipID {
		return fmt.Errorf("bme680: chip id %#02x is not a BME680 or BME688, want %#02x", b[0], chipID)
	}
	if err := d.write(regSoftReset, softResetCmd); err != nil {
		return fmt.Errorf("bme680: soft reset: %w", err)
	}
	time.Sleep(resetDelay)

	if err := d.read(regVariantID, b[:]); err != nil {
		return fmt.Errorf("bme680: reading the variant id: %w", err)
	}
	d.variant = b[0]

	if err := d.readCalibration(); err != nil {
		return err
	}
	return d.configure()
}

// Probe reads the chip id only, so a scan never disturbs a part another handle is driving.
func Probe(bus i2c.Bus, addr uint16) error {
	dev := i2c.Dev{Bus: bus, Addr: addr}
	var b [1]byte
	if err := dev.Tx([]byte{regChipID}, b[:]); err != nil {
		return fmt.Errorf("bme680: reading the chip id at %#02x: %w", addr, err)
	}
	if b[0] != chipID {
		return fmt.Errorf("bme680: chip id %#02x at %#02x is not a BME680 or BME688", b[0], addr)
	}
	return nil
}

// IsBME688 reports whether the part is a BME688, which measures gas over a wider range.
func (d *BME680) IsBME688() bool { return d.variant == variantGasHigh }

func (d *BME680) Address() uint16 { return d.opts.Address }

// readCalibration reads the trim, which the part scatters over three register blocks.
func (d *BME680) readCalibration() error {
	var c [lenCoeffAll]byte
	for _, blk := range []struct {
		reg  byte
		into []byte
	}{
		{regCoeff1, c[:lenCoeff1]},
		{regCoeff2, c[lenCoeff1 : lenCoeff1+lenCoeff2]},
		{regCoeff3, c[lenCoeff1+lenCoeff2:]},
	} {
		if err := d.read(blk.reg, blk.into); err != nil {
			return fmt.Errorf("bme680: reading calibration at %#02x: %w", blk.reg, err)
		}
	}

	u16 := func(i int) uint16 { return binary.LittleEndian.Uint16(c[i : i+2]) }
	d.calib = calib{
		t1: u16(31), t2: int16(u16(0)), t3: int8(c[2]),
		p1: u16(4), p2: int16(u16(6)), p3: int8(c[8]),
		p4: int16(u16(10)), p5: int16(u16(12)),
		// The part stores p7 before p6, which is the one place this block is not in order.
		p7: int8(c[14]), p6: int8(c[15]),
		p8: int16(u16(18)), p9: int16(u16(20)), p10: c[22],
		// h1 and h2 share byte 24, a nibble each.
		h1: uint16(c[25])<<4 | uint16(c[24]&maskH1Nibble),
		h2: uint16(c[23])<<4 | uint16(c[24])>>4,
		h3: int8(c[26]), h4: int8(c[27]), h5: int8(c[28]), h6: c[29], h7: int8(c[30]),
		gh1: int8(c[35]), gh2: int16(u16(33)), gh3: int8(c[36]),

		resHeatVal:   int8(c[37]),
		resHeatRange: (c[39] & maskResHeatRg) >> 4,
		rangeSwErr:   int8(c[41]&maskRangeErr) / 16,
	}
	if d.calib.t1 == 0 && d.calib.t2 == 0 && d.calib.p1 == 0 {
		return errors.New("bme680: calibration read back blank, so every reading would be wrong")
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%+v", d.calib))
	d.chip = hex.EncodeToString(sum[:8])
	return nil
}

// configure runs once at start-up: these registers latch only in sleep, which a forced measurement returns to.
func (d *BME680) configure() error {
	if err := d.update(regCtrlMeas, maskMode, modeSleep); err != nil {
		return fmt.Errorf("bme680: entering sleep: %w", err)
	}
	// Humidity oversampling is taken up by the following ctrl_meas write, so this order is load-bearing.
	if err := d.update(regCtrlHum, maskOSHum, byte(d.opts.Humidity)); err != nil {
		return fmt.Errorf("bme680: setting humidity oversampling: %w", err)
	}
	if err := d.update(regConfig, maskFilter, byte(d.opts.Filter)<<posFilter); err != nil {
		return fmt.Errorf("bme680: setting the IIR filter: %w", err)
	}
	osrs := byte(d.opts.Temperature)<<posOSTemp | byte(d.opts.Pressure)<<posOSPres
	if err := d.update(regCtrlMeas, maskOSTemp|maskOSPres, osrs); err != nil {
		return fmt.Errorf("bme680: setting oversampling: %w", err)
	}
	return d.configureHeater()
}

func (d *BME680) configureHeater() error {
	if !d.gasEnabled() {
		if err := d.update(regCtrlGas1, maskRunGas, 0); err != nil {
			return fmt.Errorf("bme680: disabling the gas measurement: %w", err)
		}
		return d.update(regCtrlGas0, maskHeaterOff, maskHeaterOff)
	}
	target := min(d.opts.HeaterTarget.Celsius(), maxHeaterTarget)
	if err := d.write(regResHeat0, d.calib.resHeat(target, d.opts.Ambient.Celsius())); err != nil {
		return fmt.Errorf("bme680: setting the heater resistance: %w", err)
	}
	if _, err := d.setHeatDuration(d.heatDuration()); err != nil {
		return err
	}
	if err := d.update(regCtrlGas0, maskHeaterOff, 0); err != nil {
		return fmt.Errorf("bme680: enabling the heater: %w", err)
	}
	// Profile 0 is the only one this driver writes, so nb_conv selects it.
	if err := d.update(regCtrlGas1, maskRunGas|maskNBConv, d.runGas()); err != nil {
		return fmt.Errorf("bme680: enabling the gas measurement: %w", err)
	}
	return nil
}

func (d *BME680) gasEnabled() bool { return d.opts.Heater != HeaterOff }

// heatDuration is how long this reading holds the plate; a part never measured, or idle past the model's range, is treated as fully cold.
func (d *BME680) heatDuration() time.Duration {
	switch d.opts.Heater {
	case HeaterFixed:
		return d.opts.HeaterDuration
	case HeaterOff:
		return 0
	}
	off := maxWarmUpOff
	if !d.lastGas.IsZero() {
		off = time.Since(d.lastGas)
	}
	return warmUp(off)
}

// warmUp is the library's model of how long a plate off for this long takes to reach temperature: 446 ms from hot, 855 ms after half a minute, 1858 ms from cold.
func warmUp(off time.Duration) time.Duration {
	if off < 0 || off > maxWarmUpOff {
		off = maxWarmUpOff
	}
	x := off.Seconds()
	y := ((((-4.92979491e-10*x+3.62989311e-07)*x-1.03665916e-04)*x+1.64253004e-02)*x + 4.46429551e-01)
	return time.Duration(math.Round(y*1000)) * time.Millisecond
}

// setHeatDuration writes gas_wait when the encoded byte changes and returns the time the part will really heat for, which the register rounds.
func (d *BME680) setHeatDuration(dur time.Duration) (time.Duration, error) {
	reg := gasWait(dur)
	if !d.heatRegSet || reg != d.heatReg {
		if err := d.write(regGasWait0, reg); err != nil {
			return 0, fmt.Errorf("bme680: setting the heater duration: %w", err)
		}
		d.heatReg, d.heatRegSet = reg, true
	}
	return gasWaitDuration(reg), nil
}

// runGas is the enable the part's variant wants, already in place for ctrl_gas_1.
func (d *BME680) runGas() byte {
	if d.variant == variantGasHigh {
		return enableGasHigh << posRunGas
	}
	return enableGasLow << posRunGas
}

// measureDuration is the reference driver's timing model: equal cost per oversampled conversion plus fixed overheads.
func (d *BME680) measureDuration(heat time.Duration) time.Duration {
	cycles := d.opts.Temperature.samples() + d.opts.Pressure.samples() + d.opts.Humidity.samples()
	us := cycles*1963 + 477*4 + 477*5 + 1000
	return time.Duration(us)*time.Microsecond + heat
}

// Sense triggers one forced measurement and converts it.
func (d *BME680) Sense(e *physic.Env) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sense(e)
}

func (d *BME680) sense(e *physic.Env) error {
	reg, err := d.toSleep()
	if err != nil {
		return err
	}
	// Resized in sleep with the rest of the profile: these registers do not latch while converting.
	heat := time.Duration(0)
	if d.gasEnabled() {
		if heat, err = d.setHeatDuration(d.heatDuration()); err != nil {
			return err
		}
	}
	if err := d.write(regCtrlMeas, (reg&^maskMode)|modeForced); err != nil {
		return fmt.Errorf("bme680: starting a measurement: %w", err)
	}
	if d.gasEnabled() {
		// Set before the wait, not after: the plate is hot from now, and a slow bus read is not warm-up.
		d.lastGas = time.Now()
	}
	time.Sleep(d.measureDuration(heat))
	if err := d.awaitSleep(); err != nil {
		return err
	}

	var f [lenField]byte
	if err := d.read(regField0, f[:]); err != nil {
		return fmt.Errorf("bme680: reading the measurement: %w", err)
	}
	if f[0]&maskNewData == 0 {
		return errors.New("bme680: the part finished with no new data")
	}
	d.convert(f[:], e)
	return nil
}

// toSleep returns ctrl_meas from sleep, since writing forced into a register already at forced converts the previous field.
func (d *BME680) toSleep() (byte, error) {
	deadline := time.Now().Add(d.opts.MeasurementReadTimeout)
	for {
		var b [1]byte
		if err := d.read(regCtrlMeas, b[:]); err != nil {
			return 0, fmt.Errorf("bme680: reading the operating mode: %w", err)
		}
		if b[0]&maskMode == modeSleep {
			return b[0], nil
		}
		if err := d.write(regCtrlMeas, b[0]&^maskMode); err != nil {
			return 0, fmt.Errorf("bme680: returning to sleep: %w", err)
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("bme680: will not leave mode %#02x", b[0]&maskMode)
		}
		time.Sleep(pollInterval)
	}
}

// awaitSleep waits on the mode bits, not new-data: new-data is still set from the last measurement.
func (d *BME680) awaitSleep() error {
	deadline := time.Now().Add(d.opts.MeasurementReadTimeout)
	for {
		var b [1]byte
		if err := d.read(regCtrlMeas, b[:]); err != nil {
			return fmt.Errorf("bme680: reading the operating mode: %w", err)
		}
		if b[0]&maskMode == modeSleep {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bme680: still measuring after %s", d.opts.MeasurementReadTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// SenseGas reports the most recent Sense's gas resistance and whether the part vouched for it; it does not measure.
func (d *BME680) SenseGas() (physic.ElectricResistance, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gas, d.gasValid
}

// SenseGasCompensated is the reading normalised to 20 °C and 40 %RH, the one to compare over time; SenseGas is what the plate read in the air of the moment.
func (d *BME680) SenseGasCompensated() (physic.ElectricResistance, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gasComp, d.gasValid
}

// SenseAirQuality reports the index from the last Sense without measuring; false means no fusion, or a gas reading the part would not vouch for.
func (d *BME680) SenseAirQuality() (AirQuality, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.airOut, d.airValid
}

// AirQualityState is the fusion's learned calibration for the host to keep, nil without a fusion; without it a restart calibrates again for hours.
func (d *BME680) AirQualityState() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.air == nil {
		return nil, nil
	}
	return d.air.marshalState(d.chip)
}

// SamplePeriod is how often the fusion needs Sense called, and zero without one, when any rate will do.
func (d *BME680) SamplePeriod() time.Duration {
	if d.air == nil {
		return 0
	}
	return d.opts.SamplePeriod
}

// RestoreAirQuality hands back an earlier run's calibration; a part with no fusion says so rather than succeed at nothing.
func (d *BME680) RestoreAirQuality(state []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.air == nil {
		return errors.New("bme680: no air-quality fusion is running, so there is nothing to restore into")
	}
	if err := d.air.restoreState(state, d.chip); err != nil {
		return err
	}
	// Reopened, the part was reset and its plate reads off for minutes; BSEC's hosts run in again because their clock restarts.
	d.air.restartRunIn()
	return nil
}

// SenseContinuous repeats the forced measurement, so every sample is one the caller asked for; the first failed read closes the channel.
func (d *BME680) SenseContinuous(interval time.Duration) (<-chan physic.Env, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("bme680: sensing interval %s is not positive", interval)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return nil, errors.New("bme680: already sensing continuously")
	}

	sensing := make(chan physic.Env)
	stop := make(chan struct{})
	d.stop = stop
	d.wg.Go(func() {
		defer close(sensing)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				var e physic.Env
				d.mu.Lock()
				err := d.sense(&e)
				d.mu.Unlock()
				if err != nil {
					return
				}
				select {
				case sensing <- e:
				case <-stop:
					return
				}
			}
		}
	})
	return sensing, nil
}

// Precision returns the part's output resolution, from the datasheet.
func (d *BME680) Precision(e *physic.Env) {
	e.Temperature = physic.Temperature(math.Round(float64(physic.Kelvin) / 100))
	e.Pressure = physic.Pressure(math.Round(float64(physic.Pascal) * 0.18))
	e.Humidity = physic.RelativeHumidity(math.Round(float64(physic.PercentRH) * 0.008))
}

// Halt stops any continuous sensing and returns the part to sleep with its heater off.
func (d *BME680) Halt() error {
	d.mu.Lock()
	if d.stop != nil {
		close(d.stop)
		d.stop = nil
	}
	d.mu.Unlock()
	d.wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.update(regCtrlGas1, maskRunGas, 0); err != nil {
		return fmt.Errorf("bme680: stopping the gas measurement: %w", err)
	}
	if err := d.update(regCtrlGas0, maskHeaterOff, maskHeaterOff); err != nil {
		return fmt.Errorf("bme680: turning the heater off: %w", err)
	}
	if err := d.update(regCtrlMeas, maskMode, modeSleep); err != nil {
		return fmt.Errorf("bme680: returning to sleep: %w", err)
	}
	return nil
}

// String implements conn.Resource.
func (d *BME680) String() string {
	name := "BME680"
	if d.variant == variantGasHigh {
		name = "BME688"
	}
	return fmt.Sprintf("%s{%s, %#02x}", name, d.dev.Bus, d.opts.Address)
}

// convert turns one raw field into an Env, temperature first because its intermediate feeds the others.
func (d *BME680) convert(f []byte, e *physic.Env) {
	adcPres := uint32(f[2])<<12 | uint32(f[3])<<4 | uint32(f[4])>>4
	adcTemp := uint32(f[5])<<12 | uint32(f[6])<<4 | uint32(f[7])>>4
	adcHum := uint16(f[8])<<8 | uint16(f[9])

	tC := d.calib.temperature(adcTemp)
	rh := d.calib.humidity(adcHum)
	if off := float64(d.opts.TemperatureOffset) / float64(physic.Kelvin); off != 0 {
		// Humidity moves with the offset: the water content did not change, only the saturation point.
		rh = humidityAt(rh, tC, tC-off)
		tC -= off
	}
	e.Temperature = physic.ZeroCelsius + physic.Temperature(math.Round(tC*float64(physic.Kelvin)))
	e.Pressure = physic.Pressure(math.Round(d.calib.pressure(adcPres) * float64(physic.Pascal)))
	e.Humidity = physic.RelativeHumidity(math.Round(rh * float64(physic.PercentRH)))

	d.gas, d.gasValid = d.gasFrom(f)
	d.gasComp = 0
	d.airValid = false
	// A reading the part will not vouch for is wrong, not missing, so neither the compensated resistance nor the fusion may see it.
	if d.gasValid {
		ohms := float64(d.gas) / float64(physic.Ohm)
		d.gasComp = physic.ElectricResistance(math.Round(compensateGas(ohms, tC, rh) * float64(physic.Ohm)))
		if d.air != nil {
			// Stamped at the trigger, as BSEC's host stamps it, not after a heat and a wait of varying length.
			d.airOut, d.airValid = d.air.observe(ohms, tC, rh, d.lastGas), true
		}
	}
}

// gasFrom refuses a reading the part did not mark valid and stable: that is wrong, not missing.
func (d *BME680) gasFrom(f []byte) (physic.ElectricResistance, bool) {
	if !d.gasEnabled() {
		return 0, false
	}
	msb, lsb := f[13], f[14]
	if d.variant == variantGasHigh {
		msb, lsb = f[15], f[16]
	}
	if lsb&maskGasValid == 0 || lsb&maskHeatStab == 0 {
		return 0, false
	}
	adc := uint16(msb)<<2 | uint16(lsb)>>6
	rng := lsb & maskGasRange
	ohms := d.calib.gasLow(adc, rng)
	if d.variant == variantGasHigh {
		ohms = gasHigh(adc, rng)
	}
	// A conversion can land outside what the plate can read, an artefact rather than a measurement: the low variant reaches 13 MΩ from a zero reading.
	if ohms < minGasOhms || ohms > maxGasOhms {
		return 0, false
	}
	return physic.ElectricResistance(math.Round(ohms * float64(physic.Ohm))), true
}

// humidityAt moves a humidity reading to the surrounding air's temperature by the ratio of saturation vapour pressures.
func humidityAt(rh, from, to float64) float64 {
	return math.Min(100.0, math.Max(0.0, rh*saturationPressure(from)/saturationPressure(to)))
}

// saturationPressure is the Magnus-Tetens approximation over water, in hPa.
func saturationPressure(tC float64) float64 {
	return 6.112 * math.Exp(17.62*tC/(243.12+tC))
}

func (d *BME680) read(reg byte, into []byte) error {
	return d.dev.Tx([]byte{reg}, into)
}

func (d *BME680) write(reg, value byte) error {
	return d.dev.Tx([]byte{reg, value}, nil)
}

// update rewrites only the bits mask covers, leaving the rest of the register as the part left it.
func (d *BME680) update(reg, mask, value byte) error {
	var b [1]byte
	if err := d.read(reg, b[:]); err != nil {
		return err
	}
	return d.write(reg, (b[0]&^mask)|(value&mask))
}

// gasWait encodes a heater duration into the register's 6-bit value plus a 2-bit multiplier.
func gasWait(dur time.Duration) byte {
	ms := dur.Milliseconds()
	if ms >= 0xFC0 {
		return 0xFF
	}
	factor := int64(0)
	for ms > 0x3F {
		ms /= 4
		factor++
	}
	return byte(ms + factor*64)
}

// gasWaitDuration is what a gas_wait byte means, since the encoding rounds to a 6-bit value times a power of four.
func gasWaitDuration(reg byte) time.Duration {
	ms := int64(reg&0x3F) << (2 * (reg >> 6))
	return time.Duration(ms) * time.Millisecond
}

func (c *calib) temperature(adc uint32) float64 {
	var1 := (float64(adc)/16384.0 - float64(c.t1)/1024.0) * float64(c.t2)
	v := float64(adc)/131072.0 - float64(c.t1)/8192.0
	var2 := v * v * float64(c.t3) * 16.0
	c.tFine = var1 + var2
	return c.tFine / 5120.0
}

// pressure returns Pascal.
func (c *calib) pressure(adc uint32) float64 {
	var1 := c.tFine/2.0 - 64000.0
	var2 := var1 * var1 * (float64(c.p6) / 131072.0)
	var2 += var1 * float64(c.p5) * 2.0
	var2 = var2/4.0 + float64(c.p4)*65536.0
	var1 = (float64(c.p3)*var1*var1/16384.0 + float64(c.p2)*var1) / 524288.0
	var1 = (1.0 + var1/32768.0) * float64(c.p1)
	pres := 1048576.0 - float64(adc)
	if int(var1) == 0 {
		return 0
	}
	pres = (pres - var2/4096.0) * 6250.0 / var1
	v1 := float64(c.p9) * pres * pres / 2147483648.0
	v2 := pres * (float64(c.p8) / 32768.0)
	v3 := (pres / 256.0) * (pres / 256.0) * (pres / 256.0) * (float64(c.p10) / 131072.0)
	return pres + (v1+v2+v3+float64(c.p7)*128.0)/16.0
}

// humidity returns percent relative humidity.
func (c *calib) humidity(adc uint16) float64 {
	t := c.tFine / 5120.0
	var1 := float64(adc) - (float64(c.h1)*16.0 + (float64(c.h3)/2.0)*t)
	var2 := var1 * (float64(c.h2) / 262144.0) *
		(1.0 + (float64(c.h4)/16384.0)*t + (float64(c.h5)/1048576.0)*t*t)
	var3 := float64(c.h6) / 16384.0
	var4 := float64(c.h7) / 2097152.0
	rh := var2 + (var3+var4*t)*var2*var2
	return math.Min(100.0, math.Max(0.0, rh))
}

// lookupK1 and lookupK2 are the reference driver's per-range corrections for the BME680's gas ADC.
var (
	lookupK1 = [16]float64{0, 0, 0, 0, 0, -1, 0, -0.8, 0, 0, -0.2, -0.5, 0, -1, 0, 0}
	lookupK2 = [16]float64{0, 0, 0, 0, 0.1, 0.7, 0, -0.8, -0.1, 0, 0, 0, 0, 0, 0, 0}
)

// gasLow returns ohms for a BME680.
func (c *calib) gasLow(adc uint16, rng byte) float64 {
	var1 := 1340.0 + 5.0*float64(c.rangeSwErr)
	var2 := var1 * (1.0 + lookupK1[rng]/100.0)
	var3 := 1.0 + lookupK2[rng]/100.0
	return 1.0 / (var3 * 0.000000125 * float64(uint32(1)<<rng) * ((float64(adc)-512.0)/var2 + 1.0))
}

// gasHigh returns ohms for a BME688, which needs no trim.
func gasHigh(adc uint16, rng byte) float64 {
	return 1000000.0 * float64(uint32(262144)>>rng) / float64(4096+(int32(adc)-512)*3)
}

// resHeat converts a target plate temperature into the drive the heater register wants.
func (c *calib) resHeat(target, ambient float64) byte {
	var1 := float64(c.gh1)/16.0 + 49.0
	var2 := (float64(c.gh2)/32768.0)*0.0005 + 0.00235
	var3 := float64(c.gh3) / 1024.0
	var4 := var1 * (1.0 + var2*target)
	var5 := var4 + var3*ambient
	drive := 3.4 * ((var5 * (4.0 / (4.0 + float64(c.resHeatRange))) *
		(1.0 / (1.0 + float64(c.resHeatVal)*0.002))) - 25.0)
	// Go leaves a float to byte conversion outside the range implementation-defined, and we ship on arm.
	return byte(math.Min(255, math.Max(0, drive)))
}

// Probe must keep the signature the sensor framework's chip table expects.
var _ func(i2c.Bus, uint16) error = Probe

var _ physic.SenseEnv = &BME680{}
var _ conn.Resource = &BME680{}
