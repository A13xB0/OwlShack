package repeater

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// oneRow is a channel map of a single reading, with the sensor behind it already read.
func oneRow(ch, typ byte, metric sensor.Metric, v float64) func() ([]sensor.ChannelEntry, []sensor.Status) {
	return func() ([]sensor.ChannelEntry, []sensor.Status) {
		return []sensor.ChannelEntry{{Channel: ch, Type: typ, SensorID: 1, Metric: metric}},
			[]sensor.Status{{
				Spec:     sensor.Spec{ID: 1},
				Readings: []sensor.Reading{{Metric: metric, Value: v}},
				At:       time.Now(),
			}}
	}
}

func telemetryRepeater(t *testing.T, hook func() ([]sensor.ChannelEntry, []sensor.Status)) *Repeater {
	t.Helper()
	r := &Repeater{
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		telemetry: hook,
	}
	r.batteryMV.Store(4000)
	r.haveBattery.Store(true)
	return r
}

// params[0] is an inverse mask, so 0x00 asks for everything.
func telemetryReply(t *testing.T, r *Repeater, role int, mask byte) string {
	t.Helper()
	body, ok := r.buildReqResponse(&store.RepeaterACLEntry{Permissions: role}, reqTypeGetTelemetryData, []byte{mask}, sensor.MaxReplyBody(nil))
	if !ok {
		t.Fatal("the node refused to answer a telemetry request")
	}
	return strings.ToUpper(hex.EncodeToString(body))
}

// Both have to arrive in one reply; either alone looks like a working node with half its readings missing.
func TestTelemetry_CarriesTheNodesOwnChannelAndTheMap(t *testing.T) {
	r := telemetryRepeater(t, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))

	// 4.0V at 0.01 on channel 1, then 22.5C at 0.1 on channel 2.
	const want = "01740190026700E1"
	if got := telemetryReply(t, r, permReadOnly, 0x00); got != want {
		t.Fatalf("reply %s, want %s", got, want)
	}
}

func TestTelemetry_EnvironmentPermissionGatesTheMap(t *testing.T) {
	r := telemetryRepeater(t, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))

	// Masking out the environment class leaves the node's own channel behind.
	got := telemetryReply(t, r, permReadOnly, sensor.PermEnvironment)
	if got != "01740190" {
		t.Fatalf("reply %s, want the node's own channel only", got)
	}

	// Provokes the negative above: the same map does publish when the permission is there.
	if again := telemetryReply(t, r, permReadOnly, 0x00); !strings.Contains(again, "026700E1") {
		t.Fatalf("reply %s carries no map, so the gate test proves nothing", again)
	}
}

// The firmware downgrades a guest to base (simple_repeater handleRequest), and a blank guest password admits anyone.
func TestTelemetry_AGuestGetsTheNodesOwnChannelOnly(t *testing.T) {
	r := telemetryRepeater(t, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))

	if got := telemetryReply(t, r, permGuest, 0x00); got != "01740190" {
		t.Fatalf("a guest got %s, want the node's own channel only", got)
	}
	if got := telemetryReply(t, r, permReadOnly, 0x00); !strings.Contains(got, "026700E1") {
		t.Fatalf("a read-only login got %s, no map, so the guest test proves nothing", got)
	}
}

// Checked against the packet the send path builds and the frame the requester's companion hands its app: one byte more and one of them drops it.
func TestMaxReplyBody_IsTheMostTheRequestersAppReceives(t *testing.T) {
	secret := bytes.Repeat([]byte{0x5A}, 32)
	for _, path := range []int{-1, 0, 1, 2, 3, 17, 40} {
		req := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0)}
		route := 0
		if path >= 0 {
			req = &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeReq, 0), Path: make([]byte, path)}
			route = 1 + path + 1
		}
		received := func(body int) bool {
			payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
				return (&meshcore.Response{Destination: 1, Source: 2, MAC: mac, EncryptedPayload: enc}).ToBytes()
			}, make([]byte, route+sensor.ReplyTagLen+body))
			if err != nil {
				t.Fatalf("encPacket: %v", err)
			}
			// companion_radio pushes [code, reserved, key prefix:6] then what it decrypted past the route and tag, padding included.
			frame := 8 + len(payload) - 4 - route - sensor.ReplyTagLen
			return (&meshcore.Packet{Payload: payload}).Validate() == nil && frame <= 176
		}
		limit := sensor.MaxReplyBody(req)
		if !received(limit) {
			t.Errorf("path %d (-1 is direct): a %d-byte body is dropped, so the limit is too high", path, limit)
		}
		if received(limit + 1) {
			t.Errorf("path %d (-1 is direct): a %d-byte body arrives too, so the limit is lower than it could be", path, limit+1)
		}
	}
}

// A map validated at save can still grow when a sensor is added, and the reply must stay inside one packet.
func TestTelemetry_FallsBackToTheNodesOwnChannelWhenTheMapIsTooBig(t *testing.T) {
	// Sized past what one reply carries but inside a packet, the window where a map could go out as a frame nothing receives.
	r := telemetryRepeater(t, manyRows(2, 30, meshcore.LPPGenericSensor, 123456))

	overSize := 29 * 6
	if overSize <= sensor.MaxReplyBody(nil) || overSize > maxPacketPayload {
		t.Fatalf("a %d-byte map is not in the window this test exists for (%d..%d]",
			overSize, sensor.MaxReplyBody(nil), maxPacketPayload)
	}

	got := telemetryReply(t, r, permReadOnly, 0x00)
	if len(got)/2 > sensor.MaxReplyBody(nil) {
		t.Fatalf("reply is %d bytes, over the %d one can carry", len(got)/2, sensor.MaxReplyBody(nil))
	}
	if got != "01740190" {
		t.Fatalf("reply %s, want the node's own channel only", got)
	}
}

