package sensor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const radioBoardKind = "radio-board"

var (
	// ErrNoRadio is a radio that is not connected right now.
	ErrNoRadio = errors.New("no radio is connected")
	// ErrNoBoard is a radio with no board in front of the chip, as on SPI, so nothing reports a battery or temperature.
	ErrNoBoard = errors.New("this radio has no board to report a battery or temperature")
)

// BoardReadings is what the radio board last reported; At is when it last answered, zero when it has not yet, and SilentSince when its silence began.
type BoardReadings struct {
	Transport   string
	BatteryV    float64
	HaveBattery bool
	MCUTempC    float64
	HaveMCUTemp bool
	At          time.Time
	SilentSince time.Time
}

// RadioBoardProvider reads the battery and MCU temperature the modem already asks its board for, so it adds no traffic on the link.
type RadioBoardProvider struct {
	// Board is the connected radio's readings, or ErrNoRadio or ErrNoBoard.
	Board func() (BoardReadings, error)
	// MaxAge is how long the modem keeps a board reading without an answer; it asks every 30 s.
	MaxAge time.Duration
}

func (RadioBoardProvider) ID() string    { return "radio" }
func (RadioBoardProvider) Label() string { return "Radio board" }

// Available is false only for a radio that can never report; one that is down now may come back.
func (p RadioBoardProvider) Available(context.Context) (bool, string) {
	if _, err := p.Board(); errors.Is(err, ErrNoBoard) {
		return false, err.Error()
	}
	return true, ""
}

func (RadioBoardProvider) Kinds() []KindInfo {
	return []KindInfo{{
		Kind: radioBoardKind, Label: "Radio board",
		Description: "Battery voltage and MCU temperature from the radio's board",
		Category:    "Power",
		Metrics:     []Metric{Voltage, Temperature},
	}}
}

func (p RadioBoardProvider) Discover(context.Context) ([]Candidate, error) {
	r, err := p.Board()
	if err != nil {
		return nil, nil
	}
	return []Candidate{{
		Kind: radioBoardKind, Label: "Radio board", Detail: transportNames[r.Transport] + " board", Addable: true,
		Options: map[string]string{},
	}}, nil
}

var transportNames = map[string]string{"kiss": "KISS", "openhop": "openHop"}

// Claim makes the board addable once: there is only one radio.
func (RadioBoardProvider) Claim(Spec) string { return "the radio board" }

func (p RadioBoardProvider) StaleAfter(Spec) (time.Duration, bool) { return p.MaxAge, true }

func (p RadioBoardProvider) Open(Spec) (Sensor, error) {
	return &radioBoard{board: p.Board, maxAge: p.MaxAge, opened: time.Now()}, nil
}

type radioBoard struct {
	board  func() (BoardReadings, error)
	maxAge time.Duration
	// opened bounds how long a start may wait on the radio before the wait is itself the fault.
	opened time.Time
	// last and at are the last good sample, which stands through a reconnect until it is as old as the modem keeps one.
	last []Reading
	at   time.Time
}

// waiting is an error the hub shows only once a reading has been had, so a start before the radio is up does not flash as a failure.
type waiting struct{ error }

func (waiting) Is(target error) bool { return target == errNotYet }

func (w waiting) Unwrap() error { return w.error }

func (s *radioBoard) Read(context.Context) ([]Reading, error) {
	r, err := s.board()
	var out []Reading
	if err == nil {
		if r.HaveBattery {
			out = append(out, Reading{Metric: Voltage, Label: "battery", Value: r.BatteryV, Unit: "V"})
		}
		if r.HaveMCUTemp {
			out = append(out, Reading{Metric: Temperature, Label: "MCU", Value: r.MCUTempC, Unit: "°C"})
		}
	}
	if len(out) > 0 {
		s.last, s.at = out, r.At
		return out, nil
	}
	if s.last != nil && time.Since(s.at) <= s.maxAge {
		return s.last, nil
	}
	switch {
	case errors.Is(err, ErrNoBoard):
		return nil, err // for good, so the card must say so rather than wait
	case err != nil:
		if time.Since(s.opened) <= s.maxAge {
			return nil, waiting{err}
		}
		return nil, err
	case r.At.IsZero():
		from := r.SilentSince
		if from.IsZero() {
			from = s.opened
		}
		if time.Since(from) <= s.maxAge {
			return nil, errNotYet
		}
		return nil, fmt.Errorf("the radio board has not answered in the %s since it connected", time.Since(from).Round(time.Second))
	case time.Since(r.At) > s.maxAge:
		return nil, fmt.Errorf("the radio board has not answered for %s", time.Since(r.At).Round(time.Second))
	}
	return nil, errors.New("the radio board reports neither a battery nor an MCU temperature")
}

// SampledAt is the board's last answer, which the modem asks for every 30 s, not this read.
func (s *radioBoard) SampledAt() time.Time { return s.at }
