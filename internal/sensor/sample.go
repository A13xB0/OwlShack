package sensor

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// sampledPart is what a sampledSensor drives: one read per sample, and a close once sampling has stopped.
type sampledPart interface {
	Read(ctx context.Context) ([]Reading, error)
	Close() error
}

// sampledSensor reads its part on its own clock, as the firmware runs BSEC every 3 s whatever asks, so a slow pass never becomes a gap in the fusion and a slow fetch never holds up a pass; Read hands back the latest sample.
type sampledSensor struct {
	inner  sampledPart
	period time.Duration
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	readings []Reading
	err      error
	at       time.Time
}

// sample takes its first reading straight away, off the caller's goroutine, so opening a slow source never stalls a pass.
func sample(inner sampledPart, period time.Duration) *sampledSensor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sampledSensor{inner: inner, period: period, cancel: cancel, done: make(chan struct{})}
	go s.run(ctx)
	return s
}

func (s *sampledSensor) run(ctx context.Context) {
	defer close(s.done)
	s.take(ctx)
	t := time.NewTicker(s.period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.take(ctx)
		}
	}
}

func (s *sampledSensor) take(ctx context.Context) {
	readings, err := s.inner.Read(ctx)
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	if err == nil {
		s.readings, s.at = readings, time.Now()
	}
}

func (s *sampledSensor) Read(context.Context) ([]Reading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.at.IsZero() {
		return nil, nil
	}
	// A read wedged in the driver would otherwise have the hub keep one sample as current forever.
	if age := time.Since(s.at); age > 2*s.period {
		return nil, fmt.Errorf("no sample for %s", age.Round(time.Second))
	}
	return slices.Clone(s.readings), nil
}

// SampledAt is when the latest good sample was taken, and zero before the first.
func (s *sampledSensor) SampledAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at
}

// Close stops the clock and any read in flight before the part closes, so the save at close sees the last sample and nothing reads a closed bus.
func (s *sampledSensor) Close() error {
	s.cancel()
	<-s.done
	return s.inner.Close()
}
