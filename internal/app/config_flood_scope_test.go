package app

import (
	"path/filepath"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// An edit that leaves floodScope out keeps it, as the forms that do not show it must; an empty string clears it.
func TestCompanionFloodScope_KeptUnlessTheEditSetsIt(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	def := config.DefaultConfig()
	if err := persistToTables(ctx, db, &def); err != nil {
		t.Fatal(err)
	}
	b := &backend{db: db}
	str := func(s string) *string { return &s }
	scope := func(id int64) string {
		t.Helper()
		cfg, err := readConfigFromTables(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cfg.Companions {
			if c.ID == id {
				return c.FloodScope
			}
		}
		t.Fatal("the companion is gone from the config")
		return ""
	}

	id, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "home", FloodScope: str("sco")})
	if err != nil {
		t.Fatal(err)
	}
	if got := scope(id); got != "sco" {
		t.Fatalf("created with scope %q, want sco", got)
	}
	if _, err := b.SaveCompanion(ctx, api.CompanionInput{ID: id, Name: "home renamed"}); err != nil {
		t.Fatal(err)
	}
	if got := scope(id); got != "sco" {
		t.Errorf("after an edit without floodScope the scope is %q, want sco kept", got)
	}
	if _, err := b.SaveCompanion(ctx, api.CompanionInput{ID: id, Name: "home renamed", FloodScope: str("")}); err != nil {
		t.Fatal(err)
	}
	if got := scope(id); got != "" {
		t.Errorf("after clearing the scope is %q, want unscoped", got)
	}
	if _, err := b.SaveCompanion(ctx, api.CompanionInput{ID: id, Name: "home renamed", FloodScope: str("*")}); err == nil {
		t.Error(`a floodScope of "*" was saved`)
	}
}
