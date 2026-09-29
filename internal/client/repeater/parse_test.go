package repeater

import (
	"encoding/binary"
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

// Pins the OutPath contract: only a nil OutPath floods, and pathLen is (hashSize-1)<<6 | hopCount.
func TestRouteForPeer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		peer          *node.Peer
		wantRouteType byte
		wantPathLen   uint8
		bytesPerHop   uint8 // the contact's setting; 0 reads as 1
	}{
		{
			name:          "nil peer floods",
			peer:          nil,
			wantRouteType: meshcore.RouteTypeFlood, // 0x01
			wantPathLen:   0,
		},
		{
			name:          "nil OutPath (route unknown) floods",
			peer:          &node.Peer{OutPath: nil},
			wantRouteType: meshcore.RouteTypeFlood, // 0x01
			wantPathLen:   0,
		},
		{
			name:          "empty non-nil OutPath is a direct 0-hop neighbour",
			peer:          &node.Peer{OutPath: []byte{}},
			wantRouteType: meshcore.RouteTypeDirect, // 0x02
			wantPathLen:   0,
		},
		{
			// hashSize 0 defaults to 1, so 3 path bytes are 3 hops: (1-1)<<6 | 3 = 3.
			name:          "3-hop path, default hash size (0 -> 1)",
			peer:          &node.Peer{OutPath: []byte{0xAA, 0xBB, 0xCC}, OutPathHashSize: 0},
			wantRouteType: meshcore.RouteTypeDirect, // 0x02
			wantPathLen:   3,
		},
		{
			// (1-1)<<6 | 2 = 2.
			name:          "2-hop path, explicit hash size 1",
			peer:          &node.Peer{OutPath: []byte{0x11, 0x22}, OutPathHashSize: 1},
			wantRouteType: meshcore.RouteTypeDirect,
			wantPathLen:   2,
		},
		{
			// (2-1)<<6 | 2 = 66.
			name:          "2-hop path, hash size 2",
			peer:          &node.Peer{OutPath: []byte{0x11, 0x22, 0x33, 0x44}, OutPathHashSize: 2},
			wantRouteType: meshcore.RouteTypeDirect,
			wantPathLen:   66,
		},
		{
			// A flood carries the contact's size, as the far end floods its reply at the request's.
			name:          "flood at 2 bytes per hop",
			peer:          &node.Peer{OutPath: nil},
			bytesPerHop:   2,
			wantRouteType: meshcore.RouteTypeFlood,
			wantPathLen:   0x40,
		},
		{
			name:          "0-hop direct at 3 bytes per hop",
			peer:          &node.Peer{OutPath: []byte{}},
			bytesPerHop:   3,
			wantRouteType: meshcore.RouteTypeDirect,
			wantPathLen:   0x80,
		},
		{
			// (4-1)<<6 | 2 = 194.
			name:          "2-hop path, hash size 4",
			peer:          &node.Peer{OutPath: []byte{1, 2, 3, 4, 5, 6, 7, 8}, OutPathHashSize: 4},
			wantRouteType: meshcore.RouteTypeDirect,
			wantPathLen:   194,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var path []byte
			var hashSize uint8
			if tt.peer != nil {
				path, hashSize = tt.peer.OutPath, tt.peer.OutPathHashSize
			}
			gotRouteType, gotPathLen := routeForPeer(path, hashSize, tt.bytesPerHop)
			if gotRouteType != tt.wantRouteType {
				t.Errorf("routeForPeer(%+v) routeType = 0x%02x, want 0x%02x",
					tt.peer, gotRouteType, tt.wantRouteType)
			}
			if gotPathLen != tt.wantPathLen {
				t.Errorf("routeForPeer(%+v) pathLen = %d, want %d",
					tt.peer, gotPathLen, tt.wantPathLen)
			}
		})
	}
}

