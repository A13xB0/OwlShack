package companion

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/client/repeater"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/modem"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func mode(m string) *string { return &m }

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

func telemetryCompanion(t *testing.T, cfg config.CompanionConfig, hook func() ([]sensor.ChannelEntry, []sensor.Status)) *Companion {
	t.Helper()
	return &Companion{
		cfg:       cfg,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		stats:     fakeStats{device: modem.DeviceStats{BatteryMV: 4000, HaveBattery: true}},
		telemetry: hook,
	}
}

var directRequest = &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0)}

func replyHex(t *testing.T, c *Companion, granted, mask byte) (string, bool) {
	t.Helper()
	body, ok := c.telemetryReply(granted, mask, directRequest)
	return strings.ToUpper(hex.EncodeToString(body)), ok
}

// Every class defaults to deny, so a companion that has never been configured says nothing at all.
func TestTelemetry_SilentUntilAnOperatorAllowsIt(t *testing.T) {
	c := telemetryCompanion(t, config.CompanionConfig{}, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))
	if _, ok := replyHex(t, c, sensor.PermAll, 0); ok {
		t.Fatal("a companion with no telemetry modes set answered a request")
	}

	// Provokes the negative above: the same request is answered once base is allowed.
	c.cfg.TelemetryBase = mode(config.TelemetryContacts)
	if _, ok := replyHex(t, c, sensor.PermAll, 0); !ok {
		t.Fatal("allowing the base class did not make the node answer, so the silence proves nothing")
	}
}

// Base gates the whole reply in the firmware, so sensors alone can never be published.
func TestTelemetry_BaseGatesEveryClass(t *testing.T) {
	c := telemetryCompanion(t, config.CompanionConfig{
		TelemetryEnvironment: mode(config.TelemetryContacts),
		TelemetryLocation:    mode(config.TelemetryContacts),
	}, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))

	if _, ok := replyHex(t, c, sensor.PermAll, 0); ok {
		t.Fatal("sensors were published to a requester the base class denies")
	}
}

func TestTelemetry_ClassModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     config.CompanionConfig
		granted byte
		mask    byte
		want    string
	}{
		{
			name: "every contact reads what is allowed",
			cfg: config.CompanionConfig{
				TelemetryBase:        mode(config.TelemetryContacts),
				TelemetryEnvironment: mode(config.TelemetryContacts),
			},
			want: "01740190026700E1",
		},
		{
			name: "a selected class needs this contact's grant",
			cfg: config.CompanionConfig{
				TelemetryBase:        mode(config.TelemetryContacts),
				TelemetryEnvironment: mode(config.TelemetrySelected),
			},
			want: "01740190", // the map stays behind
		},
		{
			name: "the grant unlocks the selected class",
			cfg: config.CompanionConfig{
				TelemetryBase:        mode(config.TelemetryContacts),
				TelemetryEnvironment: mode(config.TelemetrySelected),
			},
			granted: sensor.PermEnvironment,
			want:    "01740190026700E1",
		},
		{
			name: "the requester's inverse mask narrows it further",
			cfg: config.CompanionConfig{
				TelemetryBase:        mode(config.TelemetryContacts),
				TelemetryEnvironment: mode(config.TelemetryContacts),
			},
			mask: sensor.PermEnvironment,
			want: "01740190",
		},
		{
			name: "an unrecognised mode denies rather than grants",
			cfg: config.CompanionConfig{
				TelemetryBase: mode("allow-all"),
			},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := telemetryCompanion(t, tc.cfg, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))
			got, ok := replyHex(t, c, tc.granted, tc.mask)
			if tc.want == "" {
				if ok {
					t.Fatalf("answered %s, want silence", got)
				}
				return
			}
			if !ok {
				t.Fatal("the node refused to answer")
			}
			if got != tc.want {
				t.Errorf("reply %s, want %s", got, tc.want)
			}
		})
	}
}

