package ads1x15

import (
	"errors"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"
)

// defaultConfig is DefaultOpts on the wire: OS, AIN0 vs ground, +-4.096V, single-shot, ~128Hz, comparator off.
const defaultConfig = 0xC383

func w16(reg byte, v uint16) []byte { return []byte{reg, byte(v >> 8), byte(v)} }
func r16(v uint16) []byte           { return []byte{byte(v >> 8), byte(v)} }

// detectSGM is the traffic that identifies an SGM58031: its Chip_ID register answers for itself.
func detectSGM(addr uint16) []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: addr, W: []byte{regConfig}, R: r16(0x8583)},
		{Addr: addr, W: []byte{regChipID}, R: r16(0x0080)},
	}
}

// detectADS is the traffic identifying an ADS1115: a two-bit pointer answers Chip_ID with the config register.
func detectADS(addr uint16) []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: addr, W: []byte{regConfig}, R: r16(0x8583)},
		{Addr: addr, W: []byte{regChipID}, R: r16(0x8583)},
		{Addr: addr, W: []byte{regLoThresh}, R: r16(0x8000)},
		{Addr: addr, W: []byte{regHiThresh}, R: r16(0x7FFF)},
	}
}

// convert is one conversion: start it, find the part busy, wait, confirm the config held, read out.
func convert(addr uint16, raw uint16) []i2ctest.IO {
	return []i2ctest.IO{
		{Addr: addr, W: w16(regConfig, defaultConfig)},
		{Addr: addr, W: []byte{regConfig}, R: r16(defaultConfig &^ bitOS)},
		{Addr: addr, W: []byte{regConfig}, R: r16(defaultConfig)},
		{Addr: addr, W: []byte{regConfig}, R: r16(defaultConfig)},
		{Addr: addr, W: []byte{regConversion}, R: r16(raw)},
	}
}

func TestNewI2C_TellsTheTwoPartsApart(t *testing.T) {
	for name, tc := range map[string]struct {
		ops  []i2ctest.IO
		want Variant
	}{
		"SGM58031 answers with its own chip id": {detectSGM(DefaultAddress), SGM58031},
		"ADS1115 aliases the chip id read":      {detectADS(DefaultAddress), ADS1115},
	} {
		t.Run(name, func(t *testing.T) {
			bus := &i2ctest.Playback{Ops: tc.ops, DontPanic: true}
			d, err := NewI2C(bus, nil)
			if err != nil {
				t.Fatalf("NewI2C: %v", err)
			}
			if d.Variant() != tc.want {
				t.Errorf("Variant() = %s, want %s", d.Variant(), tc.want)
			}
			if err := bus.Close(); err != nil {
				t.Errorf("playback not fully consumed: %v", err)
			}
		})
	}
}

// Something that answers on the address but is neither part must be refused, not driven as an ADC.
func TestNewI2C_RefusesADeviceThatIsNeither(t *testing.T) {
	for name, ops := range map[string][]i2ctest.IO{
		"register 5 is neither an alias nor a chip id": {
			{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(0x8583)},
			{Addr: DefaultAddress, W: []byte{regChipID}, R: r16(0xBEEF)},
		},
		"every register echoes the same word": {
			{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(0x1234)},
			{Addr: DefaultAddress, W: []byte{regChipID}, R: r16(0x1234)},
			{Addr: DefaultAddress, W: []byte{regLoThresh}, R: r16(0x1234)},
			{Addr: DefaultAddress, W: []byte{regHiThresh}, R: r16(0x1234)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
			if _, err := NewI2C(bus, nil); err == nil {
				t.Fatal("NewI2C accepted a device that is not an ADS1115 or SGM58031")
			}
		})
	}
}

// A conversion is finished when the part says so: a computed delay reads the previous conversion.
func TestRead_WaitsForThePartRatherThanAClock(t *testing.T) {
	ops := append(detectSGM(DefaultAddress), convert(DefaultAddress, 0x4000)...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	s, err := d.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Raw != 0x4000 {
		t.Errorf("Raw = %#04x, want 0x4000", s.Raw)
	}
	if got, want := s.V, 2048*physic.MilliVolt; got != want {
		t.Errorf("V = %s, want %s", got, want)
	}
	// A driver that did not poll would leave the busy reply unread.
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed, so the busy reply was never read: %v", err)
	}
}

