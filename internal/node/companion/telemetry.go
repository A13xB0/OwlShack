package companion

import (
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/meshpath"
	"github.com/meshcore-go/OwlShack/internal/sensor"
)

// reqTypeGetTelemetryData is the firmware's REQ_TYPE_GET_TELEMETRY_DATA; a companion answers no other.
const reqTypeGetTelemetryData = 0x03

// serverReplyDelay is the firmware's SERVER_RESPONSE_DELAY.
const serverReplyDelay = 300 * time.Millisecond

// handleReq answers a contact's telemetry request; BaseChatMesh reaches it only for a contact, whose shared secret is the credential.
func (c *Companion) handleReq(pkt *meshcore.Packet) {
	req, err := meshcore.RequestFromBytes(pkt.Payload)
	if err != nil {
		return
	}
	self := c.node.Identity().PublicKey()
	if req.Destination != self[0] {
		return
	}

	var sender dmCandidate
	var secret, plain []byte
	for _, cand := range c.dmCandidates(req.Source) {
		if !cand.isContact {
			continue
		}
		peerID, err := meshcore.NewIdentityFromBytes(cand.pubkey)
		if err != nil {
			continue
		}
		s, err := c.node.SharedSecret(peerID)
		if err != nil || !req.VerifyMAC(s) {
			continue
		}
		sender, secret, plain = cand, s, req.Decrypt(s)
		break
	}
	// The firmware needs len > 4 for the timestamp, then one byte of request type.
	if secret == nil || len(plain) < 5 {
		return
	}
	pkt.MarkDoNotRetransmit()
	if plain[4] != reqTypeGetTelemetryData {
		return
	}

	// The first reserved byte is an inverse mask the requester applies to narrow what it asked for.
	var mask byte
	if len(plain) >= 6 {
		mask = plain[5]
	}
	body, ok := c.telemetryReply(sender.telemPerms, mask, pkt)
	if !ok {
		return
	}

	// Response plaintext is [reflected tag:4][body]; the requester matches its pending request by the tag.
	reply := make([]byte, 0, sensor.ReplyTagLen+len(body))
	reply = append(reply, plain[:4]...)
	reply = append(reply, body...)

	if err := c.sendReqReply(pkt, sender.pubkey, secret, reply); err != nil {
		c.log.Error("telemetry reply failed", "contact", sender.name, "error", err)
	}
}

// telemetryPermissions is what this requester may read, by each class's mode.
func (c *Companion) telemetryPermissions(granted byte) byte {
	classes := []struct {
		mode *string
		bit  byte
	}{
		{c.cfg.TelemetryBase, sensor.PermBase},
		{c.cfg.TelemetryLocation, sensor.PermLocation},
		{c.cfg.TelemetryEnvironment, sensor.PermEnvironment},
	}
	var perms byte
	for _, cl := range classes {
		switch config.TelemetryModeOrDefault(cl.mode) {
		case config.TelemetryContacts:
			perms |= cl.bit
		case config.TelemetrySelected:
			perms |= granted & cl.bit
		}
	}
	return perms
}

// telemetryReply is the node's own readings then the operator's map; false is the silence the firmware keeps without base permission.
func (c *Companion) telemetryReply(granted, mask byte, req *meshcore.Packet) ([]byte, bool) {
	perms := c.telemetryPermissions(granted) &^ mask
	if perms&sensor.PermBase == 0 {
		return nil, false
	}

	var entries []sensor.ChannelEntry
	var statuses []sensor.Status
	if hook := c.telemetryHook(); hook != nil {
		entries, statuses = hook()
	}
	maxBody := sensor.MaxReplyBody(req)
	body, dropped := sensor.BuildReply(perms, c.selfReadings(), entries, statuses, maxBody)
	if dropped {
		c.log.Error("telemetry map too long for one packet, sending the node's own readings only", "max", maxBody)
	}
	return body, true
}

// selfReadings is the modem board's cell and temperature; HaveBattery clears when the modem goes quiet, so a stale cell reads 0.
func (c *Companion) selfReadings() sensor.SelfReadings {
	var out sensor.SelfReadings
	if c.stats != nil {
		ds := c.stats.CachedStats()
		if ds.HaveBattery {
			out.BatteryVolts = float64(ds.BatteryMV) / 1000
		}
		if ds.HaveMCUTemp {
			t := ds.MCUTempC
			out.TempC = &t
		}
	}
	return out
}

func (c *Companion) telemetryHook() func() ([]sensor.ChannelEntry, []sensor.Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.telemetry
}

// sendReqReply mirrors BaseChatMesh: a flooded request gets a path return carrying the response, a direct one a datagram.
func (c *Companion) sendReqReply(reqPkt *meshcore.Packet, peerPubKey, secret, plaintext []byte) error {
	if reqPkt.IsRouteFlood() {
		reply, err := c.buildPathReturn(peerPubKey, secret, reqPkt.Path, reqPkt.PathLength, meshcore.PayloadTypeResponse, plaintext)
		if err != nil {
			return err
		}
		meshpath.ScopeFlood(c.node, reply)
		return c.node.SendPacketDelayed(reply, node.PriorityFloodRelay, serverReplyDelay)
	}

	encrypted, err := meshcore.EncryptThenMAC(secret, plaintext)
	if err != nil {
		return err
	}
	var mac [2]byte
	copy(mac[:], encrypted[:2])
	self := c.node.Identity().PublicKey()
	payload, err := (&meshcore.Response{
		Destination: peerPubKey[0], Source: self[0], MAC: mac, EncryptedPayload: encrypted[2:],
	}).ToBytes()
	if err != nil {
		return err
	}

	size := c.bytesPerHop(peerPubKey)
	reply := &meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeResponse, 0),
		PathLength: (size - 1) << 6,
		Payload:    payload,
	}
	if outPath, hs, ok := c.learnedRoute(peerPubKey); ok {
		if len(outPath) == 0 {
			hs = size
		}
		reply.Header = meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 0)
		reply.Path = outPath
		reply.PathLength = (hs-1)<<6 | byte(len(outPath)/int(hs))
	}
	meshpath.ScopeFlood(c.node, reply)
	return c.node.SendPacketDelayed(reply, node.PrioritySend, serverReplyDelay)
}
