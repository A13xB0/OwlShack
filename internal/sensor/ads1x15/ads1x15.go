// Package ads1x15 drives the TI ADS1115 and the SGM58031 sold as compatible, which share a register map but not their timing (datasheets: https://www.ti.com/lit/ds/symlink/ads1115.pdf, https://www.sg-micro.com/product/SGM58031).
package ads1x15

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/analog"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/pin"
)

// The ADDR pin selects one of four addresses.
const (
	DefaultAddress = 0x48
	MaxAddress     = 0x4B
)

const (
	regConversion = 0x00
	regConfig     = 0x01
	regLoThresh   = 0x02
	regHiThresh   = 0x03
	// regChipID exists only on the SGM58031; an ADS1115's two-bit pointer aliases it to regConfig.
	regChipID = 0x05
)

const (
	// bitOS starts a conversion when written and reads 1 only when the part is idle.
	bitOS = 0x8000

	maskMux = 0x7000
	posMux  = 12
	maskPGA = 0x0E00
	posPGA  = 9
	// bitSingleShot is the mode bit: set means one conversion then power down.
	bitSingleShot = 0x0100
	maskDataRate  = 0x00E0
	posDataRate   = 5
	// compQueDisable releases the ALERT/RDY pin, which nothing here uses.
	compQueDisable = 0x0003

	// chipIDReserved are the bits the SGM58031 documents as unused in its Chip_ID register.
	chipIDReserved = 0xE01F
)

const (
	// pollInterval only keeps a fast bus from hammering the part; the config read already costs half a millisecond.
	pollInterval = 200 * time.Microsecond

	// defaultTimeout covers the slowest case: an SGM58031 at 6.25Hz takes three periods, 480ms.
	defaultTimeout = time.Second
)

// Variant is which of the two parts answered.
type Variant uint8

// The zero Variant is no part, so a Dev that never identified one cannot pass for an ADS1115.
const (
	// ADS1115 cannot identify itself, so it is what this driver concludes when it finds no SGM58031.
	ADS1115 Variant = iota + 1
	// SGM58031 has seven pointer registers including a Chip_ID, so it says what it is.
	SGM58031
)

func (v Variant) String() string {
	switch v {
	case ADS1115:
		return "ADS1115"
	case SGM58031:
		return "SGM58031"
	}
	return fmt.Sprintf("Variant(%d)", uint8(v))
}

// Channel is an input selection: one pin against ground, or a documented differential pair.
type Channel uint8

const (
	Diff01   Channel = 0
	Diff03   Channel = 1
	Diff13   Channel = 2
	Diff23   Channel = 3
	Channel0 Channel = 4
	Channel1 Channel = 5
	Channel2 Channel = 6
	Channel3 Channel = 7
)

func (c Channel) String() string {
	switch c {
	case Diff01:
		return "A0-A1"
	case Diff03:
		return "A0-A3"
	case Diff13:
		return "A1-A3"
	case Diff23:
		return "A2-A3"
	default:
		return fmt.Sprintf("A%d", c-Channel0)
	}
}

// Gain is the programmable amplifier's full-scale range. Both parts use the same six settings.
type Gain uint8

const (
	Gain6V144 Gain = 0
	Gain4V096 Gain = 1
	Gain2V048 Gain = 2
	Gain1V024 Gain = 3
	Gain0V512 Gain = 4
	Gain0V256 Gain = 5
)

// FullScale is the voltage that reads as full deflection.
func (g Gain) FullScale() physic.ElectricPotential {
	switch g {
	case Gain6V144:
		return 6144 * physic.MilliVolt
	case Gain4V096:
		return 4096 * physic.MilliVolt
	case Gain2V048:
		return 2048 * physic.MilliVolt
	case Gain1V024:
		return 1024 * physic.MilliVolt
	case Gain0V512:
		return 512 * physic.MilliVolt
	default:
		return 256 * physic.MilliVolt
	}
}

func (g Gain) String() string { return g.FullScale().String() }

// dataRates are the eight DR settings each part offers; they differ at every code, which is why a conversion is waited for.
var dataRates = map[Variant][8]physic.Frequency{
	ADS1115: {
		8 * physic.Hertz, 16 * physic.Hertz, 32 * physic.Hertz, 64 * physic.Hertz,
		128 * physic.Hertz, 250 * physic.Hertz, 475 * physic.Hertz, 860 * physic.Hertz,
	},
	SGM58031: {
		6250 * physic.MilliHertz, 12500 * physic.MilliHertz, 25 * physic.Hertz, 50 * physic.Hertz,
		100 * physic.Hertz, 200 * physic.Hertz, 400 * physic.Hertz, 800 * physic.Hertz,
	},
}

// Opts configures one input. The zero value is not usable; start from DefaultOpts.
type Opts struct {
	Address uint16
	Channel Channel
	Gain    Gain
	// DataRate is the sample rate to ask for; the nearest the detected part supports is used.
	DataRate physic.Frequency
	// ConversionTimeout bounds the wait for a conversion.
	ConversionTimeout time.Duration
}

