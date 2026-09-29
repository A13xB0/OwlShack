// Package pisugar reads a PiSugar UPS HAT through pisugar-server's socket API, leaving the HAT's I2C parts to the one process allowed to drive them.
package pisugar

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultSocket, DefaultTCP and DefaultPort are where pisugar-server listens unless it was told otherwise.
	DefaultSocket = "/tmp/pisugar-server.sock"
	DefaultTCP    = "127.0.0.1:" + DefaultPort
	DefaultPort   = "8423"
	// Timeout bounds one whole exchange, so a server that accepts and then says nothing cannot hold up a caller with no deadline of its own.
	Timeout = 3 * time.Second
)

// ErrUnsupported is a command this model does not have; that reading is left out, not failed.
var ErrUnsupported = errors.New("command not supported by this PiSugar")

// Reading is one answer, keyed by the command that produced it.
type Reading struct {
	Key   string
	Value float64
	Unit  string
}

// commands is asked for in order every Sense.
var commands = []struct{ key, unit string }{
	{"battery_v", "V"},
	{"battery", "%"},
	{"battery_i", "A"},
	{"temperature", "°C"},
	{"battery_charging", ""},
	{"battery_power_plugged", ""},
}

// Conn is one exchange with the server. It is not safe for concurrent use.
type Conn struct {
	c net.Conn
	r *bufio.Reader
}

func Dial(ctx context.Context, addr string) (*Conn, error) {
	network, address, err := ParseAddress(addr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: Timeout}
	c, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return newConn(ctx, c)
}

// newConn sets one deadline for the whole exchange, the caller's if it is sooner, so Get cannot wait forever for a line that never comes.
func newConn(ctx context.Context, c net.Conn) (*Conn, error) {
	deadline := time.Now().Add(Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.SetDeadline(deadline); err != nil {
		c.Close()
		return nil, fmt.Errorf("pisugar: bounding the exchange: %w", err)
	}
	return &Conn{c: c, r: bufio.NewReader(c)}, nil
}

func (c *Conn) Close() error { return c.c.Close() }

// Get sends one command and returns its value, skipping the button events pisugar-server pushes down the same socket.
func (c *Conn) Get(key string) (string, error) {
	if _, err := fmt.Fprintf(c.c, "get %s\n", key); err != nil {
		return "", err
	}
	prefix := key + ":"
	for {
		raw, err := c.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", fmt.Errorf("pisugar: a line over %d bytes, which no pisugar-server answer is", c.r.Size())
		}
		if err != nil {
			return "", err
		}
		line := strings.TrimRight(string(raw), "\r\n")
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(after), nil
		}
		if strings.HasPrefix(line, "Invalid request") {
			return "", fmt.Errorf("%w: get %s", ErrUnsupported, key)
		}
	}
}

// Sense asks for every reading the model has; a refused command costs that reading and anything else is an error, so a server dying mid-exchange is not a short answer.
func (c *Conn) Sense() ([]Reading, error) {
	model, err := c.Get("model")
	if err != nil && !errors.Is(err, ErrUnsupported) {
		return nil, err
	}
	// A PiSugar 2 answers `get temperature` with a constant 0, which would read as a real measurement.
	hasTemperature := model != "" && !strings.Contains(model, "PiSugar 2")

	out := make([]Reading, 0, len(commands))
	for _, cmd := range commands {
		if cmd.key == "temperature" && !hasTemperature {
			continue
		}
		raw, err := c.Get(cmd.key)
		if errors.Is(err, ErrUnsupported) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", cmd.key, err)
		}
		v, err := parseValue(raw)
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", cmd.key, err)
		}
		out = append(out, Reading{Key: cmd.key, Value: v, Unit: cmd.unit})
	}
	if len(out) == 0 {
		return nil, errors.New("answered none of the battery commands, so it is not a PiSugar server")
	}
	return out, nil
}

// Model names the board, so a caller can tell a PiSugar 2 from a 3 before configuring one.
func (c *Conn) Model() (string, error) { return c.Get("model") }

// parseValue takes the number or the true/false a command answers with.
func parseValue(s string) (float64, error) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, nil
	}
	if b, err := strconv.ParseBool(s); err == nil {
		if b {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("%q is neither a number nor true/false", s)
}

// ParseAddress reads the one option: a path is the unix socket, anything else the TCP API, whose port may be left off.
func ParseAddress(s string) (network, address string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("the PiSugar server address is required, such as %s", DefaultSocket)
	}
	if strings.HasPrefix(s, "/") {
		return "unix", s, nil
	}
	s = strings.TrimPrefix(s, "tcp://")
	if !strings.Contains(s, ":") {
		s = net.JoinHostPort(s, DefaultPort)
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return "", "", fmt.Errorf("%q is neither a socket path nor a host:port", s)
	}
	return "tcp", s, nil
}
