package repeater

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/sensor"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func TestCString(t *testing.T) {
	if got := cString([]byte{'h', 'i', 0, 0, 0}); got != "hi" {
		t.Errorf("cString padded = %q, want %q", got, "hi")
	}
	if got := cString([]byte("abc")); got != "abc" {
		t.Errorf("cString unterminated = %q, want %q", got, "abc")
	}
	if got := cString([]byte{0}); got != "" {
		t.Errorf("cString empty = %q, want %q", got, "")
	}
}

// A wrong "XX|" correlation-prefix offset breaks every CLI reply on the air.
func TestRunCLIPrefix(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Name: "rp"}}
	if got := r.runCLI("A5|get name"); got != "A5|> rp" {
		t.Errorf("prefixed = %q, want %q", got, "A5|> rp")
	}
	if got := r.runCLI("get name"); got != "> rp" {
		t.Errorf("unprefixed = %q, want %q", got, "> rp")
	}
	if got := r.runCLI("  get name"); got != "> rp" { // leading spaces skipped
		t.Errorf("indented = %q, want %q", got, "> rp")
	}
	if got := r.runCLI("bogus"); got != "Unknown command" {
		t.Errorf("unknown = %q, want %q", got, "Unknown command")
	}
}

// Advert intervals must report their effective values, not 0.
func TestCLIAdvertIntervalDefaults(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{}} // nil intervals → defaults
	if got := r.runCLI("get advert.interval"); got != "> 0" {
		t.Errorf("default advert.interval = %q, want %q", got, "> 0")
	}
	if got := r.runCLI("get flood.advert.interval"); got != "> 47" {
		t.Errorf("default flood.advert.interval = %q, want %q (firmware 47h)", got, "> 47")
	}
	m, h := 20*60, 4*3600
	r.cfg.AdvertInterval, r.cfg.FloodAdvertInterval = &m, &h
	if got := r.runCLI("get advert.interval"); got != "> 20" {
		t.Errorf("advert.interval = %q, want %q", got, "> 20")
	}
	if got := r.runCLI("get flood.advert.interval"); got != "> 4" {
		t.Errorf("flood.advert.interval = %q, want %q", got, "> 4")
	}
}

// TestCLIRadioReadOnly: radio settings can be fetched but never set over mesh.
func TestCLIRadioReadOnly(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{}}
	for _, cmd := range []string{"set radio 915,250,11,1", "set freq 915", "set tx 20"} {
		if got := r.runCLI(cmd); got != "ERR: radio settings are read-only over the mesh" {
			t.Errorf("%q = %q, want read-only error", cmd, got)
		}
	}
}

// Pins `set` validation, units and error strings against the firmware.
func TestSetMutation(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{}}
	cases := []struct {
		key, val, reply string
		wantMutate      bool
	}{
		{"flood.max", "40", "OK", true},
		{"flood.max", "100", "Error, max 64", false},
		{"flood.max.unscoped", "0", "OK", true},
		{"flood.max.unscoped", "100", "Error, max 64", false},
		{"advert.interval", "120", "OK", true},
		{"advert.interval", "30", "Error: interval range is 60-240 minutes", false},
		{"flood.advert.interval", "47", "OK", true},
		{"flood.advert.interval", "200", "Error: interval range is 3-168 hours", false},
		{"loop.detect", "strict", "OK", true},
		{"loop.detect", "bogus", "Error, must be: off, minimal, moderate, or strict", false},
		{"repeat", "off", "OK - repeat is now OFF", true},
		{"repeat", "on", "OK - repeat is now ON", true},
		{"name", "good", "OK", true},
		{"name", "bad,name", "Error, bad chars", false},
		// Firmware CommonCLI checks `mode < 3`, so 0-2 (= 1-3 byte hashes).
		{"path.hash.mode", "2", "OK", true},
		{"path.hash.mode", "3", "Error, must be 0,1, or 2", false},
		{"af", "1.5", "ERR: not supported on this node", false}, // firmware key we don't model
		{"txdelay", "1.5", "OK", true},
		{"txdelay", "2.5", "Error, must be 0-2", false},
		{"direct.txdelay", "0", "OK", true},
		{"rxdelay", "10", "OK", true},
		{"rxdelay", "21", "Error, must be 0-20", false},
		{"multi.acks", "1", "OK", true},
		{"nonsense", "x", "unknown config: nonsense x", false},
		// C atoi/atof leniency + uint8 truncation, as the firmware parses them.
		{"flood.max", "-1", "Error, max 64", false}, // (uint8_t)-1 = 255
		{"flood.max", "abc", "OK", true},            // atoi → 0
		{"flood.max", "300", "OK", true},            // (uint8_t)300 = 44
		{"path.hash.mode", "256", "OK", true},       // (uint8_t)256 = 0
		{"path.hash.mode", "-1", "Error, must be 0,1, or 2", false},
		{"advert.interval", "abc", "OK", true},           // _atoi → 0 = off
		{"lat", "abc", "OK", true},                       // atof → 0.0
		{"loop.detect", "offline", "OK", true},           // memcmp prefix match
		{"repeat", "foo", "OK - repeat is now ON", true}, // anything but "off" is on
	}
	for _, c := range cases {
		mutate, reply := r.setMutation(c.key, c.val)
		if reply != c.reply {
			t.Errorf("set %s %s: reply=%q want %q", c.key, c.val, reply, c.reply)
		}
		if (mutate != nil) != c.wantMutate {
			t.Errorf("set %s %s: mutate present=%v want %v", c.key, c.val, mutate != nil, c.wantMutate)
		}
	}
	// advert.interval is minutes on the wire, seconds in config.
	m, _ := r.setMutation("advert.interval", "120")
	var cfg config.RepeaterConfig
	m(&cfg)
	if cfg.AdvertInterval == nil || *cfg.AdvertInterval != 7200 {
		t.Errorf("advert.interval 120min → %v, want 7200s", cfg.AdvertInterval)
	}
}

