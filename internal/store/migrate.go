package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// migrationFiles holds one file per user_version, named NNN_what.sql; a shipped file is frozen by migrations.sum.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	file    string
	sql     string
	sum     string
}

var migrations = mustLoadMigrations(migrationFiles)

//go:embed migrations.sum
var shippedSumFile string

// shipped is each released file's checksum; startup never refuses a database over one of these, which the frozen test already holds.
var shipped = func() map[string]string {
	lines, err := parseSum(shippedSumFile)
	if err != nil {
		panic(err)
	}
	out := make(map[string]string, len(lines))
	for _, l := range lines {
		out[l.file] = l.sum
	}
	return out
}()

type sumLine struct{ sum, file, release string }

// parseSum reads migrations.sum: "sha256 file release" per line, with # comments and blank lines skipped.
func parseSum(data string) ([]sumLine, error) {
	var out []sumLine
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("migrations.sum line %q is not: sha256 file release", line)
		}
		out = append(out, sumLine{sum: f[0], file: f[1], release: f[2]})
	}
	return out, nil
}

func mustLoadMigrations(fsys fs.FS) []migration {
	ms, err := loadMigrations(fsys)
	if err != nil {
		panic(err)
	}
	return ms
}

var migrationName = regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)

// loadMigrations reads the files in order and refuses a gap, a duplicate or a stray name, any of which would skip or repeat a version.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "migrations/*")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	out := make([]migration, 0, len(names))
	for i, p := range names {
		base := path.Base(p)
		m := migrationName.FindStringSubmatch(base)
		if m == nil {
			return nil, fmt.Errorf("migration file %s is not named NNN_name.sql", base)
		}
		n, _ := strconv.Atoi(m[1])
		if n != i+1 {
			return nil, fmt.Errorf("migration file %s is number %d where %d comes next: numbers run from 001 with no gaps or repeats", base, n, i+1)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(data)
		out = append(out, migration{version: n, file: base, sql: string(data), sum: hex.EncodeToString(h[:])})
	}
	return out, nil
}

// apply runs the file without the version bump or the checksum row; 004 also runs a Go step, frozen like the files, for what SQL cannot do.
func (m migration) apply(ctx context.Context, db dbExecer) error {
	if _, err := db.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if m.version == 4 {
		return backfillPacketFields(ctx, db)
	}
	return nil
}

// dbExecer is the subset of *sql.DB / *sql.Tx a migration needs.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LatestSchemaVersion is the version this binary migrates to.
func LatestSchemaVersion() int { return len(migrations) }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	// A database from a newer build skips nothing now and every slot appended later, drifting while it looks healthy; InspectBackup refuses a restore for the same reason.
	if version > LatestSchemaVersion() {
		return fmt.Errorf("database is at schema version %d but this build understands %d: "+
			"it was written by a newer OwlShack. Run that build%s, or if the schema is known to match, "+
			"stamp it back with PRAGMA user_version = %d",
			version, LatestSchemaVersion(), restoreHint(s.path), LatestSchemaVersion())
	}
	if err := s.syncApplied(ctx, version); err != nil {
		return err
	}
	var keep string
	if version > 0 && version < LatestSchemaVersion() {
		// The copy is a way back, not a precondition: a Pi on a full SD card must still start after an update.
		if keep, err = s.copyBeforeUpgrade(ctx, version); err != nil {
			slog.Warn("upgrading the database WITHOUT a copy to go back to; back it up by hand before downgrading",
				"error", err, "from", version, "to", LatestSchemaVersion())
		}
	}
	for _, m := range migrations[version:] {
		if err := s.runMigration(ctx, m); err != nil {
			return err
		}
	}
	if keep != "" {
		s.pruneUpgradeCopies(keep)
	}
	return nil
}

