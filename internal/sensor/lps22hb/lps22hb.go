// Package lps22hb drives the STMicroelectronics LPS22HB pressure sensor, which also reports die temperature (datasheet: https://www.st.com/resource/en/datasheet/lps22hb.pdf).
package lps22hb

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

// 7-bit, as periph and i2cdetect use; ST spells the same two 0xB8 and 0xBA, shifted for the R/W bit.
const (
	DefaultAddress = 0x5C
	AltAddress     = 0x5D
)

const (
	regWhoAmI     = 0x0F
	regCtrlReg1   = 0x10
	regCtrlReg2   = 0x11
	regIntSource  = 0x25
	regStatus     = 0x27
	regPressOutXL = 0x28 // 0x28..0x2A pressure, 0x2B..0x2C temperature, contiguous

	// whoAmIValue is what WHO_AM_I reads on a genuine LPS22HB.
	whoAmIValue = 0xB1

	// CTRL_REG2 bits.
	bitBoot    = 0x80
	bitAddInc  = 0x10 // register auto-increment across a multi-byte read
	bitSwReset = 0x04
	bitOneShot = 0x01

	// INT_SOURCE bit: CTRL_REG2's boot bit clears before the trim reload finishes, this one stays set for the whole reboot.
	bitBootRunning = 0x80

	// CTRL_REG1 bits. ODR 000 is one-shot, which is what this driver uses.
	bitBDU = 0x02 // hold the output registers together across a multi-byte read

	// STATUS bits.
	bitTempAvailable     = 0x02
	bitPressureAvailable = 0x01
)

type Opts struct {
	// Address selects the SA0 strap; zero means DefaultAddress, since 0x00 is the general call.
	Address uint16
	// MeasurementReadTimeout bounds the wait for a conversion; zero is DefaultOpts'.
	MeasurementReadTimeout time.Duration
}

// DefaultOpts holds the default configuration options for the device.
var DefaultOpts = Opts{
	Address:                DefaultAddress,
	MeasurementReadTimeout: 100 * time.Millisecond,
}

// pollInterval paces the status re-read; a one-shot completes well inside 20ms.
const pollInterval = 2 * time.Millisecond

type LPS22HB struct {
	dev  i2c.Dev
	opts Opts

	// whoAmI is the whole identity the part has: no die revision, no serial number.
	whoAmI byte

	mu   sync.Mutex
	stop chan struct{}
	wg   sync.WaitGroup
}

// NewI2C returns an LPS22HB on bus, rejecting a part whose WHO_AM_I is not an LPS22HB's.
func NewI2C(bus i2c.Bus, opts *Opts) (*LPS22HB, error) {
	if opts == nil {
		opts = &DefaultOpts
	}
	d := &LPS22HB{opts: *opts}
	if d.opts.Address == 0 {
		d.opts.Address = DefaultAddress
	}
	if d.opts.MeasurementReadTimeout <= 0 {
		d.opts.MeasurementReadTimeout = DefaultOpts.MeasurementReadTimeout
	}
	d.dev = i2c.Dev{Bus: bus, Addr: d.opts.Address}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

// init resets the sensor, checks WHO_AM_I, configures one-shot mode and spends the first conversion.
func (d *LPS22HB) init() error {
	// WHO_AM_I first: resetting whatever else answers here would be a side effect on someone else's chip.
	var id [1]byte
	if err := d.dev.Tx([]byte{regWhoAmI}, id[:]); err != nil {
		return fmt.Errorf("lps22hb: reading WHO_AM_I at %#02x: %w", d.opts.Address, err)
	}
	d.whoAmI = id[0]
	if id[0] != whoAmIValue {
		return fmt.Errorf("lps22hb: WHO_AM_I is %#02x at address %#02x (want %#02x); wrong device or bus?",
			id[0], d.opts.Address, whoAmIValue)
	}

	if err := d.write(regCtrlReg2, bitSwReset|bitBoot); err != nil {
		return fmt.Errorf("lps22hb: reset: %w", err)
	}
	if err := d.awaitClear(regCtrlReg2, bitSwReset|bitBoot); err != nil {
		return fmt.Errorf("lps22hb: waiting for reset to finish: %w", err)
	}
	// Converting before the trim reload gives a pressure wrong by hundreds of hPa that still looks real.
	if err := d.awaitClear(regIntSource, bitBootRunning); err != nil {
		return fmt.Errorf("lps22hb: waiting for the memory reboot to finish: %w", err)
	}

	// Explicit though it defaults set: without it a multi-register read returns one byte repeated.
	if err := d.write(regCtrlReg2, bitAddInc); err != nil {
		return fmt.Errorf("lps22hb: enabling register auto-increment: %w", err)
	}
	// Block data update, so a conversion cannot land between the halves of a reading.
	if err := d.write(regCtrlReg1, bitBDU); err != nil {
		return fmt.Errorf("lps22hb: configuring one-shot mode: %w", err)
	}
	// After a power-on the first conversion can be hundreds of hPa out (760 against 1026 on pi-4-2), so it is spent here.
	var discard physic.Env
	if err := d.read(&discard); err != nil {
		return fmt.Errorf("lps22hb: first conversion: %w", err)
	}
	return nil
}

// Probe finds an LPS22HB at addr; WHO_AM_I is a plain read, so a scan touches nothing.
func Probe(bus i2c.Bus, addr uint16) error {
	dev := i2c.Dev{Bus: bus, Addr: addr}
	var id [1]byte
	if err := dev.Tx([]byte{regWhoAmI}, id[:]); err != nil {
		return fmt.Errorf("lps22hb: reading WHO_AM_I at %#02x: %w", addr, err)
	}
	if id[0] != whoAmIValue {
		return fmt.Errorf("lps22hb: WHO_AM_I %#02x at %#02x is not an LPS22HB", id[0], addr)
	}
	return nil
}

func (d *LPS22HB) WhoAmI() byte { return d.whoAmI }

func (d *LPS22HB) Address() uint16 { return d.opts.Address }

// Sense performs a single-shot measurement of pressure and temperature.
func (d *LPS22HB) Sense(e *physic.Env) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return errors.New("lps22hb: Sense cannot be used while sensing continuously")
	}
	return d.read(e)
}