// buildStatus is the 56-byte little-endian wire layout parseRepeaterStatus reads; callers truncate as needed.
func buildStatus() []byte {
	b := make([]byte, 56)
	binary.LittleEndian.PutUint16(b[0:2], 3700)         // batteryMv
	binary.LittleEndian.PutUint16(b[2:4], 5)            // queueLen
	binary.LittleEndian.PutUint16(b[4:6], 0xFFA1)       // noiseFloor i16(-95) = 65441
	binary.LittleEndian.PutUint16(b[6:8], 0xFFD6)       // lastRssi   i16(-42) = 65494
	binary.LittleEndian.PutUint32(b[8:12], 1000)        // packetsRecv
	binary.LittleEndian.PutUint32(b[12:16], 2000)       // packetsSent
	binary.LittleEndian.PutUint32(b[16:20], 300)        // txAirSecs
	binary.LittleEndian.PutUint32(b[20:24], 86400)      // uptimeSecs
	binary.LittleEndian.PutUint32(b[24:28], 11)         // floodTx
	binary.LittleEndian.PutUint32(b[28:32], 22)         // directTx
	binary.LittleEndian.PutUint32(b[32:36], 33)         // floodRx
	binary.LittleEndian.PutUint32(b[36:40], 44)         // directRx
	binary.LittleEndian.PutUint16(b[40:42], 7)          // errEvents (KNOWN distinct value)
	binary.LittleEndian.PutUint16(b[42:44], uint16(24)) // lastSnr quarter-dB -> 6.0 dB
	binary.LittleEndian.PutUint16(b[44:46], 8)          // directDups
	binary.LittleEndian.PutUint16(b[46:48], 9)          // floodDups
	binary.LittleEndian.PutUint32(b[48:52], 700)        // rxAirSecs
	binary.LittleEndian.PutUint32(b[52:56], 99)         // recvErrors (KNOWN distinct value)
	return b
}

