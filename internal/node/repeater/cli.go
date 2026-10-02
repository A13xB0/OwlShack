package repeater

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/buildinfo"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

const (
	txtTypePlain   = 0 // legacy plain text
	txtTypeCliData = 1 // CLI command / response
	cliReplyDelay  = 600 * time.Millisecond
	// txtAckDelay mirrors the firmware TXT_ACK_DELAY for legacy plain-text CLI.
	txtAckDelay = 200 * time.Millisecond
	// Firmware sendSelfAdvertisement(1500, ...): hold a CLI-triggered advert so the reply transmits first.
	cliAdvertDelay = 1500 * time.Millisecond
	// Firmware's `dp - reply < 134` loop bound: keep appending while the reply is shorter than this.
	neighborsTextMax = 134

	// Firmware NodePrefs buffer sizes (StrHelper::strncpy truncates to size-1).
	maxNameLen          = 31
	maxPasswordLen      = 15
	maxOwnerInfoLen     = 119
	maxRegionNameLen    = 30
	notSupportedOnNode  = "ERR: not supported on this node"
	radioReadOnlyOnMesh = "ERR: radio settings are read-only over the mesh"
)

// handleCLI answers an ADMIN client's TXT_MSG command, mirroring the firmware onPeerDataRecv TXT_MSG path.
func (r *Repeater) handleCLI(pkt *meshcore.Packet) {
	txt, err := meshcore.TextMessageFromBytes(pkt.Payload)
	if err != nil {
		return
	}
	if txt.Destination != r.node.Identity().PublicKey()[0] {
		return
	}
	client, secret, ok := r.aclClient(txt.Source, txt.VerifyMAC)
	if !ok {
		return
	}
	plain := txt.Decrypt(secret)
	if plain == nil || len(plain) < 5 {
		return
	}
	pkt.MarkDoNotRetransmit()
	if client.Permissions&permRoleMask != permAdmin {
		return // CLI is admin-only
	}
	ts := binary.LittleEndian.Uint32(plain[:4])
	if ts < client.LastTimestamp {
		return // stale / replay
	}
	isRetry := ts == client.LastTimestamp
	flags := plain[4] >> 2
	if flags != txtTypeCliData && flags != txtTypePlain {
		return
	}

	var clientPub [32]byte
	if pub, err := hex.DecodeString(client.PubKey); err == nil && len(pub) == 32 {
		copy(clientPub[:], pub)
	}
	r.touchClient(client, ts)
	if pkt.IsRouteFlood() {
		r.learnFloodRoute(clientPub, pkt)
	}

	command := cString(plain[5:])
	if flags == txtTypePlain { // legacy CLI gets an ack (firmware TXT_TYPE_PLAIN branch), even on retries
		r.sendLegacyAck(pkt, clientPub, plain[:5+len(command)])
	}
	if isRetry {
		return // duplicate delivery — don't re-run side effects
	}

	reply := r.runCLI(command)
	if reply == "" {
		return
	}
	if err := r.sendText(pkt, clientPub, secret, ts, reply); err != nil {
		r.log.Error("cli reply failed", "error", err)
	}
}

// sendLegacyAck sends the firmware's bare 4-byte CRC over [timestamp][flags][text] plus the client's pubkey.
func (r *Repeater) sendLegacyAck(reqPkt *meshcore.Packet, clientPub [32]byte, ackPlaintext []byte) {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, meshcore.CalcAckHash(ackPlaintext, clientPub[:]))

	routeType, pathLen, path := r.replyRoute(clientPub)
	out := &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, meshcore.PayloadTypeAck, 0),
		PathLength: pathLen,
		Path:       path,
		Payload:    payload,
	}
	var err error
	if routeType == meshcore.RouteTypeFlood {
		err = r.sendFloodScoped(out, reqPkt, node.PrioritySend, txtAckDelay)
	} else {
		err = r.sendPkt(out, node.PrioritySend, txtAckDelay)
	}
	if err != nil {
		r.log.Error("legacy cli ack failed", "error", err)
	}
}

// runCLI strips the optional "XX|" correlation prefix and reflects it back so async clients can match responses.
func (r *Repeater) runCLI(command string) string {
	command = strings.TrimLeft(command, " ")
	prefix := ""
	if len(command) > 4 && command[2] == '|' {
		prefix = command[:3]
		command = command[3:]
	}
	return prefix + r.dispatchCLI(command)
}

