package modem

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-go/meshcore-go/hardware/openhop"
)

// fakeOpenhop answers the handshake and STATUS until hung, speaking the real frame format.
func fakeOpenhop(t *testing.T, conn net.Conn, hung *atomic.Bool) {
	t.Helper()
	go func() {
		defer conn.Close()
		var buf []byte
		chunk := make([]byte, 512)
		for {
			n, err := conn.Read(chunk)
			if err != nil {
				return
			}
			buf = append(buf, chunk[:n]...)
			for len(buf) >= 6 {
				if buf[0] != openhop.Sync {
					buf = buf[1:]
					continue
				}
				size := int(binary.LittleEndian.Uint16(buf[2:4]))
				if len(buf) < 6+size {
					break
				}
				cmd := buf[1]
				buf = buf[6+size:]
				var reply []byte
				switch cmd {
				case openhop.CmdPing:
					reply = openhop.EncodeFrame(openhop.CmdPong, nil)
				case openhop.CmdSetConfig:
					reply = openhop.EncodeFrame(openhop.CmdConfigResp, make([]byte, openhop.RadioConfigSize))
				case openhop.CmdStatusReq:
					if !hung.Load() {
						reply = openhop.EncodeFrame(openhop.CmdStatusResp, make([]byte, 24))
					}
				}
				if reply != nil {
					if _, err := conn.Write(reply); err != nil {
						return
					}
				}
			}
		}
	}()
}

// A serial link has no idle deadline, so the probe's STATUS is the only thing that notices a board that stopped answering.
func TestOpenhopStatsProvider_LastReplyFollowsStatus(t *testing.T) {
	host, board := net.Pipe()
	var hung atomic.Bool
	fakeOpenhop(t, board, &hung)
	dial := func(context.Context) (io.ReadWriteCloser, error) { return host, nil }

	m := openhop.New(dial, openhop.Config{Radio: openhop.RadioConfig{FreqHz: 917_375_000, BandwidthHz: 62_500, SF: 7, CR: 5, SyncWord: 0x12, PreambleLen: 16}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.Connect(ctx); err != nil {
		t.Fatalf("connect to the fake board: %v", err)
	}
	defer m.Close()

	p := NewOpenhopStatsProvider(m, RadioInfo{})
	var lr interface{ LastReply() time.Time } = p // what StartDeadWatcher needs to start the probe
	if !lr.LastReply().IsZero() {
		t.Fatal("LastReply set before the board was asked anything")
	}
	if p.ConnectedAt().IsZero() {
		t.Fatal("ConnectedAt is zero, so a board that never answers would read as never asked")
	}

	p.Stats(ctx)
	answered := p.LastReply()
	if answered.IsZero() {
		t.Fatal("a STATUS answer did not move LastReply")
	}

	hung.Store(true)
	short, cancelShort := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelShort()
	p.Stats(short)
	if got := p.LastReply(); !got.Equal(answered) {
		t.Fatalf("LastReply moved to %v with the board hung", got)
	}
}