// One conversion register for the whole part: another Dev's reconfiguration would hand us its voltage with nothing to say so.
func TestRead_RefusesAResultAnotherInputStarted(t *testing.T) {
	const otherChannel = defaultConfig&^maskMux | uint16(Channel2)<<posMux
	ops := append(detectSGM(DefaultAddress), []i2ctest.IO{
		{Addr: DefaultAddress, W: w16(regConfig, defaultConfig)},
		{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(otherChannel)},
		{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(otherChannel)},
		// Offered, so a driver skipping the check returns it happily rather than failing for want of an op.
		{Addr: DefaultAddress, W: []byte{regConversion}, R: r16(0x4000)},
	}...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if _, err := d.Read(); err == nil {
		t.Fatal("Read returned a conversion that another input had started")
	}
}

func TestRead_TimesOutOnAPartThatNeverFinishes(t *testing.T) {
	ops := detectSGM(DefaultAddress)
	ops = append(ops, i2ctest.IO{Addr: DefaultAddress, W: w16(regConfig, defaultConfig)})
	for i := 0; i < 400; i++ {
		ops = append(ops, i2ctest.IO{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(defaultConfig &^ bitOS)})
	}
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	o := DefaultOpts
	o.ConversionTimeout = 20 * time.Millisecond
	d, err := NewI2C(bus, &o)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	if _, err := d.Read(); err == nil {
		t.Fatal("Read waited out a part that never reported itself idle and returned success")
	}
}

// A rate the part does not have must land on one it does, not on a code meaning something else.
func TestNearestDataRate(t *testing.T) {
	for _, tc := range []struct {
		v    Variant
		want physic.Frequency
		ask  physic.Frequency
	}{
		{ADS1115, 128 * physic.Hertz, 128 * physic.Hertz},
		{ADS1115, 860 * physic.Hertz, 2 * physic.KiloHertz},
		{ADS1115, 8 * physic.Hertz, physic.Hertz},
		{SGM58031, 100 * physic.Hertz, 128 * physic.Hertz},
		{SGM58031, 800 * physic.Hertz, 2 * physic.KiloHertz},
		{SGM58031, 6250 * physic.MilliHertz, physic.Hertz},
	} {
		code := nearestDataRate(tc.v, tc.ask)
		if got := dataRates[tc.v][code]; got != tc.want {
			t.Errorf("%s asked %s: got %s, want %s", tc.v, tc.ask, got, tc.want)
		}
	}
}

// Channel and gain both have to reach the wire; either wrong is a plausible voltage off the wrong pin or scale.
func TestRead_ChannelAndGainReachTheWire(t *testing.T) {
	o := DefaultOpts
	o.Channel, o.Gain = Channel2, Gain0V512
	cfg := bitOS | uint16(Channel2)<<posMux | uint16(Gain0V512)<<posPGA | bitSingleShot | 4<<posDataRate | compQueDisable

	ops := append(detectSGM(DefaultAddress), []i2ctest.IO{
		{Addr: DefaultAddress, W: w16(regConfig, cfg)},
		{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(cfg)},
		{Addr: DefaultAddress, W: []byte{regConfig}, R: r16(cfg)},
		{Addr: DefaultAddress, W: []byte{regConversion}, R: r16(0x8000)},
	}...)
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	d, err := NewI2C(bus, &o)
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	s, err := d.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Full negative deflection at the 0.512V range.
	if got, want := s.V, -512*physic.MilliVolt; got != want {
		t.Errorf("V = %s, want %s", got, want)
	}
	if err := bus.Close(); err != nil {
		t.Errorf("playback not fully consumed: %v", err)
	}
}

func TestNewI2C_RejectsSettingsThePartCannotHold(t *testing.T) {
	for name, o := range map[string]Opts{
		"address below the ADDR range": {Address: 0x47},
		"address above the ADDR range": {Address: 0x4C},
		"channel past the eight":       {Address: DefaultAddress, Channel: Channel3 + 1},
		"gain past the six":            {Address: DefaultAddress, Gain: Gain0V256 + 1},
	} {
		// Playback.Count does not advance on a failed op, so an untouched bus is the proof these run before any I/O.
		bus := &refusingBus{}
		if _, err := NewI2C(bus, &o); err == nil {
			t.Errorf("%s: NewI2C accepted it", name)
		}
		if bus.touched {
			t.Errorf("%s: reached the bus before rejecting it, so the bus refused it and not the check", name)
		}
	}
}

// refusingBus separates a setting rejected on its shape from one that died on the first register read.
type refusingBus struct{ touched bool }

func (b *refusingBus) Tx(uint16, []byte, []byte) error {
	b.touched = true
	return errors.New("refusingBus: nothing should have reached the bus")
}
func (b *refusingBus) SetSpeed(physic.Frequency) error { return nil }
func (b *refusingBus) String() string                  { return "refusingBus" }

// A Dev that never identified its part must not pass for an ADS1115.
func TestVariant_ZeroIsNoPart(t *testing.T) {
	var v Variant
	if v == ADS1115 || v == SGM58031 {
		t.Fatalf("the zero Variant is %s", v)
	}
	if s := v.String(); s == ADS1115.String() || s == SGM58031.String() {
		t.Errorf("the zero Variant prints as %s", s)
	}
}
