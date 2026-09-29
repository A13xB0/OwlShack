package ens210

import (
	"strings"
	"testing"
	"time"

	"periph.io/x/conn/v3/i2c/i2ctest"
	"periph.io/x/conn/v3/physic"
)

// reference is a second implementation of the ENS210's CRC-7 (poly 0x89, 17-bit payload, IV all ones), not a copy of crc7.
func reference(payload uint32) uint32 {
	const poly = 0x89 // x^7 + x^3 + 1, with the x^7 term
	acc := payload<<7 | 0x7F
	for bit := 16 + 7; bit >= 7; bit-- {
		if acc&(1<<uint(bit)) != 0 {
			acc ^= poly << uint(bit-7)
		}
	}
	return acc & 0x7F
}

func TestCRC7_MatchesTheDefinition(t *testing.T) {
	for _, payload := range []uint32{
		0, 1, 0x1FFFF, 0x10000, 0x0FFFF,
		0x1621C, // 22.5C: raw 0x621C with VALID set
		0x1A800, // 50 %RH: raw 0xA800 with VALID set
		0x00042, 0x15555, 0x0AAAA,
	} {
		if got, want := crc7(payload), reference(payload); got != want {
			t.Errorf("crc7(%#x) = %#02x, want %#02x", payload, got, want)
		}
	}
}

// word builds the three bytes the part returns for a value: DATA, VALID and a CRC over both.
func word(raw uint16, valid bool) []byte {
	v := uint32(raw)
	if valid {
		v |= 1 << 16
	}
	v |= reference(v&0x1FFFF) << 17
	return []byte{byte(v), byte(v >> 8), byte(v >> 16)}
}

func TestDecode_SplitsDataValidAndCRC(t *testing.T) {
	raw, valid, crcOK := decode(word(0x621C, true))
	if raw != 0x621C || !valid || !crcOK {
		t.Fatalf("decode = (%#04x, %t, %t), want (0x621c, true, true)", raw, valid, crcOK)
	}

	if _, valid, _ := decode(word(0x621C, false)); valid {
		t.Error("a word with VALID clear decoded as valid")
	}
}

// Every single-bit flip must be caught, or the gate read refuses measurements for is not doing its job.
func TestDecode_CatchesASingleFlippedBit(t *testing.T) {
	good := word(0x621C, true)
	for bit := range 17 {
		bad := []byte{good[0], good[1], good[2]}
		bad[bit/8] ^= 1 << uint(bit%8)
		if _, _, crcOK := decode(bad); crcOK {
			t.Errorf("a flip of payload bit %d passed the CRC", bit)
		}
	}
}

// identifyOps is what an open says before it knows the part: read SYS_CTRL, wake, read the identity block, put SYS_CTRL back.
func identifyOps(partID uint16, ctrl byte) []i2ctest.IO {
	id := make([]byte, 12)
	id[0], id[1] = byte(partID), byte(partID>>8)
	return []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{0x10}, R: []byte{ctrl}},
		{Addr: DefaultAddress, W: []byte{0x10, 0x00}},
		{Addr: DefaultAddress, W: []byte{0x11}, R: []byte{0x01}},
		{Addr: DefaultAddress, W: []byte{0x00}, R: id},
		{Addr: DefaultAddress, W: []byte{0x10, ctrl}},
	}
}

// openOps is what NewI2C says to a part answering partID: identify it, then reset it.
func openOps(partID uint16) []i2ctest.IO {
	return append(identifyOps(partID, 0x01), i2ctest.IO{Addr: DefaultAddress, W: []byte{0x10, 0x80}})
}

// senseOps is a single-shot measurement that answers with these value fields.
func senseOps(fields ...[]byte) []i2ctest.IO {
	ops := []i2ctest.IO{
		{Addr: DefaultAddress, W: []byte{0x21, 0x00}},
		{Addr: DefaultAddress, W: []byte{0x22, 0x03}},
	}
	for _, f := range fields {
		ops = append(ops, i2ctest.IO{Addr: DefaultAddress, W: []byte{0x30}, R: f})
	}
	return ops
}

