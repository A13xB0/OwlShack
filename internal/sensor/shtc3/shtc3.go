// Package shtc3 drives the Sensirion SHTC3 humidity and temperature sensor (datasheet: https://sensirion.com/resource/datasheet/shtc3).
package shtc3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
)

// DefaultAddress is the SHTC3's fixed I2C address; the part has no address pin.
const DefaultAddress = 0x70

const (
	cmdWake           = 0x3517
	cmdSleep          = 0xB098
	cmdSoftReset      = 0x805D
	cmdReadIDRegister = 0xEFC8

	// Temperature first, no clock stretching: stretching a 12ms conversion trips host I2C bugs.
	cmdMeasureNormal   = 0x7866
	cmdMeasureLowPower = 0x609C
)

// Conversion times from the datasheet's maximum column, rounded up.
const (
	conversionNormal   = 13 * time.Millisecond
	conversionLowPower = 1 * time.Millisecond

	// wakeDelay is the datasheet's tPU, wake command to addressable.
	wakeDelay = 240 * time.Microsecond
	// resetDelay covers tPU after a soft reset.
	resetDelay = 500 * time.Microsecond

	// retryInterval paces the re-read while a conversion finishes.
	retryInterval = time.Millisecond
)

// 0x70 is also the PCA9548A multiplexer's default address, so this check is load-bearing.
const (
	idMask  = 0x083F
	idValue = 0x0807
)

type Opts struct {
	// Address is the device address; zero means DefaultAddress.
	Address uint16
	// LowPower trades repeatability for a 1ms conversion instead of 13ms.
	LowPower bool
	// MeasurementReadTimeout bounds the wait for a conversion; zero is DefaultOpts'.
	MeasurementReadTimeout time.Duration
}

// DefaultOpts holds the default configuration options for the device.
var DefaultOpts = Opts{
	LowPower:               false,
	MeasurementReadTimeout: 100 * time.Millisecond,
}

type SHTC3 struct {
	dev  i2c.Dev
	opts Opts

	// id is the whole identity the part has: no die revision, no serial number.
	id uint16

	mu   sync.Mutex
	stop chan struct{}
	wg   sync.WaitGroup
}

// NewI2C returns a SHTC3 on bus, rejecting a part whose ID register is not an SHTC3's.
func NewI2C(bus i2c.Bus, opts *Opts) (*SHTC3, error) {
	if opts == nil {
		opts = &DefaultOpts
	}
	addr := opts.Address
	if addr == 0 {
		addr = DefaultAddress
	}
	d := &SHTC3{dev: i2c.Dev{Bus: bus, Addr: addr}, opts: *opts}
	if d.opts.MeasurementReadTimeout <= 0 {
		d.opts.MeasurementReadTimeout = DefaultOpts.MeasurementReadTimeout
	}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *SHTC3) init() (err error) {
	id, err := d.identify()
	if err != nil {
		return err
	}
	d.id = id
	defer d.sleepAfter(&err)

	if cerr := d.command(cmdSoftReset); cerr != nil {
		return fmt.Errorf("shtc3: reset: %w", cerr)
	}
	time.Sleep(resetDelay)
	// A reset leaves the device asleep again, and the deferred sleep wants it awake.
	return d.wake()
}

// identify wakes the part, which answers nothing asleep, and reads its ID; a part that is not an SHTC3 is sent nothing more.
func (d *SHTC3) identify() (uint16, error) {
	if err := d.wake(); err != nil {
		return 0, err
	}
	id, err := d.readWord(cmdReadIDRegister)
	if err != nil {
		return 0, fmt.Errorf("shtc3: reading identity: %w", err)
	}
	if id&idMask != idValue {
		return id, fmt.Errorf("shtc3: unexpected ID %#04x (want %#04x in bits %#04x); wrong device or bus?", id, idValue, idMask)
	}
	return id, nil
}

// Probe finds an SHTC3 at addr, waking it and sleeping it again so a scan leaves it as it found it.
func Probe(bus i2c.Bus, addr uint16) (err error) {
	d := &SHTC3{dev: i2c.Dev{Bus: bus, Addr: addr}}
	if _, err := d.identify(); err != nil {
		return fmt.Errorf("at %#02x: %w", addr, err)
	}
	d.sleepAfter(&err)
	return err
}

func (d *SHTC3) ID() uint16 { return d.id }

// Sense performs a single-shot measurement of both temperature and humidity.
func (d *SHTC3) Sense(e *physic.Env) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return errors.New("shtc3: Sense cannot be used while sensing continuously")
	}
	return d.read(e)
}

