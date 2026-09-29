package store

import (
	"path/filepath"
	"testing"
)

func TestStore_UpgradeMapProvider(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{"NULL": "osm", "''": "osm", "'abc'": "carto"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 18)
			if _, err := db.ExecContext(t.Context(), `INSERT INTO settings (id, map_tile_key) VALUES (1, `+key+`)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			s, err := st.Settings.Get(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if s.MapProvider != want {
				t.Fatalf("key %s: map_provider = %q, want %q", key, s.MapProvider, want)
			}
			if s.MapDarkStyle != "original" {
				t.Fatalf("map_dark_style = %q, want original", s.MapDarkStyle)
			}
		})
	}
}
