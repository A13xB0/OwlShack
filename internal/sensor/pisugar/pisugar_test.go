package pisugar

import (
	"bufio"
	"context"
	"io"
	"maps"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The answers a real PiSugar 2 (2-LEDs) gave over /tmp/pisugar-server.sock, captured 2026-09-22.
var piSugar2 = map[string]string{
	"get model":                 "model: PiSugar 2 (2-LEDs)",
	"get battery":               "battery: 36.05795",
	"get battery_v":             "battery_v: 3.8405669",
	"get battery_i":             "battery_i: -0.22991261",
	"get battery_charging":      "battery_charging: false",
	"get battery_power_plugged": "battery_power_plugged: true",
	"get temperature":           "temperature: 0",
	"get firmware_version":      "firmware_version: ",
}

// fake answers on a unix socket like pisugar-server, "Invalid request." to anything unknown; push is written out of turn, as a button event is.
func fake(t *testing.T, answers map[string]string, push string) string {
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
				first := true
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if first && push != "" {
						first = false
						if _, err := c.Write([]byte(push + "\n")); err != nil {
							return
						}
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

func sense(t *testing.T, addr string) map[string]Reading {
	t.Helper()
	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial %s: %v", addr, err)
	}
	defer c.Close()
	got, err := c.Sense()
	if err != nil {
		t.Fatalf("Sense: %v", err)
	}
	out := map[string]Reading{}
	for _, r := range got {
		out[r.Key] = r
	}
	return out
}

func TestSense_ReadsWhatTheServerReports(t *testing.T) {
	got := sense(t, fake(t, piSugar2, ""))
	for _, tc := range []struct {
		key  string
		want float64
		unit string
	}{
		{"battery_v", 3.8405669, "V"},
		{"battery", 36.05795, "%"},
		// Negative is the battery discharging, which is why it may not be published as an LPP current.
		{"battery_i", -0.22991261, "A"},
		{"battery_charging", 0, ""},
		{"battery_power_plugged", 1, ""},
	} {
		r, ok := got[tc.key]
		if !ok {
			t.Errorf("%s missing", tc.key)
			continue
		}
		if r.Value != tc.want || r.Unit != tc.unit {
			t.Errorf("%s read %v %q, want %v %q", tc.key, r.Value, r.Unit, tc.want, tc.unit)
		}
	}
}

// A PiSugar 2 answers `get temperature` with 0 rather than refusing it, so a gate on the command alone would publish a permanent 0 °C.
func TestSense_LeavesOutTheTemperatureAPiSugar2CannotMeasure(t *testing.T) {
	if r, ok := sense(t, fake(t, piSugar2, ""))["temperature"]; ok {
		t.Fatalf("published the PiSugar 2's constant %v as a temperature", r.Value)
	}

	// The same reading on a model that has the sensor, so the check above is a gate and not a hole.
	three := maps.Clone(piSugar2)
	three["get model"] = "model: PiSugar 3"
	three["get temperature"] = "temperature: 31"
	if r, ok := sense(t, fake(t, three, ""))["temperature"]; !ok || r.Value != 31 {
		t.Fatalf("PiSugar 3 temperature read %v (present %v), want 31", r.Value, ok)
	}
}

// A model that does not answer a command must cost that one reading, not the whole poll.
func TestSense_SkipsACommandTheModelDoesNotHave(t *testing.T) {
	answers := maps.Clone(piSugar2)
	delete(answers, "get battery_i") // PiSugar 3 has no current sense

	got := sense(t, fake(t, answers, ""))
	if _, ok := got["battery_i"]; ok {
		t.Error("reported a current the server refused")
	}
	// Asked after the refused command, so stopping there instead of skipping it loses them.
	for _, after := range []string{"battery_charging", "battery_power_plugged"} {
		if _, ok := got[after]; !ok {
			t.Errorf("%s was not read after the refused command", after)
		}
	}
}

// The server pushes button events down the same socket, so reading the next line as the answer would report an event as the battery voltage.
func TestGet_IgnoresAnEventPushedMidExchange(t *testing.T) {
	if v := sense(t, fake(t, piSugar2, "single"))["battery_v"].Value; v != 3.8405669 {
		t.Fatalf("voltage read %v, want 3.8405669", v)
	}
}

// A line is read into memory whole, so a server that never ends one must be cut off at a line's length, not run on to the deadline.
func TestGet_RefusesALineLongerThanAnyAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pisugar.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		endless := []byte(strings.Repeat("x", 4096))
		for {
			if _, err := c.Write(endless); err != nil {
				return
			}
		}
	}()

	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if _, err := c.Get("battery_v"); err == nil {
		t.Fatal("an endless line read as an answer")
	}
	if took := time.Since(start); took > Timeout/2 {
		t.Errorf("an endless line took %v to refuse, which is the deadline and not the line's length", took)
	}
}