// dispatchCLI returns the reply text; config-mutating commands validate inline and reply optimistically before reconfigure runs.
func (r *Repeater) dispatchCLI(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "ver":
		return buildinfo.Version + " (Build: " + buildinfo.Date + ")"
	case cmd == "clock":
		return formatClock(time.Now())
	case strings.HasPrefix(cmd, "clock sync"):
		// Host time is authoritative, so there is nothing to set; reply in the firmware's shape anyway.
		return "OK - clock set: " + formatClock(time.Now())
	case cmd == "advert.zerohop":
		r.advertAfterReply(false)
		return "OK - zerohop advert sent"
	case cmd == "advert":
		r.advertAfterReply(true)
		return "OK - Advert sent"
	case cmd == "discover.neighbors":
		if err := r.sendDiscover(); err != nil {
			return "ERR: " + err.Error()
		}
		return "OK - Discover sent"
	case strings.HasPrefix(cmd, "discover.neighbors "):
		return "Err - discover.neighbors has no options"
	case cmd == "neighbors":
		return r.neighborsList()
	// dispatchCLI trims the command, which eats the trailing space the firmware treats as
	// significant (memcmp of "neighbor.remove " is 16 chars), so the no-argument form has to be
	// matched separately or the app's "Remove all Neighbours" reads as an unknown command.
	case cmd == "neighbor.remove", strings.HasPrefix(cmd, "neighbor.remove "):
		return r.cliNeighborRemove(strings.TrimSpace(strings.TrimPrefix(cmd, "neighbor.remove")))
	case cmd == "clear stats":
		r.clearStats()
		return "(OK - stats reset)"
	case cmd == "log start":
		r.logging.Store(true)
		return "   logging on"
	case cmd == "log stop":
		r.logging.Store(false)
		return "   logging off"
	case cmd == "log erase":
		return "   log erased" // no packet-log file on this node; nothing to erase
	case strings.HasPrefix(cmd, "setperm "):
		return r.cliSetPerm(cmd[len("setperm "):])
	case strings.HasPrefix(cmd, "password "):
		return r.cliPassword(cmd[len("password "):])
	case cmd == "region" || strings.HasPrefix(cmd, "region "):
		return r.cliRegion(cmd)
	case strings.HasPrefix(cmd, "get "):
		return r.cliGet(cmd[len("get "):])
	case strings.HasPrefix(cmd, "set radio "), strings.HasPrefix(cmd, "set freq "), strings.HasPrefix(cmd, "set tx "):
		return radioReadOnlyOnMesh
	case strings.HasPrefix(cmd, "set "):
		return r.cliSet(cmd[len("set "):])
	case isUnsupportedCmd(cmd):
		return notSupportedOnNode
	default:
		return "Unknown command"
	}
}

// isUnsupportedCmd reports firmware commands a Linux-hosted relay can't do, answered with a clear error not "Unknown command".
func isUnsupportedCmd(cmd string) bool {
	for _, p := range []string{
		"poweroff", "shutdown", "reboot", "clkreboot", "board",
		"powersaving", "gps", "sensor", "start ota", "tempradio",
		"erase", "time", "log",
		"stats-core", "stats-packets", "stats-radio", // serial-only in firmware
	} {
		if cmd == p || strings.HasPrefix(cmd, p+" ") {
			return true
		}
	}
	return false
}

// Config keys we don't model: answered "not supported", since the unknown-key reply would misreport them as non-existent.
var unsupportedGetKeys = []string{
	"dutycycle", "af", "int.thresh", "cad", "agc.reset.interval",
	"allow.read.only", "prv.key", "acl", "radio.rxgain", "radio.fem.rxgain", "radio.fem.txgain",
	"bridge.", "bootloader.ver", "adc.multiplier",
	"pwrmgt.", "extra.sf",
}

var unsupportedSetKeys = []string{
	"dutycycle", "af", "int.thresh", "cad", "agc.reset.interval",
	"allow.read.only", "prv.key", "radio.rxgain", "radio.fem.rxgain", "radio.fem.txgain",
	"bridge.", "adc.multiplier", "extra.sf",
}

