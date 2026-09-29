package modem

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/meshcore-go/meshcore-go/hardware/openhop"
)

// openhopStatsProvider reads the openHop firmware's own counters. Unlike KISS, one STATUS command
// answers with every reading at once, so there is nothing to fan out and nothing to wait for.
type openhopStatsProvider struct {
	modem     *openhop.Modem
	radio     RadioInfo
	startTime time.Time
	log       *slog.Logger

	mu     sync.Mutex
	last   openhop.Status
	lastAt time.Time
}

func NewOpenhopStatsProvider(m *openhop.Modem, radio RadioInfo) *openhopStatsProvider {
	return &openhopStatsProvider{
		modem:     m,
		radio:     radio,
		startTime: time.Now(),
		log:       slog.Default().With("component", "stats", "type", "openhop"),
	}
}

func (p *openhopStatsProvider) Transport() string { return "openhop" }

// LastReply is the modem's last STATUS answer. It is what starts the liveness probe, which a serial
// link needs: TCP drops a silent link after 60 s, but serial has no idle deadline, so a hung board
// otherwise stays connected and healthy-looking.
func (p *openhopStatsProvider) LastReply() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAt
}

// ConnectedAt is when this link was set up, the start of a silence for a board that has never answered.
func (p *openhopStatsProvider) ConnectedAt() time.Time { return p.startTime }

// Connected reports the live link state. This driver reconnects on its own, so the modem outlives a
// dropped link and its existence alone would report a radio that is not there as healthy.
func (p *openhopStatsProvider) Connected() bool { return p.modem.Connected() }

func (p *openhopStatsProvider) RadioConfig() RadioInfo { return p.radio }

func (p *openhopStatsProvider) Stats(ctx context.Context) DeviceStats {
	st, err := p.modem.Status(ctx)
	if err != nil {
		p.log.Debug("status unavailable", "error", err)
		return p.snapshot()
	}
	p.mu.Lock()
	p.last, p.lastAt = st, time.Now()
	p.mu.Unlock()
	return p.snapshot()
}

func (p *openhopStatsProvider) CachedStats() DeviceStats { return p.snapshot() }

// snapshot drops the board readings once the modem stops answering, so a stale voltage cannot look
// like a healthy board. UptimeSecs is time since this link came up, as the other transports report
// it — the modem's own uptime would make one field mean two different things.
func (p *openhopStatsProvider) snapshot() DeviceStats {
	p.mu.Lock()
	st, at := p.last, p.lastAt
	p.mu.Unlock()

	ds := DeviceStats{UptimeSecs: uint32(time.Since(p.startTime).Seconds())}
	if at.IsZero() || time.Since(at) > StaleReadingAfter {
		return ds
	}
	ds.NoiseFloor = int16(st.NoiseFloor)
	ds.BatteryMV, ds.HaveBattery = st.BatteryMV, st.BatteryValid
	ds.MCUTempC, ds.HaveMCUTemp = float64(st.TempC), st.TempValid
	return ds
}

// LinkStats leaves the KISS framing fields nil: openHop frames carry their own CRC and length, so
// none of those faults exist here. CRCErrors comes from the last STATUS, which is the chip's count.
func (p *openhopStatsProvider) LinkStats() LinkStats {
	s := p.modem.Stats()
	recv, sent := s.RxPackets, s.TxPackets

	p.mu.Lock()
	crc := uint64(p.last.CRCErrors)
	fresh := !p.lastAt.IsZero()
	p.mu.Unlock()

	ls := LinkStats{
		InboundDroppedNew: s.InboundDropped,
		PacketsRecv:       &recv,
		PacketsSent:       &sent,
	}
	if fresh {
		ls.CRCErrors = &crc
	}
	return ls
}

func (p *openhopStatsProvider) EstAirtimeMs(packetLen int) uint32 {
	return p.modem.AirtimeEstimator()(packetLen)
}

func (p *openhopStatsProvider) PacketScore(snrDB float64, packetLen int) float64 {
	return p.modem.PacketScore(snrDB, packetLen)
}

var _ StatsProvider = (*openhopStatsProvider)(nil)