// TestUnsupportedCmd: N/A firmware commands return a clear error, not "unknown".
func TestUnsupportedCmd(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{}}
	for _, cmd := range []string{"reboot", "gps on", "sensor list", "poweroff", "start ota"} {
		if got := r.runCLI(cmd); got != "ERR: not supported on this node" {
			t.Errorf("%q = %q, want not-supported error", cmd, got)
		}
	}
	if got := r.runCLI("totallybogus"); got != "Unknown command" {
		t.Errorf("unknown = %q, want unknown-command error", got)
	}
}

// Region list filters by flood state, and removing a missing region must not touch config.
func TestRegionReads(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Regions: []config.RepeaterRegion{
		{Name: "alpha"}, {Name: "bravo", DenyFlood: true},
	}}}
	if got := r.runCLI("region list allowed"); got != "alpha" {
		t.Errorf("region list allowed = %q, want %q", got, "alpha")
	}
	if got := r.runCLI("region list denied"); got != "*,bravo" { // no "*" entry ⇒ the wildcard denies
		t.Errorf("region list denied = %q, want %q", got, "*,bravo")
	}
	if got := r.runCLI("region remove ghost"); got != "Err - not found" {
		t.Errorf("region remove ghost = %q, want %q", got, "Err - not found")
	}
}

// Pins the "*" entry mapping to wildcard flags, and an absent "*" denying unscoped flood.
func TestRegionsFromConfig(t *testing.T) {
	cases := []struct {
		name      string
		regions   []config.RepeaterRegion
		wantNamed []string
		wantDeny  bool // wildcard denies unscoped flood
	}{
		{"empty ⇒ deny", nil, nil, true},
		{"* allow", []config.RepeaterRegion{{Name: "*"}}, nil, false},
		{"* deny", []config.RepeaterRegion{{Name: "*", DenyFlood: true}}, nil, true},
		{"named + *", []config.RepeaterRegion{{Name: "alpha"}, {Name: "*"}}, []string{"alpha"}, false},
		{"named only ⇒ deny unscoped", []config.RepeaterRegion{{Name: "alpha"}}, []string{"alpha"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			named, flags := regionsFromConfig(c.regions)
			gotNames := make([]string, len(named))
			for i, r := range named {
				gotNames[i] = r.Name
				if r.Name == "*" {
					t.Errorf("wildcard leaked into named regions")
				}
			}
			if len(gotNames) != len(c.wantNamed) {
				t.Fatalf("named = %v, want %v", gotNames, c.wantNamed)
			}
			for i := range gotNames {
				if gotNames[i] != c.wantNamed[i] {
					t.Errorf("named[%d] = %q, want %q", i, gotNames[i], c.wantNamed[i])
				}
			}
			gotDeny := flags&meshcore.RegionDenyFlood != 0
			if gotDeny != c.wantDeny {
				t.Errorf("wildcard deny = %v, want %v", gotDeny, c.wantDeny)
			}
		})
	}
}

// TestClearStats zeroes the counters.
func TestClearStats(t *testing.T) {
	r := &Repeater{}
	r.recvCount.Store(5)
	r.fwdCount.Store(3)
	r.haveSignal.Store(true)
	r.clearStats()
	if r.recvCount.Load() != 0 || r.fwdCount.Load() != 0 {
		t.Errorf("clearStats left non-zero counters")
	}
	if !r.haveSignal.Load() {
		t.Errorf("clearStats cleared the last signal reading; the firmware's doesn't")
	}
}

