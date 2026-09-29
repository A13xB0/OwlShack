package modem

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// probeStub is a StatsProvider that reports when it last "answered", and can be told to stop.
type probeStub struct {
	mu     sync.Mutex
	last   time.Time
	answer bool
	flaky  bool // answer every other poll
	polls  int
}

func (p *probeStub) CachedStats() DeviceStats         { return DeviceStats{} }
func (p *probeStub) Transport() string                { return "kiss" }
func (p *probeStub) RadioConfig() RadioInfo           { return RadioInfo{} }
func (p *probeStub) LinkStats() LinkStats             { return LinkStats{} }
func (p *probeStub) EstAirtimeMs(int) uint32          { return 0 }
func (p *probeStub) PacketScore(float64, int) float64 { return 0 }

func (p *probeStub) Stats(context.Context) DeviceStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	if p.answer || (p.flaky && p.polls%2 == 0) {
		p.last = time.Now()
	}
	return DeviceStats{}
}

func (p *probeStub) LastReply() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *probeStub) pollCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polls
}

func shrinkProbe(t *testing.T) {
	t.Helper()
	old := probeInterval
	probeInterval = 3 * time.Millisecond
	t.Cleanup(func() { probeInterval = old })
}

// runProbe starts the watcher and guarantees it has exited before the test returns, so restoring
// probeInterval in cleanup cannot race the goroutine reading it.
func runProbe(t *testing.T, stub *probeStub) chan struct{} {
	t.Helper()
	reconnect := make(chan struct{}, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	m := &State{Stats: stub}
	go func() { defer close(finished); m.probeLiveness(stub, reconnect, done) }()
	t.Cleanup(func() {
		close(done)
		<-finished
	})
	return reconnect
}

// A modem that has never answered may simply not implement the queries. Reconnecting it forever
// would take a working radio down, so the probe stays disarmed until it has seen one reply; it still
// asks, or on a node with nothing else polling the board it would never arm at all.
func TestProbeLiveness_StaysDisarmedUntilTheModemHasAnswered(t *testing.T) {
	shrinkProbe(t)
	stub := &probeStub{} // last is the zero time: never answered
	reconnect := runProbe(t, stub)

	time.Sleep(probeInterval * 20)

	select {
	case <-reconnect:
		t.Fatal("asked for a reconnect on a modem that never answered a query")
	default:
	}
	if stub.pollCount() == 0 {
		t.Error("never asked a modem that has not answered yet, so nothing else polling the board leaves the probe disarmed forever")
	}
}

// The first answer can only come from the probe itself when nothing else polls the board.
func TestProbeLiveness_ArmsItselfAndCatchesAStuckBoard(t *testing.T) {
	shrinkProbe(t)
	stub := &probeStub{answer: true}
	reconnect := runProbe(t, stub)
	for deadline := time.Now().Add(3 * time.Second); stub.LastReply().IsZero(); time.Sleep(probeInterval) {
		if time.Now().After(deadline) {
			t.Fatal("the probe never asked a board nothing else polls")
		}
	}
	stub.mu.Lock()
	stub.answer = false
	stub.mu.Unlock()

	select {
	case <-reconnect:
	case <-time.After(3 * time.Second):
		t.Fatal("a board that answered only the probe then went silent was never reconnected")
	}
}

// Once it has answered, three consecutive unanswered probes mean the port is open but the device is gone.
func TestProbeLiveness_ReconnectsAfterConsecutiveMisses(t *testing.T) {
	shrinkProbe(t)
	stub := &probeStub{last: time.Now(), answer: false}
	reconnect := runProbe(t, stub)

	select {
	case <-reconnect:
	case <-time.After(3 * time.Second):
		t.Fatal("a silent modem never triggered a reconnect")
	}
	if got := stub.pollCount(); got < probeMisses {
		t.Errorf("fired after %d polls, want at least %d", got, probeMisses)
	}
}

// A modem that keeps answering must never be reconnected, however quiet the mesh is.
func TestProbeLiveness_LeavesAnsweringModemAlone(t *testing.T) {
	shrinkProbe(t)
	stub := &probeStub{last: time.Now(), answer: true}
	reconnect := runProbe(t, stub)

	time.Sleep(probeInterval * 30)

	select {
	case <-reconnect:
		t.Fatal("reconnected a modem that answered every probe")
	default:
	}
	if stub.pollCount() == 0 {
		t.Fatal("never probed, so the test proves nothing")
	}
}

// A modem that drops the occasional reply under load is not a dead one: a single answer clears the
// count. Without the reset, misses accumulate across healthy probes and a working modem is
// eventually reconnected — which is why this case is separate from the always-answers one.
func TestProbeLiveness_OneAnswerClearsTheMissCount(t *testing.T) {
	shrinkProbe(t)
	stub := &probeStub{last: time.Now(), flaky: true}
	reconnect := runProbe(t, stub)

	time.Sleep(probeInterval * 40)

	select {
	case <-reconnect:
		t.Fatal("reconnected a modem that answered every other probe")
	default:
	}
	if got := stub.pollCount(); got <= probeMisses {
		t.Fatalf("only %d polls, too few to have accumulated %d misses; the test proves nothing", got, probeMisses)
	}
}

// From a user log on v1.2.0: every query returned "write frame: input/output error" while the status
// payload still carried battery_mv=4148 and mcu_temp_c=20.5, so a consumer saw a healthy 4.1 V board
// at the moment the serial port had closed. A reading is only current while the modem is answering.
func TestKissStats_DropsReadingsOnceTheModemStopsAnswering(t *testing.T) {
	p := &kissStatsProvider{startTime: time.Now(), log: slog.New(slog.DiscardHandler)}

	// A live modem: reply handlers have run, so the readings are real.
	p.onBattery(0, []byte{0x34, 0x10}) // 4148 mV
	p.onMCUTemp(0, []byte{0xCD, 0x00}) // 20.5 C
	if ds := p.snapshot(); !ds.HaveBattery || !ds.HaveMCUTemp {
		t.Fatal("a modem that just answered must report its readings")
	}

	// The port has gone away: the sticky flags are still set, but nothing has answered since.
	p.lastReply.Store(time.Now().Add(-StaleReadingAfter - time.Second).UnixNano())

	ds := p.snapshot()
	if ds.HaveBattery {
		t.Errorf("published a stale battery reading (%d mV) after the modem stopped answering", ds.BatteryMV)
	}
	if ds.HaveMCUTemp {
		t.Errorf("published a stale MCU temperature (%.1f C) after the modem stopped answering", ds.MCUTempC)
	}
}