func isUnsupportedKey(keys []string, key string) bool {
	return slices.ContainsFunc(keys, func(k string) bool {
		return key == k || (strings.HasSuffix(k, ".") && strings.HasPrefix(key, k))
	})
}

// cliGet reads radio params from the shared Settings and everything else from the repeater's own config.
func (r *Repeater) cliGet(key string) string {
	cfg := r.cfgSnapshot()
	switch key {
	case "name":
		return "> " + cfg.Name
	case "lat":
		return "> " + floatOrZero(cfg.Latitude)
	case "lon":
		return "> " + floatOrZero(cfg.Longitude)
	case "owner.info":
		return "> " + strings.ReplaceAll(cfg.OwnerInfo, "\n", "|")
	case "repeat":
		if cfg.IsFwdDisabled() {
			return "> off"
		}
		return "> on"
	case "loop.detect":
		return "> " + cfg.LoopDetectOr()
	case "path.hash.mode": // firmware unit: bytes-1
		return fmt.Sprintf("> %d", cfg.PathHashSizeOr()-1)
	case "flood.max":
		return fmt.Sprintf("> %d", cfg.FloodMaxOr())
	case "flood.max.advert":
		return fmt.Sprintf("> %d", cfg.FloodMaxAdvertOr())
	case "flood.max.unscoped":
		return fmt.Sprintf("> %d", cfg.FloodMaxUnscopedOr())
	case "guest.password":
		return "> " + cfg.GuestPassword
	case "txdelay":
		v := cfg.TxDelayFactorOr()
		return "> " + floatOrZero(&v)
	case "direct.txdelay":
		v := cfg.DirectTxDelayFactorOr()
		return "> " + floatOrZero(&v)
	case "rxdelay":
		v := cfg.RxDelayBaseOr()
		return "> " + floatOrZero(&v)
	case "multi.acks":
		return fmt.Sprintf("> %d", cfg.MultiAcksOr())
	case "public.key":
		return "> " + hex.EncodeToString(r.node.Identity().PublicKeyBytes())
	case "role":
		return "> repeater"
	case "bridge.type":
		return "> none"
	case "advert.interval": // minutes (firmware unit); config stores seconds
		return fmt.Sprintf("> %d", (cfg.AdvertIntervalOr()+30)/60) // effective (incl. default), rounded
	case "flood.advert.interval": // hours (firmware unit)
		return fmt.Sprintf("> %d", (cfg.FloodAdvertIntervalOr()+1800)/3600) // effective (incl. default), rounded
	case "radio", "tx", "freq":
		return r.cliGetRadio(key)
	default:
		if isUnsupportedKey(unsupportedGetKeys, key) {
			return notSupportedOnNode
		}
		return "??: " + key
	}
}

func (r *Repeater) cliGetRadio(key string) string {
	s, err := r.store.Settings.Get(context.Background())
	if err != nil {
		return "ERR: no radio settings"
	}
	switch key {
	case "freq":
		return "> " + floatOrZero(s.Freq)
	case "tx":
		return fmt.Sprintf("> %d", intPtrOr(s.TX, 0))
	default: // radio: freq,bw,sf,cr
		return fmt.Sprintf("> %s,%s,%d,%d", floatOrZero(s.Freq), floatOrZero(s.BW), intPtrOr(s.SF, 0), intPtrOr(s.CR, 0))
	}
}

var (
	errBadPubKey     = errors.New("bad pubkey")
	errInvalidParams = errors.New("invalid params")
)

// cliSetPerm handles `setperm <pubkey-hex> <perms-int8>`.
func (r *Repeater) cliSetPerm(args string) string {
	sp := strings.IndexByte(args, ' ')
	if sp < 0 {
		return "Err - bad params"
	}
	pubHex := args[:sp]
	if len(pubHex) > 64 || len(pubHex)%2 != 0 {
		return "Err - bad pubkey" // Utils::fromHex demands exactly hex_len/2 bytes
	}
	switch err := r.SetACL(pubHex, int(uint8(atoi(args[sp+1:])))); {
	case err == nil:
		return "OK"
	case errors.Is(err, errBadPubKey):
		return "Err - bad pubkey"
	default:
		return "Err - invalid params"
	}
}