// Pins the neighbours layout the client decodes, and the telemetry order (voltage then temperature, temperature only when measurable).
func TestTelemetryBody(t *testing.T) {
	r := &Repeater{}
	r.batteryMV.Store(4168)
	r.haveBattery.Store(true)

	body, ok := r.buildReqResponse(&store.RepeaterACLEntry{Permissions: permAdmin}, reqTypeGetTelemetryData, nil, sensor.MaxReplyBody(nil))
	if !ok {
		t.Fatal("telemetry request answered nothing")
	}
	readings, err := meshcore.LPPDecode(body)
	if err != nil {
		t.Fatalf("LPPDecode: %v", err)
	}
	if len(readings) != 1 {
		t.Fatalf("got %d readings without an MCU temp, want 1 (voltage only)", len(readings))
	}
	// LPP voltage resolution is 0.01 V and the encoder rounds, so 4168 mV encodes as 4.17.
	if readings[0].Channel != telemChannelSelf || readings[0].Value != 4.17 {
		t.Errorf("voltage = ch%d %v, want ch%d 4.17", readings[0].Channel, readings[0].Value, telemChannelSelf)
	}

	r.mcuTempC.Store(227)
	r.haveMCUTemp.Store(true)
	body, _ = r.buildReqResponse(&store.RepeaterACLEntry{Permissions: permAdmin}, reqTypeGetTelemetryData, nil, sensor.MaxReplyBody(nil))
	readings, err = meshcore.LPPDecode(body)
	if err != nil {
		t.Fatalf("LPPDecode with temp: %v", err)
	}
	if len(readings) != 2 {
		t.Fatalf("got %d readings with an MCU temp, want 2", len(readings))
	}
	if readings[1].Channel != telemChannelSelf || readings[1].Value != 22.7 {
		t.Errorf("temperature = ch%d %v, want ch%d 22.7", readings[1].Channel, readings[1].Value, telemChannelSelf)
	}
}

func TestNeighboursBody(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	var a, b [32]byte
	a[0], a[1] = 0xAA, 0x01
	b[0], b[1] = 0xBB, 0x02
	now := time.Now()
	r.neighbors.m[a] = &neighbor{pubkey: a, snr: 5.0, heard: now}
	r.neighbors.m[b] = &neighbor{pubkey: b, snr: -2.5, heard: now.Add(-30 * time.Second)}

	// params: [version:1][count:1][offset:2][order_by:1][prefix_len:1]
	params := []byte{0, 10, 0, 0, 2, 6}
	body := r.neighboursBody(params)

	if total := binary.LittleEndian.Uint16(body[0:2]); total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	results := int(binary.LittleEndian.Uint16(body[2:4]))
	if results != 2 {
		t.Fatalf("results = %d, want 2", results)
	}
	const entrySize = 6 + 4 + 1
	if len(body) != 4+results*entrySize {
		t.Fatalf("body len = %d, want %d", len(body), 4+results*entrySize)
	}
	// Newest first: entry 0 is neighbour a (heard now, snr 5.0 → +20 quarter-dB).
	if body[4] != 0xAA {
		t.Errorf("entry0 prefix[0] = %#x, want 0xAA", body[4])
	}
	if snr := int8(body[4+6+4]); snr != 20 {
		t.Errorf("entry0 snr = %d, want 20 (quarter-dB)", snr)
	}
}

// TestRateLimiter: max allows per window, denied until it slides.
func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(2, 30*time.Millisecond)
	if !l.allow() || !l.allow() {
		t.Fatal("first two allows should pass")
	}
	if l.allow() {
		t.Fatal("third allow within window should be denied")
	}
	time.Sleep(35 * time.Millisecond)
	if !l.allow() {
		t.Fatal("allow after window expiry should pass")
	}
}

// Pins the anon REGIONS reply body against firmware exportNamesTo(mask=DENY_FLOOD).
func TestRegionsExport(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Regions: []config.RepeaterRegion{
		{Name: "alpha"}, {Name: "bravo", DenyFlood: true}, {Name: "*"},
	}}}
	if got := r.regionsExport(); got != "*,alpha" {
		t.Errorf("export = %q, want %q", got, "*,alpha")
	}
	r.cfg.Regions = nil // no "*" entry ⇒ unscoped flood denied ⇒ nothing exported
	if got := r.regionsExport(); got != "" {
		t.Errorf("empty export = %q, want \"\"", got)
	}
}

