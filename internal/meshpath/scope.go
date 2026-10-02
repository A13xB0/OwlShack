package meshpath

import meshcore "github.com/meshcore-go/meshcore-go"

// Scoper is the node a flood leaves from; *node.Node satisfies it.
type Scoper interface {
	FloodScope() *meshcore.Region
}

// ScopeFlood puts a flood this node originates inside its flood scope, as the firmware's sendFloodScoped does; direct packets are left alone.
func ScopeFlood(n Scoper, pkt *meshcore.Packet) {
	if pkt.RouteType() != meshcore.RouteTypeFlood {
		return
	}
	if r := n.FloodScope(); r != nil {
		r.ScopeFlood(pkt)
	}
}
