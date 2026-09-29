package sensor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"time"

	"github.com/meshcore-go/OwlShack/internal/sensor/pisugar"
)

// Charging and Plugged are states rather than measurements, carried as 1 and 0.
const (
	Charging Metric = "charging"
	Plugged  Metric = "plugged"
)

const piSugarKind = "pisugar"

// piSugarAddrs is where pisugar-server listens by default, a var so a test can point it at a socket it controls.
var piSugarAddrs = []string{pisugar.DefaultSocket, pisugar.DefaultTCP}

// piSugarMetrics is what each of the server's commands measures; a command with no entry here is read but not published.
var piSugarMetrics = map[string]Metric{
	"battery_v":             Voltage,
	"battery":               Percentage,
	"battery_i":             Current,
	"temperature":           Temperature,
	"battery_charging":      Charging,
	"battery_power_plugged": Plugged,
}

// PiSugarProvider adds a PiSugar UPS HAT, which reports through pisugar-server rather than the bus.
type PiSugarProvider struct{}

func (PiSugarProvider) ID() string    { return "pisugar" }
func (PiSugarProvider) Label() string { return "PiSugar" }

// Available is about this host: nothing answering is a scan finding no PiSugar, and only a socket that refuses us is a problem to report.
func (PiSugarProvider) Available(ctx context.Context) (bool, string) {
	for _, addr := range piSugarAddrs {
		c, err := pisugar.Dial(ctx, addr)
		if err == nil {
			c.Close()
			return true, ""
		}
		if errors.Is(err, fs.ErrPermission) {
			return false, fmt.Sprintf("%s will not accept a connection from this user: %v", addr, err)
		}
	}
	return true, ""
}

func (PiSugarProvider) Kinds() []KindInfo {
	return []KindInfo{{
		Kind: piSugarKind, Label: "PiSugar",
		Description: "Battery voltage, charge, current and charging state from a PiSugar UPS HAT",
		Category:    "Power",
		Metrics:     []Metric{Voltage, Percentage, Current, Temperature, Charging, Plugged},
		Fields: []Field{{
			Key: "address", Label: "Server", Required: true, Identifies: true,
			Default: pisugar.DefaultSocket,
			Help:    "pisugar-server's socket path, or host:port for its TCP API",
		}},
	}}
}

// Validate parses the address without dialling, so a PiSugar can be configured before its server is up.
func (PiSugarProvider) Validate(spec Spec) error {
	_, _, err := pisugar.ParseAddress(spec.Options["address"])
	return err
}

// Discover stops at the first default address that answers, as one local server listens on both; the model names the candidate, since a PiSugar 2 and a 3 answer different commands.
func (PiSugarProvider) Discover(ctx context.Context) ([]Candidate, error) {
	for _, addr := range piSugarAddrs {
		c, err := pisugar.Dial(ctx, addr)
		if err != nil {
			continue
		}
		model, err := c.Model()
		c.Close()
		if err != nil {
			continue
		}
		return []Candidate{{
			Kind: piSugarKind, Label: model, Detail: addr, Addable: true,
			Options: map[string]string{"address": addr},
		}}, nil
	}
	return nil, nil
}

// Claim is the server a spec reads, so one board is not added twice; the default socket and a loopback default port are the one local server.
func (PiSugarProvider) Claim(spec Spec) string {
	_, address, err := pisugar.ParseAddress(spec.Options["address"])
	if err != nil {
		return ""
	}
	host, port, _ := net.SplitHostPort(address)
	if address == pisugar.DefaultSocket || port == pisugar.DefaultPort && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return "this host's pisugar-server"
	}
	return address
}

func (PiSugarProvider) Open(spec Spec) (Sensor, error) {
	if _, _, err := pisugar.ParseAddress(spec.Options["address"]); err != nil {
		return nil, err
	}
	return &piSugar{addr: spec.Options["address"]}, nil
}

// piSugarRetry is how long a server that timed out is left alone, since the hub reads one sensor at a time and a silent server costs its whole timeout.
const piSugarRetry = 5 * time.Minute

// piSugar dials per read rather than holding the socket, which would have to survive the server restarting under it between polls.
type piSugar struct {
	addr string
	// timedOut is answered, without dialling, until retryAt.
	timedOut error
	retryAt  time.Time
}

func (s *piSugar) Read(ctx context.Context) ([]Reading, error) {
	if time.Now().Before(s.retryAt) {
		return nil, s.timedOut
	}
	out, err := s.read(ctx)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		s.timedOut = fmt.Errorf("%w; trying again every %s", err, piSugarRetry)
		s.retryAt = time.Now().Add(piSugarRetry)
		return nil, s.timedOut
	}
	return out, err
}

func (s *piSugar) read(ctx context.Context) ([]Reading, error) {
	c, err := pisugar.Dial(ctx, s.addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	got, err := c.Sense()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.addr, err)
	}
	out := make([]Reading, 0, len(got))
	for _, r := range got {
		m, ok := piSugarMetrics[r.Key]
		if !ok {
			continue
		}
		out = append(out, Reading{Metric: m, Value: r.Value, Unit: r.Unit})
	}
	return out, nil
}