// SetACL ports ClientACL::applyPermissions: a guest role revokes and accepts a pubkey prefix, any other role needs the full key.
func (r *Repeater) SetACL(pubHex string, perms int) error {
	pubHex = strings.ToLower(strings.TrimSpace(pubHex))
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) == 0 || len(pub) > 32 {
		return errBadPubKey
	}
	if perms&permRoleMask == permGuest {
		full, ok := r.aclMatchPrefix(pubHex)
		if !ok {
			return fmt.Errorf("%w: client not found", errInvalidParams)
		}
		r.aclDelete(full)
		return nil
	}
	if len(pub) != 32 {
		return fmt.Errorf("%w: full pubkey required to grant a role", errInvalidParams)
	}
	e := r.aclGet(pubHex) // preserve an existing client's replay timestamp
	if e == nil {
		e = &store.RepeaterACLEntry{PubKey: pubHex}
	}
	e.Permissions = perms & 0xFF
	e.LastSeen = time.Now()
	r.aclPut(e)
	return nil
}

func (r *Repeater) ClearStats() { r.clearStats() }

// advertAfterReply delays the advert so the CLI reply, queued at cliReplyDelay, transmits first.
func (r *Repeater) advertAfterReply(flood bool) {
	go func() {
		t := time.NewTimer(cliAdvertDelay)
		defer t.Stop()
		select {
		case <-r.runContext().Done():
		case <-t.C:
			if err := r.SendAdvert(flood); err != nil {
				r.log.Error("cli advert failed", "error", err)
			}
		}
	}()
}

// sendText replies as a TXT_MSG datagram, never a PATH-return, with a timestamp never equal to the command's (firmware CLI-view workaround).
func (r *Repeater) sendText(reqPkt *meshcore.Packet, clientPub [32]byte, secret []byte, senderTS uint32, text string) error {
	ts := r.uniqueTimestamp()
	if ts == senderTS {
		ts = r.uniqueTimestamp()
	}
	me := r.node.Identity().PublicKey()
	plaintext := meshcore.BuildTextPlaintext(time.Unix(int64(ts), 0), txtTypeCliData<<2, []byte(text))
	payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
		return (&meshcore.TextMessage{Destination: clientPub[0], Source: me[0], MAC: mac, EncryptedPayload: enc}).ToBytes()
	}, plaintext)
	if err != nil {
		return err
	}
	routeType, pathLen, path := r.replyRoute(clientPub)
	out := &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, meshcore.PayloadTypeTxtMsg, 0),
		PathLength: pathLen,
		Path:       path,
		Payload:    payload,
	}
	if routeType == meshcore.RouteTypeFlood {
		return r.sendFloodScoped(out, reqPkt, node.PrioritySend, cliReplyDelay)
	}
	return r.sendPkt(out, node.PrioritySend, cliReplyDelay)
}

// cliSet handles `set <key> <value>` for the repeater's own config.
func (r *Repeater) cliSet(kv string) string {
	key, val, ok := strings.Cut(kv, " ")
	if !ok {
		return "unknown config: " + kv
	}
	mutate, reply := r.setMutation(key, val)
	if mutate == nil {
		return reply // validation error / unsupported key
	}
	return r.applyCfg(mutate, reply)
}