// Pins the order_by selector: 0/absent=newest, 1=oldest, 2=strongest, 3=weakest.
func TestNeighboursOrderBy(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	var a, b, c [32]byte
	a[0], b[0], c[0] = 0x01, 0x02, 0x03
	now := time.Now()
	r.neighbors.m[a] = &neighbor{pubkey: a, snr: 5.0, heard: now.Add(-60 * time.Second)} // oldest, strongest
	r.neighbors.m[b] = &neighbor{pubkey: b, snr: -3.0, heard: now}                       // newest, weakest
	r.neighbors.m[c] = &neighbor{pubkey: c, snr: 0.0, heard: now.Add(-30 * time.Second)}

	firstPrefix := func(orderBy byte) byte {
		params := []byte{0, 10, 0, 0, orderBy, 6}
		return r.neighboursBody(params)[4]
	}
	if got := firstPrefix(0); got != 0x02 {
		t.Errorf("order_by 0 (newest): first=%#x, want b(0x02)", got)
	}
	if got := firstPrefix(1); got != 0x01 {
		t.Errorf("order_by 1 (oldest): first=%#x, want a(0x01)", got)
	}
	if got := firstPrefix(2); got != 0x01 {
		t.Errorf("order_by 2 (strongest): first=%#x, want a(0x01)", got)
	}
	if got := firstPrefix(3); got != 0x02 {
		t.Errorf("order_by 3 (weakest): first=%#x, want b(0x02)", got)
	}
}

// Pins `region default`; the config write lands after the reply-TX delay, so it polls.
func TestRegionDefaultCLI(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Name: "rp"}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background()) // applyCfg's goroutine selects on runCtx
	defer cancel()
	r.runCtx = ctx

	await := func(region string, nRegions int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if cfg := r.cfgSnapshot(); cfg.DefaultRegion == region && len(cfg.Regions) == nRegions {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cfg := r.cfgSnapshot()
		t.Fatalf("cfg never became {region=%q nRegions=%d}; have region=%q regions=%+v",
			region, nRegions, cfg.DefaultRegion, cfg.Regions)
	}

	if got := r.runCLI("region default"); got != " default scope is <null>" {
		t.Fatalf("read empty = %q", got)
	}
	if got := r.runCLI("region default alpha"); got != " default scope is now alpha" {
		t.Fatalf("set = %q", got)
	}
	await("alpha", 1)
	if rg := r.cfgSnapshot().Regions[0]; rg.Name != "alpha" || rg.DenyFlood {
		t.Fatalf("auto-created region wrong: %+v", rg)
	}
	if got := r.runCLI("region default"); got != " default scope is alpha" {
		t.Fatalf("read set = %q", got)
	}
	if got := r.runCLI("region default <null>"); got != " default scope is now <null>" {
		t.Fatalf("clear = %q", got)
	}
	await("", 1) // clearing keeps the region itself
}

// Pins the firmware asymmetry: a revoke accepts a pubkey prefix, a grant does not.
func TestSetACLPrefix(t *testing.T) {
	full := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	r := &Repeater{}
	r.acl.m = map[string]*store.RepeaterACLEntry{full: {PubKey: full, Permissions: permAdmin}}

	if got, ok := r.aclMatchPrefix("aabbccddeeff"); !ok || got != full {
		t.Errorf("prefix match = %q,%v, want %q,true", got, ok, full)
	}
	if got, ok := r.aclMatchPrefix(full); !ok || got != full {
		t.Errorf("exact match = %q,%v, want %q,true", got, ok, full)
	}
	if _, ok := r.aclMatchPrefix("ffffffffffff"); ok {
		t.Error("unknown prefix matched")
	}

	if err := r.SetACL("aabbccddeeff", permAdmin); err == nil {
		t.Error("granting a role by prefix should fail")
	}
	if err := r.SetACL("ffffffffffff", 0); err == nil {
		t.Error("revoking an unknown prefix should fail")
	}
	if err := r.SetACL("zz", 0); err == nil {
		t.Error("non-hex pubkey should fail")
	}
}

// `neighbor.remove` matches on the bytes supplied, prefix included.
func TestNeighborRemove(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	var a [32]byte
	a[0], a[1] = 0xAA, 0xBB
	r.neighbors.m[a] = &neighbor{pubkey: a}

	if got := r.runCLI("neighbor.remove zz"); got != "ERR: bad pubkey" {
		t.Errorf("bad hex = %q", got)
	}
	if got := r.runCLI("neighbor.remove aabb"); got != "OK" {
		t.Errorf("prefix remove = %q, want OK", got)
	}
	if len(r.neighbors.m) != 0 {
		t.Errorf("neighbour not removed: %+v", r.neighbors.m)
	}
	if got := r.runCLI("neighbor.remove aabb"); got != "OK" {
		t.Errorf("removing a missing neighbour = %q, want OK (firmware always replies OK)", got)
	}
}

// Pins the bare `region` reply against the firmware's exportTo.
// "Remove all Neighbours" in the app sends an empty pubkey: a zero-length prefix matches every
// entry, matching fromHex(dest, 0, "") plus a 0-byte memcmp in the firmware.
func TestCLINeighborRemoveAll(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	var a, b [32]byte
	a[0], b[0] = 0xaa, 0xcc
	r.neighbors.m[a] = &neighbor{pubkey: a}
	r.neighbors.m[b] = &neighbor{pubkey: b}

	if got := r.runCLI("neighbor.remove "); got != "OK" {
		t.Fatalf("empty pubkey = %q, want OK (this is the app's Remove all Neighbours)", got)
	}
	if len(r.neighbors.m) != 0 {
		t.Errorf("%d neighbours left, want all cleared: %+v", len(r.neighbors.m), r.neighbors.m)
	}
}