func TestParseRepeaterStatus(t *testing.T) {
	t.Parallel()

	t.Run("full 56-byte payload parses every field", func(t *testing.T) {
		t.Parallel()
		got, err := parseRepeaterStatus(buildStatus())
		if err != nil {
			t.Fatalf("parseRepeaterStatus returned error: %v", err)
		}

		want := Status{
			BatteryMV:   3700,
			QueueLen:    5,
			NoiseFloor:  -95,
			LastRSSI:    -42,
			PacketsRecv: 1000,
			PacketsSent: 2000,
			TxAirSecs:   300,
			RxAirSecs:   700,
			UptimeSecs:  86400,
			FloodTx:     11,
			DirectTx:    22,
			FloodRx:     33,
			DirectRx:    44,
			ErrEvents:   7,
			LastSNR:     6.0, // 24 / 4
			DirectDups:  8,
			FloodDups:   9,
			RecvErrors:  99,
			// chanUtil = (300+700)/86400*100 = 1.157407...
			ChanUtil: float64(300+700) / float64(86400) * 100,
		}
		if *got != want {
			t.Errorf("parseRepeaterStatus mismatch:\n got = %+v\nwant = %+v", *got, want)
		}
	})

	t.Run("errEvents and recvErrors are NOT conflated", func(t *testing.T) {
		t.Parallel()
		// Distinct known values at distinct offsets must land in distinct fields.
		got, err := parseRepeaterStatus(buildStatus())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ErrEvents != 7 {
			t.Errorf("ErrEvents = %d, want 7 (from bytes 40-42)", got.ErrEvents)
		}
		if got.RecvErrors != 99 {
			t.Errorf("RecvErrors = %d, want 99 (from bytes 52-56)", got.RecvErrors)
		}
		if uint32(got.ErrEvents) == got.RecvErrors {
			t.Errorf("ErrEvents and RecvErrors collapsed to the same value %d", got.RecvErrors)
		}
	})

	t.Run("52-byte payload (no recvErrors) parses without error", func(t *testing.T) {
		t.Parallel()
		data := buildStatus()[:52] // drop the trailing recvErrors u32
		got, err := parseRepeaterStatus(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.RxAirSecs != 700 {
			t.Errorf("RxAirSecs = %d, want 700", got.RxAirSecs)
		}
		if got.RecvErrors != 0 {
			t.Errorf("RecvErrors = %d, want 0 (field absent at 52 bytes)", got.RecvErrors)
		}
	})

	t.Run("truncated payload returns error, no panic", func(t *testing.T) {
		t.Parallel()
		for _, n := range []int{0, 1, 51} {
			got, err := parseRepeaterStatus(make([]byte, n))
			if err == nil {
				t.Errorf("len=%d: expected error, got nil (status=%+v)", n, got)
			}
			if got != nil {
				t.Errorf("len=%d: expected nil status on error, got %+v", n, got)
			}
		}
	})
}

func TestParseRepeaterNeighbors(t *testing.T) {
	t.Parallel()

	const prefixLen = 6
	// Entry layout: [prefix:prefixLen][secsAgo:u32 LE][snr:i8].
	buildNeighbors := func(total, results int16, entries [][]byte) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint16(b[0:2], uint16(total))
		binary.LittleEndian.PutUint16(b[2:4], uint16(results))
		for _, e := range entries {
			b = append(b, e...)
		}
		return b
	}
	entry := func(prefix []byte, secsAgo uint32, snr int8) []byte {
		e := make([]byte, 0, prefixLen+5)
		e = append(e, prefix...)
		s := make([]byte, 4)
		binary.LittleEndian.PutUint32(s, secsAgo)
		e = append(e, s...)
		e = append(e, byte(snr))
		return e
	}

	t.Run("two entries parse with correct fields", func(t *testing.T) {
		t.Parallel()
		p1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
		p2 := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
		data := buildNeighbors(2, 2, [][]byte{
			entry(p1, 120, -5),
			entry(p2, 3600, 10),
		})

		got, err := parseRepeaterNeighbors(data, prefixLen)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.TotalCount != 2 || got.ResultsCount != 2 {
			t.Fatalf("counts = total %d / results %d, want 2 / 2", got.TotalCount, got.ResultsCount)
		}
		if len(got.Neighbors) != 2 {
			t.Fatalf("got %d neighbors, want 2", len(got.Neighbors))
		}
		if got.Neighbors[0].PubkeyPrefix != "010203040506" {
			t.Errorf("neighbor[0] prefix = %q, want 010203040506", got.Neighbors[0].PubkeyPrefix)
		}
		if got.Neighbors[0].SecsAgo != 120 || got.Neighbors[0].SNR != -1.25 {
			t.Errorf("neighbor[0] = secsAgo %d / snr %v, want 120 / -1.25", got.Neighbors[0].SecsAgo, got.Neighbors[0].SNR)
		}
		if got.Neighbors[1].PubkeyPrefix != "aabbccddeeff" {
			t.Errorf("neighbor[1] prefix = %q, want aabbccddeeff", got.Neighbors[1].PubkeyPrefix)
		}
		if got.Neighbors[1].SecsAgo != 3600 || got.Neighbors[1].SNR != 2.5 {
			t.Errorf("neighbor[1] = secsAgo %d / snr %v, want 3600 / 2.5", got.Neighbors[1].SecsAgo, got.Neighbors[1].SNR)
		}
	})

	t.Run("zero results yields empty list with counts preserved", func(t *testing.T) {
		t.Parallel()
		data := buildNeighbors(5, 0, nil)
		got, err := parseRepeaterNeighbors(data, prefixLen)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.TotalCount != 5 || got.ResultsCount != 0 {
			t.Errorf("counts = %d / %d, want 5 / 0", got.TotalCount, got.ResultsCount)
		}
		if len(got.Neighbors) != 0 {
			t.Errorf("got %d neighbors, want 0", len(got.Neighbors))
		}
	})

	t.Run("results count larger than available data stops at boundary", func(t *testing.T) {
		t.Parallel()
		// Header claims 3 results but only one full entry follows.
		data := buildNeighbors(3, 3, [][]byte{entry([]byte{1, 2, 3, 4, 5, 6}, 7, 1)})
		got, err := parseRepeaterNeighbors(data, prefixLen)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got.Neighbors) != 1 {
			t.Errorf("got %d neighbors, want 1 (loop must break at data boundary)", len(got.Neighbors))
		}
	})

	t.Run("header too short returns error, no panic", func(t *testing.T) {
		t.Parallel()
		for _, n := range []int{0, 1, 3} {
			got, err := parseRepeaterNeighbors(make([]byte, n), prefixLen)
			if err == nil {
				t.Errorf("len=%d: expected error, got nil (%+v)", n, got)
			}
		}
	})
}

func TestParseRepeaterAccessList(t *testing.T) {
	t.Parallel()
	// stride = 7: 6-byte pubkey prefix + 1-byte permissions.

	t.Run("two entries with decoded permissions", func(t *testing.T) {
		t.Parallel()
		data := []byte{
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x03, // ADMIN (perms=3)
			0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x01, // READ_ONLY (perms=1)
		}
		got := parseRepeaterAccessList(data)
		if len(got.Entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(got.Entries))
		}
		if got.Entries[0].PubkeyPrefix != "010203040506" || got.Entries[0].Permissions != 3 {
			t.Errorf("entry[0] = %q/%d, want 010203040506/3", got.Entries[0].PubkeyPrefix, got.Entries[0].Permissions)
		}
		if got.Entries[1].PubkeyPrefix != "aabbccddeeff" || got.Entries[1].Permissions != 1 {
			t.Errorf("entry[1] = %q/%d, want aabbccddeeff/1", got.Entries[1].PubkeyPrefix, got.Entries[1].Permissions)
		}
	})

	t.Run("empty input yields zero entries (non-nil slice)", func(t *testing.T) {
		t.Parallel()
		got := parseRepeaterAccessList(nil)
		if got == nil {
			t.Fatal("got nil AccessList")
		}
		if len(got.Entries) != 0 {
			t.Errorf("got %d entries, want 0", len(got.Entries))
		}
	})

	t.Run("trailing partial entry (< stride) is ignored", func(t *testing.T) {
		t.Parallel()
		// One full 7-byte entry plus 3 trailing bytes that don't form an entry.
		data := []byte{1, 2, 3, 4, 5, 6, 2, 0xDE, 0xAD, 0xBE}
		got := parseRepeaterAccessList(data)
		if len(got.Entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(got.Entries))
		}
		if got.Entries[0].Permissions != 2 { // READ_WRITE
			t.Errorf("entry[0] perms = %d, want 2", got.Entries[0].Permissions)
		}
	})
}

func TestParseRepeaterOwnerInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         []byte
		wantFirmware  string
		wantNodeName  string
		wantOwnerInfo string
	}{
		{
			name:          "three newline-separated fields",
			input:         []byte("v1.2.3\nRepeaterAlpha\nOwned by Wesley"),
			wantFirmware:  "v1.2.3",
			wantNodeName:  "RepeaterAlpha",
			wantOwnerInfo: "Owned by Wesley",
		},
		{
			name:          "owner-info field keeps embedded newlines (SplitN limit 3)",
			input:         []byte("v1\nNode\nline1\nline2"),
			wantFirmware:  "v1",
			wantNodeName:  "Node",
			wantOwnerInfo: "line1\nline2",
		},
		{
			name:          "trailing NUL padding is trimmed",
			input:         []byte("v1\nNode\nInfo\x00\x00\x00"),
			wantFirmware:  "v1",
			wantNodeName:  "Node",
			wantOwnerInfo: "Info",
		},
		{
			name:          "only firmware field present",
			input:         []byte("v9.9.9"),
			wantFirmware:  "v9.9.9",
			wantNodeName:  "",
			wantOwnerInfo: "",
		},
		{
			name:          "empty input yields all-empty struct",
			input:         []byte{},
			wantFirmware:  "",
			wantNodeName:  "",
			wantOwnerInfo: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseRepeaterOwnerInfo(tt.input)
			if got.FirmwareVersion != tt.wantFirmware {
				t.Errorf("FirmwareVersion = %q, want %q", got.FirmwareVersion, tt.wantFirmware)
			}
			if got.NodeName != tt.wantNodeName {
				t.Errorf("NodeName = %q, want %q", got.NodeName, tt.wantNodeName)
			}
			if got.OwnerInfo != tt.wantOwnerInfo {
				t.Errorf("OwnerInfo = %q, want %q", got.OwnerInfo, tt.wantOwnerInfo)
			}
		})
	}
}

// The firmware never emits a permissions==0 entry, so an all-zero stride is cipher-block padding, not a client.
func TestParseRepeaterAccessListPadding(t *testing.T) {
	t.Parallel()
	data := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x03,
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0xC3, // sensor admin + both alert bits
	}
	data = append(data, make([]byte, 14)...) // 18 -> 32 bytes
	got := parseRepeaterAccessList(data)
	if len(got.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (padding must not become clients): %+v", len(got.Entries), got.Entries)
	}
	if got.Entries[1].Permissions != 0xC3 {
		t.Errorf("entry[1] perms = %#x, want 0xc3", got.Entries[1].Permissions)
	}
}

// Pins [ts:4][RESP_SERVER_LOGIN_OK=0][0][isAdmin][permissions][rand:4][ver] against tagged REQ responses.
func TestIsLoginReply(t *testing.T) {
	t.Parallel()
	login := make([]byte, 16) // 13 bytes + block padding
	binary.LittleEndian.PutUint32(login[:4], 1_700_000_000)
	login[6], login[7], login[12] = 1, 3, 2
	status := make([]byte, 64)
	binary.LittleEndian.PutUint32(status[:4], 1_700_000_000)
	binary.LittleEndian.PutUint16(status[4:6], 3700) // batt mV
	telem := append([]byte{0, 0, 0, 0, 1, 116, 0x01, 0x72}, make([]byte, 8)...)

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"login reply", login, true},
		{"status response", status, false},
		{"telemetry response", telem, false},
		{"short", login[:7], false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		if got := isLoginReply(tt.data); got != tt.want {
			t.Errorf("%s: isLoginReply = %v, want %v", tt.name, got, tt.want)
		}
	}
}
