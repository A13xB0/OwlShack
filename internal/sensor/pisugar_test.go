package sensor

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-go/OwlShack/internal/sensor/pisugar"
)

// fakePiSugar answers like pisugar-server; the protocol itself is exercised in the driver's own tests.
func fakePiSugar(t *testing.T, answers map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pisugar.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					reply, ok := answers[strings.TrimSpace(line)]
					if !ok {
						reply = "Invalid request."
					}
					if _, err := c.Write([]byte(reply + "\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	return path
}

// The mapping is the provider's whole job: a command read then dropped, or named as the wrong metric, publishes a battery as something else or not at all.
func TestPiSugarProvider_NamesEveryReadingItPublishes(t *testing.T) {
	addr := fakePiSugar(t, map[string]string{
		"get model":                 "model: PiSugar 3",
		"get battery_v":             "battery_v: 3.84",
		"get battery":               "battery: 36",
		"get battery_i":             "battery_i: -0.23",
		"get temperature":           "temperature: 31",
		"get battery_charging":      "battery_charging: false",
		"get battery_power_plugged": "battery_power_plugged: true",
	})
	s, err := PiSugarProvider{}.Open(Spec{Options: map[string]string{"address": addr}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := []Reading{
		{Metric: Voltage, Value: 3.84, Unit: "V"},
		{Metric: Percentage, Value: 36, Unit: "%"},
		{Metric: Current, Value: -0.23, Unit: "A"},
		{Metric: Temperature, Value: 31, Unit: "°C"},
		{Metric: Charging, Value: 0},
		{Metric: Plugged, Value: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("read %+v, want %d readings", got, len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("reading %d is %+v, want %+v", i, got[i], w)
		}
	}

	// Every metric the picker promises has to be one the provider actually publishes.
	promised := PiSugarProvider{}.Kinds()[0].Metrics
	for _, m := range promised {
		if !hasMetric(got, m) {
			t.Errorf("the catalogue lists %s, which this reading does not carry", m)
		}
	}
}

func hasMetric(rs []Reading, m Metric) bool {
	for _, r := range rs {
		if r.Metric == m {
			return true
		}
	}
	return false
}

func TestPiSugarProvider_RefusesAnAddressItCannotUse(t *testing.T) {
	if err := (PiSugarProvider{}).Validate(Spec{Options: map[string]string{"address": ""}}); err == nil {
		t.Fatal("accepted a spec with no address")
	}
	if err := (PiSugarProvider{}).Validate(Spec{Options: map[string]string{"address": pisugar.DefaultSocket}}); err != nil {
		t.Fatalf("refused the default socket: %v", err)
	}
}

// A host with no PiSugar is not a failed scan, or the picker warns about every part nobody has attached.
func TestPiSugarProvider_AnAbsentHatIsNotAScanFailure(t *testing.T) {
	ok, reason := PiSugarProvider{}.Available(context.Background())
	if !ok {
		t.Fatalf("reported a problem with nothing attached: %s", reason)
	}
	if reason != "" {
		t.Errorf("reason %q, want nothing to report", reason)
	}
}

// silentPiSugar accepts and never answers, like a hung server; it counts the connections it took.
func silentPiSugar(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "silent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })
	var dials atomic.Int32
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			held = append(held, c)
		}
	}()
	return path, &dials
}

func readWithin(s Sensor, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, err := s.Read(ctx)
	return err
}

// The hub reads one sensor at a time and config writes wait for the pass, so a silent server may cost its timeout once per retry, not once per poll.
func TestPiSugar_LeavesASilentServerAloneUntilItsRetry(t *testing.T) {
	addr, dials := silentPiSugar(t)
	s, err := PiSugarProvider{}.Open(Spec{Options: map[string]string{"address": addr}})
	if err != nil {
		t.Fatal(err)
	}
	first := readWithin(s, 50*time.Millisecond)
	if first == nil {
		t.Fatal("a server that said nothing read fine")
	}
	start := time.Now()
	again := readWithin(s, time.Second)
	if again == nil || again.Error() != first.Error() {
		t.Errorf("the read after a timeout gave %v, want the same %v, which keeps the hub from logging it twice", again, first)
	}
	if took := time.Since(start); took > 25*time.Millisecond {
		t.Errorf("the read after a timeout took %v, so it waited on the server again", took)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dialled %d times, want the second read to wait for the retry", n)
	}

	// Provokes the positive: once the retry is due, it dials again.
	s.(*piSugar).retryAt = time.Time{}
	readWithin(s, 50*time.Millisecond)
	for deadline := time.Now().Add(time.Second); dials.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if n := dials.Load(); n != 2 {
		t.Fatalf("dialled %d times once the retry was due, want 2", n)
	}
}

// A refusal costs nothing, and a server restarting refuses for a moment, so only a timeout waits for the retry.
func TestPiSugar_DialsARefusingServerEveryRead(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "pisugar.sock")
	s, err := PiSugarProvider{}.Open(Spec{Options: map[string]string{"address": addr}})
	if err != nil {
		t.Fatal(err)
	}
	if err := readWithin(s, time.Second); err == nil {
		t.Fatal("a server that is not there read fine")
	}
	// A listening socket keeps accepting at a path it is renamed to, which is the server coming back at the configured address.
	if err := os.Rename(fakePiSugar(t, map[string]string{"get model": "model: PiSugar 3", "get battery_v": "battery_v: 3.84"}), addr); err != nil {
		t.Fatal(err)
	}
	if err := readWithin(s, time.Second); err != nil {
		t.Fatalf("the server came back and the next read gave %v", err)
	}
}

// One pisugar-server listens on both default addresses, so a scan that answers on both has found one board.
func TestPiSugarProvider_ListsTheLocalServerOnce(t *testing.T) {
	answers := map[string]string{"get model": "model: PiSugar 3"}
	defer func(prev []string) { piSugarAddrs = prev }(piSugarAddrs)
	piSugarAddrs = []string{fakePiSugar(t, answers), fakePiSugar(t, answers)}
	found, err := PiSugarProvider{}.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Detail != piSugarAddrs[0] {
		t.Fatalf("found %+v, want the first address alone", found)
	}

	// Provokes the positive: a first address that does not answer leaves the second to be found.
	piSugarAddrs[0] = filepath.Join(t.TempDir(), "absent.sock")
	if found, _ = (PiSugarProvider{}).Discover(context.Background()); len(found) != 1 || found[0].Detail != piSugarAddrs[1] {
		t.Fatalf("found %+v, want the second address", found)
	}
}

// Two sensors on one server publish one battery twice, so an address the hub already reads is refused however it is spelt.
func TestPiSugarProvider_RefusesAServerAlreadyRead(t *testing.T) {
	h := NewHub(testLog(), PiSugarProvider{})
	ups := func(id int64, name, addr string) Spec {
		return Spec{ID: id, Provider: "pisugar", Kind: piSugarKind, Name: name, Options: map[string]string{"address": addr}}
	}
	h.Set([]Spec{ups(1, "UPS", pisugar.DefaultSocket), ups(2, "Remote", "pizero-w")})
	for _, addr := range []string{pisugar.DefaultTCP, "127.0.0.1", "localhost", "[::1]:8423", "pizero-w:8423", "tcp://pizero-w"} {
		if _, err := h.Prepare(ups(0, "again", addr)); err == nil || !strings.Contains(err.Error(), "already uses") {
			t.Errorf("a second sensor on %s gave %v, want it refused", addr, err)
		}
	}
	// Provokes the positive: another host's server is a board of its own, and an edit never collides with itself.
	for _, s := range []Spec{ups(0, "Shed", "shed-pi"), ups(1, "UPS renamed", pisugar.DefaultSocket)} {
		if _, err := h.Prepare(s); err != nil {
			t.Errorf("%s on %s was refused: %v", s.Name, s.Options["address"], err)
		}
	}
}

// A socket we are refused is the other case: we could not look, and that has to stay distinguishable.
func TestPiSugarProvider_ASocketItCannotOpenIsAProblem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens anything, so this cannot be provoked here")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatalf("making an unreadable directory: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	defer func(prev []string) { piSugarAddrs = prev }(piSugarAddrs)
	piSugarAddrs = []string{filepath.Join(dir, "pisugar.sock")}
	if ok, reason := (PiSugarProvider{}).Available(context.Background()); ok || !strings.Contains(reason, "will not accept") {
		t.Fatalf("Available = %v, %q; want a refused socket reported as a problem", ok, reason)
	}

	// Provokes the negative: nothing listening is a scan finding nothing, not a problem.
	piSugarAddrs = []string{filepath.Join(t.TempDir(), "absent.sock")}
	if ok, reason := (PiSugarProvider{}).Available(context.Background()); !ok {
		t.Fatalf("an absent socket made the provider unavailable: %q", reason)
	}
}
