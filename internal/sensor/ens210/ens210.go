// Package ens210 drives the ScioSense ENS210 humidity and temperature sensor (datasheet: https://www.sciosense.com/wp-content/uploads/2023/12/ENS210-Datasheet.pdf).
package ens210

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

// DefaultAddress is the fixed I2C address of the ENS210.
const DefaultAddress = 0x43

const (
	regPartID    = 0x00 // 2 bytes, little-endian, reads 0x0210
	regDieRev    = 0x02 // 2 bytes, little-endian
	regUID       = 0x04 // 8 bytes, unique identifier
	regSysCtrl   = 0x10 // bit0 LOW_POWER, bit7 RESET
	regSysStat   = 0x11 // bit0 SYS_ACTIVE
	regSensRun   = 0x21 // bit0 T, bit1 H: 1=continuous, 0=single-shot
	regSensStart = 0x22 // bit0 T, bit1 H: 1=start
	regSensStop  = 0x23 // bit0 T, bit1 H: 1=stop
	regSensStat  = 0x24 // bit0 T, bit1 H: 1=measuring
	regTVal      = 0x30 // 3 bytes
	regHVal      = 0x33 // 3 bytes
)

// partIDValue is the value PART_ID reads on a genuine ENS210.
const partIDValue = 0x0210

type Opts struct {
	// Address is the device address; zero means DefaultAddress.
	Address uint16
	// MeasurementReadTimeout bounds the wait after triggering; zero is DefaultOpts'.
	MeasurementReadTimeout time.Duration
	// MeasurementWaitInterval paces the re-read while a conversion finishes; zero is DefaultOpts'.
	MeasurementWaitInterval time.Duration
}

// DefaultOpts holds the default configuration options for the device.
var DefaultOpts = Opts{
	// A T+H conversion measured 126-135ms on a Pi Zero W; this catches a part that stopped answering, it does not pace a healthy one.
	MeasurementReadTimeout:  time.Second,
	MeasurementWaitInterval: 10 * time.Millisecond,
}

// ENS210 is an open connection to the part.
type ENS210 struct {
	dev  i2c.Dev
	opts Opts

	// Device identity, populated by NewI2C.
	partID uint16
	dieRev uint16
	uid    uint64

	mu   sync.Mutex
	stop chan struct{}
	wg   sync.WaitGroup
}

// NewI2C returns an ENS210 on bus, rejecting a part whose PART_ID is not an ENS210's.
func NewI2C(bus i2c.Bus, opts *Opts) (*ENS210, error) {
	if opts == nil {
		opts = &DefaultOpts
	}
	addr := opts.Address
	if addr == 0 {
		addr = DefaultAddress
	}
	d := &ENS210{dev: i2c.Dev{Bus: bus, Addr: addr}, opts: *opts}
	if d.opts.MeasurementReadTimeout <= 0 {
		d.opts.MeasurementReadTimeout = DefaultOpts.MeasurementReadTimeout
	}
	if d.opts.MeasurementWaitInterval <= 0 {
		d.opts.MeasurementWaitInterval = DefaultOpts.MeasurementWaitInterval
	}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *ENS210) init() error {
	// PART_ID, DIE_REV and UID are contiguous at 0x00..0x0B, so one read returns all three.
	var id [12]byte
	if err := d.identify(id[:]); err != nil {
		return fmt.Errorf("ens210: reading identity: %w", err)
	}
	d.partID = binary.LittleEndian.Uint16(id[0:2])
	d.dieRev = binary.LittleEndian.Uint16(id[2:4])
	d.uid = binary.LittleEndian.Uint64(id[4:12])
	if d.partID != partIDValue {
		return fmt.Errorf("ens210: unexpected PART_ID %#04x (want %#04x); wrong device or bus?", d.partID, partIDValue)
	}

	// Soft reset, then wait the boot time (tbooting, ~1.2ms typ); it leaves low power on.
	if err := d.dev.Tx([]byte{regSysCtrl, 0x80}, nil); err != nil {
		return fmt.Errorf("ens210: reset: %w", err)
	}
	time.Sleep(2 * time.Millisecond)
	return nil
}

// identify reads the identity block, readable only when active, and puts SYS_CTRL back as it found it, so a part that is not an ENS210 is left as it was.
func (d *ENS210) identify(id []byte) error {
	var ctrl [1]byte
	if err := d.dev.Tx([]byte{regSysCtrl}, ctrl[:]); err != nil {
		return fmt.Errorf("reading SYS_CTRL: %w", err)
	}
	err := d.enterActive()
	if err == nil {
		err = d.dev.Tx([]byte{regPartID}, id)
	}
	if rerr := d.dev.Tx([]byte{regSysCtrl, ctrl[0]}, nil); rerr != nil && err == nil {
		err = fmt.Errorf("restoring SYS_CTRL: %w", rerr)
	}
	return err
}

// Probe is the one probe that writes: PART_ID reads only while active, so it activates, reads, and restores low power.
func Probe(bus i2c.Bus, addr uint16) error {
	d := &ENS210{dev: i2c.Dev{Bus: bus, Addr: addr}}
	var id [2]byte
	if err := d.identify(id[:]); err != nil {
		return fmt.Errorf("ens210: reading identity at %#02x: %w", addr, err)
	}
	if got := binary.LittleEndian.Uint16(id[:]); got != partIDValue {
		return fmt.Errorf("ens210: PART_ID %#04x at %#02x is not an ENS210", got, addr)
	}
	return nil
}