// An oversized reply is refused on the way out, which the requester cannot tell from a node that is off the air.
func TestTelemetry_OversizedMapFallsBackToTheNodesOwnChannel(t *testing.T) {
	c := telemetryCompanion(t, config.CompanionConfig{
		TelemetryBase:        mode(config.TelemetryContacts),
		TelemetryEnvironment: mode(config.TelemetryContacts),
	}, func() ([]sensor.ChannelEntry, []sensor.Status) {
		var entries []sensor.ChannelEntry
		for ch := byte(2); ch < 60; ch++ {
			entries = append(entries, sensor.ChannelEntry{
				Channel: ch, Type: meshcore.LPPTemperature, SensorID: 1, Metric: sensor.Temperature,
			})
		}
		return entries, []sensor.Status{{
			Spec:     sensor.Spec{ID: 1},
			Readings: []sensor.Reading{{Metric: sensor.Temperature, Value: 22.5}},
			At:       time.Now(),
		}}
	})
	got, ok := replyHex(t, c, sensor.PermAll, 0)
	if !ok {
		t.Fatal("the node refused to answer")
	}
	if got != "01740190" {
		t.Fatalf("reply %s, want the node's own channel only", got)
	}
}

// The firmware sends a position only from a GPS module, never the one typed in for adverts, and no host here has one.
func TestTelemetry_NeverSendsATypedInPosition(t *testing.T) {
	lat, lon := -41.2865, 174.7762
	c := telemetryCompanion(t, config.CompanionConfig{
		TelemetryBase:     mode(config.TelemetryContacts),
		TelemetryLocation: mode(config.TelemetryContacts),
		Latitude:          &lat, Longitude: &lon,
	}, nil)

	got, ok := replyHex(t, c, sensor.PermAll, 0)
	if !ok || got != "01740190" {
		t.Fatalf("reply %s (answered %v), want the battery and no position", got, ok)
	}
}

// The editor's budget has to hold on the companion's own reply path, or a saved map goes out as channel 1 alone.
func TestTelemetry_AMapAtTheBudgetReachesTheRequester(t *testing.T) {
	c := telemetryCompanion(t, config.CompanionConfig{
		TelemetryBase:        mode(config.TelemetryContacts),
		TelemetryEnvironment: mode(config.TelemetryContacts),
	}, func() ([]sensor.ChannelEntry, []sensor.Status) {
		var entries []sensor.ChannelEntry
		for ch := 2; ch <= 24; ch++ {
			entries = append(entries, sensor.ChannelEntry{Channel: byte(ch), Type: meshcore.LPPGenericSensor, SensorID: 1, Metric: sensor.Temperature})
		}
		entries = append(entries,
			sensor.ChannelEntry{Channel: 25, Type: meshcore.LPPTemperature, SensorID: 1, Metric: sensor.Temperature},
			sensor.ChannelEntry{Channel: 26, Type: meshcore.LPPRelativeHumidity, SensorID: 1, Metric: sensor.Humidity})
		return entries, []sensor.Status{{
			Spec:     sensor.Spec{ID: 1},
			Readings: []sensor.Reading{{Metric: sensor.Temperature, Value: 22.5}, {Metric: sensor.Humidity, Value: 50}},
			At:       time.Now(),
		}}
	})
	c.stats = fakeStats{device: modem.DeviceStats{BatteryMV: 4000, HaveBattery: true, MCUTempC: 21.5, HaveMCUTemp: true}}

	body, ok := c.telemetryReply(sensor.PermAll, 0, directRequest)
	if !ok {
		t.Fatal("the node refused to answer")
	}
	if want := sensor.MaxTelemetryPayload + 8; len(body) != want {
		t.Fatalf("reply is %d bytes, want %d: the map at the budget beside the node's own 8", len(body), want)
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

// handleReq as the air sees it: an encrypted request in, the reply decrypted as the contact would.
func TestHandleReq_AnswersAContactAndNotAStranger(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	self := meshcore.NewLocalIdentityFromSeed([32]byte{1})
	friend := meshcore.NewLocalIdentityFromSeed([32]byte{2})
	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{3})
	comp := store.Companion{Name: "home"}
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &comp); err == nil {
			err = st.Contacts.Add(ctx, comp.ID, friend.PublicKeyBytes(), "friend", "CHAT")
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	radio := &recordingRadio{}
	n := node.New(self, radio)
	t.Cleanup(n.Stop)
	// Heard advertising, so only the contact gate keeps the stranger unanswered.
	n.Peers().Insert(&node.Peer{Identity: stranger.Identity, Name: "stranger"})
	c := telemetryCompanion(t, config.CompanionConfig{
		ID:                   comp.ID,
		TelemetryBase:        mode(config.TelemetryContacts),
		TelemetryEnvironment: mode(config.TelemetryContacts),
	}, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))
	c.node, c.store, c.runCtx = n, st, ctx

	reply := func(from meshcore.LocalIdentity) (string, bool) {
		t.Helper()
		secret, _ := from.SharedSecret(self.Identity)
		plain := []byte{7, 0, 0, 0, reqTypeGetTelemetryData, 0}
		enc, err := meshcore.EncryptThenMAC(secret, plain)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := (&meshcore.Request{
			Destination: self.PublicKey()[0], Source: from.PublicKey()[0], MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:],
		}).ToBytes()
		c.handleReq(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0), Payload: payload})

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
		got := resp.Decrypt(secret)
		if len(got) < 4 || binary.LittleEndian.Uint32(got) != 7 {
			t.Fatalf("the reply %x does not reflect the request's tag", got)
		}
		return strings.ToUpper(hex.EncodeToString(bytes.TrimRight(got[4:], "\x00"))), true
	}

	if got, ok := reply(friend); !ok || got != "01740190026700E1" {
		t.Errorf("the contact got %q (answered %v), want the battery and the map", got, ok)
	}
	if _, ok := reply(stranger); ok {
		t.Error("answered a stranger whose shared secret is no credential")
	}
}

