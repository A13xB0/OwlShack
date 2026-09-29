package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openReadOnly inspects a database file without migrating it; mode=ro fails rather than creating a missing file.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return db, nil
}

// BackupTo snapshots the database with VACUUM INTO; read-only, so it bypasses the writer goroutine, and the destination must not exist.
func (s *Store) BackupTo(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup target %s already exists", path)
	}
	// Bind the path so quotes in it can't break out of the statement.
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}
	return nil
}

// SchemaVersion reports the DB's user_version, the migration level.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("reading user_version: %w", err)
	}
	return v, nil
}

// InspectBackup returns path's schema version, rejecting a file from a newer build since migrations never run backwards.
func InspectBackup(ctx context.Context, path string) (int, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("not a readable SQLite database: %w", err)
	}
	// user_version alone is cheap to forge; require the baseline table too.
	var name string
	err = db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='settings'").Scan(&name)
	if err != nil {
		return 0, fmt.Errorf("does not look like an OwlShack database (no settings table)")
	}
	if v > LatestSchemaVersion() {
		return v, fmt.Errorf("backup is from a newer version (schema %d, this build understands %d) — upgrade first",
			v, LatestSchemaVersion())
	}
	// Open would refuse it after the restore had already set the live database aside.
	if err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&name); err == nil {
		file, err := draftApplied(ctx, db, v)
		if err != nil {
			return v, err
		}
		if file != "" {
			return v, fmt.Errorf("backup ran an unreleased draft of %s, so this build cannot open it", file)
		}
	}
	return v, nil
}

// RestorePath is where a restore is staged for startup to swap in; the running process cannot replace the DB underneath itself.
func RestorePath(dbPath string) string { return dbPath + ".restore" }

// AdoptPendingRestore moves a staged restore over dbPath; call before Open.
func AdoptPendingRestore(dbPath string) (bool, error) {
	pending := RestorePath(dbPath)
	if _, err := os.Stat(pending); err != nil {
		return false, nil // nothing staged
	}
	// Keep the outgoing DB next to the new one rather than deleting it.
	if _, err := os.Stat(dbPath); err == nil {
		prev := dbPath + ".replaced"
		_ = os.Remove(prev)
		if err := os.Rename(dbPath, prev); err != nil {
			return false, fmt.Errorf("setting aside current database: %w", err)
		}
	}
	// WAL and shm belong to the old DB; leaving them would corrupt the new one.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	if err := os.Rename(pending, dbPath); err != nil {
		return false, fmt.Errorf("adopting restored database: %w", err)
	}
	return true, nil
}

// StageRestore writes backup bytes beside the live DB for the next startup to adopt.
func StageRestore(ctx context.Context, dbPath string, data []byte) (schemaVersion int, err error) {
	dir := filepath.Dir(dbPath)
	tmp, err := os.CreateTemp(dir, ".owlshack-restore-*")
	if err != nil {
		return 0, fmt.Errorf("staging restore: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()
	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		return 0, fmt.Errorf("writing staged restore: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return 0, fmt.Errorf("writing staged restore: %w", cerr)
	}

	v, err := InspectBackup(ctx, tmpName)
	if err != nil {
		return v, err
	}
	if err = os.Rename(tmpName, RestorePath(dbPath)); err != nil {
		return v, fmt.Errorf("staging restore: %w", err)
	}
	return v, nil
}

// PruneOptions selects what a backup keeps; day counts are DaysAll, DaysNone, or a positive number of days back.
type PruneOptions struct {
	// CompanionIDs is exactly what to keep: empty keeps none, and there is no "unset means all" case.
	CompanionIDs []int64
	Contacts     bool
	Triggers     bool
	Mqtt         bool
	Repeater     bool
	Peers        bool
	MessageDays  int
	PacketDays   int
	MetricDays   int
	// IdentityKeys keeps the node private keys; false blanks them and startup mints new ones.
	IdentityKeys bool
}

const (
	DaysNone = 0
	DaysAll  = -1
)

// PruneBackup deletes unselected data in place then vacuums; only ever call it on a copy.
func PruneBackup(ctx context.Context, path string, opts PruneOptions) error {
	db, err := openWritableDB(path)
	if err != nil {
		return err
	}
	defer db.Close()

	// Cascades do the dependent rows, so companion pruning must come first.
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enabling cascades: %w", err)
	}

	// NOT IN () is not valid SQL, so an empty selection is a plain delete.
	q, args := "DELETE FROM companions", []any(nil)
	if len(opts.CompanionIDs) > 0 {
		ph, ids := placeholders(opts.CompanionIDs)
		q, args = q+" WHERE id NOT IN ("+ph+")", ids
	}
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("pruning companions: %w", err)
	}

	type step struct {
		sql  string
		skip bool
	}
	steps := []step{
		{"DELETE FROM companion_contacts", opts.Contacts},
		{"DELETE FROM triggers", opts.Triggers},
		{"DELETE FROM mqtt_brokers", opts.Mqtt},
		{"DELETE FROM repeater", opts.Repeater},
		{"DELETE FROM repeater_acl", opts.Repeater},
		{"DELETE FROM discovered_peers", opts.Peers},
	}
	for _, s := range steps {
		if s.skip {
			continue
		}
		if _, err := db.ExecContext(ctx, s.sql); err != nil {
			return fmt.Errorf("%s: %w", s.sql, err)
		}
	}

	// The map keys on node kind and id, so no cascade reaches it and an orphan would restore onto the next node to take that id.
	if _, err := db.ExecContext(ctx, `DELETE FROM telemetry_map
		WHERE (node_kind = 'companion' AND node_id NOT IN (SELECT id FROM companions))
		   OR (node_kind = 'repeater' AND NOT EXISTS (SELECT 1 FROM repeater))`); err != nil {
		return fmt.Errorf("pruning telemetry_map: %w", err)
	}

	// DATETIME columns compare against a SQL timestamp; the metrics/neighbour tables store unix seconds.
	windows := []struct {
		table, col string
		days       int
		unix       bool
	}{
		{"packets", "received_at", opts.PacketDays, false},
		{"messages", "timestamp", opts.MessageDays, false},
		{"message_echoes", "", opts.MessageDays, false}, // no timestamp; tied to messages
		{"node_metrics", "ts", opts.MetricDays, true},
		{"node_neighbors", "ts", opts.MetricDays, true},
	}
	for _, w := range windows {
		if w.days == DaysAll {
			continue
		}
		if w.days == DaysNone || w.col == "" {
			if _, err := db.ExecContext(ctx, "DELETE FROM "+w.table); err != nil {
				return fmt.Errorf("clearing %s: %w", w.table, err)
			}
			continue
		}
		var q string
		if w.unix {
			q = fmt.Sprintf("DELETE FROM %s WHERE %s < unixepoch('now', ?)", w.table, w.col)
		} else {
			q = fmt.Sprintf("DELETE FROM %s WHERE %s < datetime('now', ?)", w.table, w.col)
		}
		if _, err := db.ExecContext(ctx, q, fmt.Sprintf("-%d days", w.days)); err != nil {
			return fmt.Errorf("trimming %s: %w", w.table, err)
		}
	}
	// Echoes belong to messages that may have just gone.
	if opts.MessageDays != DaysAll {
		if _, err := db.ExecContext(ctx,
			"DELETE FROM message_echoes WHERE message_id NOT IN (SELECT id FROM messages)"); err != nil {
			return fmt.Errorf("trimming message_echoes: %w", err)
		}
	}

	if !opts.IdentityKeys {
		for _, q := range []string{
			"UPDATE companions SET private_key = ''",
			"UPDATE repeater SET private_key = ''",
		} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("stripping identity keys: %w", err)
			}
		}
	}

	// Without this the file keeps the freed pages and does not shrink.
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("compacting backup: %w", err)
	}
	return nil
}

func openWritableDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return db, nil
}

// BackupCounts is what a set of PruneOptions would capture; Bytes is the live DB size, the ceiling for a pruned backup.
type BackupCounts struct {
	Companions int64
	Contacts   int64
	Messages   int64
	Packets    int64
	Peers      int64
	Metrics    int64
	Bytes      int64
}

// CountForBackup counts what a backup would include, using the same predicates PruneBackup deletes by.
func (s *Store) CountForBackup(ctx context.Context, opts PruneOptions) (*BackupCounts, error) {
	out := &BackupCounts{}

	compWhere, compArgs := " WHERE 0", []any(nil) // empty selection keeps none
	if len(opts.CompanionIDs) > 0 {
		ph, ids := placeholders(opts.CompanionIDs)
		compWhere, compArgs = " WHERE id IN ("+ph+")", ids
	}
	if err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM companions"+compWhere, compArgs...).Scan(&out.Companions); err != nil {
		return nil, fmt.Errorf("counting companions: %w", err)
	}

	// Rows belonging to companions the backup keeps.
	keptIDs := "SELECT id FROM companions" + compWhere
	if opts.Contacts {
		if err := s.db.QueryRowContext(ctx,
			"SELECT count(*) FROM companion_contacts WHERE companion_id IN ("+keptIDs+")",
			compArgs...).Scan(&out.Contacts); err != nil {
			return nil, fmt.Errorf("counting contacts: %w", err)
		}
	}
	if opts.MessageDays != DaysNone {
		q := "SELECT count(*) FROM messages WHERE companion_id IN (" + keptIDs + ")"
		args := append([]any(nil), compArgs...)
		if opts.MessageDays != DaysAll {
			q += " AND timestamp >= datetime('now', ?)"
			args = append(args, fmt.Sprintf("-%d days", opts.MessageDays))
		}
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&out.Messages); err != nil {
			return nil, fmt.Errorf("counting messages: %w", err)
		}
	}

	if err := s.countWindow(ctx, "packets", "received_at", false, opts.PacketDays, &out.Packets); err != nil {
		return nil, err
	}
	if err := s.countWindow(ctx, "node_metrics", "ts", true, opts.MetricDays, &out.Metrics); err != nil {
		return nil, err
	}
	if opts.Peers {
		if err := s.db.QueryRowContext(ctx,
			"SELECT count(*) FROM discovered_peers").Scan(&out.Peers); err != nil {
			return nil, fmt.Errorf("counting peers: %w", err)
		}
	}

	var pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err == nil {
		if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err == nil {
			out.Bytes = pageCount * pageSize
		}
	}
	return out, nil
}

func (s *Store) countWindow(ctx context.Context, table, col string, unix bool, days int, dst *int64) error {
	if days == DaysNone {
		return nil
	}
	q := "SELECT count(*) FROM " + table
	var args []any
	if days != DaysAll {
		if unix {
			q += " WHERE " + col + " >= unixepoch('now', ?)"
		} else {
			q += " WHERE " + col + " >= datetime('now', ?)"
		}
		args = append(args, fmt.Sprintf("-%d days", days))
	}
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(dst); err != nil {
		return fmt.Errorf("counting %s: %w", table, err)
	}
	return nil
}

// placeholders renders ids as "?,?,?" plus the matching args.
func placeholders(ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id
	}
	return strings.Join(ph, ","), args
}
