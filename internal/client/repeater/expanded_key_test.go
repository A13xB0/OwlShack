package repeater

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/store"
)

type captureRadio struct {
	silentRadio
	sent chan []byte
}

func (r captureRadio) Enqueue(b []byte, _ uint8, _ time.Duration) bool {
	r.sent <- bytes.Clone(b)
	return true
}

// An imported prv.key has no seed, so a secret built from Seed() is one the repeater cannot decrypt and it drops the login.
func TestExpandedKey_RequestsDecryptAtTheRepeater(t *testing.T) {
	t.Parallel()

	self, err := meshcore.NewLocalIdentityFromExpandedKey(bytes.Repeat([]byte{0x5a}, 64))
	if err != nil {
		t.Fatal(err)
	}
	if self.Seed() != [32]byte{} {
		t.Fatal("precondition: an expanded-key identity should have no seed")
	}
	repeater := meshcore.NewLocalIdentityFromSeed([32]byte{0x8d})
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	radio := captureRadio{sent: make(chan []byte, 4)}
	n := node.New(self, radio)
	t.Cleanup(n.Stop)
	n.Peers().Insert(&node.Peer{Identity: repeater.Identity, Name: "OldWestRPT0"})
	rm := NewClient(n, st, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func() uint8 { return 1 })
	pk := hex.EncodeToString(repeater.PublicKeyBytes())

	secret, err := repeater.SharedSecret(self.Identity)
	if err != nil {
		t.Fatal(err)
	}
	sent := func() []byte {
		pkt, err := meshcore.PacketFromBytes(<-radio.sent)
		if err != nil {
			t.Fatal(err)
		}
		return pkt.Payload
	}

	_, _ = rm.SendLogin(pk, "password", 10*time.Millisecond)
	req, err := meshcore.AnonReqFromBytes(sent())
	if err != nil {
		t.Fatal(err)
	}
	if req.Decrypt(secret) == nil {
		t.Error("the repeater cannot decrypt the login")
	}

	_, _ = rm.SendContactTelemetryReq(pk, 10*time.Millisecond)
	if _, err := meshcore.MACThenDecrypt(secret, sent()[2:]); err != nil {
		t.Errorf("the repeater cannot decrypt the telemetry request: %v", err)
	}
}
