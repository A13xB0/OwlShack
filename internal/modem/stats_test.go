package modem

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/meshcore-go/meshcore-go/hardware"
)

// Zero radio params must give 0 rather than a fabricated number the caller would publish.
func TestKissStatsProvider_DerivedValues(t *testing.T) {
	p := &kissStatsProvider{radio: RadioInfo{FreqHz: 917_375_000, BwHz: 62_500, SF: 7, CR: 5}}

	// 16 bytes at SF7/62.5 kHz is tens of ms; the exact figure is the library's.
	if ms := p.EstAirtimeMs(16); ms < 10 || ms > 500 {
		t.Errorf("EstAirtimeMs(16) = %d, want a plausible tens-of-ms figure", ms)
	}
	if score := p.PacketScore(5, 16); score <= 0 || score > 1 {
		t.Errorf("PacketScore(5dB, 16) = %v, want a 0-1 score above 0", score)
	}
	// SF below 7 is out of the score table's range and reports 0.
	if score := p.PacketScore(5, 16); (&kissStatsProvider{}).PacketScore(5, 16) >= score {
		t.Error("an unconfigured radio must score 0, below a configured one")
	}

	if ms := (&kissStatsProvider{}).EstAirtimeMs(16); ms != 0 {
		t.Errorf("EstAirtimeMs with no radio params = %d, want 0", ms)
	}
}

// nil must mean the modem did not answer: a 0 here would read as a radio hearing everything cleanly.
func TestKissLinkStats_FirmwareCountersAbsentUntilPolled(t *testing.T) {
	p := &kissStatsProvider{modem: &hardware.KissModem{}, log: slog.Default()}

	ls := p.LinkStats()
	if ls.RecvErrors != nil || ls.PacketsRecv != nil || ls.PacketsSent != nil {
		t.Errorf("unpolled: RecvErrors=%v PacketsRecv=%v PacketsSent=%v, want all nil",
			ls.RecvErrors, ls.PacketsRecv, ls.PacketsSent)
	}

	p.mu.Lock()
	p.fwCounters = &hardware.FirmwareStats{PacketsRecv: 900, PacketsSent: 12, PacketsErrors: 4}
	p.mu.Unlock()

	ls = p.LinkStats()
	if ls.RecvErrors == nil || *ls.RecvErrors != 4 {
		t.Errorf("RecvErrors = %v, want 4", ls.RecvErrors)
	}
	if ls.PacketsRecv == nil || *ls.PacketsRecv != 900 {
		t.Errorf("PacketsRecv = %v, want 900", ls.PacketsRecv)
	}
	if ls.PacketsSent == nil || *ls.PacketsSent != 12 {
		t.Errorf("PacketsSent = %v, want 12", ls.PacketsSent)
	}
	// KISS measures this one and SPI does not; the reverse of the fields above.
	if ls.HwDecodeErrors == nil {
		t.Error("HwDecodeErrors = nil on KISS, where a SETHARDWARE frame can fail to decode")
	}
}

type frameFeed struct {
	handler func(*hardware.KissFrame)
	dead    chan struct{}
}

func (f *frameFeed) Connect(context.Context) error               { return nil }
func (f *frameFeed) Close() error                                { return nil }
func (f *frameFeed) Send([]byte) error                           { return nil }
func (f *frameFeed) SetFrameHandler(h func(*hardware.KissFrame)) { f.handler = h }
func (f *frameFeed) SetErrorHandler(func(error))                 {}
func (f *frameFeed) Dead() <-chan struct{}                       { return f.dead }

// Firmware without a query still answers it, with HW_ERR_UNKNOWN_CMD; ignoring that reply read a live board as one that never answered.
func TestKissStatsProvider_AnErrorReplyIsAnAnswer(t *testing.T) {
	feed := &frameFeed{dead: make(chan struct{})}
	km := hardware.NewKissModem(feed)
	if err := km.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer km.Close()
	p := NewKissStatsProvider(km, RadioInfo{})

	feed.handler(&hardware.KissFrame{Command: hardware.KISS_CMD_SETHARDWARE, Data: []byte{hardware.HW_RESP_ERROR, hardware.HW_ERR_UNKNOWN_CMD}})
	for deadline := time.Now().Add(2 * time.Second); p.LastReply().IsZero(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("an HW_RESP_ERROR reply did not count as the board answering")
		}
	}
}