// setMutation returns the config mutation plus success reply, or (nil, errorReply), mirroring the firmware CommonCLI set branch.
func (r *Repeater) setMutation(key, val string) (func(*config.RepeaterConfig), string) {
	switch key {
	case "name":
		if !isValidRepeaterName(val) {
			return nil, "Error, bad chars"
		}
		name := truncate(val, maxNameLen)
		return func(c *config.RepeaterConfig) { c.Name = name }, "OK"
	case "lat", "lon":
		f := atof(val)
		// Divergence: atof accepts "nan", which would make Stats() un-marshalable.
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, "Error, invalid number"
		}
		if key == "lat" {
			return func(c *config.RepeaterConfig) { c.Latitude = &f }, "OK"
		}
		return func(c *config.RepeaterConfig) { c.Longitude = &f }, "OK"
	case "advert.interval": // minutes; 0 (off) or 60-240 (firmware range)
		m := int(digits(val))
		if (m > 0 && m < config.MinAdvertIntervalSecs/60) || m > config.MaxAdvertIntervalSecs/60 {
			return nil, "Error: interval range is 60-240 minutes"
		}
		secs := (m / 2) * 2 * 60 // firmware stores this in 2-minute units
		return func(c *config.RepeaterConfig) { c.AdvertInterval = &secs }, "OK"
	case "flood.advert.interval": // hours; 0 (off) or 3-168
		h := int(digits(val))
		if (h > 0 && h < config.MinFloodAdvertIntervalSecs/3600) || h > config.MaxFloodAdvertIntervalSecs/3600 {
			return nil, "Error: interval range is 3-168 hours"
		}
		secs := h * 3600
		return func(c *config.RepeaterConfig) { c.FloodAdvertInterval = &secs }, "OK"
	case "flood.max", "flood.max.advert", "flood.max.unscoped":
		n := int(uint8(atoi(val))) // firmware: uint8_t m = atoi(...)
		if n > 64 {
			return nil, "Error, max 64"
		}
		switch key {
		case "flood.max":
			return func(c *config.RepeaterConfig) { c.FloodMax = &n }, "OK"
		case "flood.max.advert":
			return func(c *config.RepeaterConfig) { c.FloodMaxAdvert = &n }, "OK"
		default:
			return func(c *config.RepeaterConfig) { c.FloodMaxUnscoped = &n }, "OK"
		}
	case "path.hash.mode":
		mode := uint8(atoi(val))
		if mode >= 3 {
			return nil, "Error, must be 0,1, or 2"
		}
		size := int(mode) + 1
		return func(c *config.RepeaterConfig) { c.PathHashSize = &size }, "OK"
	case "loop.detect":
		var v string
		for _, level := range []string{"off", "minimal", "moderate", "strict"} { // firmware memcmp: prefix match
			if strings.HasPrefix(val, level) {
				v = level
				break
			}
		}
		if v == "" {
			return nil, "Error, must be: off, minimal, moderate, or strict"
		}
		return func(c *config.RepeaterConfig) { c.LoopDetect = &v }, "OK"
	case "repeat":
		disable := strings.HasPrefix(val, "off") // firmware: anything else is ON
		msg := "OK - repeat is now ON"
		if disable {
			msg = "OK - repeat is now OFF"
		}
		return func(c *config.RepeaterConfig) { c.DisableFwd = &disable }, msg
	case "owner.info":
		info := truncate(strings.ReplaceAll(val, "|", "\n"), maxOwnerInfoLen) // firmware stores | as newline
		return func(c *config.RepeaterConfig) { c.OwnerInfo = info }, "OK"
	case "guest.password":
		pass := truncate(val, maxPasswordLen)
		return func(c *config.RepeaterConfig) { c.GuestPassword = pass }, "OK"
	case "txdelay", "direct.txdelay":
		f := atof(val)
		if !(f >= 0 && f <= config.MaxTxDelayFactor) { // NaN-rejecting, like the firmware gate
			return nil, "Error, must be 0-2"
		}
		if key == "txdelay" {
			return func(c *config.RepeaterConfig) { c.TxDelayFactor = &f }, "OK"
		}
		return func(c *config.RepeaterConfig) { c.DirectTxDelayFactor = &f }, "OK"
	case "rxdelay":
		f := atof(val)
		if !(f >= 0 && f <= config.MaxRxDelayBase) {
			return nil, "Error, must be 0-20"
		}
		return func(c *config.RepeaterConfig) { c.RxDelayBase = &f }, "OK"
	case "multi.acks":
		n := int(uint8(atoi(val))) // firmware: uint8_t, no range check on set
		return func(c *config.RepeaterConfig) { c.MultiAcks = &n }, "OK"
	default:
		if isUnsupportedKey(unsupportedSetKeys, key) {
			return nil, notSupportedOnNode
		}
		return nil, "unknown config: " + key + " " + val
	}
}

// cliPassword echoes the stored, 15-char-truncated password, as the firmware reply does. The blank
// check is belt-and-braces: dispatchCLI's TrimSpace already means "password " cannot match the
// prefix, so nothing reaches here empty today. It is stated at the point of mutation because the
// invariant matters more than the incidental trim — a blank admin password compares equal to the
// blank a login sends, so clearing it hands admin to every node in range. Firmware allows it.
func (r *Repeater) cliPassword(pass string) string {
	pass = truncate(pass, maxPasswordLen)
	if strings.TrimSpace(pass) == "" {
		return "Error, password cannot be blank"
	}
	return r.applyCfg(func(c *config.RepeaterConfig) { c.AdminPassword = pass }, "password now: "+pass)
}

