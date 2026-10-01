package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The feed may drop keys or change types without notice, so a bad entry is skipped, never written.
func TestBuild_KeepsOnlyPresetsSettingsWouldAccept(t *testing.T) {
	t.Parallel()
	feed := `{"config":{"suggested_radio_settings":{"entries":[
		{"title":"New Zealand (Narrow)","frequency":"917.375","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5","network_settings":{"path_hash_size":2}},
		{"title":"Numbers now","frequency":915.8,"spreading_factor":10,"bandwidth":250,"coding_rate":5},
		{"title":"No coding rate","frequency":"915.8","spreading_factor":"10","bandwidth":"250"},
		{"title":"Bad CR","frequency":"915.8","spreading_factor":"10","bandwidth":"250","coding_rate":"9"},
		{"title":"Off the band","frequency":"2400","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5"},
		{"title":"New Zealand (Narrow)","frequency":"917.375","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5"},
		{"frequency":"917.375","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5"},
		{"title":"SF wraps to 7","frequency":"915.8","spreading_factor":"263","bandwidth":"250","coding_rate":"5"},
		{"title":"Hash too big","frequency":"915.8","spreading_factor":"10","bandwidth":"250","coding_rate":"5","network_settings":{"path_hash_size":4}},
		{"title":"Bandwidth in Hz","frequency":"917.375","spreading_factor":"7","bandwidth":"62500","coding_rate":"5"},
		{"title":"No number","frequency":"NaN","spreading_factor":"7","bandwidth":"62.5","coding_rate":"5"}
	]}}}`
	presets, skipped, err := build([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 9 {
		t.Errorf("skipped %d, want 9: %q", len(skipped), skipped)
	}
	got := string(render(presets))
	want := `[
  { "name": "New Zealand (Narrow)", "freq": 917.375, "sf": 7, "bw": 62.5, "cr": 5, "pathHashSize": 2 },
  { "name": "Numbers now", "freq": 915.8, "sf": 10, "bw": 250, "cr": 5 }
]
`
	if got != want {
		t.Errorf("render:\n%s\nwant:\n%s", got, want)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Errorf("rendered file is not JSON: %v", err)
	}
	if _, _, err := build([]byte(`{"config":`)); err == nil || !strings.Contains(err.Error(), "decoding") {
		t.Errorf("a truncated feed: %v, want a decode error", err)
	}
}

// A feed that lost most of its entries is more likely broken than pruned, so it never replaces the file.
func TestGenerate_RefusesAFeedThatLostMostOfItsPresets(t *testing.T) {
	t.Parallel()
	one := `{"config":{"suggested_radio_settings":{"entries":[{"title":"A","frequency":"915.8","spreading_factor":"10","bandwidth":"250","coding_rate":"5"}]}}}`
	current := []byte(`[{"name":"A"},{"name":"B"},{"name":"C"},{"name":"D"}]`)
	if _, _, err := generate([]byte(one), current); err == nil {
		t.Error("1 preset against 4 in the file was written")
	}
	if _, _, err := generate([]byte(one), []byte(`[{"name":"A"},{"name":"B"}]`)); err != nil {
		t.Errorf("1 preset against 2 is half, not a collapse: %v", err)
	}
}
