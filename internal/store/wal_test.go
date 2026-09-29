package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wal.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// A checkpoint rewinds the WAL but never shrinks it, so one burst (a restore, a retention trim) kept its size for good.
func TestOpen_TheWALShrinksBackAfterABurst(t *testing.T) {
	t.Parallel()
	st, path := openAt(t)
	ctx := t.Context()
	blob := strings.Repeat("x", 3000)
	st.WriteSync(func() {
		if _, err := st.db.ExecContext(ctx, `CREATE TABLE junk (b TEXT)`); err != nil {
			t.Error(err)
			return
		}
		tx, err := st.db.BeginTx(ctx, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer tx.Rollback()
		for range 4000 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO junk VALUES (?)`, blob); err != nil {
				t.Error(err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Error(err)
		}
	})
	if n := fileSize(t, path+"-wal"); n <= walSizeLimit {
		t.Fatalf("the burst left a %d byte WAL, so it proves nothing about the limit", n)
	}
	st.WriteSync(func() { st.db.ExecContext(ctx, `INSERT INTO junk VALUES ('a')`) })
	if n := fileSize(t, path+"-wal"); n > walSizeLimit {
		t.Errorf("WAL is %d bytes after the next write, want at most %d", n, walSizeLimit)
	}
}

// NORMAL only syncs at a checkpoint, which saves a flush per commit on an SD card; the pool must hand it to every connection.
func TestOpen_SyncsAtCheckpointsNotEveryCommit(t *testing.T) {
	t.Parallel()
	st, _ := openAt(t)
	conns := make([]interface{ Close() error }, 0, 3)
	for range 3 {
		c, err := st.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var mode int
		if err := c.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != 1 {
			t.Errorf("synchronous = %d on a pooled connection, want 1 (NORMAL)", mode)
		}
	}
	for _, c := range conns {
		c.Close()
	}
}

// Under NORMAL a commit can sit unsynced in the WAL, so a save the page reported as done could come back after a power cut unless WriteSync checkpoints it.
func TestWriteSync_ASaveIsInTheMainFileWhenItReturns(t *testing.T) {
	t.Parallel()
	st, path := openAt(t)
	var id int64
	st.WriteSync(func() { id = mkCompanion(t, st, "durable") })

	// Only the main file, as if the WAL's unsynced tail were lost.
	copyPath := filepath.Join(t.TempDir(), "main-only.db")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := openWritableDB(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRowContext(t.Context(), `SELECT name FROM companions WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("the saved companion is not in the main file: %v", err)
	}
}

func TestStore_WALBytesReportsTheWAL(t *testing.T) {
	t.Parallel()
	st, path := openAt(t)
	mkCompanion(t, st, "a")
	if got, want := st.WALBytes(), fileSize(t, path+"-wal"); got != want || got == 0 {
		t.Errorf("WALBytes = %d, want the file's %d (and not 0 after a write)", got, want)
	}
}
