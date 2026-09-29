package sensor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/meshcore-go/OwlShack/internal/sensor/pisugar"
)

// Skipped unless PISUGAR_HW_ADDR names a running pisugar-server; build with `go test -c`, as the Pi Zero the HAT sits on has no toolchain.
func hwPiSugar(t *testing.T) Sensor {
	t.Helper()
	addr := os.Getenv("PISUGAR_HW_ADDR")
	if addr == "" {
		t.Skip("set PISUGAR_HW_ADDR to a socket path or host:port to run against a real PiSugar")
	}
	s, err := PiSugarProvider{}.Open(Spec{Options: map[string]string{"address": addr}})
	if err != nil {
		t.Fatalf("Open %q: %v", addr, err)
	}
	return s
}

// Discover and Available only look at the default addresses, so this one has to run on the Pi.
func TestHardware_PiSugarDiscover(t *testing.T) {
	switch os.Getenv("PISUGAR_HW_ADDR") {
	case pisugar.DefaultSocket, pisugar.DefaultTCP:
	default:
		t.Skip("set PISUGAR_HW_ADDR to a default address, on the host the HAT is attached to")
	}
	if ok, reason := (PiSugarProvider{}).Available(context.Background()); !ok {
		t.Fatalf("a PiSugar is attached but the provider is unavailable: %s", reason)
	}
	found, err := PiSugarProvider{}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found nothing on either default address")
	}
	for _, c := range found {
		t.Logf("%s at %s", c.Label, c.Detail)
		if c.Kind != piSugarKind || !c.Addable {
			t.Errorf("candidate %+v is not addable as a %s", c, piSugarKind)
		}
	}
}

// The voltage is the reading the node's own channel publishes, so a plausible one is the point.
func TestHardware_PiSugarReadsAPlausibleCell(t *testing.T) {
	got, err := hwPiSugar(t).Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	by := map[Metric]Reading{}
	for _, r := range got {
		t.Logf("%s = %v %s", r.Metric, r.Value, r.Unit)
		by[r.Metric] = r
	}

	v, ok := by[Voltage]
	if !ok {
		t.Fatal("no battery voltage, which is the whole point of the sensor")
	}
	// A single LiPo cell. Outside this the server is reporting something that is not a battery.
	if v.Value < 2.5 || v.Value > 4.5 {
		t.Errorf("battery voltage %v V is outside 2.5..4.5, so it is not a LiPo cell", v.Value)
	}
	if p, ok := by[Percentage]; ok && (p.Value < 0 || p.Value > 100) {
		t.Errorf("charge %v %% is outside 0..100", p.Value)
	}
	for _, m := range []Metric{Charging, Plugged} {
		if r, ok := by[m]; ok && r.Value != 0 && r.Value != 1 {
			t.Errorf("%s read %v, want 0 or 1", m, r.Value)
		}
	}
}

// Two reads a second apart, because a cached value that never moves looks the same as a live one.
func TestHardware_PiSugarRepeatedRead(t *testing.T) {
	s := hwPiSugar(t)
	for i := range 3 {
		start := time.Now()
		got, err := s.Read(context.Background())
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		if took := time.Since(start); took > pisugar.Timeout {
			t.Errorf("read %d took %v, past the %v budget", i+1, took, pisugar.Timeout)
		}
		t.Logf("read %d in %v: %+v", i+1, time.Since(start), got)
		time.Sleep(time.Second)
	}
}
