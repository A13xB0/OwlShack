package meshpath

import meshcore "github.com/meshcore-go/meshcore-go"

// AdvertHashSize is the size a peer floods its adverts at, or 0 for a zero-hop advert, which always reads as 1 byte (Mesh::sendZeroHop).
func AdvertHashSize(pkt *meshcore.Packet) uint8 {
	if !pkt.IsRouteFlood() {
		return 0
	}
	return pkt.PathHashSize()
}