// cliNeighborRemove matches on the bytes supplied; the firmware accepts a prefix.
// An empty pubkey is a zero-length prefix that matches every entry, which is how the app sends
// "Remove all Neighbours": the firmware's fromHex(dest, 0, "") succeeds and its memcmp of 0 bytes
// is always equal (CommonCLI.cpp:230-239, examples/simple_repeater/MyMesh.cpp:1134-1142). Rejecting it broke that button.
func (r *Repeater) cliNeighborRemove(pubHex string) string {
	pub, err := hex.DecodeString(strings.ToLower(pubHex))
	if err != nil || len(pub) > 32 {
		return "ERR: bad pubkey"
	}
	r.removeNeighbor(pub)
	return "OK"
}

// cliRegion ports CommonCLI::handleRegionCmd onto our flat list: no parent tree, so `region def` isn't supported and parents are only validated.
func (r *Repeater) cliRegion(cmd string) string {
	if cmd == "region def" || strings.HasPrefix(cmd, "region def ") {
		return notSupportedOnNode
	}
	parts := strings.SplitN(cmd, " ", 5)
	if len(parts) > 4 {
		parts = parts[:4] // Utils::parseTextParts(max=4) drops the rest
	}
	n := len(parts)
	sub := ""
	if n >= 2 {
		sub = parts[1]
	}
	arg := ""
	if n >= 3 {
		arg = parts[2]
	}
	cfg := r.cfgSnapshot()

	switch {
	case n == 1:
		return r.regionTree()
	case sub == "load":
		return "" // firmware reloads asynchronously and replies nothing
	case sub == "save":
		return "OK" // every change is already persisted by reconfigure
	case n >= 3 && (sub == "allowf" || sub == "denyf"):
		name, ok := regionByPrefix(cfg.Regions, arg)
		if !ok {
			return "Err - unknown region"
		}
		deny := sub == "denyf"
		return r.applyCfg(func(c *config.RepeaterConfig) { setRegionDeny(c, name, deny) }, "OK")
	case n >= 3 && sub == "get":
		name, ok := regionByPrefix(cfg.Regions, arg)
		if !ok {
			return "Err - unknown region"
		}
		flag := "F"
		if regionDenies(cfg.Regions, name) {
			flag = ""
		}
		return " " + name + " " + flag
	case n >= 3 && sub == "home":
		name, ok := regionByPrefix(cfg.Regions, arg)
		if !ok {
			return "Err - unknown region"
		}
		home := name
		if home == config.WildcardRegion {
			home = "" // home_id 0 is the wildcard
		}
		return r.applyCfg(func(c *config.RepeaterConfig) { c.HomeRegion = home }, " home is now "+name)
	case n == 2 && sub == "home":
		if cfg.HomeRegion == "" {
			return " home is *"
		}
		return " home is " + cfg.HomeRegion
	case n >= 3 && sub == "default": // the default advert scope (firmware default_scope)
		if arg == "<null>" {
			return r.applyCfg(func(c *config.RepeaterConfig) { c.DefaultRegion = "" }, " default scope is now <null>")
		}
		name, ok := regionByPrefix(cfg.Regions, arg)
		if !ok {
			if !isValidRegionName(arg) {
				return "Err - region table full" // putRegion refused it
			}
			name = truncate(arg, maxRegionNameLen)
		}
		if name == config.WildcardRegion {
			return "Err - region table full" // our config can't scope adverts to "*"
		}
		return r.applyCfg(func(c *config.RepeaterConfig) {
			setRegionDeny(c, name, false) // firmware: def->flags = 0, auto-creating if missing
			c.DefaultRegion = name
		}, " default scope is now "+name)
	case n == 2 && sub == "default":
		if cfg.DefaultRegion == "" {
			return " default scope is <null>"
		}
		return " default scope is " + cfg.DefaultRegion
	case n >= 3 && sub == "put":
		parent := config.WildcardRegion
		if n >= 4 {
			p, ok := regionByPrefix(cfg.Regions, parts[3])
			if !ok {
				return "Err - unknown parent"
			}
			parent = p
		}
		name := truncate(arg, maxRegionNameLen)
		if !isValidRegionName(name) || name == config.WildcardRegion || config.SameRegion(name, parent) {
			return "Err - unable to put"
		}
		if existing, ok := regionByName(cfg.Regions, name); ok { // putRegion reuses "sco" for "#sco"
			name = existing
		}
		return r.applyCfg(func(c *config.RepeaterConfig) { setRegionDeny(c, name, false) }, "OK - (flood allowed)")
	case n >= 3 && sub == "remove":
		if arg == config.WildcardRegion {
			return "Err - not empty" // the wildcard can't be removed
		}
		name, ok := regionByName(cfg.Regions, arg)
		if !ok {
			return "Err - not found"
		}
		return r.applyCfg(func(c *config.RepeaterConfig) {
			c.Regions = slices.DeleteFunc(c.Regions, func(rg config.RepeaterRegion) bool { return rg.Name == name })
			if c.DefaultRegion == name {
				c.DefaultRegion = "" // a dangling reference fails validation
			}
			if c.HomeRegion == name {
				c.HomeRegion = ""
			}
		}, "OK")
	case n >= 3 && sub == "list":
		switch arg {
		case "allowed", "denied":
			if names := regionNames(cfg.Regions, arg == "denied"); names != "" {
				return names
			}
			return "-none-"
		default:
			return "Err - use 'allowed' or 'denied'"
		}
	default:
		return "Err - ??"
	}
}