// SenseContinuous repeats the one-shot sequence, so every sample was converted for the caller that asked; the first failed read closes the channel.
func (d *LPS22HB) SenseContinuous(interval time.Duration) (<-chan physic.Env, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("lps22hb: sensing interval %s is not positive", interval)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return nil, errors.New("lps22hb: already sensing continuously")
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

// read triggers one conversion and fills e; the caller must hold d.mu.
func (d *LPS22HB) read(e *physic.Env) error {
	if err := d.write(regCtrlReg2, bitAddInc|bitOneShot); err != nil {
		return fmt.Errorf("lps22hb: starting measurement: %w", err)
	}

	// Both flags, not either: the two finish at different times and would pair across conversions.
	const ready = bitPressureAvailable | bitTempAvailable
	if err := d.awaitSet(regStatus, ready); err != nil {
		return fmt.Errorf("lps22hb: waiting for measurement: %w", err)
	}

	var v [5]byte
	if err := d.dev.Tx([]byte{regPressOutXL}, v[:]); err != nil {
		return fmt.Errorf("lps22hb: reading measurement: %w", err)
	}

	// Pressure is a 24-bit signed value, little-endian, in 1/4096 hPa.
	pRaw := int32(uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16)
	if pRaw&0x800000 != 0 {
		pRaw |= ^0xFFFFFF // sign-extend bit 23 into the top byte
	}
	hPa := float64(pRaw) / 4096
	e.Pressure = physic.Pressure(math.Round(hPa * float64(100*physic.Pascal)))

	// Temperature is 16-bit signed, little-endian, in 1/100 C.
	tC := float64(int16(binary.LittleEndian.Uint16(v[3:5]))) / 100
	e.Temperature = physic.ZeroCelsius + physic.Temperature(math.Round(tC*float64(physic.Kelvin)))
	return nil
}

func (d *LPS22HB) awaitSet(reg byte, mask byte) error {
	return d.await(reg, func(v byte) bool { return v&mask == mask })
}

func (d *LPS22HB) awaitClear(reg byte, mask byte) error {
	return d.await(reg, func(v byte) bool { return v&mask == 0 })
}

func (d *LPS22HB) await(reg byte, done func(byte) bool) error {
	deadline := time.Now().Add(d.opts.MeasurementReadTimeout)
	for {
		var v [1]byte
		if err := d.dev.Tx([]byte{reg}, v[:]); err != nil {
			return err
		}
		if done(v[0]) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out with register %#02x reading %#02x", reg, v[0])
		}
		time.Sleep(pollInterval)
	}
}

func (d *LPS22HB) write(reg, value byte) error {
	return d.dev.Tx([]byte{reg, value}, nil)
}

// Precision returns the sensor's measurement resolution (1 LSB).
func (d *LPS22HB) Precision(e *physic.Env) {
	e.Pressure = physic.Pressure(math.Round(float64(100*physic.Pascal) / 4096))
	e.Temperature = physic.Temperature(math.Round(float64(physic.Kelvin) / 100))
	e.Humidity = 0
}

// Halt stops any continuous sensing and returns the device to standby.
func (d *LPS22HB) Halt() error {
	d.mu.Lock()
	if d.stop != nil {
		close(d.stop)
		d.stop = nil
	}
	d.mu.Unlock()
	d.wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	// ODR back to 000, which only matters if something else left the part free-running.
	if err := d.write(regCtrlReg1, bitBDU); err != nil {
		return fmt.Errorf("lps22hb: returning to standby: %w", err)
	}
	return nil
}

// String implements conn.Resource.
func (d *LPS22HB) String() string {
	return fmt.Sprintf("LPS22HB{%s, %#02x}", d.dev.Bus, d.opts.Address)
}

// Probe must keep the signature the sensor framework's chip table expects.
var _ func(i2c.Bus, uint16) error = Probe

var _ physic.SenseEnv = &LPS22HB{}
var _ conn.Resource = &LPS22HB{}
