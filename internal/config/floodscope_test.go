package config

import (
	"strings"
	"testing"
)

func TestValidate_CompanionFloodScope(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		scope string
		ok    bool
	}{
		{"", true},
		{"sco", true},
		{"#sco", true},
		{"fife-north", true},
		{"*", false},
		{"$private", false},
		{"no spaces", false},
		{strings.Repeat("a", 31), false},
	} {
		cfg := Config{Companions: []CompanionConfig{{Name: "home", FloodScope: tt.scope}}}
		if err := cfg.Validate(); (err == nil) != tt.ok {
			t.Errorf("floodScope %q: Validate() = %v, want ok=%v", tt.scope, err, tt.ok)
		}
	}
}

// A bare scope name is the "#name" hashtag region, so "sco" and "#sco" carry the same key.
func TestScopeRegion_BareNameIsTheHashtag(t *testing.T) {
	t.Parallel()
	bare, tagged := ScopeRegion("sco"), ScopeRegion("#sco")
	if bare.Key != tagged.Key {
		t.Error(`"sco" and "#sco" derived different keys`)
	}
	if bare.Name != "sco" {
		t.Errorf("name %q, want the configured name kept", bare.Name)
	}
}