// regionByName is RegionMap::findByName: exact match, "#" ignored.
func regionByName(regions []config.RepeaterRegion, name string) (string, bool) {
	if name == config.WildcardRegion {
		return name, true
	}
	return config.FindRegion(regions, name)
}

// regionByPrefix is RegionMap::findByNamePrefix: exact match wins, else the last region starting with the prefix.
func regionByPrefix(regions []config.RepeaterRegion, prefix string) (string, bool) {
	if name, ok := regionByName(regions, prefix); ok {
		return name, true
	}
	prefix = strings.TrimPrefix(prefix, "#")
	partial := ""
	for _, rg := range regions {
		if rg.Name != config.WildcardRegion && strings.HasPrefix(strings.TrimPrefix(rg.Name, "#"), prefix) {
			partial = rg.Name
		}
	}
	return partial, partial != ""
}

// regionDenies reports the deny-flood flag; the wildcard denies when it has no config entry.
func regionDenies(regions []config.RepeaterRegion, name string) bool {
	for _, rg := range regions {
		if rg.Name == name {
			return rg.DenyFlood
		}
	}
	return true
}

// setRegionDeny creates the entry if needed, so `allowf *` materialises the wildcard.
func setRegionDeny(c *config.RepeaterConfig, name string, deny bool) {
	for i := range c.Regions {
		if c.Regions[i].Name == name {
			c.Regions[i].DenyFlood = deny
			return
		}
	}
	c.Regions = append(c.Regions, config.RepeaterRegion{Name: name, DenyFlood: deny})
}

// isValidRegionName is RegionMap::is_name_char per byte: alnum, "-", "$", "#" and anything at or above 'A'.
func isValidRegionName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '-' || c == '$' || c == '#' || (c >= '0' && c <= '9') || c >= 'A') {
			return false
		}
	}
	return true
}

// cfgRegions is safe to iterate after unlocking because ApplyRegions replaces the slice rather than mutating it.
func (r *Repeater) cfgRegions() []config.RepeaterRegion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.Regions
}

// cfgSnapshot copies the config under the lock (scalar reads like DefaultRegion).
func (r *Repeater) cfgSnapshot() config.RepeaterConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// regionNames renders RegionMap::exportNamesTo(mask=DENY_FLOOD): names matching `denied`, "*" first, leading "#" stripped.
func regionNames(regions []config.RepeaterRegion, denied bool) string {
	var names []string
	if regionDenies(regions, config.WildcardRegion) == denied {
		names = append(names, config.WildcardRegion)
	}
	for _, rg := range regions {
		if rg.Name != config.WildcardRegion && rg.DenyFlood == denied {
			names = append(names, strings.TrimPrefix(rg.Name, "#"))
		}
	}
	return strings.Join(names, ",")
}

