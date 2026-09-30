package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openHop firmware answers its serial link at 921600 only, so a stored openHop serial rate is moved there; every other link keeps its own.
func TestStore_UpgradeOpenhopSerialBaud(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, connection string
		baud             any
		want             sql.NullInt64
	}{
		{"openHop serial at the old default", "openhop:///dev/ttyUSB0", 115200, sql.NullInt64{Int64: 921600, Valid: true}},
		{"openHop serial at a chosen rate", "openhop:///dev/ttyUSB0", 9600, sql.NullInt64{Int64: 921600, Valid: true}},
		{"openHop serial with no rate", "openhop:///dev/ttyUSB0", nil, sql.NullInt64{Int64: 921600, Valid: true}},
		{"openHop over the network", "openhop://192.168.1.5:5055", 115200, sql.NullInt64{Int64: 115200, Valid: true}},
		{"KISS over serial", "serial:///dev/ttyACM0", 115200, sql.NullInt64{Int64: 115200, Valid: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 20)
			exec(t, db, `INSERT INTO settings (id, connection, baud_rate) VALUES (1, ?, ?)`, tc.connection, tc.baud)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var got sql.NullInt64
			if err := st.db.QueryRowContext(t.Context(), `SELECT baud_rate FROM settings WHERE id = 1`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("baud_rate %v, want %v", got, tc.want)
			}
		})
	}
}