// Anything else at 0x43 would be driven as an ENS210 and read garbage that looks like weather.
func TestNewI2C_RefusesAPartThatIsNotAnENS210(t *testing.T) {
	// 0x5a is no ENS210's SYS_CTRL, so the write putting it back proves the part was left as found.
	ops := append(identifyOps(0x0211, 0x5A), i2ctest.IO{Addr: DefaultAddress, W: []byte{0x10, 0x80}})
	bus := &i2ctest.Playback{Ops: ops, DontPanic: true}
	if _, err := NewI2C(bus, nil); err == nil || !strings.Contains(err.Error(), "PART_ID") {
		t.Fatalf("NewI2C gave %v, want the wrong part refused", err)
	}
	if bus.Count != len(ops)-1 {
		t.Errorf("%d of %d transactions reached the wrong part, want SYS_CTRL put back and no reset", bus.Count, len(ops))
	}
	// Provokes the negative: the real ID opens.
	if _, err := NewI2C(&i2ctest.Playback{Ops: openOps(0x0210), DontPanic: true}, nil); err != nil {
		t.Fatalf("an ENS210 was refused: %v", err)
	}
}

// 22.5 C is 295.65 K, 18922 at 1/64 K; 50 %RH is 25600 at 1/512.
func TestSense_ReadsAndRefusesACorruptedMeasurement(t *testing.T) {
	good := append(word(18922, true), word(25600, true)...)
	bus := &i2ctest.Playback{Ops: append(openOps(0x0210), senseOps(good)...), DontPanic: true}
	d, err := NewI2C(bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	var e physic.Env
	if err := d.Sense(&e); err != nil {
		t.Fatalf("Sense: %v", err)
	}
	if c := e.Temperature.Celsius(); c < 22.49 || c > 22.52 {
		t.Errorf("temperature %.3f C, want 22.5", c)
	}
	if h := float64(e.Humidity) / float64(physic.PercentRH); h != 50 {
		t.Errorf("humidity %v %%RH, want 50", h)
	}

	flipped := append([]byte(nil), good...)
	flipped[0] ^= 0x04
	bus = &i2ctest.Playback{Ops: append(openOps(0x0210), senseOps(flipped)...), DontPanic: true}
	if d, err = NewI2C(bus, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Sense(&e); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("a flipped bit gave %v, want the measurement refused", err)
	}
}

// A part that never marks a value valid is a failed read, not a wait without end.
func TestSense_TimesOutWhenNothingIsValid(t *testing.T) {
	pending := append(word(0, false), word(0, false)...)
	bus := &i2ctest.Playback{Ops: append(openOps(0x0210), senseOps(pending, pending, pending, pending)...), DontPanic: true}
	d, err := NewI2C(bus, &Opts{MeasurementReadTimeout: 25 * time.Millisecond, MeasurementWaitInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var e physic.Env
	if err := d.Sense(&e); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Sense gave %v, want a timeout", err)
	}
}

// neverValidBus is an ENS210 that goes active and identifies, then never marks a measurement valid.
type neverValidBus struct{}

func (neverValidBus) String() string                  { return "neverValidBus" }
func (neverValidBus) SetSpeed(physic.Frequency) error { return nil }

func (neverValidBus) Tx(addr uint16, w, r []byte) error {
	switch {
	case len(r) == 0:
	case w[0] == 0x11:
		r[0] = 0x01
	case w[0] == 0x00:
		r[0], r[1] = 0x10, 0x02
	case w[0] == 0x30:
		copy(r, append(word(0, false), word(0, false)...))
	}
	return nil
}

// Zero is unset, as in every driver here, not "wait for ever": a part that stopped converting would hold its caller for good.
func TestSense_TakesTheDefaultTimeoutForZero(t *testing.T) {
	d, err := NewI2C(neverValidBus{}, &Opts{})
	if err != nil {
		t.Fatalf("NewI2C: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		var e physic.Env
		done <- d.Sense(&e)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Sense gave %v, want a timeout", err)
		}
	case <-time.After(3 * DefaultOpts.MeasurementReadTimeout):
		t.Fatal("a zero timeout waited for ever on a part that never finished")
	}
}

// A stream that carried on past a failed read would go quiet for an unplugged part, which reads as a long interval, and a zero interval would panic its ticker.
func TestSenseContinuous_EndsAtAFailedReadAndRefusesNoInterval(t *testing.T) {
	ops := append(openOps(0x0210),
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x21, 0x03}},
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x22, 0x03}},
		i2ctest.IO{Addr: DefaultAddress, W: []byte{0x30}, R: append(word(18922, true), word(25600, true)...)},
	)
	d, err := NewI2C(&i2ctest.Playback{Ops: ops, DontPanic: true}, nil)
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
		case <-time.After(3 * DefaultOpts.MeasurementReadTimeout):
			t.Fatalf("receive %d: the stream went quiet instead of ending", i)
		}
	}
}
