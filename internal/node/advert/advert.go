// Package advert builds and sends MeshCore self-adverts for the node personalities that announce themselves.
package advert

import (
	"log/slog"
	"math"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

// floodPathLength encodes the path-hash width in BYTES as (size-1) in PathLength's top 2 bits; clamped, a 0 would underflow to 0xC0.
func floodPathLength(pathHashSize int) byte {
	if pathHashSize < 1 || pathHashSize > 3 {
		pathHashSize = 1
	}
	return byte((pathHashSize - 1) << 6)
}

// SendSelf transmits a self-advert: flood is mesh-wide, otherwise zero-hop to direct neighbours only; scope wraps a flood in a transport region.
func SendSelf(n *node.Node, log *slog.Logger, advType, name string, lat, lon *float64, flood bool, pathHashSize int, scope *meshcore.Region) error {
	pkt, err := BuildSelf(n.Identity(), advType, name, lat, lon, flood, pathHashSize, scope)
	if err != nil {
		return err
	}
	mode := "flood"
	if !flood {
		mode = "zero-hop"
	} else if scope != nil {
		mode += " scoped"
	}
	log.Info("sending self-advert", "mode", mode)

	return n.SendPacket(pkt)
}

// BuildSelf is the self-advert SendSelf sends, signed now; a companion app also exports it as this node's card.
func BuildSelf(id meshcore.LocalIdentity, advType, name string, lat, lon *float64, flood bool, pathHashSize int, scope *meshcore.Region) (*meshcore.Packet, error) {
	appData := meshcore.AdvertAppData{Type: advType, Name: name}
	if lat != nil && lon != nil && (*lat != 0 || *lon != 0) {
		appData.Lat = int32(math.Round(*lat * 1_000_000.0))
		appData.Lon = int32(math.Round(*lon * 1_000_000.0))
	}
	rawAppData, err := appData.ToBytes()
	if err != nil {
		return nil, err
	}

	adv := meshcore.Advert{
		PublicKey:  id.Identity,
		Timestamp:  uint32(time.Now().Unix()),
		RawAppData: rawAppData,
	}
	// SignWith, not Sign(PrivateKey()): an imported expanded-key identity has no usable seed.
	adv.SignWith(id)

	payload, err := adv.ToBytes()
	if err != nil {
		return nil, err
	}

	// The firmware reads a direct packet with path_len==0 as zero-hop: accepted by neighbours, never relayed.
	routeType := meshcore.RouteTypeFlood
	pathLength := floodPathLength(pathHashSize)
	if !flood {
		routeType = meshcore.RouteTypeDirect
		pathLength = 0
	}

	pkt := &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, meshcore.PayloadTypeAdvert, 0),
		PathLength: pathLength,
		Payload:    payload,
	}
	if flood && scope != nil { // scoped flood advert (firmware sendFloodScoped(default_scope, ...)); code 2 stays 0
		pkt.Header = meshcore.MakeHeader(meshcore.RouteTypeTransportFlood, meshcore.PayloadTypeAdvert, 0)
		pkt.TransportCode1 = scope.CalcTransportCode(pkt)
	}
	return pkt, nil
}