// syncApplied records the files a database has run, trusting one migrated before checksums were kept, and refuses one that ran a different draft of an unreleased file.
func (s *Store) syncApplied(ctx context.Context, version int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("recording applied migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		file       TEXT NOT NULL,
		sha256     TEXT NOT NULL,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	for _, m := range migrations[:version] {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations (version, file, sha256) VALUES (?, ?, ?)`,
			m.version, m.file, m.sum); err != nil {
			return fmt.Errorf("recording migration %s: %w", m.file, err)
		}
	}
	file, err := draftApplied(ctx, tx, version)
	if err != nil {
		return err
	}
	if file != "" {
		return fmt.Errorf("migration %s changed after this database ran it, so it ran an unreleased draft of that file "+
			"and this build cannot tell what it holds. Start from a fresh database%s", file, restoreHint(s.path))
	}
	return tx.Commit()
}

// draftApplied names an unreleased file the database ran in a different draft, or "" when there is none; a shipped file is not compared, as the frozen test holds it and a cosmetic edit must not lock out every install.
func draftApplied(ctx context.Context, db dbExecer, version int) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, sha256 FROM schema_migrations WHERE version <= ?`, version)
	if err != nil {
		return "", fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return "", fmt.Errorf("reading applied migrations: %w", err)
		}
		if v < 1 || v > len(migrations) {
			return "", fmt.Errorf("schema_migrations records version %d, which no migration file has", v)
		}
		m := migrations[v-1]
		if _, ok := shipped[m.file]; !ok && m.sum != sum {
			return m.file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("reading applied migrations: %w", err)
	}
	return "", nil
}

// runMigration applies one migration, its checksum row and its user_version bump atomically, so a crash mid-way can't leave a half-applied schema.
func (s *Store) runMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.file, err)
	}
	defer tx.Rollback()

	if err := m.apply(ctx, tx); err != nil {
		return fmt.Errorf("migration %s: %w", m.file, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO schema_migrations (version, file, sha256) VALUES (?, ?, ?)`,
		m.version, m.file, m.sum); err != nil {
		return fmt.Errorf("migration %s: recording it: %w", m.file, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("setting schema version %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.file, err)
	}
	return nil
}

// upgradeCopyName is <db>.pre-v<from>-to-v<to>: the target says whether the upgrade that took it finished.
var upgradeCopyName = regexp.MustCompile(`^(.+)\.pre-v(\d+)-to-v(\d+)$`)

type upgradeCopy struct {
	path     string
	from, to int
}

// upgradeCopies lists the database's pre-upgrade copies by exact name, as a glob would read [ or * in the path as a pattern and reach another directory's.
func upgradeCopies(dbPath string) ([]upgradeCopy, error) {
	dir, base := filepath.Split(dbPath)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []upgradeCopy
	for _, e := range entries {
		m := upgradeCopyName.FindStringSubmatch(e.Name())
		if m == nil || m[1] != base || !e.Type().IsRegular() {
			continue
		}
		from, _ := strconv.Atoi(m[2])
		to, _ := strconv.Atoi(m[3])
		out = append(out, upgradeCopy{path: filepath.Join(dir, e.Name()), from: from, to: to})
	}
	return out, nil
}

// copyBeforeUpgrade returns the copy to go back to: a new one, or the one an unfinished upgrade left, since a copy of a half-upgraded database is one no release can open.
func (s *Store) copyBeforeUpgrade(ctx context.Context, version int) (string, error) {
	copies, err := upgradeCopies(s.path)
	if err != nil {
		return "", fmt.Errorf("listing pre-upgrade copies: %w", err)
	}
	var unfinished *upgradeCopy
	for i, c := range copies {
		if c.to > version && (unfinished == nil || c.from < unfinished.from) {
			unfinished = &copies[i]
		}
	}
	if unfinished != nil {
		slog.Info("keeping the copy from an upgrade that did not finish", "copy", unfinished.path)
		return unfinished.path, nil
	}

	dst := fmt.Sprintf("%s.pre-v%d-to-v%d", s.path, version, LatestSchemaVersion())
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("replacing the pre-upgrade copy: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("copying the database to %s, which needs about the database's size free on disk: %w", dst, err)
	}
	// VACUUM INTO does not sync its output, and a power cut soon after an upgrade would truncate the one way back.
	toSync := []string{dst}
	// Windows refuses to flush a directory opened read-only, and NTFS journals the entry itself.
	if runtime.GOOS != "windows" {
		toSync = append(toSync, filepath.Dir(dst))
	}
	for _, p := range toSync {
		if err := syncPath(p); err != nil {
			return "", fmt.Errorf("syncing the pre-upgrade copy: %w", err)
		}
	}
	slog.Info("copied the database before upgrading it; move the copy back to undo the upgrade",
		"copy", dst, "from", version, "to", LatestSchemaVersion())
	return dst, nil
}

func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// pruneUpgradeCopies runs once an upgrade has finished, so a failed one never costs the copy it started from.
func (s *Store) pruneUpgradeCopies(keep string) {
	copies, err := upgradeCopies(s.path)
	if err != nil {
		slog.Warn("could not list older pre-upgrade copies", "error", err)
		return
	}
	for _, c := range copies {
		if c.path == keep {
			continue
		}
		if err := os.Remove(c.path); err != nil {
			slog.Warn("could not remove an older pre-upgrade copy", "path", c.path, "error", err)
		}
	}
}

// restoreHint names what an operator can move back into place, for an error message, or says nothing when there is none.
func restoreHint(dbPath string) string {
	var hint string
	copies, _ := upgradeCopies(dbPath)
	if len(copies) > 0 {
		latest := slices.MaxFunc(copies, func(a, b upgradeCopy) int { return a.from - b.from })
		hint += ", or stop OwlShack and move " + latest.path + " back to " + dbPath + " (the copy taken before the last upgrade)"
	}
	if _, err := os.Stat(dbPath + ".replaced"); err == nil {
		hint += ", or move " + dbPath + ".replaced back, the database a restore set aside"
	}
	return hint
}

// backfillPacketFields fills packet_hash and path for rows stored before 004 added them; decoding a packet takes Go, not SQL.
func backfillPacketFields(ctx context.Context, tx dbExecer) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, raw FROM packets")
	if err != nil {
		return fmt.Errorf("reading packets for backfill: %w", err)
	}
	type row struct {
		id  int64
		raw []byte
	}
	var batch []row
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.id, &rr.raw); err != nil {
			rows.Close()
			return fmt.Errorf("scanning packet for backfill: %w", err)
		}
		batch = append(batch, rr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating packets for backfill: %w", err)
	}

	for _, rr := range batch {
		packetHash, path := derivePacketFields(rr.raw)
		if packetHash == "" && path == "" {
			continue // unparseable row; leave the '' defaults
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE packets SET packet_hash = ?, path = ? WHERE id = ?",
			packetHash, path, rr.id); err != nil {
			return fmt.Errorf("backfilling packet %d: %w", rr.id, err)
		}
	}
	return nil
}