// SenseContinuous repeats the single-shot sequence, the part having no continuous mode; the first failed read closes the channel.
func (d *SHTC3) SenseContinuous(interval time.Duration) (<-chan physic.Env, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("shtc3: sensing interval %s is not positive", interval)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return nil, errors.New("shtc3: already sensing continuously")
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
				err := d.read(&e)
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

// read runs one wake, measure, sleep cycle and fills e. The caller must hold d.mu.
func (d *SHTC3) read(e *physic.Env) (err error) {
	if werr := d.wake(); werr != nil {
		return werr
	}
	defer d.sleepAfter(&err)

	cmd, conversion := uint16(cmdMeasureNormal), conversionNormal
	if d.opts.LowPower {
		cmd, conversion = cmdMeasureLowPower, conversionLowPower
	}
	if cerr := d.command(cmd); cerr != nil {
		return fmt.Errorf("shtc3: starting measurement: %w", cerr)
	}
	time.Sleep(conversion)

	v, rerr := d.readMeasurement()
	if rerr != nil {
		return rerr
	}

	// CRC is part of the read protocol, not an option: a flipped bit is a plausible temperature.
	tCRC := crc8(v[0:2]) == v[2]
	hCRC := crc8(v[3:5]) == v[5]
	if !tCRC || !hCRC {
		return fmt.Errorf("shtc3: CRC mismatch (temperature ok=%t, humidity ok=%t)", tCRC, hCRC)
	}
	tRaw := binary.BigEndian.Uint16(v[0:2])
	hRaw := binary.BigEndian.Uint16(v[3:5])

	// Datasheet: T[C] = -45 + 175 * S_T / 2^16, RH[%] = 100 * S_RH / 2^16.
	tC := -45 + 175*float64(tRaw)/65536
	e.Temperature = physic.ZeroCelsius + physic.Temperature(math.Round(tC*float64(physic.Kelvin)))
	e.Humidity = physic.RelativeHumidity(math.Round(100 * float64(hRaw) / 65536 * float64(physic.PercentRH)))
	return nil
}

// readMeasurement retries because a NACK means the conversion is not done, not that it failed.
func (d *SHTC3) readMeasurement() ([6]byte, error) {
	var v [6]byte
	deadline := time.Now().Add(d.opts.MeasurementReadTimeout)
	for {
		err := d.dev.Tx(nil, v[:])
		if err == nil {
			return v, nil
		}
		if !time.Now().Before(deadline) {
			return v, fmt.Errorf("shtc3: timed out reading measurement: %w", err)
		}
		time.Sleep(retryInterval)
	}
}

// sleepAfter returns the part to sleep on every path out of a woken exchange.
func (d *SHTC3) sleepAfter(err *error) {
	if serr := d.command(cmdSleep); serr != nil && *err == nil {
		*err = fmt.Errorf("shtc3: returning to sleep: %w", serr)
	}
}

// wake brings the device out of sleep and waits for it to be addressable.
func (d *SHTC3) wake() error {
	if err := d.command(cmdWake); err != nil {
		return fmt.Errorf("shtc3: wake: %w", err)
	}
	time.Sleep(wakeDelay)
	return nil
}

// command writes one 16-bit command, most significant byte first.
func (d *SHTC3) command(cmd uint16) error {
	return d.dev.Tx([]byte{byte(cmd >> 8), byte(cmd)}, nil)
}

// readWord writes a command and reads back a word with its CRC byte.
func (d *SHTC3) readWord(cmd uint16) (uint16, error) {
	if err := d.command(cmd); err != nil {
		return 0, err
	}
	var v [3]byte
	if err := d.dev.Tx(nil, v[:]); err != nil {
		return 0, err
	}
	if crc8(v[0:2]) != v[2] {
		return 0, errors.New("CRC mismatch")
	}
	return binary.BigEndian.Uint16(v[0:2]), nil
}

// Precision returns the sensor's measurement resolution (1 LSB).
func (d *SHTC3) Precision(e *physic.Env) {
	e.Temperature = physic.Temperature(math.Round(175 * float64(physic.Kelvin) / 65536))
	e.Humidity = physic.RelativeHumidity(math.Round(100 * float64(physic.PercentRH) / 65536))
	e.Pressure = 0
}

// Halt stops any continuous sensing and returns the device to sleep.
func (d *SHTC3) Halt() error {
	d.mu.Lock()
	if d.stop != nil {
		close(d.stop)
		d.stop = nil
	}
	d.mu.Unlock()
	d.wg.Wait()

	// Already asleep, and a sleeping part NACKs everything but wake, so sending one would invent a fault.
	return nil
}

// String implements conn.Resource.
func (d *SHTC3) String() string {
	return fmt.Sprintf("SHTC3{%s}", d.dev.Bus)
}

// crc8 is the Sensirion CRC-8: polynomial 0x31, initial value 0xFF, no final xor.
func crc8(b []byte) byte {
	crc := byte(0xFF)
	for _, v := range b {
		crc ^= v
		for range 8 {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x31
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Probe must keep the signature the sensor framework's chip table expects.
var _ func(i2c.Bus, uint16) error = Probe

var _ physic.SenseEnv = &SHTC3{}
var _ conn.Resource = &SHTC3{}
