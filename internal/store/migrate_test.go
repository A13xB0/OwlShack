package store

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	meshcore "github.com/meshcore-go/meshcore-go"
)

var releaseMigrations = flag.String("release-migrations", "", "record every unlisted migration file in migrations.sum as shipped in this release tag")

func readSum(t *testing.T) []sumLine {
	t.Helper()
	lines, err := parseSum(shippedSumFile)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// dbAt builds a database as a release at version left it, by running only the files it shipped.
func dbAt(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	db, err := openWritableDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:version] {
		if err := m.apply(t.Context(), db); err != nil {
			t.Fatalf("%s: %v", m.file, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	return db
}

// A shipped file is frozen, as a released database has stamped its version and will never run it again: add a file instead.
func TestMigrations_ShippedFilesAreFrozen(t *testing.T) {
	listed := readSum(t)
	if len(listed) > len(migrations) {
		t.Fatalf("migrations.sum lists %d files but there are %d: a shipped file was deleted", len(listed), len(migrations))
	}
	for i, l := range listed {
		m := migrations[i]
		if l.file != m.file {
			t.Fatalf("migrations.sum line %d is %s but file %d is %s: a shipped file was renamed or renumbered", i+1, l.file, i+1, m.file)
		}
		if l.sum != m.sum {
			t.Errorf("%s shipped in %s and has changed since. A database at version %d has already run it and never will again, "+
				"so the change reaches no one who upgraded. Put it back and add a new file for the change.", m.file, l.release, m.version)
		}
	}

	if *releaseMigrations != "" && !t.Failed() {
		f, err := os.OpenFile("migrations.sum", os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, m := range migrations[len(listed):] {
			if _, err := fmt.Fprintf(f, "%s  %s  %s\n", m.sum, m.file, *releaseMigrations); err != nil {
				t.Fatal(err)
			}
			t.Logf("recorded %s as shipped in %s", m.file, *releaseMigrations)
		}
		return
	}
	// The release workflow sets this, so a tag cannot ship a file that the sum would leave editable.
	if os.Getenv("OWLSHACK_RELEASE") != "" {
		for _, m := range migrations[len(listed):] {
			t.Errorf("%s is not in migrations.sum: run go test ./internal/store -run TestMigrations -release-migrations <tag> and commit it before tagging", m.file)
		}
	}
}

// A gap or a repeat would skip or double a version, and a stray name would never run.
func TestLoadMigrations_RefusesAGapARepeatAndAStrayName(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("SELECT 1;")}
	for name, files := range map[string]fstest.MapFS{
		"a gap":        {"migrations/001_a.sql": file, "migrations/003_c.sql": file},
		"a repeat":     {"migrations/001_a.sql": file, "migrations/001_b.sql": file},
		"a stray name": {"migrations/001_a.sql": file, "migrations/2_b.sql": file},
		"not SQL":      {"migrations/001_a.sql": file, "migrations/002_b.txt": file},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Errorf("%s was loaded", name)
		}
	}
	ms, err := loadMigrations(fstest.MapFS{"migrations/001_a.sql": file, "migrations/002_b.sql": file})
	if err != nil || len(ms) != 2 || ms[1].version != 2 {
		t.Fatalf("a good pair: %v, %+v", err, ms)
	}
}

// An unreleased file edited after a dev database ran it failed later as a missing column; it is refused at open instead, naming the file.
func TestOpen_RefusesADatabaseThatRanADifferentFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "draft.db")
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	last := migrations[len(migrations)-1]
	if _, ok := shipped[last.file]; ok {
		t.Skipf("%s has shipped, so no unreleased file is left to draft", last.file)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE schema_migrations SET sha256 = 'draft' WHERE version = ?`, last.version); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), last.file) {
		t.Fatalf("Open = %v, want a refusal naming %s", err, last.file)
	}
}

// A database migrated before checksums were kept is trusted as it stands, and every file it has run is recorded.
func TestOpen_RecordsWhatAnOlderDatabaseRan(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "older.db")
	dbAt(t, path, 16).Close()

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.db.QueryRowContext(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) {
		t.Errorf("%d migrations recorded, want all %d", n, len(migrations))
	}
}

// Going back after an upgrade is moving the copy taken before it into place, so the copy must hold the old version and its data, and a finished upgrade keeps only it.
func TestOpen_CopiesTheDatabaseBeforeUpgradingIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "meshcore.db")
	db := dbAt(t, path, 16)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO companions (name) VALUES ('keep-me')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	older := path + ".pre-v12-to-v16"
	unrelated := path + ".pre-airquality"
	for _, p := range []string{older, unrelated} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()

	cp, err := openWritableDB(fmt.Sprintf("%s.pre-v16-to-v%d", path, LatestSchemaVersion()))
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var version int
	var name string
	if err := cp.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 16 {
		t.Errorf("the copy is at version %d (%v), want 16", version, err)
	}
	if err := cp.QueryRow(`SELECT name FROM companions`).Scan(&name); err != nil || name != "keep-me" {
		t.Errorf("the copy's companion is %q (%v), want keep-me", name, err)
	}
	if _, err := os.Stat(older); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy from an earlier finished upgrade was kept (%v), want only the latest", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("a file that only looks like a copy was removed: %v", err)
	}

	st, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if copies, _ := upgradeCopies(path); len(copies) != 1 {
		t.Errorf("copies after a second open with nothing to upgrade: %v, want the one", copies)
	}
}

// A failed upgrade must keep the copy it started from, and the next try must not replace it with a copy of the half-upgraded database, which no release can open.
func TestOpen_AFailedUpgradeKeepsTheCopyItStartedFrom(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "meshcore.db")
	db := dbAt(t, path, 16)
	// 017 adds this column, so it fails part way, as a bad migration would.
	if _, err := db.ExecContext(t.Context(), `ALTER TABLE companions ADD COLUMN telem_env TEXT`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	finished := path + ".pre-v12-to-v16"
	if err := os.WriteFile(finished, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := fmt.Sprintf("%s.pre-v16-to-v%d", path, LatestSchemaVersion())

	for try := range 2 {
		if _, err := Open(t.Context(), path); err == nil {
			t.Fatalf("try %d: Open succeeded on a database 017 cannot apply to", try+1)
		}
		copies, err := upgradeCopies(path)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, c := range copies {
			got[c.path] = true
		}
		if !got[started] || !got[finished] || len(copies) != 2 {
			t.Errorf("try %d: copies %v, want %s and %s untouched", try+1, copies, started, finished)
		}
	}
}

// Restarting part way through an upgrade finishes it from the copy that upgrade took, not a new one of the half-upgraded database.
func TestOpen_AResumedUpgradeKeepsTheOriginalCopy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "meshcore.db")
	dbAt(t, path, 14).Close()
	original := fmt.Sprintf("%s.pre-v12-to-v%d", path, LatestSchemaVersion())
	if err := os.WriteFile(original, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()
	if copies, _ := upgradeCopies(path); len(copies) != 1 || copies[0].path != original {
		t.Errorf("copies %v, want only %s", copies, original)
	}
}

// A glob would read the brackets in this path as a pattern and reach the sibling directory's copies.
func TestOpen_CopiesAreFoundByExactName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	own := filepath.Join(root, "owl[12]")
	sibling := filepath.Join(root, "owl2")
	for _, d := range []string{own, sibling} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(sibling, "meshcore.db.pre-v9-to-v10")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(own, "meshcore.db")
	dbAt(t, path, 16).Close()
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another directory's copy was removed: %v", err)
	}
	if copies, _ := upgradeCopies(path); len(copies) != 1 {
		t.Errorf("own copies %v, want the one just taken", copies)
	}
}

// The hint must name the newest copy, which a string sort puts before .pre-v9.
func TestRestoreHint_NamesTheNewestCopy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "meshcore.db")
	for _, name := range []string{".pre-v9-to-v10", ".pre-v16-to-v17"} {
		if err := os.WriteFile(path+name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if hint := restoreHint(path); !strings.Contains(hint, ".pre-v16-to-v17") {
		t.Errorf("hint %q, want the v16 copy", hint)
	}
}

// Old packets must get the hash and path 004 indexes, or a search misses every packet stored before it while looking fine.
func TestMigration004_BackfillsStoredPackets(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "old.db")
	db := dbAt(t, path, 3)
	raw := []byte{meshcore.MakeHeader(meshcore.RouteTypeFlood, 4, 0), 0x02, 0xAA, 0xBB, 0xDE, 0xAD}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO packets (direction, raw) VALUES ('rx', ?)`, raw); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	var hash, hops string
	if err := st.db.QueryRow(`SELECT packet_hash, path FROM packets`).Scan(&hash, &hops); err != nil {
		t.Fatal(err)
	}
	wantHash, wantPath := derivePacketFields(raw)
	if hash != wantHash || hops != wantPath || hops != "aabb" {
		t.Errorf("packet_hash %q path %q, want %q and %q", hash, hops, wantHash, wantPath)
	}
}