// DefaultOpts suits a 3.3V signal; the part's own 2.048V default would clip one, which is a wrong reading rather than an error.
var DefaultOpts = Opts{
	Address:           DefaultAddress,
	Channel:           Channel0,
	Gain:              Gain4V096,
	DataRate:          128 * physic.Hertz,
	ConversionTimeout: defaultTimeout,
}

// Dev is one input; two Devs on one part must not be read concurrently, so Read verifies the config survived.
type Dev struct {
	c       *i2c.Dev
	opts    Opts
	variant Variant
	drCode  uint16

	mu sync.Mutex
}

// NewI2C opens one input and works out which of the two parts it is talking to.
func NewI2C(bus i2c.Bus, opts *Opts) (*Dev, error) {
	o := DefaultOpts
	if opts != nil {
		o = *opts
	}
	if o.Address == 0 {
		o.Address = DefaultAddress
	}
	if o.Address < DefaultAddress || o.Address > MaxAddress {
		return nil, fmt.Errorf("ads1x15: address %#02x is outside the %#02x..%#02x the ADDR pin selects",
			o.Address, DefaultAddress, MaxAddress)
	}
	if o.Channel > Channel3 {
		return nil, fmt.Errorf("ads1x15: channel %d is not one of the eight inputs", o.Channel)
	}
	if o.Gain > Gain0V256 {
		return nil, fmt.Errorf("ads1x15: gain %d is not one of the six ranges", o.Gain)
	}
	if o.ConversionTimeout <= 0 {
		o.ConversionTimeout = defaultTimeout
	}
	if o.DataRate <= 0 {
		o.DataRate = DefaultOpts.DataRate
	}

	d := &Dev{c: &i2c.Dev{Bus: bus, Addr: o.Address}, opts: o}
	v, err := d.detect()
	if err != nil {
		return nil, err
	}
	d.variant = v
	d.drCode = nearestDataRate(v, o.DataRate)
	return d, nil
}

// detect reads only: TI's two-bit pointer answers a Chip_ID read with the config register, and comparing the two is the test.
func (d *Dev) detect() (Variant, error) {
	cfg, err := d.read16(regConfig)
	if err != nil {
		return 0, fmt.Errorf("ads1x15: reading the config register at %#02x: %w", d.opts.Address, err)
	}
	id, err := d.read16(regChipID)
	if err != nil {
		return 0, fmt.Errorf("ads1x15: reading the chip id at %#02x: %w", d.opts.Address, err)
	}
	if id != cfg && id&chipIDReserved == 0 {
		return SGM58031, nil
	}
	if id != cfg {
		return 0, fmt.Errorf("ads1x15: %#02x answers but register 5 reads %#04x, which is neither the "+
			"config register an ADS1115 aliases nor a valid SGM58031 chip id", d.opts.Address, id)
	}
	// A stuck bus also aliases, so check the thresholds, the only other known reset values.
	lo, err := d.read16(regLoThresh)
	if err != nil {
		return 0, err
	}
	hi, err := d.read16(regHiThresh)
	if err != nil {
		return 0, err
	}
	if lo == cfg && hi == cfg {
		return 0, fmt.Errorf("ads1x15: %#02x echoes %#04x for every register, so it is not an ADS1115", d.opts.Address, cfg)
	}
	return ADS1115, nil
}

// Probe finds an SGM58031 only: an ADS1115 has no identity register, and 0x48 is also the LM75 and TMP102.
func Probe(bus i2c.Bus, addr uint16) error {
	d := &Dev{c: &i2c.Dev{Bus: bus, Addr: addr}, opts: Opts{Address: addr}}
	v, err := d.detect()
	if err != nil {
		return err
	}
	if v != SGM58031 {
		return fmt.Errorf("ads1x15: the part at %#02x is a %s, which cannot prove what it is", addr, v)
	}
	return nil
}