// The deadline is the only bound on an exchange, so a caller's shorter one has to reach it.
func TestDial_KeepsTheCallersEarlierDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pisugar.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if _, err := c.Get("model"); err == nil {
		t.Fatal("a server that said nothing answered")
	}
	if took := time.Since(start); took > Timeout/2 {
		t.Errorf("a silent server held the exchange %v, past the caller's 50ms", took)
	}
}

// Conn's reads go through its line reader; a Read of its own would take bytes that reader already buffered.
func TestConn_OffersNoReadAroundItsLineReader(t *testing.T) {
	if _, ok := any(&Conn{}).(io.Reader); ok {
		t.Error("Conn is an io.Reader, so a caller can read past the buffered lines")
	}
	if _, ok := any(&Conn{}).(io.Writer); ok {
		t.Error("Conn is an io.Writer, so a caller can write outside an exchange")
	}
}

// A connection that cannot be bounded could wait forever, so it is refused rather than used.
func TestNewConn_RefusesAConnectionItCannotBound(t *testing.T) {
	a, b := net.Pipe()
	b.Close()
	a.Close()
	if c, err := newConn(context.Background(), a); err == nil {
		c.Close()
		t.Fatal("a connection whose deadline could not be set was used")
	}
	// Provokes the positive: an open one is bounded and used.
	a, b = net.Pipe()
	defer b.Close()
	c, err := newConn(context.Background(), a)
	if err != nil {
		t.Fatalf("an open connection was refused: %v", err)
	}
	c.Close()
}

func TestSense_RefusesAServerThatIsNotOne(t *testing.T) {
	c, err := Dial(context.Background(), fake(t, map[string]string{"get model": "model: something else"}, ""))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Sense(); err == nil || !strings.Contains(err.Error(), "not a PiSugar server") {
		t.Fatalf("error %v, want a refusal", err)
	}
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in          string
		net, addr   string
		wantRefused bool
	}{
		{in: DefaultSocket, net: "unix", addr: DefaultSocket},
		{in: "  /run/pisugar.sock  ", net: "unix", addr: "/run/pisugar.sock"},
		{in: "pizero-w", net: "tcp", addr: "pizero-w:8423"},
		{in: "10.0.0.5:9000", net: "tcp", addr: "10.0.0.5:9000"},
		{in: "tcp://10.0.0.5:9000", net: "tcp", addr: "10.0.0.5:9000"},
		{in: "[::1]:8423", net: "tcp", addr: "[::1]:8423"},
		{in: "", wantRefused: true},
		{in: "   ", wantRefused: true},
		{in: "host:port:extra", wantRefused: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			network, address, err := ParseAddress(tc.in)
			if tc.wantRefused {
				if err == nil {
					t.Fatalf("accepted %q as %s %s", tc.in, network, address)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused %q: %v", tc.in, err)
			}
			if network != tc.net || address != tc.addr {
				t.Fatalf("%q parsed as %s %s, want %s %s", tc.in, network, address, tc.net, tc.addr)
			}
		})
	}
}
