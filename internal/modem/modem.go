// Package modem connects to the radio hardware — KISS or openHop firmware, or a bare SX12xx — and exposes it as a node.Modem, a stats provider and the standard mux options.
package modem

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/meshcore-go/hardware"
	kissTransport "github.com/meshcore-go/meshcore-go/hardware/transport"
	"github.com/meshcore-go/meshcore-go/node"
)

// handlerWatchdog is deliberately not configurable: DATA dispatch is sub-millisecond, so an operator's value could only mask a stall.
const handlerWatchdog = 500 * time.Millisecond

// State holds a live modem connection with the resources torn down when it is replaced or shut down.
type State struct {
	Modem node.Modem
	Stats StatsProvider
	// ParseErrors is intact bytes that did not decode as a MeshCore packet; the radio did its job.
	ParseErrors *atomic.Uint64

	radioConfig   *hardware.RadioConfig
	airtimeFactor float64
	closers       []io.Closer
	watcherDone   chan struct{}
}

// Close stops the dead-watcher and closes the modem's resources in reverse order of acquisition.
func (m *State) Close() {
	if m.watcherDone != nil {
		select {
		case <-m.watcherDone:
		default:
			close(m.watcherDone)
		}
	}
	for i := len(m.closers) - 1; i >= 0; i-- {
		m.closers[i].Close()
	}
}

// Liveness probe cadence. Three misses at 30s is ~90s to react, which is slow enough that a busy
// modem dropping one reply cannot trigger a reconnect.
const (
	probeTimeout = 2 * time.Second
	probeMisses  = 3
)

// A var so tests can shrink it; a probe goroutine must be stopped before a test restores it.
var probeInterval = 30 * time.Second

// AnswerDeadline is how long a board may go without answering before it counts as stuck: past the
// probe's own give-up, so a dropped reply or two never flags.
func AnswerDeadline() time.Duration { return (probeMisses + 1) * probeInterval }

// StartDeadWatcher starts the watchers that ask for a reconnect: the transport's own read-loop-exited
// signal, and a liveness probe for the case that signal cannot see. Both are skipped for a modem or
// stats provider that does not support them, and both stop on Close.
func (m *State) StartDeadWatcher(reconnectCh chan<- struct{}) {
	m.watcherDone = make(chan struct{})
	done := m.watcherDone

	if d, ok := m.Modem.(interface{ Dead() <-chan struct{} }); ok {
		dead := d.Dead()
		go func() {
			select {
			case <-dead:
				select {
				case reconnectCh <- struct{}{}:
				default:
				}
			case <-done:
			}
		}()
	}

	if lr, ok := m.Stats.(interface{ LastReply() time.Time }); ok {
		go m.probeLiveness(lr, reconnectCh, done)
	}
}

// probeLiveness reconnects a modem that has stopped answering while its port stays open. The read
// loop cannot detect this: a serial read timeout returns (0, nil), not an error, so a device that is
// still enumerated but silent — a USB autosuspend that never resumes, wedged firmware, a stalled
// passthrough — leaves the loop spinning every 100ms and Dead() never fires. Inbound frames are not
// the signal: a quiet mesh is normal, so this asks the modem a question instead of waiting for one.
func (m *State) probeLiveness(lr interface{ LastReply() time.Time }, reconnectCh chan<- struct{}, done <-chan struct{}) {
	misses := 0
	tick := time.NewTicker(probeInterval)
	defer tick.Stop()

	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}

		before := lr.LastReply()
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		m.Stats.Stats(ctx)
		cancel()

		if before.IsZero() {
			// Asked, but never reconnected: this firmware may not implement the queries, and a probe
			// that cannot tell "unsupported" from "dead" would reconnect a working radio forever.
			continue
		}
		if lr.LastReply().After(before) {
			misses = 0
			continue
		}

		misses++
		if misses < probeMisses {
			slog.Warn("modem did not answer a status query",
				"component", "modem", "misses", misses, "limit", probeMisses)
			continue
		}
		slog.Error("modem stopped answering, reconnecting",
			"component", "modem", "silent_for", time.Since(before).Round(time.Second))
		select {
		case reconnectCh <- struct{}{}:
		default:
		}
		return
	}
}

