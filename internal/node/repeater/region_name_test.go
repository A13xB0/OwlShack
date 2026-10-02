package repeater

import (
	"context"
	"slices"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
)

// "sco" and "#sco" share a key, so a config holding both builds one region, the first.
func TestRegionsFromConfig_OneRegionPerName(t *testing.T) {
	named, _ := regionsFromConfig([]config.RepeaterRegion{{Name: "sco"}, {Name: "#sco", DenyFlood: true}, {Name: "fif"}})
	if len(named) != 2 || named[0].Name != "sco" || named[0].DenyFlood() || named[1].Name != "fif" {
		t.Errorf("built %d regions %+v, want sco (flood allowed) and fif", len(named), named)
	}
}

// A default region written either way scopes adverts to the configured region.
func TestDefaultRegionScope_EitherSpelling(t *testing.T) {
	for _, tt := range []struct{ region, def string }{{"sco", "#sco"}, {"#sco", "sco"}, {"sco", "sco"}} {
		named, _ := regionsFromConfig([]config.RepeaterRegion{{Name: tt.region}})
		r := &Repeater{cfg: config.RepeaterConfig{Regions: []config.RepeaterRegion{{Name: tt.region}}, DefaultRegion: tt.def}}
		r.node = node.New(meshcore.NewLocalIdentityFromSeed([32]byte{1}), &recordingRadio{}, node.WithRegions(named...))
		got := r.defaultRegionScope()
		r.node.Stop()
		if got == nil || got.Name != tt.region || got.Key != meshcore.NewRegionFromHashtag("sco").Key {
			t.Errorf("region %q, default %q: scope %+v, want the #sco region", tt.region, tt.def, got)
		}
	}
}

// `region put #sco` with "sco" configured reuses it, as putRegion's findByName does.
func TestRegionPutCLI_ReusesTheOtherSpelling(t *testing.T) {
	r := &Repeater{cfg: config.RepeaterConfig{Name: "rp", Regions: []config.RepeaterRegion{{Name: "sco", DenyFlood: true}}}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.runCtx = ctx

	if got := r.runCLI("region put #sco"); got != "OK - (flood allowed)" {
		t.Fatalf("put = %q", got)
	}
	// testReconfigure edits r.cfg in place under r.mu, so read it under the lock too.
	regions := func() []config.RepeaterRegion {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Clone(r.cfg.Regions)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rs := regions(); len(rs) == 1 && !rs[0].DenyFlood {
			if rs[0].Name != "sco" {
				t.Fatalf("region renamed to %q, want sco kept", rs[0].Name)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("regions are %+v, want sco alone with flood allowed", regions())
}