// hashSizeCompanion is a companion on a recording radio with one contact, friend, and a two-byte path hash size.
func hashSizeCompanion(t *testing.T) (*Companion, *recordingRadio, meshcore.LocalIdentity, meshcore.LocalIdentity) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	self := meshcore.NewLocalIdentityFromSeed([32]byte{1})
	friend := meshcore.NewLocalIdentityFromSeed([32]byte{2})
	two := 2
	// The stored row carries the size too, as it does on a real node, so a contact added now takes it.
	comp := store.Companion{Name: "home", PathHashSize: &two}
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &comp); err == nil {
			err = st.Contacts.Add(ctx, comp.ID, friend.PublicKeyBytes(), "friend", "CHAT")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	radio := &recordingRadio{}
	n := node.New(self, radio)
	t.Cleanup(n.Stop)
	c := telemetryCompanion(t, config.CompanionConfig{
		ID: comp.ID, PathHashSize: &two,
		TelemetryBase: mode(config.TelemetryContacts),
	}, nil)
	c.node, c.store, c.runCtx = n, st, ctx
	return c, radio, self, friend
}

// floodHashSize is the hash size of the one flood the radio sent, failing if it sent anything else.
func floodHashSize(t *testing.T, radio *recordingRadio) int {
	t.Helper()
	sent := radio.take()
	if len(sent) != 1 {
		t.Fatalf("%d packets sent, want one", len(sent))
	}
	pkt, err := meshcore.PacketFromBytes(sent[0])
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.IsRouteFlood() {
		t.Fatal("the packet went direct, so this proves nothing about a flood")
	}
	return int(pkt.PathHashSize())
}

// A reply with no route back floods at the contact's bytes per hop, and a path return at the companion's (sendFloodScoped); both are 2 here.
func TestHandleReq_RepliesFloodAtTwoBytes(t *testing.T) {
	c, radio, self, friend := hashSizeCompanion(t)
	secret, _ := friend.SharedSecret(self.Identity)
	enc, err := meshcore.EncryptThenMAC(secret, []byte{7, 0, 0, 0, reqTypeGetTelemetryData, 0})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := (&meshcore.Request{
		Destination: self.PublicKey()[0], Source: friend.PublicKey()[0], MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:],
	}).ToBytes()

	for name, req := range map[string]*meshcore.Packet{
		// No route to the contact yet, so the datagram floods.
		"direct": {Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, 0), Payload: payload},
		// Answered with a path return, which floods too.
		"flooded": {Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeReq, 0), PathLength: 1<<6 | 1, Path: []byte{0xAA, 0xBB}, Payload: payload},
	} {
		c.handleReq(req)
		if hs := floodHashSize(t, radio); hs != 2 {
			t.Errorf("%s request: the reply flooded with %d-byte hashes, want 2", name, hs)
		}
	}
}

// A DM's ACK floods at the contact's bytes per hop with no route back, and rides a path return when the DM was flooded.
func TestSendDMAck_FloodsAtTwoBytes(t *testing.T) {
	c, radio, self, friend := hashSizeCompanion(t)
	secret, _ := friend.SharedSecret(self.Identity)
	ack := []byte{1, 2, 3, 4, 0, 9}
	for name, dm := range map[string]*meshcore.Packet{
		"direct":  {Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)},
		"flooded": {Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), PathLength: 1},
	} {
		if dm.PathLength == 1 {
			dm.Path = []byte{0xAA}
		}
		c.sendDMAck(dm, friend.PublicKeyBytes(), secret, ack)
		if hs := floodHashSize(t, radio); hs != 2 {
			t.Errorf("%s DM: the ACK flooded with %d-byte hashes, want 2", name, hs)
		}
	}
}

