package app

import (
	"path/filepath"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// The app connection is saved apart from the companion, and an app's preferences survive the operator changing the port.
func TestCompanionApp_SavedAndReloaded(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.DefaultConfig()
	web := "0.0.0.0:8860"
	cfg.ListenAddr = &web
	cfg.Companions = []config.CompanionConfig{{Name: "home", App: &config.AppConnection{Port: 5055}}}
	if err := persistToTables(ctx, db, &cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := readConfigFromTables(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	id := loaded.Companions[0].ID
	if a := loaded.Companions[0].App; a == nil || a.Port != 5055 {
		t.Fatalf("app after import = %+v, want port 5055", a)
	}

	prefs := store.DefaultCompanionApp(id)
	prefs.ManualAdd = 1
	if err := db.CompanionApps.SetPrefs(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	b := &backend{db: db}
	if err := b.SetCompanionApp(ctx, id, api.CompanionAppInput{Port: 5056, AllowKeyExport: true}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCompanionApp(ctx, id, api.CompanionAppInput{Port: 8860}); err == nil {
		t.Error("an app port on the web UI's port was saved")
	}
	got, err := db.CompanionApps.Get(ctx, id)
	if err != nil || got.Port != 5056 || !got.AllowKeyExport || got.ManualAdd != 1 {
		t.Errorf("stored %+v, %v; want port 5056 with export allowed and the app's manual add kept", got, err)
	}
	if err := b.SetCompanionApp(ctx, id, api.CompanionAppInput{}); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = readConfigFromTables(ctx, db); loaded.Companions[0].App != nil {
		t.Errorf("port 0 left the app connection %+v", loaded.Companions[0].App)
	}
}