// A shipped file is held by the frozen test, so a cosmetic edit that moved its checksum must not lock out every database that ran it.
func TestOpen_TrustsAShippedFileWhoseChecksumMoved(t *testing.T) {
	t.Parallel()
	_, path := openAt(t)
	db, err := openWritableDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET sha256 = 'before-a-comment-edit' WHERE version = 5`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open refused over a shipped file: %v", err)
	}
	st.Close()
}

// A restore of a draft database was accepted, set the live one aside, and then failed at the next start.
func TestInspectBackup_RefusesADraft(t *testing.T) {
	t.Parallel()
	st, path := openAt(t)
	last := migrations[len(migrations)-1]
	if _, ok := shipped[last.file]; ok {
		t.Skipf("%s has shipped, so no unreleased file is left to draft", last.file)
	}
	if _, err := st.db.ExecContext(t.Context(), `UPDATE schema_migrations SET sha256 = 'draft' WHERE version = ?`, last.version); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := InspectBackup(t.Context(), path); err == nil || !strings.Contains(err.Error(), last.file) {
		t.Fatalf("InspectBackup = %v, want a refusal naming %s", err, last.file)
	}
}

// A full disk must not stop an updated node from starting: the upgrade goes ahead without a copy, and the older copy stays.
func TestOpen_UpgradesWithoutACopyWhenOneCannotBeMade(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "meshcore.db")
	dbAt(t, path, 16).Close()
	older := path + ".pre-v12-to-v16"
	if err := os.WriteFile(older, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory where the copy goes cannot be replaced, as a full disk cannot be written.
	blocked := fmt.Sprintf("%s.pre-v16-to-v%d", path, LatestSchemaVersion())
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open refused to upgrade without a copy: %v", err)
	}
	defer st.Close()
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != LatestSchemaVersion() {
		t.Errorf("user_version %d (%v), want %d", v, err, LatestSchemaVersion())
	}
	if _, err := os.Stat(older); err != nil {
		t.Errorf("the older copy was removed although no new one was taken: %v", err)
	}
}

// A fresh database has nothing to go back to, so no copy is taken.
func TestOpen_NoCopyForAFreshDatabase(t *testing.T) {
	t.Parallel()
	_, path := openAt(t)
	if copies, _ := upgradeCopies(path); len(copies) != 0 {
		t.Errorf("a fresh database left copies %v", copies)
	}
}