func TestTelemetry_AnswersWithNothingConfigured(t *testing.T) {
	r := telemetryRepeater(t, nil)
	if got := telemetryReply(t, r, permReadOnly, 0x00); got != "01740190" {
		t.Fatalf("reply %s, want the node's own channel only", got)
	}
}

// manyRows is a map wide enough to outgrow one reply, one type per channel.
func manyRows(from, to int, typ byte, v float64) func() ([]sensor.ChannelEntry, []sensor.Status) {
	return func() ([]sensor.ChannelEntry, []sensor.Status) {
		var entries []sensor.ChannelEntry
		for ch := from; ch <= to; ch++ {
			entries = append(entries, sensor.ChannelEntry{
				Channel: byte(ch), Type: typ, SensorID: 1, Metric: sensor.Temperature,
			})
		}
		return entries, []sensor.Status{{
			Spec:     sensor.Spec{ID: 1},
			Readings: []sensor.Reading{{Metric: sensor.Temperature, Value: v}},
			At:       time.Now(),
		}}
	}
}

// recordingRadio is a TxRadio, so node.New sends straight to it and a test reads what went out.
type recordingRadio struct {
	mu   sync.Mutex
	sent [][]byte
}

func (r *recordingRadio) SendData(d []byte) error                             { r.record(d); return nil }
func (r *recordingRadio) SetDataHandler(func(*meshcore.Packet))               {}
func (r *recordingRadio) SetRawDataHandler(func([]byte, float32, int8, bool)) {}
func (r *recordingRadio) AddOutboundHandler(func([]byte))                     {}
func (r *recordingRadio) Close() error                                        { return nil }
func (r *recordingRadio) Enqueue(d []byte, _ uint8, _ time.Duration) bool     { r.record(d); return true }
func (r *recordingRadio) TxQueueLen() int                                     { return 0 }

func (r *recordingRadio) record(d []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, bytes.Clone(d))
}

func (r *recordingRadio) take() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

// telemetryRequest is a REQ as a client sends it, [tag:4][type][inverse mask], encrypted to the node.
func telemetryRequest(t *testing.T, from meshcore.LocalIdentity, to meshcore.Identity, tag uint32) *meshcore.Packet {
	t.Helper()
	secret, err := from.SharedSecret(to)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 6)
	binary.LittleEndian.PutUint32(plain, tag)
	plain[4] = reqTypeGetTelemetryData
	payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
		return (&meshcore.Request{Destination: to.PublicKey()[0], Source: from.PublicKey()[0], MAC: mac, EncryptedPayload: enc}).ToBytes()
	}, plain)
	if err != nil {
		t.Fatal(err)
	}
	return &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0), Payload: payload}
}

// handleReq as the air sees it: an encrypted request in, and the reply decrypted as its client would.
func TestHandleReq_AnswersALoggedInClientAndNoOneElse(t *testing.T) {
	self := meshcore.NewLocalIdentityFromSeed([32]byte{1})
	guest := meshcore.NewLocalIdentityFromSeed([32]byte{2})
	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{3})
	radio := &recordingRadio{}
	r := telemetryRepeater(t, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))
	r.node = node.New(self, radio)
	t.Cleanup(r.node.Stop)
	r.routes.m = map[[32]byte]clientRoute{}
	key := hex.EncodeToString(guest.PublicKeyBytes())
	r.acl.m = map[string]*store.RepeaterACLEntry{key: {PubKey: key, Permissions: permGuest}}

	reply := func(from meshcore.LocalIdentity, tag uint32) (string, bool) {
		t.Helper()
		r.handleReq(telemetryRequest(t, from, self.Identity, tag))
		sent := radio.take()
		if len(sent) == 0 {
			return "", false
		}
		pkt, err := meshcore.PacketFromBytes(sent[0])
		if err != nil {
			t.Fatal(err)
		}
		resp, err := meshcore.ResponseFromBytes(pkt.Payload)
		if err != nil {
			t.Fatal(err)
		}
		secret, _ := from.SharedSecret(self.Identity)
		plain := resp.Decrypt(secret)
		if len(plain) < 4 || binary.LittleEndian.Uint32(plain) != tag {
			t.Fatalf("the reply %x does not reflect tag %d", plain, tag)
		}
		return strings.ToUpper(hex.EncodeToString(bytes.TrimRight(plain[4:], "\x00"))), true
	}

	if got, ok := reply(guest, 100); !ok || got != "01740190" {
		t.Errorf("a guest got %q (answered %v), want the node's own channel only", got, ok)
	}
	if _, ok := reply(guest, 100); ok {
		t.Error("answered a replayed tag, which the firmware treats as a retry")
	}
	if _, ok := reply(stranger, 200); ok {
		t.Error("answered a node that never logged in")
	}
}