func TestRegionTree(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{
		HomeRegion: "alpha",
		Regions: []config.RepeaterRegion{
			{Name: "*"}, {Name: "alpha"}, {Name: "bravo", DenyFlood: true},
		},
	}}
	want := "* F\n alpha^ F\n bravo\n"
	if got := r.runCLI("region"); got != want {
		t.Errorf("region tree = %q, want %q", got, want)
	}

	r.cfg.Regions = []config.RepeaterRegion{{Name: "alpha"}} // no "*" ⇒ unscoped flood denied
	if got := r.runCLI("region"); got != "*\n alpha^ F\n" {
		t.Errorf("region tree without wildcard = %q", got)
	}
}

func TestRegionGetHomeSaveLoad(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Regions: []config.RepeaterRegion{
		{Name: "alpha"}, {Name: "bravo", DenyFlood: true},
	}}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.runCtx = ctx

	if got := r.runCLI("region get alpha"); got != " alpha F" {
		t.Errorf("get alpha = %q", got)
	}
	if got := r.runCLI("region get bravo"); got != " bravo " { // sprintf(" %s %s") leaves a trailing space
		t.Errorf("get bravo = %q", got)
	}
	if got := r.runCLI("region get ghost"); got != "Err - unknown region" {
		t.Errorf("get ghost = %q", got)
	}
	if got := r.runCLI("region save"); got != "OK" {
		t.Errorf("save = %q", got)
	}
	if got := r.runCLI("region load"); got != "" {
		t.Errorf("load = %q, want empty (firmware replies nothing)", got)
	}
	if got := r.runCLI("region home"); got != " home is *" {
		t.Errorf("home read = %q", got)
	}
	if got := r.runCLI("region home ghost"); got != "Err - unknown region" {
		t.Errorf("home ghost = %q", got)
	}
	if got := r.runCLI("region home alpha"); got != " home is now alpha" {
		t.Errorf("home set = %q", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for r.cfgSnapshot().HomeRegion != "alpha" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.runCLI("region home"); got != " home is alpha" {
		t.Errorf("home read back = %q", got)
	}
}

// A dangling defaultRegion/homeRegion fails Config.Validate and would break the reload after the reply.
func TestRegionRemoveClearsRefs(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{
		DefaultRegion: "alpha", HomeRegion: "alpha",
		Regions: []config.RepeaterRegion{{Name: "alpha"}},
	}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.runCtx = ctx

	if got := r.runCLI("region remove alpha"); got != "OK" {
		t.Fatalf("remove = %q", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(r.cfgSnapshot().Regions) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if cfg := r.cfgSnapshot(); cfg.DefaultRegion != "" || cfg.HomeRegion != "" {
		t.Errorf("dangling refs left: default=%q home=%q", cfg.DefaultRegion, cfg.HomeRegion)
	}
}

// A reply carries at most what fits the firmware's 130-byte results_buffer, however many the client asks for.
func TestNeighboursBodyCap(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	now := time.Now()
	for i := 0; i < 40; i++ {
		var k [32]byte
		k[0] = byte(i)
		r.neighbors.m[k] = &neighbor{pubkey: k, heard: now}
	}
	body := r.neighboursBody([]byte{0, 255, 0, 0, 0, 6})
	if total := binary.LittleEndian.Uint16(body[0:2]); total != 40 {
		t.Errorf("total = %d, want 40", total)
	}
	results := int(binary.LittleEndian.Uint16(body[2:4]))
	if results != 130/11 {
		t.Errorf("results = %d, want %d", results, 130/11)
	}
	if len(body) != 4+results*11 {
		t.Errorf("body len = %d, want %d", len(body), 4+results*11)
	}
}

// Pins the RepeaterStats field offsets for the counters.
func TestStatusBodyCounters(t *testing.T) {
	r := &Repeater{}
	r.sentFlood.Store(11)
	r.sentDirect.Store(22)
	r.routeStats = func() node.RouteStats {
		return node.RouteStats{FloodReceived: 33, DirectReceived: 44, DirectDuplicates: 55, FloodDuplicates: 66}
	}

	b := r.statusBody()
	for _, c := range []struct {
		name string
		off  int
		want uint32
	}{
		{"n_sent_flood", 24, 11},
		{"n_sent_direct", 28, 22},
		{"n_recv_flood", 32, 33},
		{"n_recv_direct", 36, 44},
		{"n_packets_sent", 12, 33}, // radio getPacketsSent: every TX, flood + direct
	} {
		if got := binary.LittleEndian.Uint32(b[c.off : c.off+4]); got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}
	if got := binary.LittleEndian.Uint16(b[44:46]); got != 55 {
		t.Errorf("n_direct_dups = %d, want 55", got)
	}
	if got := binary.LittleEndian.Uint16(b[46:48]); got != 66 {
		t.Errorf("n_flood_dups = %d, want 66", got)
	}

	r.clearStats()
	b = r.statusBody()
	for off := 24; off < 40; off += 4 {
		if got := binary.LittleEndian.Uint32(b[off : off+4]); got != 0 {
			t.Errorf("clearStats left offset %d = %d", off, got)
		}
	}
}

// Pins the rand[0, 5·airtime·factor] envelope and rxdelay being off at base 0.
func TestRelayDelay(t *testing.T) {
	for range 200 {
		if d := relayDelay(100, 0.5); d < 0 || d > 250*time.Millisecond {
			t.Fatalf("relayDelay = %v, want 0-250ms", d)
		}
	}
	if d := relayDelay(100, 0); d != 0 {
		t.Errorf("factor 0 delay = %v, want 0", d)
	}
	r := &Repeater{sf: 7}
	if d := r.rxDelay(&meshcore.Packet{}, 40, 100); d != 0 {
		t.Errorf("rxdelay base 0 = %v, want 0", d)
	}
	base := 10.0
	r.cfg.RxDelayBase = &base
	weak := r.rxDelay(&meshcore.Packet{SNR: -7}, 40, 100)
	strong := r.rxDelay(&meshcore.Packet{SNR: 10}, 40, 100)
	// score 0.042 → (10^0.808-1)×100ms ≈ 543ms; score 1 → negative, which the library treats as "now".
	if weak < 500*time.Millisecond || weak > 600*time.Millisecond || strong > 0 {
		t.Errorf("rxdelay weak %v strong %v", weak, strong)
	}
}

// Our own sends count alongside relays, as the firmware Dispatcher does.
func TestCountTx(t *testing.T) {
	r := &Repeater{}
	r.countTx(true)
	r.countTx(false)
	r.countTx(false)
	if r.sentFlood.Load() != 1 || r.sentDirect.Load() != 2 {
		t.Errorf("flood = %d direct = %d, want 1/2", r.sentFlood.Load(), r.sentDirect.Load())
	}
}

// `get` must report back in the same firmware units `set` accepts; the stored value is seconds.
func TestAdvertIntervalRoundTrip(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.runCtx = ctx

	await := func(read, want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if r.runCLI(read) == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("%q never became %q; have %q", read, want, r.runCLI(read))
	}

	if got := r.runCLI("set advert.interval 60"); got != "OK" {
		t.Fatalf("set advert.interval 60 = %q", got)
	}
	await("get advert.interval", "> 60")
	if v := *r.cfgSnapshot().AdvertInterval; v != 3600 {
		t.Errorf("stored %d seconds, want 3600", v)
	}

	// Firmware keeps this in 2-minute units, so odd minutes round down.
	if got := r.runCLI("set advert.interval 61"); got != "OK" {
		t.Fatalf("set advert.interval 61 = %q", got)
	}
	await("get advert.interval", "> 60")

	if got := r.runCLI("set flood.advert.interval 12"); got != "OK" {
		t.Fatalf("set flood.advert.interval 12 = %q", got)
	}
	await("get flood.advert.interval", "> 12")

	// 1 minute is what a seconds-based UI produces, and the firmware range rejects it.
	if got := r.runCLI("set advert.interval 1"); got != "Error: interval range is 60-240 minutes" {
		t.Errorf("set advert.interval 1 = %q, want the range error", got)
	}
}

// Pins the reply shapes clients parse against the firmware's own sprintf formats.
func TestCLIReplyFormats(t *testing.T) {
	lat, lon, freq := -41.28, 174.0, 915.0
	r := &Repeater{cfg: config.RepeaterConfig{Latitude: &lat, Longitude: &lon}}

	if got := formatClock(time.Date(2026, 9, 3, 7, 5, 30, 0, time.UTC)); got != "07:05 - 3/9/2026 UTC" {
		t.Errorf("formatClock = %q, want %q", got, "07:05 - 3/9/2026 UTC")
	}
	if got := r.runCLI("clock"); !strings.HasSuffix(got, " UTC") {
		t.Errorf("clock = %q, want a DateTime string, not a unix timestamp", got)
	}
	if got := r.runCLI("clock sync"); !strings.HasPrefix(got, "OK - clock set: ") {
		t.Errorf("clock sync = %q", got)
	}

	// A whole number still carries ".0", as StrHelper::ftoa does.
	if got := r.runCLI("get lon"); got != "> 174.0" {
		t.Errorf("get lon = %q, want %q", got, "> 174.0")
	}
	if got := r.runCLI("get lat"); got != "> -41.28" {
		t.Errorf("get lat = %q, want %q", got, "> -41.28")
	}
	r.cfg.Latitude = nil
	if got := r.runCLI("get lat"); got != "> 0.0" {
		t.Errorf("unset lat = %q, want %q", got, "> 0.0")
	}
	if got := floatOrZero(&freq); got != "915.0" {
		t.Errorf("freq = %q, want %q", got, "915.0")
	}

	if got := r.runCLI("ver"); !strings.Contains(got, "(Build: ") {
		t.Errorf("ver = %q, want the firmware's \"<ver> (Build: <date>)\" shape", got)
	}
}

// The text reply stays inside the firmware's 134-byte buffer however many neighbours are known.
func TestNeighborsListCap(t *testing.T) {
	r := &Repeater{}
	r.neighbors.m = map[[32]byte]*neighbor{}
	if got := r.runCLI("neighbors"); got != "-none-" {
		t.Errorf("empty = %q, want -none-", got)
	}
	now := time.Now()
	for i := 0; i < 40; i++ {
		var k [32]byte
		k[0] = byte(i)
		r.neighbors.m[k] = &neighbor{pubkey: k, heard: now}
	}
	got := r.runCLI("neighbors")
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected several neighbours, got %q", got)
	}
	// Firmware appends while `dp - reply < 134`, so everything but the last entry fits under the bound.
	if before := len(strings.Join(lines[:len(lines)-1], "\n")); before >= neighborsTextMax {
		t.Errorf("appended an entry at %d bytes, past the %d bound", before, neighborsTextMax)
	}
	if len(got) < neighborsTextMax {
		t.Errorf("stopped at %d bytes with neighbours left over", len(got))
	}
}

// testReconfigure stands in for the app's persist+reload hook, mutating r.cfg under the config lock.
func testReconfigure(r *Repeater) func(func(*config.RepeaterConfig)) error {
	return func(m func(*config.RepeaterConfig)) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		m(&r.cfg)
		return nil
	}
}

// Pins the send-order flip: a flood request's accumulated path lists the client's neighbour first.
func TestReverseHops(t *testing.T) {
	if got := reverseHops([]byte{1, 2, 3}, 1); string(got) != string([]byte{3, 2, 1}) {
		t.Errorf("1-byte = %x", got)
	}
	if got := reverseHops([]byte{1, 2, 3, 4}, 2); string(got) != string([]byte{3, 4, 1, 2}) {
		t.Errorf("2-byte = %x", got)
	}
	if got := reverseHops(nil, 0); len(got) != 0 {
		t.Errorf("empty = %x", got)
	}
}

// Pins the firmware's unknown-command reply strings and the parsing helpers behind `set`.
func TestCLIFirmwareReplies(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Name: "rp"}}
	for cmd, want := range map[string]string{
		"get nonsense":         "??: nonsense",
		"get af":               "ERR: not supported on this node",
		"get bridge.type":      "> none",
		"set name":             "unknown config: name",
		"discover.neighbors x": "Err - discover.neighbors has no options",
		"region bogus":         "Err - ??",
		"region list":          "Err - ??",
		"region list foo":      "Err - use 'allowed' or 'denied'",
		"region remove *":      "Err - not empty",
		"region put":           "Err - ??",
		"region put bad name":  "Err - unknown parent",
		"region put a,b":       "Err - unable to put",
		"region default":       " default scope is <null>",
		"setperm abc":          "Err - bad params",
		"setperm abc 3":        "Err - bad pubkey",
		"setperm zz 3":         "Err - bad pubkey",
		"setperm aabb 3":       "Err - invalid params",
		"setperm aabb 0":       "Err - invalid params",
		"neighbor.remove aab":  "ERR: bad pubkey",
	} {
		if got := r.runCLI(cmd); got != want {
			t.Errorf("%q = %q, want %q", cmd, got, want)
		}
	}
	for s, want := range map[string]int{"12": 12, " -7x": -7, "+3": 3, "abc": 0, "": 0, "2.9": 2} {
		if got := atoi(s); got != want {
			t.Errorf("atoi(%q) = %d, want %d", s, got, want)
		}
	}
	for s, want := range map[string]float64{"1.5": 1.5, "-41.28abc": -41.28, "abc": 0, "": 0, " 3": 3} {
		if got := atof(s); got != want {
			t.Errorf("atof(%q) = %v, want %v", s, got, want)
		}
	}
	if got := digits("-5"); got != 0 {
		t.Errorf("digits(-5) = %d, want 0 (no sign in _atoi)", got)
	}

	// `password` echoes the stored value, truncated to the firmware's 15 chars.
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.runCtx = ctx
	if got := r.runCLI("password 0123456789abcdefX"); got != "password now: 0123456789abcde" {
		t.Errorf("password = %q", got)
	}
}

