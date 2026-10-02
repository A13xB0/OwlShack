package config

import "testing"

func TestSameRegion_IgnoresTheLeadingHash(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		a, b string
		same bool
	}{
		{"sco", "sco", true},
		{"sco", "#sco", true},
		{"#sco", "#sco", true},
		{"sco", "SCO", false},
		{"sco", "fif", false},
		{"sco", "##sco", false},
	} {
		if got := SameRegion(tt.a, tt.b); got != tt.same {
			t.Errorf("SameRegion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.same)
		}
	}
}

func TestValidate_DefaultAndHomeRegionMatchEitherSpelling(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ region, ref string }{{"sco", "#sco"}, {"#sco", "sco"}, {"#sco", "#sco"}} {
		cfg := Config{Repeater: &RepeaterConfig{
			Name:          "rpt",
			Regions:       []RepeaterRegion{{Name: tt.region}},
			DefaultRegion: tt.ref,
			HomeRegion:    tt.ref,
		}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("region %q referenced as %q: %v", tt.region, tt.ref, err)
		}
	}
	cfg := Config{Repeater: &RepeaterConfig{Name: "rpt", Regions: []RepeaterRegion{{Name: "sco"}}, DefaultRegion: "fif"}}
	if cfg.Validate() == nil {
		t.Error("a default region that is not configured validated")
	}
}
