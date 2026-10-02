package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// newTestStore opens a fresh temp-file store per test, closed at test end.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// mkCompanion returns the id of a new companion row, for use as a valid companion_id FK.
func mkCompanion(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	c := &Companion{Name: name}
	if err := st.Companions.Create(t.Context(), c); err != nil {
		t.Fatalf("Companions.Create(%q): %v", name, err)
	}
	if c.ID <= 0 {
		t.Fatalf("Companions.Create(%q): got id %d, want > 0", name, c.ID)
	}
	return c.ID
}

func f64(v float64) *float64 { return &v }
func i8(v int8) *int8        { return &v }
func iptr(v int) *int        { return &v }
func i64ptr(v int64) *int64  { return &v }
func sptr(v string) *string  { return &v }

func TestMessageRepo_InsertRoundTrip(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")

	ts := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	want := &Message{
		CompanionID:  cid,
		Channel:      "public",
		ChannelHash:  0x2a,
		Sender:       "node-x",
		Text:         "hello mesh",
		Direction:    "rx",
		Timestamp:    ts,
		SNR:          f64(7.25),
		RSSI:         i8(-95),
		PathHashes:   []byte{0x01, 0x02, 0x03},
		PathHashSize: iptr(1),
		Hops:         iptr(3),
		Status:       sptr("delivered"),
	}

	if err := st.Messages.Insert(t.Context(), want); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if want.ID <= 0 {
		t.Fatalf("Insert assigned id %d, want > 0", want.ID)
	}

	got, err := st.Messages.GetByID(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.ID != want.ID {
		t.Errorf("ID = %d, want %d", got.ID, want.ID)
	}
	if got.CompanionID != cid {
		t.Errorf("CompanionID = %d, want %d", got.CompanionID, cid)
	}
	if got.Channel != want.Channel {
		t.Errorf("Channel = %q, want %q", got.Channel, want.Channel)
	}
	if got.ChannelHash != want.ChannelHash {
		t.Errorf("ChannelHash = %d, want %d", got.ChannelHash, want.ChannelHash)
	}
	if got.Sender != want.Sender {
		t.Errorf("Sender = %q, want %q", got.Sender, want.Sender)
	}
	if got.Text != want.Text {
		t.Errorf("Text = %q, want %q", got.Text, want.Text)
	}
	if got.Direction != want.Direction {
		t.Errorf("Direction = %q, want %q", got.Direction, want.Direction)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, ts)
	}
	if got.SNR == nil || *got.SNR != 7.25 {
		t.Errorf("SNR = %v, want 7.25", got.SNR)
	}
	if got.RSSI == nil || *got.RSSI != -95 {
		t.Errorf("RSSI = %v, want -95", got.RSSI)
	}
	if string(got.PathHashes) != string(want.PathHashes) {
		t.Errorf("PathHashes = %v, want %v", got.PathHashes, want.PathHashes)
	}
	if got.PathHashSize == nil || *got.PathHashSize != 1 {
		t.Errorf("PathHashSize = %v, want 1", got.PathHashSize)
	}
	if got.Hops == nil || *got.Hops != 3 {
		t.Errorf("Hops = %v, want 3", got.Hops)
	}
	if got.Status == nil || *got.Status != "delivered" {
		t.Errorf("Status = %v, want delivered", got.Status)
	}
}