// nearestDataRate picks the closest rate the part offers, so 128Hz on an SGM58031 gets its 100Hz.
func nearestDataRate(v Variant, want physic.Frequency) uint16 {
	rates := dataRates[v]
	best, bestDiff := 0, physic.Frequency(-1)
	for i, r := range rates {
		diff := r - want
		if diff < 0 {
			diff = -diff
		}
		if bestDiff < 0 || diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	return uint16(best)
}

// DataRate is the rate actually in use, which may not be the one asked for.
func (d *Dev) DataRate() physic.Frequency { return dataRates[d.variant][d.drCode] }

func (d *Dev) Variant() Variant { return d.variant }

// config is the register value that selects this input and starts a conversion.
func (d *Dev) config() uint16 {
	return bitOS |
		uint16(d.opts.Channel)<<posMux |
		uint16(d.opts.Gain)<<posPGA |
		bitSingleShot |
		d.drCode<<posDataRate |
		compQueDisable
}

func (d *Dev) Read() (analog.Sample, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cfg := d.config()
	if err := d.write16(regConfig, cfg); err != nil {
		return analog.Sample{}, fmt.Errorf("ads1x15: starting a conversion: %w", err)
	}
	if err := d.awaitConversion(); err != nil {
		return analog.Sample{}, err
	}
	// One conversion register for the whole part: a second Dev reconfiguring it mid-flight would hand us its channel silently.
	back, err := d.read16(regConfig)
	if err != nil {
		return analog.Sample{}, fmt.Errorf("ads1x15: confirming the conversion: %w", err)
	}
	if back&^bitOS != cfg&^bitOS {
		return analog.Sample{}, fmt.Errorf("ads1x15: the config changed from %#04x to %#04x during the "+
			"conversion, so the result is another input's", cfg&^bitOS, back&^bitOS)
	}
	raw, err := d.read16(regConversion)
	if err != nil {
		return analog.Sample{}, fmt.Errorf("ads1x15: reading the conversion: %w", err)
	}
	return d.sample(int16(raw)), nil
}

// awaitConversion polls OS: a computed delay is three times too short for an SGM58031, and reading early returns the previous conversion.
func (d *Dev) awaitConversion() error {
	deadline := time.Now().Add(d.opts.ConversionTimeout)
	for {
		v, err := d.read16(regConfig)
		if err != nil {
			return fmt.Errorf("ads1x15: polling for the conversion: %w", err)
		}
		if v&bitOS != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ads1x15: %s still converting after %s at %s",
				d.variant, d.opts.ConversionTimeout, d.DataRate())
		}
		time.Sleep(pollInterval)
	}
}

// sample scales a raw count to volts. Full scale is the gain's range at 2^15 counts on both parts.
func (d *Dev) sample(raw int16) analog.Sample {
	return analog.Sample{
		Raw: int32(raw),
		V:   physic.ElectricPotential(raw) * d.opts.Gain.FullScale() / (1 << 15),
	}
}

// Range implements analog.PinADC.
func (d *Dev) Range() (analog.Sample, analog.Sample) {
	fs := d.opts.Gain.FullScale()
	return analog.Sample{Raw: -1 << 15, V: -fs}, analog.Sample{Raw: 1<<15 - 1, V: fs}
}

// Halt powers the part down; single-shot already does that, so this only matters after continuous mode.
func (d *Dev) Halt() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg := d.config() &^ bitOS
	if err := d.write16(regConfig, cfg); err != nil {
		return fmt.Errorf("ads1x15: powering down: %w", err)
	}
	return nil
}

func (d *Dev) Name() string     { return fmt.Sprintf("%s/%s", d.variant, d.opts.Channel) }
func (d *Dev) Number() int      { return int(d.opts.Channel) }
func (d *Dev) Function() string { return "ADC" }

func (d *Dev) String() string {
	return fmt.Sprintf("%s{%s, %#02x, %s, %s}", d.variant, d.c.Bus, d.opts.Address, d.opts.Channel, d.opts.Gain)
}

func (d *Dev) read16(reg byte) (uint16, error) {
	var b [2]byte
	if err := d.c.Tx([]byte{reg}, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b[:]), nil
}

func (d *Dev) write16(reg byte, v uint16) error {
	var b [3]byte
	b[0] = reg
	binary.BigEndian.PutUint16(b[1:], v)
	return d.c.Tx(b[:], nil)
}

// Probe must keep the signature the sensor framework's chip table expects.
var _ func(i2c.Bus, uint16) error = Probe

var (
	_ analog.PinADC = &Dev{}
	_ pin.Pin       = &Dev{}
	_ conn.Resource = &Dev{}
)

// ParseChannel reads the forms the UI offers, so a stored option is never a raw MUX code.
func ParseChannel(s string) (Channel, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "A0":
		return Channel0, nil
	case "A1":
		return Channel1, nil
	case "A2":
		return Channel2, nil
	case "A3":
		return Channel3, nil
	case "A0-A1":
		return Diff01, nil
	case "A0-A3":
		return Diff03, nil
	case "A1-A3":
		return Diff13, nil
	case "A2-A3":
		return Diff23, nil
	}
	return 0, fmt.Errorf("ads1x15: %q is not an input; use A0..A3 or a documented pair such as A0-A1", s)
}

// ParseGain reads the full-scale range as the UI shows it.
func ParseGain(s string) (Gain, error) {
	switch strings.TrimSuffix(strings.TrimSpace(s), "V") {
	case "6.144":
		return Gain6V144, nil
	case "4.096":
		return Gain4V096, nil
	case "2.048":
		return Gain2V048, nil
	case "1.024":
		return Gain1V024, nil
	case "0.512":
		return Gain0V512, nil
	case "0.256":
		return Gain0V256, nil
	}
	return 0, fmt.Errorf("ads1x15: %q is not one of the six full-scale ranges", s)
}