// setperm stores the whole permission byte and revokes on any guest role (perms&3 == 0).
func TestSetPermByte(t *testing.T) {
	full := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	r := &Repeater{} // no store: the ACL cache is memory-only
	r.acl.m = map[string]*store.RepeaterACLEntry{full: {PubKey: full, Permissions: permAdmin}}
	if err := r.SetACL(full, 0xC3); err != nil {
		t.Fatalf("grant 0xC3: %v", err)
	}
	if got := r.acl.m[full].Permissions; got != 0xC3 {
		t.Errorf("stored perms = %#x, want 0xc3 (whole byte)", got)
	}
	if err := r.SetACL("aabbccddeeff", 4); err != nil { // 4&3 == 0 → revoke by prefix
		t.Fatalf("revoke with perms 4: %v", err)
	}
	if _, ok := r.acl.m[full]; ok {
		t.Error("client not revoked")
	}
}

// Pins findByNamePrefix: exact wins, else the last prefix match, and the wildcard is always known.
func TestRegionPrefixLookup(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Regions: []config.RepeaterRegion{
		{Name: "alpha"}, {Name: "alphabet", DenyFlood: true}, {Name: "al"},
	}}}
	for cmd, want := range map[string]string{
		"region get al":    " al F",
		"region get alp":   " alphabet ",
		"region get alpha": " alpha F",
		"region get *":     " * ",
		"region get zz":    "Err - unknown region",
	} {
		if got := r.runCLI(cmd); got != want {
			t.Errorf("%q = %q, want %q", cmd, got, want)
		}
	}
}