func TestMessageRepo_NullableFields(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")

	m := &Message{CompanionID: cid, Channel: "public", Direction: "tx", Timestamp: time.Now()}
	if err := st.Messages.Insert(t.Context(), m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := st.Messages.GetByID(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SNR != nil {
		t.Errorf("SNR = %v, want nil", got.SNR)
	}
	if got.RSSI != nil {
		t.Errorf("RSSI = %v, want nil", got.RSSI)
	}
	if got.PathHashes != nil {
		t.Errorf("PathHashes = %v, want nil", got.PathHashes)
	}
	if got.Status != nil {
		t.Errorf("Status = %v, want nil", got.Status)
	}
}

func TestMessageRepo_GetByIDMissing(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	// GetByID wraps the scan error; an absent id yields sql.ErrNoRows wrapped.
	_, err := st.Messages.GetByID(t.Context(), 999)
	if err == nil {
		t.Fatalf("GetByID(999) = nil error, want error")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID(999) err = %v, want wrapping sql.ErrNoRows", err)
	}
}

// insertN returns the ascending ids of n messages inserted on a channel.
func insertN(t *testing.T, st *Store, cid int64, channel, direction string, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		m := &Message{CompanionID: cid, Channel: channel, Direction: direction, Timestamp: time.Now()}
		if err := st.Messages.Insert(t.Context(), m); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
		ids[i] = m.ID
	}
	return ids
}

func TestMessageRepo_List(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	ids := insertN(t, st, cid, "public", "rx", 5) // ascending ids

	t.Run("newest first, limit honored", func(t *testing.T) {
		got, err := st.Messages.List(t.Context(), cid, "public", 3, 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		want := []int64{ids[4], ids[3], ids[2]} // DESC
		for i, w := range want {
			if got[i].ID != w {
				t.Errorf("got[%d].ID = %d, want %d", i, got[i].ID, w)
			}
		}
	})

	t.Run("offset", func(t *testing.T) {
		got, err := st.Messages.List(t.Context(), cid, "public", 2, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		want := []int64{ids[2], ids[1]}
		if len(got) != 2 || got[0].ID != want[0] || got[1].ID != want[1] {
			t.Errorf("List offset 2 = %v, want ids %v", idsOf(got), want)
		}
	})
}

func TestMessageRepo_ListBefore(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	ids := insertN(t, st, cid, "public", "rx", 6) // ids[0..5] ascending

	// ListBefore(ids[4]) → only ids strictly < ids[4], DESC, capped at limit 2.
	got, err := st.Messages.ListBefore(t.Context(), cid, "public", ids[4], 2)
	if err != nil {
		t.Fatalf("ListBefore: %v", err)
	}
	want := []int64{ids[3], ids[2]} // the two newest below the cursor, DESC
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (cap honored)", len(got))
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %d, want %d (DESC, id<before)", i, got[i].ID, w)
		}
	}
	for _, m := range got {
		if m.ID >= ids[4] {
			t.Errorf("ListBefore returned id %d >= beforeID %d", m.ID, ids[4])
		}
	}
}

func TestMessageRepo_ListAfter(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	ids := insertN(t, st, cid, "public", "rx", 6)

	got, err := st.Messages.ListAfter(t.Context(), cid, "public", ids[2], 0) // limit 0 → default
	if err != nil {
		t.Fatalf("ListAfter: %v", err)
	}
	want := []int64{ids[3], ids[4], ids[5]} // ASC, id>after
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %d, want %d (ASC, id>after)", i, got[i].ID, w)
		}
	}
}