// A contact's own bytes per hop, not the companion's, frames an ACK and a DM with no route back.
func TestContactHashSize_FramesACKsAndDMs(t *testing.T) {
	c, radio, self, friend := hashSizeCompanion(t)
	secret, _ := friend.SharedSecret(self.Identity)
	var err error
	c.store.WriteSync(func() {
		err = c.store.Contacts.SetRoute(c.runCtx, c.cfg.ID, friend.PublicKeyBytes(), nil, 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	c.sendDMAck(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}, friend.PublicKeyBytes(), secret, []byte{1, 2, 3, 4})
	if hs := floodHashSize(t, radio); hs != 3 {
		t.Errorf("the ACK flooded with %d-byte hashes, want the contact's 3", hs)
	}
	c.repeaters = repeater.NewClient(c.node, c.store, c.cfg.ID, c.log, nil, c.pathHashSize) // owns the timestamp counter DMs share
	if err := c.SendContactMessage(hex.EncodeToString(friend.PublicKeyBytes()), "hi"); err != nil {
		t.Fatal(err)
	}
	if hs := floodHashSize(t, radio); hs != 3 {
		t.Errorf("the DM flooded with %d-byte hashes, want the contact's 3", hs)
	}
}

// A node that is not a contact has no size of its own, so its ACK floods at the companion's.
func TestBytesPerHop_ANonContactGetsTheCompanionsOwn(t *testing.T) {
	c, radio, self, _ := hashSizeCompanion(t)
	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{9})
	secret, _ := stranger.SharedSecret(self.Identity)
	c.sendDMAck(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}, stranger.PublicKeyBytes(), secret, []byte{1, 2, 3, 4})
	if hs := floodHashSize(t, radio); hs != 2 {
		t.Errorf("a stranger's ACK flooded with %d-byte hashes, want the companion's 2", hs)
	}
}

// A 0-hop route has no hashes to carry a size, so the length byte takes the contact's.
func TestSendDMAck_ZeroHopCarriesTheContactsSize(t *testing.T) {
	c, radio, self, friend := hashSizeCompanion(t)
	secret, _ := friend.SharedSecret(self.Identity)
	var err error
	c.store.WriteSync(func() {
		err = c.store.Contacts.SetRoute(c.runCtx, c.cfg.ID, friend.PublicKeyBytes(), []byte{}, 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	c.node.Peers().Insert(&node.Peer{Identity: friend.Identity, Name: "friend"})
	c.node.Peers().SetOutPath(friend.PublicKey(), []byte{}, 1)
	c.sendDMAck(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}, friend.PublicKeyBytes(), secret, []byte{1, 2, 3, 4})
	sent := radio.take()
	if len(sent) != 1 {
		t.Fatalf("%d packets sent, want one", len(sent))
	}
	pkt, err := meshcore.PacketFromBytes(sent[0])
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.IsRouteDirect() || pkt.PathLength != 0x80 {
		t.Errorf("sent as 0x%02x with length byte 0x%02x, want direct at 0x80 (3 bytes, 0 hops)", pkt.RouteType(), pkt.PathLength)
	}
}

// A grant opens only the classes set to "selected", or granting both lets a contact read a class the operator denied.
func TestTelemetry_AGrantCannotOpenADeniedClass(t *testing.T) {
	lat, lon := -41.2865, 174.7762
	c := telemetryCompanion(t, config.CompanionConfig{
		TelemetryBase:        mode(config.TelemetryContacts),
		TelemetryLocation:    mode(config.TelemetryDeny),
		TelemetryEnvironment: mode(config.TelemetrySelected),
		Latitude:             &lat, Longitude: &lon,
	}, oneRow(2, meshcore.LPPTemperature, sensor.Temperature, 22.5))

	got, _ := replyHex(t, c, sensor.PermLocation|sensor.PermEnvironment, 0)
	if strings.Contains(got, "0188") {
		t.Errorf("reply %s carries a position the operator denied", got)
	}
	if !strings.Contains(got, "026700E1") {
		t.Errorf("reply %s lacks the map the grant did open, so the test proves nothing", got)
	}
}
