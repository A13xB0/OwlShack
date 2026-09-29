package store

import (
	"path/filepath"
	"testing"
)

func TestStore_UpgradeFailoverAndOpenHop(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"pre-feature", "upstream-v15"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 14)
			defer db.Close()
			if _, err := db.ExecContext(ctx, `
				INSERT INTO settings (id, connection) VALUES (1, 'tcp://radio:5000');
				INSERT INTO companions (id, name) VALUES (1, 'backup');
				INSERT INTO triggers (id, companion_id, type, template) VALUES (1, 1, 'group', 'reply');
			`); err != nil {
				t.Fatal(err)
			}
			var wantToken string
			if source == "upstream-v15" {
				if _, err := db.ExecContext(ctx, `
					ALTER TABLE settings ADD COLUMN modem_token TEXT;
					UPDATE settings SET modem_token = 'saved-token';
					PRAGMA user_version = 15;
				`); err != nil {
					t.Fatal(err)
				}
				wantToken = "saved-token"
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				st, err := Open(ctx, path)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer st.Close()
				var version int
				if err := st.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
					t.Fatal(err)
				}
				// Bump alongside TestStore_MigrateUserVersion whenever a migration is appended.
				if version != 20 {
					t.Fatalf("schema version = %d, want 20", version)
				}
				settings, err := st.Settings.Get(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if settings.Connection == nil || *settings.Connection != "tcp://radio:5000" {
					t.Fatalf("connection was not preserved: %+v", settings)
				}
				if wantToken == "" {
					if settings.ModemToken != nil {
						t.Fatalf("new modem token = %q, want NULL", *settings.ModemToken)
					}
				} else if settings.ModemToken == nil || *settings.ModemToken != wantToken {
					t.Fatal("existing modem token was not preserved")
				}
				trigger, err := st.Triggers.Get(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				if trigger.Template != "reply" || trigger.FailoverPattern != "" || trigger.FailoverTimeout != 0 {
					t.Fatalf("trigger settings after upgrade: %+v", trigger)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
