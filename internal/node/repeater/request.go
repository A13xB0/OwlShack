package repeater

import (
	"encoding/binary"
	"encoding/hex"
	"sort"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/buildinfo"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// Admin request types (firmware REQ_TYPE_*), mirroring internal/client/repeater.
const (
	reqTypeGetStatus        = 0x01
	reqTypeGetTelemetryData = 0x03
	reqTypeGetAccessList    = 0x05
	reqTypeGetNeighbours    = 0x06
	reqTypeGetOwnerInfo     = 0x07

	telemChannelSelf = sensor.ChannelSelf
	maxPacketPayload = 184 // firmware MAX_PACKET_PAYLOAD (sizeof reply_data)
	// Matches the firmware's results_buffer so a neighbours reply still fits one packet.
	neighboursMaxBody = 130
)

// handleReq fires for every REQ we hear and self-filters by destination hash plus MAC-against-ACL.
func (r *Repeater) handleReq(pkt *meshcore.Packet) {
	req, err := meshcore.RequestFromBytes(pkt.Payload)
	if err != nil {
		return
	}
	if req.Destination != r.node.Identity().PublicKey()[0] {
		return
	}
	client, secret, ok := r.aclClient(req.Source, req.VerifyMAC)
	if !ok {
		return
	}
	plain := req.Decrypt(secret)
	if len(plain) < 5 {
		return
	}
	pkt.MarkDoNotRetransmit()
	tag := binary.LittleEndian.Uint32(plain[:4]) // client timestamp, reflected back
	if tag <= client.LastTimestamp {
		return // firmware: `timestamp > client->last_timestamp` or it's a replay
	}
	reqType := plain[4]
	params := plain[5:]

	body, ok := r.buildReqResponse(client, reqType, params, sensor.MaxReplyBody(pkt))
	if !ok {
		return
	}

	// Response plaintext is [reflected tag:4][body]; the client matches its pending request by the echoed tag.
	reply := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(reply[:4], tag)
	copy(reply[4:], body)

	r.touchClient(client, tag)

	var clientPub [32]byte
	if pub, err := hex.DecodeString(client.PubKey); err == nil && len(pub) == 32 {
		copy(clientPub[:], pub)
	}
	if err := r.sendServerReply(pkt, clientPub, secret, reply); err != nil {
		r.log.Error("request reply failed", "reqType", reqType, "error", err)
	}
}

// buildReqResponse builds everything after the reflected tag; false answers nothing, and only telemetry can exceed maxBody.
func (r *Repeater) buildReqResponse(client *store.RepeaterACLEntry, reqType byte, params []byte, maxBody int) ([]byte, bool) {
	switch reqType {
	case reqTypeGetStatus:
		return r.statusBody(), true
	case reqTypeGetNeighbours:
		if len(params) >= 1 && params[0] != 0 {
			return nil, false // unknown request version
		}
		return r.neighboursBody(params), true
	case reqTypeGetOwnerInfo:
		return r.ownerInfoBody(), true
	case reqTypeGetAccessList:
		if client.Permissions&permRoleMask != permAdmin {
			return nil, false // admin-only
		}
		if len(params) >= 2 && (params[0] != 0 || params[1] != 0) {
			return nil, false // reserved query params
		}
		return r.accessListBody(), true
	case reqTypeGetTelemetryData:
		// payload[0] is an INVERSE mask; handleRequest sends base whatever it says, and a guest nothing more.
		perms := sensor.PermAll
		if len(params) >= 1 {
			perms = ^params[0]
		}
		if client.Permissions&permRoleMask == permGuest {
			perms = 0
		}
		perms |= sensor.PermBase
		var entries []sensor.ChannelEntry
		var statuses []sensor.Status
		if r.telemetry != nil {
			entries, statuses = r.telemetry()
		}
		body, dropped := sensor.BuildReply(perms, r.selfReadings(), entries, statuses, maxBody)
		if dropped {
			r.log.Error("telemetry map too long for one packet, sending the node's own readings only", "max", maxBody)
		}
		return body, true
	default:
		return nil, false
	}
}

// selfReadings is what the radio board says about itself; one with no cell, or gone quiet, leaves the battery at 0 rather than a stale reading.
func (r *Repeater) selfReadings() sensor.SelfReadings {
	var out sensor.SelfReadings
	if r.haveBattery.Load() {
		out.BatteryVolts = float64(r.batteryMV.Load()) / 1000
	}
	if r.haveMCUTemp.Load() {
		c := float64(r.mcuTempC.Load()) / 10
		out.TempC = &c
	}
	return out
}

// statusBody builds the full 56-byte RepeaterStats blob (firmware layout, little-endian); untracked counters stay zero.
func (r *Repeater) statusBody() []byte {
	rc := r.routeCounters()
	r.mu.Lock()
	started := r.startedAt
	r.mu.Unlock()
	uptime := uint32(0)
	if !started.IsZero() {
		uptime = uint32(time.Since(started).Seconds())
	}

	b := make([]byte, 56)
	if r.haveDeviceStats.Load() { // real modem readings (poll cache)
		binary.LittleEndian.PutUint16(b[0:2], uint16(r.batteryMV.Load()))         // batt_milli_volts
		binary.LittleEndian.PutUint16(b[4:6], uint16(int16(r.noiseFloor.Load()))) // noise_floor (radio getNoiseFloor)
	}
	if r.node != nil {
		binary.LittleEndian.PutUint16(b[2:4], uint16(r.node.TxQueueLen())) // curr_tx_queue_len
	}
	if r.haveSignal.Load() {
		binary.LittleEndian.PutUint16(b[6:8], uint16(int16(r.lastRSSI.Load())))    // last_rssi
		binary.LittleEndian.PutUint16(b[42:44], uint16(int16(r.lastSNRx4.Load()))) // last_snr (quarter-dB)
	}
	binary.LittleEndian.PutUint32(b[8:12], uint32(r.recvCount.Load()))                      // n_packets_recv
	binary.LittleEndian.PutUint32(b[12:16], uint32(r.sentFlood.Load()+r.sentDirect.Load())) // n_packets_sent (radio getPacketsSent: every TX)
	binary.LittleEndian.PutUint32(b[16:20], uint32(r.txAirtimeMs.Load()/1000))              // total_air_time_secs (tx)
	binary.LittleEndian.PutUint32(b[20:24], uptime)                                         // total_up_time_secs
	binary.LittleEndian.PutUint32(b[24:28], uint32(r.sentFlood.Load()))                     // n_sent_flood
	binary.LittleEndian.PutUint32(b[28:32], uint32(r.sentDirect.Load()))                    // n_sent_direct
	binary.LittleEndian.PutUint32(b[32:36], uint32(rc.FloodReceived))                       // n_recv_flood
	binary.LittleEndian.PutUint32(b[36:40], uint32(rc.DirectReceived))                      // n_recv_direct
	binary.LittleEndian.PutUint16(b[44:46], uint16(rc.DirectDuplicates))                    // n_direct_dups
	binary.LittleEndian.PutUint16(b[46:48], uint16(rc.FloodDuplicates))                     // n_flood_dups
	binary.LittleEndian.PutUint32(b[48:52], uint32(r.rxAirtimeMs.Load()/1000))              // rx_air_time_secs
	return b
}

// neighboursBody builds [total:2][results:2] then [prefix:prefixLen][secsAgo:4][snr:i8] entries.
func (r *Repeater) neighboursBody(params []byte) []byte {
	// params: [version:1][count:1][offset:2][order_by:1][prefix_len:1][rand:4]
	count := 255
	offset := 0
	prefixLen := 6
	var orderBy byte
	if len(params) >= 6 {
		count = int(params[1])
		offset = int(binary.LittleEndian.Uint16(params[2:4]))
		orderBy = params[4]
		prefixLen = min(int(params[5]), 32) // firmware clamps to PUB_KEY_SIZE; 0 is allowed
	}

	now := time.Now()
	list := r.snapshotNeighbors() // newest-first native order
	sortNeighbours(list, orderBy)

	total := len(list)
	if offset > total {
		offset = total
	}
	page := list[offset:]
	if count < len(page) {
		page = page[:count]
	}

	if fit := neighboursMaxBody / (prefixLen + 5); len(page) > fit {
		page = page[:fit]
	}

	body := make([]byte, 4, 4+len(page)*(prefixLen+5))
	binary.LittleEndian.PutUint16(body[0:2], uint16(total))
	binary.LittleEndian.PutUint16(body[2:4], uint16(len(page)))
	for _, n := range page {
		body = append(body, n.pubkey[:prefixLen]...)
		var secs [4]byte
		binary.LittleEndian.PutUint32(secs[:], uint32(now.Sub(n.heard).Seconds()))
		body = append(body, secs[:]...)
		body = append(body, byte(int8(n.snr*4))) // firmware (int8_t)(snr*4): quarter-dB, truncated
	}
	return body
}

// sortNeighbours applies the firmware order_by selector: 0=newest (native order), 1=oldest, 2=strongest, 3=weakest.
func sortNeighbours(list []neighbor, orderBy byte) {
	switch orderBy {
	case 1:
		sort.Slice(list, func(i, j int) bool { return list[i].heard.Before(list[j].heard) })
	case 2:
		sort.Slice(list, func(i, j int) bool { return list[i].snr > list[j].snr })
	case 3:
		sort.Slice(list, func(i, j int) bool { return list[i].snr < list[j].snr })
	}
}

// accessListBody builds [pubkey-prefix:6][permissions:1] per non-guest client, capped like the firmware's `ofs + 7 <= sizeof(reply_data) - 4`.
func (r *Repeater) accessListBody() []byte {
	r.acl.RLock()
	keys := make([]string, 0, len(r.acl.m))
	perms := make(map[string]int, len(r.acl.m))
	for k, e := range r.acl.m {
		keys = append(keys, k)
		perms[k] = e.Permissions
	}
	r.acl.RUnlock()
	sort.Strings(keys)

	body := make([]byte, 0, len(keys)*7)
	for _, k := range keys {
		if perms[k] == 0 || len(body)+4+7 > maxPacketPayload-4 {
			continue // guest / deleted, or no room left
		}
		pub, err := hex.DecodeString(k)
		if err != nil || len(pub) < 6 {
			continue
		}
		body = append(body, pub[:6]...)
		body = append(body, byte(perms[k]))
	}
	return body
}

// ownerInfoBody builds the owner-info response: "VERSION\nNODE_NAME\nOWNER_INFO".
func (r *Repeater) ownerInfoBody() []byte {
	return []byte(buildinfo.Version + "\n" + r.cfg.Name + "\n" + r.cfg.OwnerInfo)
}
