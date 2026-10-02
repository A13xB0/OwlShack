package app

import (
	"path/filepath"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// Adding "#sco" beside "sco" edits the one region, and removing it by either spelling clears the default that named it.
func TestRepeaterRegion_EitherSpellingIsTheSameRegion(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "regions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	def := config.DefaultConfig()
	def.Repeater = &config.RepeaterConfig{Name: "rpt", Regions: []config.RepeaterRegion{{Name: "sco"}}, DefaultRegion: "sco"}
	if err := persistToTables(ctx, db, &def); err != nil {
		t.Fatal(err)
	}
	b := &backend{db: db}
	repeater := func() *config.RepeaterConfig {
		t.Helper()
		cfg, err := readConfigFromTables(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Repeater
	}

	if err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "#sco", DenyFlood: true}); err != nil {
		t.Fatal(err)
	}
	if rs := repeater().Regions; len(rs) != 1 || rs[0].Name != "sco" || !rs[0].DenyFlood {
		t.Fatalf("regions %+v, want sco alone with flood denied", rs)
	}
	if err := b.SetRepeaterRegionFlood(ctx, "#sco", false); err != nil {
		t.Fatal(err)
	}
	if rs := repeater().Regions; rs[0].DenyFlood {
		t.Fatal("toggling #sco did not reach sco")
	}
	if err := b.RemoveRepeaterRegion(ctx, "#sco"); err != nil {
		t.Fatal(err)
	}
	if r := repeater(); len(r.Regions) != 0 || r.DefaultRegion != "" {
		t.Errorf("regions %+v default %q, want both cleared", r.Regions, r.DefaultRegion)
	}
}
