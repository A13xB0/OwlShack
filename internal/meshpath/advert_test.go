package meshpath

import (
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// A zero-hop advert's length byte is always 0, so reading its size would call every direct neighbour a 1-byte node.
func TestAdvertHashSize(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pkt  meshcore.Packet
		want uint8
	}{
		"flood at 2, heard first-hand":  {meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), PathLength: 0x40}, 2},
		"flood at 3 after a hop":        {meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), PathLength: 0x81, Path: []byte{1, 2, 3}}, 3},
		"transport flood at 2":          {meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeTransportFlood, meshcore.PayloadTypeAdvert, 0), PathLength: 0x40}, 2},
		"zero-hop, sent direct at 0x00": {meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeAdvert, 0)}, 0},
	} {
		if got := AdvertHashSize(&tc.pkt); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}