// enterActive clears LOW_POWER and waits for the part to say it is active; identify puts SYS_CTRL back.
func (d *ENS210) enterActive() error {
	if err := d.dev.Tx([]byte{regSysCtrl, 0x00}, nil); err != nil { // LOW_POWER = 0
		return fmt.Errorf("disabling low power: %w", err)
	}
	for i := 0; i < 20; i++ {
		var st [1]byte
		if err := d.dev.Tx([]byte{regSysStat}, st[:]); err != nil {
			return fmt.Errorf("reading status: %w", err)
		}
		if st[0]&0x01 != 0 {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("the part did not reach active state")
}

// PartID returns the part identifier (0x0210 for an ENS210).
func (d *ENS210) PartID() uint16 { return d.partID }

func (d *ENS210) DieRev() uint16 { return d.dieRev }

func (d *ENS210) UID() uint64 { return d.uid }

// Sense performs a single-shot measurement of both temperature and humidity.
func (d *ENS210) Sense(e *physic.Env) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return errors.New("ens210: Sense cannot be used while sensing continuously")
	}
	// Single-shot mode: SENS_RUN bits cleared, then start both sensors.
	if err := d.dev.Tx([]byte{regSensRun, 0x00}, nil); err != nil {
		return fmt.Errorf("ens210: setting single-shot mode: %w", err)
	}
	if err := d.dev.Tx([]byte{regSensStart, 0x03}, nil); err != nil {
		return fmt.Errorf("ens210: starting measurement: %w", err)
	}
	return d.read(e)
}

// SenseContinuous puts the device in continuous mode; Halt stops it and closes the channel, as does the first failed read.
func (d *ENS210) SenseContinuous(interval time.Duration) (<-chan physic.Env, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("ens210: sensing interval %s is not positive", interval)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop != nil {
		return nil, errors.New("ens210: already sensing continuously")
	}
	if err := d.dev.Tx([]byte{regSensRun, 0x03}, nil); err != nil { // continuous, both
		return nil, fmt.Errorf("ens210: setting continuous mode: %w", err)
	}
	if err := d.dev.Tx([]byte{regSensStart, 0x03}, nil); err != nil {
		return nil, fmt.Errorf("ens210: starting measurement: %w", err)
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

// read polls until a measurement is valid and fills e; the caller must hold d.mu.
func (d *ENS210) read(e *physic.Env) error {
	deadline := time.Now().Add(d.opts.MeasurementReadTimeout)
	for {
		var v [6]byte
		if err := d.dev.Tx([]byte{regTVal}, v[:]); err != nil {
			return fmt.Errorf("ens210: reading measurement: %w", err)
		}
		tRaw, tValid, tCRC := decode(v[0:3])
		hRaw, hValid, hCRC := decode(v[3:6])
		if tValid && hValid {
			// CRC is part of the read protocol, not an option: a flipped bit is a plausible temperature.
			if !tCRC || !hCRC {
				return fmt.Errorf("ens210: CRC mismatch (temperature ok=%t, humidity ok=%t)", tCRC, hCRC)
			}
			// Temperature: 1/64 Kelvin. physic.Temperature is absolute nK.
			e.Temperature = physic.Temperature(math.Round(float64(tRaw) / 64.0 * float64(physic.Kelvin)))
			// Humidity: 1/512 %RH.
			e.Humidity = physic.RelativeHumidity(math.Round(float64(hRaw) / 512.0 * float64(physic.PercentRH)))
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("ens210: timed out waiting for a valid measurement")
		}
		time.Sleep(d.opts.MeasurementWaitInterval)
	}
}

// Precision returns the sensor's measurement resolution (1 LSB).
func (d *ENS210) Precision(e *physic.Env) {
	e.Temperature = physic.Temperature(math.Round(float64(physic.Kelvin) / 64.0))
	e.Humidity = physic.RelativeHumidity(math.Round(float64(physic.PercentRH) / 512.0))
	e.Pressure = 0
}

// Halt stops any continuous sensing and returns the device to standby.
func (d *ENS210) Halt() error {
	d.mu.Lock()
	if d.stop != nil {
		close(d.stop)
		d.stop = nil
	}
	d.mu.Unlock()
	d.wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.dev.Tx([]byte{regSensStop, 0x03}, nil); err != nil {
		return fmt.Errorf("ens210: stopping measurement: %w", err)
	}
	return nil
}

// String implements conn.Resource.
func (d *ENS210) String() string {
	return fmt.Sprintf("ENS210{%s}", d.dev.Bus)
}

// decode unpacks one value field, little-endian: bits 15:0 DATA, bit 16 VALID, bits 23:17 CRC.
func decode(b []byte) (raw uint16, valid, crcOK bool) {
	word := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	raw = uint16(word & 0xFFFF)
	valid = word&(1<<16) != 0
	payload := word & 0x1FFFF        // 17 bits: DATA + VALID
	storedCRC := (word >> 17) & 0x7F // 7 bits
	crcOK = crc7(payload) == storedCRC
	return raw, valid, crcOK
}

// crc7 is the ENS210's CRC-7 over a 17-bit payload: polynomial 0x89, initial vector all ones.
func crc7(val uint32) uint32 {
	const (
		width    = 7
		poly     = 0x89
		ivec     = 0x7F
		dataBits = 17
	)
	const dataMask = (1 << dataBits) - 1
	const dataMSB = 1 << (dataBits - 1)

	p := uint32(poly) << (dataBits - width - 1)
	bit := uint32(dataMSB)
	val <<= width
	bit <<= width
	p <<= width
	val |= ivec // insert initial vector

	for bit&(dataMask<<width) != 0 {
		if bit&val != 0 {
			val ^= p
		}
		bit >>= 1
		p >>= 1
	}
	return val & 0x7F
}

// Probe must keep the signature the sensor framework's chip table expects.
var _ func(i2c.Bus, uint16) error = Probe

var _ physic.SenseEnv = &ENS210{}
var _ conn.Resource = &ENS210{}