func TestMessageRepo_UpdateStatus(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	m := &Message{CompanionID: cid, Channel: "public", Direction: "tx", Timestamp: time.Now()}
	if err := st.Messages.Insert(t.Context(), m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := st.Messages.UpdateStatus(t.Context(), m.ID, "failed"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := st.Messages.GetByID(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status == nil || *got.Status != "failed" {
		t.Errorf("Status = %v, want failed", got.Status)
	}
}

func TestMessageRepo_IncrementRepeatCount(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	m := &Message{CompanionID: cid, Channel: "public", Direction: "tx", Timestamp: time.Now()}
	if err := st.Messages.Insert(t.Context(), m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// confirmed starts NULL → COALESCE(.,0)+1 returns 1, then 2.
	for _, want := range []int{1, 2} {
		got, err := st.Messages.IncrementRepeatCount(t.Context(), m.ID)
		if err != nil {
			t.Fatalf("IncrementRepeatCount: %v", err)
		}
		if got != want {
			t.Errorf("IncrementRepeatCount = %d, want %d", got, want)
		}
	}
	// The RETURNING value persists; RepeatCount maps to the confirmed column.
	row, err := st.Messages.GetByID(t.Context(), m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.RepeatCount == nil || *row.RepeatCount != 2 {
		t.Errorf("RepeatCount = %v, want 2", row.RepeatCount)
	}
}

func TestMessageRepo_Delete(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")

	t.Run("Delete one row", func(t *testing.T) {
		ids := insertN(t, st, cid, "del-one", "rx", 2)
		if err := st.Messages.Delete(t.Context(), ids[0]); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := st.Messages.GetByID(t.Context(), ids[0]); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetByID after Delete err = %v, want sql.ErrNoRows", err)
		}
		if _, err := st.Messages.GetByID(t.Context(), ids[1]); err != nil {
			t.Errorf("sibling row got removed: %v", err)
		}
	})

	t.Run("DeleteByChannel", func(t *testing.T) {
		insertN(t, st, cid, "del-ch", "rx", 3)
		insertN(t, st, cid, "keep-ch", "rx", 1)
		if err := st.Messages.DeleteByChannel(t.Context(), cid, "del-ch"); err != nil {
			t.Fatalf("DeleteByChannel: %v", err)
		}
		got, err := st.Messages.List(t.Context(), cid, "del-ch", 10, 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("del-ch has %d rows, want 0", len(got))
		}
		keep, err := st.Messages.List(t.Context(), cid, "keep-ch", 10, 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(keep) != 1 {
			t.Errorf("keep-ch has %d rows, want 1", len(keep))
		}
	})
}

// A channel with no rx rows must return (nil, nil), not an error.
func TestMessageRepo_LatestRxSentinel(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")

	t.Run("no rows returns nil,nil", func(t *testing.T) {
		got, err := st.Messages.LatestRx(t.Context(), cid, "empty")
		if err != nil {
			t.Fatalf("LatestRx err = %v, want nil", err)
		}
		if got != nil {
			t.Errorf("LatestRx = %v, want nil", got)
		}
	})

	t.Run("only tx rows still returns nil,nil", func(t *testing.T) {
		insertN(t, st, cid, "txonly", "tx", 2)
		got, err := st.Messages.LatestRx(t.Context(), cid, "txonly")
		if err != nil {
			t.Fatalf("LatestRx err = %v, want nil", err)
		}
		if got != nil {
			t.Errorf("LatestRx = %v, want nil (tx-only channel)", got)
		}
	})

	t.Run("returns newest rx", func(t *testing.T) {
		ids := insertN(t, st, cid, "rxch", "rx", 3)
		got, err := st.Messages.LatestRx(t.Context(), cid, "rxch")
		if err != nil {
			t.Fatalf("LatestRx: %v", err)
		}
		if got == nil || got.ID != ids[2] {
			t.Errorf("LatestRx = %v, want id %d", got, ids[2])
		}
	})
}

func TestPeerRepo_UpsertRoundTrip(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	ls := time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC)
	want := &Peer{
		PubKey:          []byte{0xaa, 0xbb, 0xcc, 0xdd},
		Name:            "Repeater-1",
		Type:            "REPEATER",
		Lat:             -33123456,
		Lon:             151456789,
		Feat1:           7,
		Feat2:           42,
		OutPath:         []byte{0x11, 0x22},
		OutPathHashSize: 1,
		LastAdvertTS:    1700000000,
		LastSeen:        ls,
		SNR:             f64(-3.5),
		RSSI:            i8(-110),
	}
	if err := st.Peers.Upsert(t.Context(), want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := st.Peers.GetByPubKey(t.Context(), want.PubKey)
	if err != nil {
		t.Fatalf("GetByPubKey: %v", err)
	}
	if got == nil {
		t.Fatalf("GetByPubKey = nil, want peer")
	}
	if string(got.PubKey) != string(want.PubKey) {
		t.Errorf("PubKey = %x, want %x", got.PubKey, want.PubKey)
	}
	if got.Name != want.Name || got.Type != want.Type {
		t.Errorf("Name/Type = %q/%q, want %q/%q", got.Name, got.Type, want.Name, want.Type)
	}
	if got.Lat != want.Lat || got.Lon != want.Lon {
		t.Errorf("Lat/Lon = %d/%d, want %d/%d", got.Lat, got.Lon, want.Lat, want.Lon)
	}
	if got.Feat1 != want.Feat1 || got.Feat2 != want.Feat2 {
		t.Errorf("Feat1/Feat2 = %d/%d, want %d/%d", got.Feat1, got.Feat2, want.Feat1, want.Feat2)
	}
	if string(got.OutPath) != string(want.OutPath) {
		t.Errorf("OutPath = %x, want %x", got.OutPath, want.OutPath)
	}
	if got.OutPathHashSize != want.OutPathHashSize {
		t.Errorf("OutPathHashSize = %d, want %d", got.OutPathHashSize, want.OutPathHashSize)
	}
	if got.LastAdvertTS != want.LastAdvertTS {
		t.Errorf("LastAdvertTS = %d, want %d", got.LastAdvertTS, want.LastAdvertTS)
	}
	if !got.LastSeen.Equal(ls) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, ls)
	}
	if got.SNR == nil || *got.SNR != -3.5 {
		t.Errorf("SNR = %v, want -3.5", got.SNR)
	}
	if got.RSSI == nil || *got.RSSI != -110 {
		t.Errorf("RSSI = %v, want -110", got.RSSI)
	}

	t.Run("Upsert updates in place", func(t *testing.T) {
		want.Name = "Repeater-renamed"
		want.Type = "ROOM"
		if err := st.Peers.Upsert(t.Context(), want); err != nil {
			t.Fatalf("Upsert (update): %v", err)
		}
		all, err := st.Peers.LoadAll(t.Context())
		if err != nil {
			t.Fatalf("LoadAll: %v", err)
		}
		if len(all) != 1 {
			t.Fatalf("LoadAll len = %d, want 1 (upsert, not insert)", len(all))
		}
		if all[0].Name != "Repeater-renamed" || all[0].Type != "ROOM" {
			t.Errorf("after update Name/Type = %q/%q, want Repeater-renamed/ROOM", all[0].Name, all[0].Type)
		}
	})
}

func TestPeerRepo_GetByPubKeyMissing(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	got, err := st.Peers.GetByPubKey(t.Context(), []byte{0xde, 0xad})
	if err != nil {
		t.Fatalf("GetByPubKey err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("GetByPubKey unknown = %v, want nil", got)
	}
}

// nil (unknown), empty (direct neighbour) and non-empty (multi-hop) must all survive the DB round-trip distinctly.
func TestPeerRepo_OutPathSemantics(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	tests := []struct {
		name    string
		path    []byte
		size    uint8
		wantNil bool // unknown path reads back as nil; known paths (incl. empty) do not
		wantLen int
	}{
		{name: "unknown (nil)", path: nil, size: 0, wantNil: true},
		{name: "direct neighbour (empty)", path: []byte{}, size: 0, wantNil: false, wantLen: 0},
		{name: "multi-hop", path: []byte{0x01, 0x02, 0x03}, size: 1, wantNil: false, wantLen: 3},
	}

	for i, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pk := []byte{0x10, byte(i)}
			// Upsert then UpdateOutPath exercises both write paths.
			if err := st.Peers.Upsert(t.Context(), &Peer{PubKey: pk, OutPath: []byte{0xff}, OutPathHashSize: 1, LastSeen: time.Now()}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if err := st.Peers.UpdateOutPath(t.Context(), pk, tt.path, tt.size); err != nil {
				t.Fatalf("UpdateOutPath: %v", err)
			}
			got, err := st.Peers.GetByPubKey(t.Context(), pk)
			if err != nil {
				t.Fatalf("GetByPubKey: %v", err)
			}
			if tt.wantNil {
				if got.OutPath != nil {
					t.Errorf("OutPath = %v, want nil after round-trip", got.OutPath)
				}
				return
			}
			if got.OutPath == nil {
				t.Fatalf("OutPath = nil, want non-nil len %d", tt.wantLen)
			}
			if len(got.OutPath) != tt.wantLen {
				t.Errorf("len(OutPath) = %d, want %d", len(got.OutPath), tt.wantLen)
			}
			if got.OutPathHashSize != tt.size {
				t.Errorf("OutPathHashSize = %d, want %d", got.OutPathHashSize, tt.size)
			}
		})
	}
}

func TestPeerRepo_LookupByHash(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	for _, p := range []*Peer{
		{PubKey: []byte{0xab, 0xcd, 0xef, 0x01}, Name: "Alice", LastSeen: time.Now()},
		{PubKey: []byte{0xab, 0xcd, 0x99, 0x99}, Name: "Bob", LastSeen: time.Now()},
		{PubKey: []byte{0x00, 0x11, 0x22, 0x33}, Name: "Carol", LastSeen: time.Now()},
	} {
		if err := st.Peers.Upsert(t.Context(), p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	t.Run("prefix matches two", func(t *testing.T) {
		names, err := st.Peers.LookupByHash(t.Context(), []byte{0xab, 0xcd})
		if err != nil {
			t.Fatalf("LookupByHash: %v", err)
		}
		if len(names) != 2 {
			t.Fatalf("got %d names, want 2: %v", len(names), names)
		}
	})

	t.Run("full key matches one", func(t *testing.T) {
		names, err := st.Peers.LookupByHash(t.Context(), []byte{0x00, 0x11, 0x22, 0x33})
		if err != nil {
			t.Fatalf("LookupByHash: %v", err)
		}
		if len(names) != 1 || names[0] != "Carol" {
			t.Errorf("got %v, want [Carol]", names)
		}
	})

	t.Run("empty hash returns nil", func(t *testing.T) {
		names, err := st.Peers.LookupByHash(t.Context(), nil)
		if err != nil {
			t.Fatalf("LookupByHash(nil): %v", err)
		}
		if names != nil {
			t.Errorf("got %v, want nil", names)
		}
	})
}

// A path hop names the repeater the map would plot for it: never a chat node, and the most recently heard first.
func TestPeerRepo_LookupRepeatersByHash(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	now := time.Now()
	for _, p := range []*Peer{
		{PubKey: []byte{0xe6, 0x83, 0x01}, Name: "Torbay Station", Type: "REPEATER", LastSeen: now.Add(-time.Hour)},
		{PubKey: []byte{0xe6, 0x11, 0x02}, Name: "Steve_local", Type: "CHAT", LastSeen: now},
		{PubKey: []byte{0xe6, 0xd1, 0x03}, Name: "Torbay Heights", Type: "REPEATER", LastSeen: now.Add(-time.Minute)},
	} {
		if err := st.Peers.Upsert(t.Context(), p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	names, err := st.Peers.LookupRepeatersByHash(t.Context(), []byte{0xe6})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "Torbay Heights" || names[1] != "Torbay Station" {
		t.Errorf("got %v, want [Torbay Heights Torbay Station]", names)
	}
}

// A batch larger than the 500-row chunk size crosses eachInChunk's boundary.
func TestPeerRepo_DeleteMany(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	const n = 1200 // > 2 chunks of 500
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		pk := []byte{byte(i >> 8), byte(i), 0x55}
		keys[i] = pk
		if err := st.Peers.Upsert(t.Context(), &Peer{PubKey: pk, LastSeen: time.Now()}); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}

	// Delete all but the last one, across the chunk boundary.
	if err := st.Peers.DeleteMany(t.Context(), keys[:n-1]); err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}

	all, err := st.Peers.LoadAll(t.Context())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("after DeleteMany, LoadAll len = %d, want 1", len(all))
	}
	if string(all[0].PubKey) != string(keys[n-1]) {
		t.Errorf("surviving peer = %x, want %x", all[0].PubKey, keys[n-1])
	}

	t.Run("empty batch is a no-op", func(t *testing.T) {
		if err := st.Peers.DeleteMany(t.Context(), nil); err != nil {
			t.Errorf("DeleteMany(nil): %v", err)
		}
	})
}

// An absent name must return an error whose chain still includes sql.ErrNoRows.
func TestCompanionRepo_IDByNameSentinel(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "present")

	t.Run("present name resolves to id", func(t *testing.T) {
		got, err := st.Companions.IDByName(t.Context(), "present")
		if err != nil {
			t.Fatalf("IDByName(present): %v", err)
		}
		if got != cid {
			t.Errorf("IDByName(present) = %d, want %d", got, cid)
		}
	})

	t.Run("absent name wraps sql.ErrNoRows", func(t *testing.T) {
		_, err := st.Companions.IDByName(t.Context(), "ghost")
		if err == nil {
			t.Fatalf("IDByName(ghost) = nil error, want error")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("IDByName(ghost) err = %v, want errors.Is(err, sql.ErrNoRows) == true", err)
		}
	})
}

// The unread count must survive the no-read-marker branch without erroring.
func TestConversationRepo_List(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")

	// Three rx messages on the public channel; one tx (not counted as unread).
	rxIDs := insertN(t, st, cid, "public", "rx", 3)
	insertN(t, st, cid, "public", "tx", 1)

	channels := []string{"public"}
	contacts := []ContactInfo{{PubKeyHex: "abcd", Name: "Bob"}}

	t.Run("no read marker: counts all rx", func(t *testing.T) {
		convos, err := st.Conversations.List(t.Context(), cid, channels, contacts)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(convos) != 2 {
			t.Fatalf("len = %d, want 2 (1 channel + 1 contact)", len(convos))
		}
		pub := findConv(t, convos, "channel:public")
		if pub.UnreadCount != 3 {
			t.Errorf("unread (no marker) = %d, want 3", pub.UnreadCount)
		}
		if pub.LastMessage == nil {
			t.Errorf("LastMessage = nil, want last message present")
		}
		// Contact conversation has no messages at all → no-rows on both queries.
		ct := findConv(t, convos, "contact:abcd")
		if ct.UnreadCount != 0 {
			t.Errorf("empty contact unread = %d, want 0", ct.UnreadCount)
		}
		if ct.LastMessage != nil {
			t.Errorf("empty contact LastMessage = %v, want nil", ct.LastMessage)
		}
	})

	t.Run("with read marker: counts only newer rx", func(t *testing.T) {
		// Mark read up to the first rx message → 2 remain unread.
		if err := st.Conversations.MarkRead(t.Context(), cid, "channel:public", rxIDs[0]); err != nil {
			t.Fatalf("MarkRead: %v", err)
		}
		convos, err := st.Conversations.List(t.Context(), cid, channels, contacts)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		pub := findConv(t, convos, "channel:public")
		if pub.UnreadCount != 2 {
			t.Errorf("unread (marker at rxIDs[0]) = %d, want 2", pub.UnreadCount)
		}
	})
}

func findConv(t *testing.T, convos []Conversation, id string) Conversation {
	t.Helper()
	for _, c := range convos {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("conversation %q not found in %d convos", id, len(convos))
	return Conversation{}
}

func idsOf(ms []Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// packet_hash/path are derived from raw bytes on insert, so these packets are hand-built with known hop paths.
func TestPacketRepo_ListFilter(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	// pathLenByte 0x02 = hashSize 1, hashCount 2 → a 2-byte hop path follows.
	mk := func(payloadType uint8, path ...byte) *PacketRecord {
		raw := []byte{meshcore.MakeHeader(meshcore.RouteTypeFlood, payloadType, 0), 0x02}
		raw = append(raw, path...)
		raw = append(raw, 0xDE, 0xAD) // payload
		pt := payloadType
		return &PacketRecord{ReceivedAt: time.Now(), Direction: "rx", Raw: raw, PayloadType: &pt}
	}
	if err := st.Packets.Insert(ctx, mk(4, 0xAA, 0xBB)); err != nil { // advert, path aabb
		t.Fatal(err)
	}
	if err := st.Packets.Insert(ctx, mk(3, 0xCC, 0xDD)); err != nil { // ack, path ccdd
		t.Fatal(err)
	}

	count := func(f PacketFilter) int {
		got, err := st.Packets.List(ctx, 100, 0, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	if n := count(PacketFilter{}); n != 2 {
		t.Errorf("no filter: got %d, want 2", n)
	}
	advert := uint8(4)
	if n := count(PacketFilter{PayloadType: &advert}); n != 1 {
		t.Errorf("payloadType=4: got %d, want 1", n)
	}
	if n := count(PacketFilter{Search: "AABB"}); n != 1 { // case-insensitive
		t.Errorf("search aabb: got %d, want 1", n)
	}
	if n := count(PacketFilter{Search: "ccdd"}); n != 1 {
		t.Errorf("search ccdd: got %d, want 1", n)
	}
	if n := count(PacketFilter{Search: "ffff"}); n != 0 {
		t.Errorf("search ffff: got %d, want 0", n)
	}
}

// Bump wantVersion whenever a migration file is added.
func TestStore_MigrateUserVersion(t *testing.T) {
	t.Parallel()
	const wantVersion = 22 // one per file in migrations/
	st := newTestStore(t)
	var v int
	if err := st.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if v != wantVersion {
		t.Errorf("user_version = %d, want %d", v, wantVersion)
	}
}

// The foreign_keys pragma must be on: a message with an unknown companion_id must fail.
func TestStore_ForeignKeysEnforced(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	m := &Message{CompanionID: 9999, Channel: "x", Direction: "rx", Timestamp: time.Now()}
	err := st.Messages.Insert(t.Context(), m)
	if err == nil {
		t.Fatalf("Insert with dangling companion_id = nil error, want FK violation")
	}
}

// A late write must not read as a success: WriteAsync says it dropped the closure, WriteSync runs it so the error carries the closed database.
func TestStore_WriteAfterClose(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if st.WriteAsync(func() {}) {
		t.Error("WriteAsync after Close returned true")
	}
	var err error
	st.WriteSync(func() { err = st.Messages.Delete(t.Context(), 1) })
	if err == nil {
		t.Error("WriteSync after Close left the error nil, which the caller reads as a write that happened")
	}
}

// New slots must apply to a database that already has tables and rows, not just a fresh one.
func TestStore_UpgradeFromReleasedSchema(t *testing.T) {
	t.Parallel()
	const releasedVersion = 7 // len(migrations) at v1.1.0

	path := filepath.Join(t.TempDir(), "released.db")

	// Running only the shipped files reproduces a node in the field.
	db := dbAt(t, path, releasedVersion)
	// Operator data that must still be there afterwards.
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO companions (name) VALUES ('upgrade-me')"); err != nil {
		t.Fatalf("seeding operator data: %v", err)
	}
	db.Close()

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("upgrading a released database: %v", err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version after upgrade = %d, want %d", version, len(migrations))
	}

	// The columns the new slots add must exist on the upgraded table.
	cols := map[string]bool{}
	rows, err := st.db.QueryContext(t.Context(), "PRAGMA table_info(settings)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scanning table_info: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, c := range []string{"duty_cycle_pct", "map_provider", "map_dark_style", "map_tile_key", "path_hash_size"} {
		if !cols[c] {
			t.Errorf("settings.%s missing after the upgrade", c)
		}
	}

	// The sensor slot both creates tables and alters one, so an upgraded database is where a half-applied slot shows.
	for _, q := range []string{
		"SELECT bindings FROM sensors",
		"SELECT node_kind FROM telemetry_map",
		"SELECT state FROM sensor_state",
		"SELECT telem_base, telem_loc, telem_env FROM companions",
	} {
		if _, err := st.db.ExecContext(t.Context(), q); err != nil {
			t.Errorf("after the upgrade, %q: %v", q, err)
		}
	}

	if id, err := st.Companions.IDByName(t.Context(), "upgrade-me"); err != nil || id <= 0 {
		t.Errorf("companion after upgrade: id %d, err %v; want a live row", id, err)
	}
}

// A 4-byte trigger could only come from the bot form, which alone offered it. Config.Validate now
// rejects that, and a save validates the whole assembled config — so leaving one in place would
// wall off every later config change, not just an edit to that bot.
func TestMigration014_ClampsTriggerPathHashSize(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()

	var companionID int64
	st.WriteSync(func() {
		c := Companion{Name: "c"}
		if err := st.Companions.Create(ctx, &c); err != nil {
			t.Fatalf("creating companion: %v", err)
		}
		companionID = c.ID
	})

	for _, size := range []int{4, 3, 1} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO triggers (companion_id, type, template, path_hash_size) VALUES (?, 'dm', 'x', ?)`,
			companionID, size); err != nil {
			t.Fatalf("seeding trigger with %d: %v", size, err)
		}
	}
	// And one that mirrors, which must survive: 0 is not a byte count.
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO triggers (companion_id, type, template, path_hash_size) VALUES (?, 'dm', 'x', 0)`,
		companionID); err != nil {
		t.Fatalf("seeding mirror trigger: %v", err)
	}

	if err := migrations[13].apply(ctx, st.db); err != nil {
		t.Fatalf("014_trigger_path_hash_clamp.sql: %v", err)
	}

	rows, err := st.db.QueryContext(ctx, `SELECT path_hash_size FROM triggers ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	want := []int{3, 3, 1, 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("path_hash_size = %v, want %v (4 clamped, the rest untouched)", got, want)
	}
}

// A database from a newer build must be refused: accepting it skips every slot appended later while looking healthy.
func TestStore_RefusesADatabaseFromANewerBuild(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "newer.db")

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()

	db, err := openWritableDB(path)
	if err != nil {
		t.Fatalf("openWritableDB: %v", err)
	}
	ahead := len(migrations) + 1
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version = %d", ahead)); err != nil {
		t.Fatalf("stamping ahead: %v", err)
	}
	db.Close()

	st2, err := Open(t.Context(), path)
	if err == nil {
		st2.Close()
		t.Fatal("a database stamped past the last slot opened, so a later migration would be skipped in silence")
	}
	if !strings.Contains(err.Error(), "newer OwlShack") {
		t.Errorf("error %q does not say the database came from a newer build", err)
	}
}