// A guest is answered too, payload[0] is an INVERSE mask, and base goes out whatever it says (firmware handleRequest).
func TestTelemetryHonoursTheRequestersInverseMask(t *testing.T) {
	newRepeater := func() *Repeater {
		r := &Repeater{}
		r.batteryMV.Store(4168)
		r.haveBattery.Store(true)
		r.mcuTempC.Store(215)
		r.haveMCUTemp.Store(true)
		return r
	}

	for _, tc := range []struct {
		name   string
		params []byte
		want   int // readings on the self channel
	}{
		{"no params means everything", nil, 2},
		{"zero mask means everything", []byte{0x00}, 2},
		{"excluding environment leaves base alone", []byte{sensor.PermEnvironment}, 2},
		{"excluding location leaves base alone", []byte{sensor.PermLocation}, 2},
		{"excluding base still sends it", []byte{sensor.PermBase}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := newRepeater().buildReqResponse(
				&store.RepeaterACLEntry{Permissions: permGuest}, reqTypeGetTelemetryData, tc.params, sensor.MaxReplyBody(nil))
			if !ok {
				t.Fatal("telemetry request was not answered; the firmware answers a guest too")
			}
			readings, err := meshcore.LPPDecode(body)
			if err != nil {
				t.Fatalf("LPPDecode: %v", err)
			}
			if len(readings) != tc.want {
				t.Fatalf("got %d readings, want %d: %+v", len(readings), tc.want, readings)
			}
			for _, rd := range readings {
				if rd.Channel != telemChannelSelf {
					t.Errorf("reading on channel %d, want %d", rd.Channel, telemChannelSelf)
				}
			}
		})
	}
}

