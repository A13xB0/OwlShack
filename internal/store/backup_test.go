package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// openWritable bypasses migrations so a test can plant a schema version or a foreign table.
func openWritable(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func TestBackupTo_ProducesAdoptableCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")

	st, err := Open(ctx, src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	dst := filepath.Join(dir, "copy.db")
	if err := st.BackupTo(ctx, dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}

	v, err := InspectBackup(ctx, dst)
	if err != nil {
		t.Fatalf("InspectBackup on our own backup: %v", err)
	}
	if want := LatestSchemaVersion(); v != want {
		t.Errorf("backup schema = %d, want %d", v, want)
	}

	// VACUUM INTO refuses an existing target, so BackupTo must too.
	if err := st.BackupTo(ctx, dst); err == nil {
		t.Error("BackupTo overwrote an existing file")
	}
}

func TestInspectBackup_Rejects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	notDB := filepath.Join(dir, "cat.jpg")
	if err := os.WriteFile(notDB, []byte("not a database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBackup(ctx, notDB); err == nil {
		t.Error("accepted a non-database file")
	}

	// A real SQLite file that isn't ours: no settings table.
	foreign := filepath.Join(dir, "foreign.db")
	fdb, err := openWritable(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fdb.ExecContext(ctx, "CREATE TABLE cats (name TEXT)"); err != nil {
		t.Fatal(err)
	}
	fdb.Close()
	if _, err := InspectBackup(ctx, foreign); err == nil {
		t.Error("accepted a SQLite file with no settings table")
	}

	// A database from a future build: migrations never run backwards.
	future := filepath.Join(dir, "future.db")
	st, err := Open(ctx, future)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	fut, err := openWritable(future)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fut.ExecContext(ctx, "PRAGMA user_version = 9999"); err != nil {
		t.Fatal(err)
	}
	fut.Close()
	if _, err := InspectBackup(ctx, future); err == nil {
		t.Error("accepted a database from a newer schema version")
	}

	if _, err := InspectBackup(ctx, filepath.Join(dir, "missing.db")); err == nil {
		t.Error("accepted a path that does not exist")
	}
}

func TestStageRestore_AndAdopt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := filepath.Join(dir, "meshcore.db")

	// A live DB with a marker row, and a backup of a *different* DB to restore.
	st, err := Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "CREATE TABLE marker (which TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "INSERT INTO marker VALUES ('live')"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	other := filepath.Join(dir, "other.db")
	ost, err := Open(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ost.db.ExecContext(ctx, "CREATE TABLE marker (which TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := ost.db.ExecContext(ctx, "INSERT INTO marker VALUES ('restored')"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	if err := ost.BackupTo(ctx, backup); err != nil {
		t.Fatal(err)
	}
	ost.Close()

	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}

	// A rejected restore must leave nothing staged.
	if _, err := StageRestore(ctx, live, []byte("rubbish")); err == nil {
		t.Fatal("staged a file that is not a database")
	}
	if _, err := os.Stat(RestorePath(live)); err == nil {
		t.Fatal("a rejected restore left a staged file behind")
	}
	if adopted, err := AdoptPendingRestore(live); err != nil || adopted {
		t.Fatalf("AdoptPendingRestore with nothing staged: adopted=%v err=%v", adopted, err)
	}

	if _, err := StageRestore(ctx, live, data); err != nil {
		t.Fatalf("StageRestore: %v", err)
	}
	if _, err := os.Stat(RestorePath(live)); err != nil {
		t.Fatalf("nothing staged: %v", err)
	}

	// Leftover WAL/shm from the outgoing DB would corrupt the adopted one.
	for _, sfx := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(live+sfx, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	adopted, err := AdoptPendingRestore(live)
	if err != nil || !adopted {
		t.Fatalf("AdoptPendingRestore: adopted=%v err=%v", adopted, err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(live + sfx); err == nil {
			t.Errorf("stale %s left beside the adopted database", sfx)
		}
	}
	if _, err := os.Stat(live + ".replaced"); err != nil {
		t.Error("the outgoing database was not kept as .replaced")
	}
	if _, err := os.Stat(RestorePath(live)); err == nil {
		t.Error("staged file still present after adoption")
	}

	// The adopted DB must be the backup, not the original.
	reopened, err := Open(ctx, live)
	if err != nil {
		t.Fatalf("reopening adopted database: %v", err)
	}
	defer reopened.Close()
	var which string
	if err := reopened.db.QueryRowContext(ctx, "SELECT which FROM marker").Scan(&which); err != nil {
		t.Fatalf("reading marker: %v", err)
	}
	if which != "restored" {
		t.Errorf("adopted database says %q, want %q", which, "restored")
	}
}

// seedForPrune returns a backup copy of a DB with two companions and mixed fresh/stale history.
func seedForPrune(t *testing.T, opts PruneOptions) *sql.DB {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	st, err := Open(ctx, filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO companions (id, name, private_key, pubkey) VALUES
		(1,'keep','aa','k1'), (2,'drop','bb','k2')`)
	exec(`INSERT INTO companion_contacts (companion_id, peer_pubkey, name) VALUES
		(1, X'01', 'c-keep'), (2, X'02', 'c-drop')`)
	exec(`INSERT INTO messages (companion_id, channel, channel_hash, direction, timestamp) VALUES
		(1,'Public',0,'rx', datetime('now','-1 days')),
		(1,'Public',0,'rx', datetime('now','-40 days')),
		(2,'Public',0,'rx', datetime('now','-1 days'))`)
	exec(`INSERT INTO packets (direction, raw, received_at) VALUES
		('rx','00', datetime('now','-1 days')),
		('rx','01', datetime('now','-40 days'))`)
	exec(`INSERT INTO discovered_peers (pubkey, name) VALUES ('p1','peer')`)
	exec(`INSERT INTO node_metrics (ts, pubkey, metric, value) VALUES
		(unixepoch('now','-1 days'),'p1','battery',1),
		(unixepoch('now','-40 days'),'p1','battery',2)`)
	exec(`INSERT INTO repeater (id, name, private_key) VALUES (1,'rptr','cc')`)
	exec(`INSERT INTO sensors (id, provider, kind, name) VALUES (1,'i2c','shtc3','air')`)
	exec(`INSERT INTO telemetry_map (node_kind, node_id, channel, lpp_type, sensor_id, metric) VALUES
		('companion',1,2,103,1,'temperature'),
		('companion',2,2,103,1,'temperature'),
		('repeater',1,3,103,1,'temperature')`)
	// The settings singleton must survive every prune.
	exec(`INSERT INTO settings (id, freq) VALUES (1, 917.375)`)

	backup := filepath.Join(dir, "backup.db")
	if err := st.BackupTo(ctx, backup); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := PruneBackup(ctx, backup, opts); err != nil {
		t.Fatalf("PruneBackup: %v", err)
	}
	db, err := openWritable(backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestPruneBackup_SelectsCompanionsAndWindows(t *testing.T) {
	db := seedForPrune(t, PruneOptions{
		CompanionIDs: []int64{1}, // "keep"
		Contacts:     true,
		Repeater:     true,
		Peers:        false,
		MessageDays:  7,
		PacketDays:   7,
		MetricDays:   DaysAll,
	})

	if n := count(t, db, "SELECT count(*) FROM companions"); n != 1 {
		t.Errorf("companions = %d, want 1", n)
	}
	if n := count(t, db, "SELECT count(*) FROM companions WHERE name='keep'"); n != 1 {
		t.Error("kept the wrong companion")
	}
	// The dropped companion's contacts and messages must go with it (cascade).
	if n := count(t, db, "SELECT count(*) FROM companion_contacts"); n != 1 {
		t.Errorf("contacts = %d, want 1 (the dropped companion's should cascade)", n)
	}
	if n := count(t, db, "SELECT count(*) FROM messages"); n != 1 {
		t.Errorf("messages = %d, want 1 (fresh, kept companion only)", n)
	}
	if n := count(t, db, "SELECT count(*) FROM packets"); n != 1 {
		t.Errorf("packets = %d, want 1 (40-day-old one trimmed)", n)
	}
	if n := count(t, db, "SELECT count(*) FROM discovered_peers"); n != 0 {
		t.Errorf("peers = %d, want 0 (not selected)", n)
	}
	if n := count(t, db, "SELECT count(*) FROM node_metrics"); n != 2 {
		t.Errorf("metrics = %d, want 2 (DaysAll)", n)
	}
	// IdentityKeys defaulted to false, so keys must be stripped.
	if n := count(t, db, "SELECT count(*) FROM companions WHERE private_key <> ''"); n != 0 {
		t.Error("identity keys were not stripped")
	}
	if n := count(t, db, "SELECT count(*) FROM repeater WHERE private_key <> ''"); n != 0 {
		t.Error("repeater identity key was not stripped")
	}
}

func TestPruneBackup_KeepsEverythingWhenAsked(t *testing.T) {
	db := seedForPrune(t, PruneOptions{
		CompanionIDs: []int64{1, 2},
		Contacts:     true, Triggers: true, Mqtt: true, Repeater: true, Peers: true,
		MessageDays: DaysAll, PacketDays: DaysAll, MetricDays: DaysAll,
		IdentityKeys: true,
	})
	for _, c := range []struct {
		q    string
		want int
	}{
		{"SELECT count(*) FROM companions", 2},
		{"SELECT count(*) FROM companion_contacts", 2},
		{"SELECT count(*) FROM messages", 3},
		{"SELECT count(*) FROM packets", 2},
		{"SELECT count(*) FROM discovered_peers", 1},
		{"SELECT count(*) FROM node_metrics", 2},
		{"SELECT count(*) FROM companions WHERE private_key <> ''", 2},
	} {
		if n := count(t, db, c.q); n != c.want {
			t.Errorf("%s = %d, want %d", c.q, n, c.want)
		}
	}
}

// The selection is exactly what is kept: an empty slice must produce the smallest backup, never the largest.
func TestPruneBackup_CompanionSelectionSemantics(t *testing.T) {
	t.Run("every id keeps all", func(t *testing.T) {
		db := seedForPrune(t, PruneOptions{CompanionIDs: []int64{1, 2}, Contacts: true})
		if n := count(t, db, "SELECT count(*) FROM companions"); n != 2 {
			t.Errorf("companions = %d, want 2", n)
		}
	})
	t.Run("nil keeps none", func(t *testing.T) {
		db := seedForPrune(t, PruneOptions{CompanionIDs: nil, Contacts: true})
		if n := count(t, db, "SELECT count(*) FROM companions"); n != 0 {
			t.Errorf("companions = %d, want 0 (unset must not mean all)", n)
		}
	})
	t.Run("empty keeps none", func(t *testing.T) {
		db := seedForPrune(t, PruneOptions{CompanionIDs: []int64{}, Contacts: true})
		if n := count(t, db, "SELECT count(*) FROM companions"); n != 0 {
			t.Errorf("companions = %d, want 0", n)
		}
		// Their contacts must cascade away with them.
		if n := count(t, db, "SELECT count(*) FROM companion_contacts"); n != 0 {
			t.Errorf("contacts = %d, want 0", n)
		}
		// Settings survive: that is the point of a settings-only backup.
		if n := count(t, db, "SELECT count(*) FROM settings"); n != 1 {
			t.Errorf("settings rows = %d, want 1", n)
		}
	})
	t.Run("single id keeps one", func(t *testing.T) {
		db := seedForPrune(t, PruneOptions{CompanionIDs: []int64{2}, Contacts: true})
		var name string
		if err := db.QueryRow("SELECT name FROM companions").Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "drop" {
			t.Errorf("kept %q, want %q", name, "drop")
		}
	})
}

func TestCountForBackup_MatchesSelection(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO companions (id, name, private_key) VALUES (1,'a','aa'),(2,'b','bb')`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		sel  []int64
		want int64
	}{
		{"every id is all", []int64{1, 2}, 2},
		{"nil is none", nil, 0},
		{"empty is none", []int64{}, 0},
		{"one id", []int64{1}, 1},
	} {
		got, err := st.CountForBackup(ctx, PruneOptions{CompanionIDs: tc.sel})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Companions != tc.want {
			t.Errorf("%s: companions = %d, want %d", tc.name, got.Companions, tc.want)
		}
	}
}

func TestPruneBackup_DropsAllHistoryByDefault(t *testing.T) {
	db := seedForPrune(t, PruneOptions{
		CompanionIDs: []int64{1, 2}, Contacts: true, Repeater: true,
	})
	for _, c := range []struct {
		q    string
		want int
	}{
		{"SELECT count(*) FROM messages", 0},
		{"SELECT count(*) FROM packets", 0},
		{"SELECT count(*) FROM node_metrics", 0},
		{"SELECT count(*) FROM discovered_peers", 0},
		{"SELECT count(*) FROM companions", 2},
		{"SELECT count(*) FROM companion_contacts", 2},
	} {
		if n := count(t, db, c.q); n != c.want {
			t.Errorf("%s = %d, want %d", c.q, n, c.want)
		}
	}
	// The pruned copy must still be adoptable.
	if _, err := InspectBackup(context.Background(), ""); err == nil {
		t.Error("InspectBackup accepted an empty path")
	}
}

// A map row keys on node kind and id, so nothing cascades and an orphan restores onto whatever takes that id.
func TestPruneBackup_DropsMapRowsForPrunedNodes(t *testing.T) {
	db := seedForPrune(t, PruneOptions{
		CompanionIDs: []int64{1}, // drops companion 2
		Repeater:     false,      // drops the repeater
		MessageDays:  DaysAll,
		PacketDays:   DaysAll,
		MetricDays:   DaysAll,
	})
	if n := count(t, db, "SELECT COUNT(*) FROM telemetry_map"); n != 1 {
		t.Errorf("telemetry_map rows = %d, want 1 (only the kept companion)", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM telemetry_map WHERE node_kind='companion' AND node_id=1"); n != 1 {
		t.Errorf("the kept companion lost its map: %d rows", n)
	}
}

func TestPruneBackup_KeepsRepeaterMap(t *testing.T) {
	db := seedForPrune(t, PruneOptions{
		CompanionIDs: []int64{1},
		Repeater:     true,
		MessageDays:  DaysAll,
		PacketDays:   DaysAll,
		MetricDays:   DaysAll,
	})
	if n := count(t, db, "SELECT COUNT(*) FROM telemetry_map WHERE node_kind='repeater'"); n != 1 {
		t.Errorf("repeater map rows = %d, want 1", n)
	}
}
