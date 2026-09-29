package sensor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func boardSensor(t *testing.T, r BoardReadings, err error) (RadioBoardProvider, *radioBoard) {
	t.Helper()
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return r, err }, MaxAge: 45 * time.Second}
	s, openErr := p.Open(Spec{})
	if openErr != nil {
		t.Fatal(openErr)
	}
	return p, s.(*radioBoard)
}

func TestRadioBoard_Read(t *testing.T) {
	t.Parallel()
	now := time.Now()
	answered := now.Add(-10 * time.Second)
	for name, tc := range map[string]struct {
		r       BoardReadings
		err     error
		want    []Metric
		notYet  bool   // the hub hides it until a first reading
		wantErr string // part of the message shown once there has been one
	}{
		"no radio": {err: ErrNoRadio, notYet: true, wantErr: "no radio"},
		// An SPI radio never will, so waiting would sit on the card for good with no reason.
		"no board":         {err: ErrNoBoard, wantErr: "no board"},
		"not answered yet": {r: BoardReadings{Transport: "kiss"}, notYet: true},
		"both":             {r: BoardReadings{BatteryV: 4.1, HaveBattery: true, MCUTempC: 31.5, HaveMCUTemp: true, At: answered}, want: []Metric{Voltage, Temperature}},
		// getBattMilliVolts is 0 on a board that cannot measure one, and 0 V is published as the firmware does.
		"battery unsupported": {r: BoardReadings{HaveBattery: true, MCUTempC: 30, HaveMCUTemp: true, At: answered}, want: []Metric{Voltage, Temperature}},
		"neither":             {r: BoardReadings{At: answered}, wantErr: "neither"},
		"starting":            {r: BoardReadings{SilentSince: now.Add(-10 * time.Second)}, notYet: true},
		// Its TCP link reconnects every 60 s, so only the kept silence start shows it never answered.
		"hung from the start": {r: BoardReadings{SilentSince: now.Add(-2 * time.Minute)}, wantErr: "since it connected"},
		"gone quiet":          {r: BoardReadings{At: now.Add(-2 * time.Minute)}, wantErr: "not answered for"},
	} {
		_, s := boardSensor(t, tc.r, tc.err)
		got, err := s.Read(context.Background())
		if errors.Is(err, errNotYet) != tc.notYet {
			t.Errorf("%s: waiting = %v, want %v (err %v)", name, errors.Is(err, errNotYet), tc.notYet, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: err %v, want it to say %q", name, err, tc.wantErr)
		}
		var metrics []Metric
		for _, r := range got {
			metrics = append(metrics, r.Metric)
		}
		if strings.Join(toStrings(metrics), ",") != strings.Join(toStrings(tc.want), ",") {
			t.Errorf("%s: read %v, want %v", name, metrics, tc.want)
		}
		if len(tc.want) > 0 && !s.SampledAt().Equal(answered) {
			t.Errorf("%s: SampledAt %v, want the board's answer at %v, since the modem only asks every 30 s", name, s.SampledAt(), answered)
		}
	}
}

func toStrings(ms []Metric) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return out
}

// SPI can never report, so the picker says why; a radio that is only down may come back and stays offered.
func TestRadioBoard_AvailableAndDiscover(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err       error
		available bool
		found     int
	}{
		"kiss up":  {available: true, found: 1},
		"no radio": {err: ErrNoRadio, available: true},
		"spi":      {err: ErrNoBoard},
	} {
		p, _ := boardSensor(t, BoardReadings{Transport: "kiss"}, tc.err)
		if ok, _ := p.Available(context.Background()); ok != tc.available {
			t.Errorf("%s: available %v, want %v", name, ok, tc.available)
		}
		if got, _ := p.Discover(context.Background()); len(got) != tc.found {
			t.Errorf("%s: found %d, want %d", name, len(got), tc.found)
		}
	}
}

// A hung board's TCP link reconnects every 60 s; each new link has no answer yet, which must not read as a first start.
func TestRadioBoard_ThroughAReconnect(t *testing.T) {
	t.Parallel()
	answered := time.Now().Add(-5 * time.Second)
	cur := BoardReadings{BatteryV: 4.1, HaveBattery: true, At: answered}
	var curErr error
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return cur, curErr }, MaxAge: 45 * time.Second}
	opened, _ := p.Open(Spec{})
	s := opened.(*radioBoard)
	if got, err := s.Read(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("first read: %v, %v", got, err)
	}

	curErr = ErrNoRadio // between links
	if got, err := s.Read(context.Background()); err != nil || len(got) != 1 || !s.SampledAt().Equal(answered) {
		t.Fatalf("between links: %v, %v; want the last sample with its own time", got, err)
	}

	curErr = nil
	cur = BoardReadings{At: answered.Add(-time.Minute)} // the new link has not answered; the kept answer is old
	s.at = answered.Add(-time.Minute)
	if _, err := s.Read(context.Background()); err == nil || errors.Is(err, errNotYet) || !strings.Contains(err.Error(), "not answered for") {
		t.Fatalf("hung past the limit: %v, want it to say how long the board has been silent", err)
	}
}

// A radio that never comes up must not wait on the card for good, and the wrapper must keep the reason findable.
func TestRadioBoard_NoRadioStopsWaiting(t *testing.T) {
	t.Parallel()
	_, s := boardSensor(t, BoardReadings{}, ErrNoRadio)
	_, err := s.Read(context.Background())
	if !errors.Is(err, errNotYet) || !errors.Is(err, ErrNoRadio) {
		t.Fatalf("just opened: %v, want a wait that still names ErrNoRadio", err)
	}
	s.opened = time.Now().Add(-time.Minute)
	if _, err := s.Read(context.Background()); errors.Is(err, errNotYet) || !errors.Is(err, ErrNoRadio) {
		t.Fatalf("a minute on: %v, want ErrNoRadio shown, not waited on", err)
	}
}