// A host with no battery still sends one at 0 V, as every firmware node does.
func TestTelemetryReportsZeroVoltsWhenTheHostHasNoBattery(t *testing.T) {
	r := &Repeater{} // batteryMV left at 0, as on a Raspberry Pi
	r.mcuTempC.Store(352)
	r.haveMCUTemp.Store(true)

	body, _ := r.buildReqResponse(&store.RepeaterACLEntry{Permissions: permAdmin}, reqTypeGetTelemetryData, nil, sensor.MaxReplyBody(nil))
	readings, err := meshcore.LPPDecode(body)
	if err != nil {
		t.Fatalf("LPPDecode: %v", err)
	}
	// LPPReading.Value is an any, so the 0 has to be written as a float or it never compares equal.
	if len(readings) != 2 || readings[0].Value != 0.0 || readings[1].Value != 35.2 {
		t.Fatalf("got %+v, want 0 V and the 35.2 C temperature", readings)
	}

	// A board that does report a battery still publishes it.
	r.batteryMV.Store(4168)
	r.haveBattery.Store(true)
	body, _ = r.buildReqResponse(&store.RepeaterACLEntry{Permissions: permAdmin}, reqTypeGetTelemetryData, nil, sensor.MaxReplyBody(nil))
	readings, _ = meshcore.LPPDecode(body)
	// LPP voltage has 0.01 V resolution and the encoder rounds, so 4168 mV comes back as 4.17.
	if len(readings) != 2 || readings[0].Value != 4.17 {
		t.Errorf("got %+v, want the battery plus the temperature", readings)
	}
}