// regionTree renders the firmware exportTo shape: wildcard first, children indented one space, "^" for home and " F" for flood-allowed.
func (r *Repeater) regionTree() string {
	cfg := r.cfgSnapshot()
	home := cfg.HomeRegion
	if home == "" {
		home = config.WildcardRegion
	}
	line := func(name string, deny bool, indent string) string {
		s := indent + strings.TrimPrefix(name, "#")
		if name == home {
			s += "^"
		}
		if !deny {
			s += " F"
		}
		return s + "\n"
	}

	var b strings.Builder
	b.WriteString(line(config.WildcardRegion, regionDenies(cfg.Regions, config.WildcardRegion), ""))
	for _, rg := range cfg.Regions {
		if rg.Name != config.WildcardRegion {
			b.WriteString(line(rg.Name, rg.DenyFlood, " "))
		}
	}
	return b.String()
}

// clearStats resets what the firmware's `clear stats` does: recv/sent, route and dup counters, but not airtime or the last signal.
func (r *Repeater) clearStats() {
	r.recvCount.Store(0)
	r.fwdCount.Store(0)
	r.sentFlood.Store(0)
	r.sentDirect.Store(0)
	if r.routeStats != nil {
		base := r.routeStats()
		r.mu.Lock()
		r.statsBase = base
		r.mu.Unlock()
	}
}

// neighborsList renders "hexprefix:secsAgo:snrX4" per line, newest first, or "-none-".
func (r *Repeater) neighborsList() string {
	list := r.snapshotNeighbors()
	if len(list) == 0 {
		return "-none-"
	}
	now := time.Now()
	var b strings.Builder
	for _, n := range list {
		if b.Len() >= neighborsTextMax {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s:%d:%d", hex.EncodeToString(n.pubkey[:4]), int64(now.Sub(n.heard).Seconds()), int8(n.snr*4))
	}
	return b.String()
}

// applyCfg guards the missing-reconfigure case, then schedules the mutation for after the reply TXes.
func (r *Repeater) applyCfg(mutate func(*config.RepeaterConfig), ok string) string {
	if r.reconfigure == nil {
		return "ERR: config changes not available"
	}
	r.reconfigureAfterReply(mutate)
	return ok
}

// reconfigureAfterReply waits for the CLI reply to transmit because the reload restarts this node.
func (r *Repeater) reconfigureAfterReply(mutate func(*config.RepeaterConfig)) {
	go func() {
		t := time.NewTimer(cliReplyDelay + 400*time.Millisecond)
		defer t.Stop()
		select {
		case <-r.runContext().Done():
			return
		case <-t.C:
		}
		if err := r.reconfigure(mutate); err != nil {
			r.log.Error("cli reconfigure failed", "error", err)
		}
	}()
}

// isValidRepeaterName mirrors the firmware isValidName, but also refuses an empty name, which this node cannot run under.
func isValidRepeaterName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "[]\\:,?*")
}

// formatClock renders the firmware DateTime shape "HH:MM - D/M/YYYY UTC"; only the time is zero-padded.
func formatClock(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%02d:%02d - %d/%d/%d UTC", t.Hour(), t.Minute(), t.Day(), int(t.Month()), t.Year())
}

// floatOrZero renders like StrHelper::ftoa, always with a decimal point, so 915 reads "915.0" and nil reads "0.0".
func floatOrZero(f *float64) string {
	v := 0.0
	if f != nil {
		v = *f
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.ContainsRune(s, '.') {
		s += ".0"
	}
	return s
}

func intPtrOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// truncate is StrHelper::strncpy into a buffer of n+1 bytes: at most n bytes.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// atoi is C atoi: leading whitespace, optional sign, then digits; 0 otherwise.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := int(digits(s))
	if neg {
		return -n
	}
	return n
}

// digits is the firmware's _atoi: unsigned decimal digits from the start, no sign or whitespace, else 0.
func digits(s string) uint32 {
	var n uint32
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + uint32(s[i]-'0')
	}
	return n
}

// atof is C atof: the longest leading decimal prefix, 0 when there is none.
func atof(s string) float64 {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	for i := len(s); i > 0; i-- {
		if f, err := strconv.ParseFloat(s[:i], 64); err == nil {
			return f
		}
	}
	return 0
}
