package repeater

import (
	"encoding/binary"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// The remote answers a request with its tag; Wait hands back what follows it.
func TestStartRequest_ReplyResolvesTheWait(t *testing.T) {
	for _, anon := range []bool{false, true} {
		rm, _, pubkey := routeTestClient(t)
		peer := meshcore.NewLocalIdentityFromSeed([32]byte{0x8d})
		key := pubkeyArray(pubkey)

		p, err := rm.StartRequest(key, []byte{0x03, 0, 0, 0, 0}, RequestOptions{Anon: anon, Floor: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if !p.Flood || p.Tag == 0 {
			t.Fatalf("pending %+v, want a flood with a tag", p)
		}

		secret, _ := peer.SharedSecret(rm.node.Identity().Identity)
		plain := binary.LittleEndian.AppendUint32(nil, p.Tag)
		plain = append(plain, 0xde, 0xad)
		enc, err := meshcore.EncryptThenMAC(secret, plain)
		if err != nil {
			t.Fatal(err)
		}
		self := rm.node.Identity().PublicKey()
		payload, _ := (&meshcore.Response{Destination: self[0], Source: pubkey[0], MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:]}).ToBytes()
		rm.HandleResponsePacket(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 0), Payload: payload})

		data, err := p.Wait(t.Context())
		if err != nil || len(data) < 2 || data[0] != 0xde || data[1] != 0xad {
			t.Errorf("anon=%v: Wait = %x, %v; want the bytes after the tag", anon, data, err)
		}
		rm.pendingMu.Lock()
		left := len(rm.pending)
		rm.pendingMu.Unlock()
		if left != 0 {
			t.Errorf("anon=%v: %d requests still pending after the reply", anon, left)
		}
	}
}

func TestStartRequest_TimesOut(t *testing.T) {
	rm, _, pubkey := routeTestClient(t)
	p, err := rm.StartRequest(pubkeyArray(pubkey), []byte{0x01}, RequestOptions{Floor: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(t.Context()); err == nil {
		t.Error("a request nobody answered resolved")
	}
}