// MuxOptions builds the standard mux options, shared by startup and reconnect.
func MuxOptions(ms *State) []node.MuxOption {
	opts := []node.MuxOption{
		node.WithMuxLogger(slog.Default()),
		node.WithMuxErrorHandler(func(err error) {
			slog.Debug("mux receive error", "component", "modem", "error", err)
			ms.ParseErrors.Add(1)
		}),
	}
	if ms.radioConfig != nil {
		opts = append(opts, node.WithMuxAirtimeEstimator(hardware.LoRaAirtimeEstimator(ms.radioConfig)))
	}
	opts = append(opts, node.WithMuxAirtimeFactor(ms.airtimeFactor))
	return opts
}

// Setup connects the radio: KISS firmware over serial/TCP, openHop Modem firmware over serial/TCP, or a bare SX12xx on the host's SPI bus.
func Setup(ctx context.Context, cfg *config.Config) (*State, error) {
	ms := &State{
		ParseErrors: &atomic.Uint64{},
	}

	conn := *cfg.Connection
	connScheme, connAddr, ok := config.ParseConnection(conn)
	if !ok {
		return nil, fmt.Errorf("invalid connection string: %s", conn)
	}

	radioConfig := &hardware.RadioConfig{
		FreqHz: uint32(*cfg.Freq * 1000000),
		BwHz:   uint32(*cfg.Bw * 1000),
		SF:     *cfg.SF,
		CR:     *cfg.CR,
	}
	ms.radioConfig = radioConfig
	ms.airtimeFactor = cfg.AirtimeFactorOr()
	// The library takes an inverted factor; log the percentage instead.
	slog.Info("airtime budget",
		"duty_cycle_pct", cfg.DutyCyclePercentOr(), "airtime_factor", ms.airtimeFactor)

	var err error
	switch connScheme {
	case "spi":
		err = setupSPI(ms, cfg, connAddr, radioConfig)
	case "openhop":
		err = setupOpenhop(ctx, ms, cfg, connAddr, radioConfig)
	default:
		err = setupKiss(ctx, ms, cfg, connScheme, connAddr, radioConfig)
	}
	if err != nil {
		return nil, err
	}
	return ms, nil
}

// setupKiss connects to MeshCore firmware over KISS and configures its radio.
func setupKiss(ctx context.Context, ms *State, cfg *config.Config, connScheme, connAddr string, radioConfig *hardware.RadioConfig) error {
	var t hardware.Transport
	switch connScheme {
	case "serial":
		t = kissTransport.NewSerialTransport(kissTransport.SerialConfig{
			Port:     connAddr,
			BaudRate: *cfg.BaudRate,
		})
	case "tcp":
		t = kissTransport.NewTCPTransport(kissTransport.TCPConfig{
			Address: connAddr,
		})
	}

	// The firmware holds ONE pending TX slot and drops frames arriving while busy (HW_ERR_TX_BUSY), so sends wait for TX_DONE.
	kissModem := hardware.NewKissModem(
		t,
		hardware.WithSignalReport(true),
		hardware.WithLogger(slog.Default()),
		hardware.WithTxFlowControl(hardware.DefaultTxTimeout),
		hardware.WithTxAirtimeEstimator(hardware.LoRaAirtimeEstimator(radioConfig)),
		hardware.WithHandlerWatchdog(handlerWatchdog),
	)
	kissModem.SetErrorHandler(func(err error) {
		slog.Warn("modem error", "component", "modem", "error", err)
	})

	connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
	defer connectCancel()

	if err := kissModem.Connect(connectCtx); err != nil {
		return fmt.Errorf("kiss connect: %w", err)
	}
	ms.closers = append(ms.closers, kissModem)

	if err := kissModem.SetRadio(radioConfig); err != nil {
		ms.Close()
		return fmt.Errorf("SET_RADIO: %w", err)
	}
	slog.Info("SET_RADIO", "freq", *cfg.Freq, "bw", *cfg.Bw, "sf", *cfg.SF, "cr", *cfg.CR)

	if err := kissModem.SetTxPower(*cfg.TX); err != nil {
		ms.Close()
		return fmt.Errorf("SET_TX_POWER: %w", err)
	}
	slog.Info("SET_TX_POWER", "tx", *cfg.TX)

	ms.Stats = NewKissStatsProvider(kissModem, RadioInfo{
		FreqHz:  uint32(*cfg.Freq * 1000000),
		BwHz:    uint32(*cfg.Bw * 1000),
		SF:      *cfg.SF,
		CR:      *cfg.CR,
		TxPower: *cfg.TX,
	})

	ms.Modem = kissModem

	return nil
}
