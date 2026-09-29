package config

import (
	"strings"
	"testing"
)

func cronTrigger(tmpl string) *TriggerConfig {
	t := &TriggerConfig{Type: "cron", Template: "x", Schedule: "*/5 * * * *"}
	t.Template = tmpl
	return t
}

// Validate parses the template against a stub func map, so a function added to the templater and
// not here is rejected on save: the UI reports "invalid template" for a template that works.
func TestValidate_AcceptsEveryTemplateFunction(t *testing.T) {
	for _, tmpl := range []string{
		`{{now.Year}}`,
		`{{date now "15:04"}}`,
		`{{date now "15:04" "Pacific/Auckland"}}`,
		`{{date .Timestamp "2006-01-02"}}`,
		`{{formatPathBytes .PathHashes}}`,
	} {
		if err := cronTrigger(tmpl).Validate(); err != nil {
			t.Errorf("%s should validate: %v", tmpl, err)
		}
	}
}

func TestValidate_RejectsUnknownFunction(t *testing.T) {
	err := cronTrigger(`{{strftime now "%H:%M"}}`).Validate()
	if err == nil {
		t.Fatal("want an error for a function that does not exist")
	}
	if !strings.Contains(err.Error(), "invalid template") {
		t.Errorf("got %v", err)
	}
}

// Mirroring answers an incoming message with the size it arrived on. A scheduled or feed trigger
// answers nothing, so asking it to mirror is a config that cannot mean what it says.
func TestTriggerConfig_MirrorPathHashSizeNeedsAnIncomingMessage(t *testing.T) {
	t.Parallel()
	mirror := uint8(MirrorIncomingPathHashSize)
	twoBytes := uint8(2)
	over := uint8(MaxPathHashSize + 1)

	for _, tc := range []struct {
		name    string
		cfg     TriggerConfig
		wantErr bool
	}{
		{"group mirrors", TriggerConfig{Type: "group", Template: "x", Channels: &ChannelList{{Name: "Public"}}, PathHashSize: &mirror}, false},
		{"dm mirrors", TriggerConfig{Type: "dm", Template: "x", PathHashSize: &mirror}, false},
		{"cron cannot mirror", TriggerConfig{Type: "cron", Template: "x", Schedule: "@every 1h", Contacts: &[]string{"aa"}, PathHashSize: &mirror}, true},
		{"rss cannot mirror", TriggerConfig{Type: "rss", Template: "x", URL: "https://example.org/f.xml", Contacts: &[]string{"aa"}, PathHashSize: &mirror}, true},
		{"cap cannot mirror", TriggerConfig{Type: "cap", Template: "x", URL: "https://example.org/f.xml", Contacts: &[]string{"aa"}, PathHashSize: &mirror}, true},
		{"cron takes a fixed size", TriggerConfig{Type: "cron", Template: "x", Schedule: "@every 1h", Contacts: &[]string{"aa"}, PathHashSize: &twoBytes}, false},
		{"past the maximum is rejected", TriggerConfig{Type: "dm", Template: "x", PathHashSize: &over}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("%s accepted a mirror pathHashSize; it answers no message", tc.cfg.Type)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("rejected a valid config: %v", err)
			}
		})
	}
}

func TestTriggerConfig_Failover(t *testing.T) {
	for _, tc := range []struct {
		name, kind, pattern string
		timeout             int64
		valid               bool
	}{
		{"disabled", "group", "", 0, true},
		{"enabled", "group", `^@\[{{.Sender | reQuote}}\].+`, 10, true},
		{"legacy channel", "channel", `^pong$`, 1, true},
		{"dm unsupported", "dm", `pong`, 10, false},
		{"missing pattern", "group", "", 10, false},
		{"blank pattern", "group", " ", 10, false},
		{"missing timeout", "group", "pong", 0, false},
		{"negative timeout", "group", "pong", -1, false},
		{"excessive timeout", "group", "pong", 3601, false},
		{"bad regex", "group", `[`, 10, false},
		{"bad template", "group", `{{`, 10, false},
		{"unknown function", "group", `{{.Sender | typo}}`, 10, false},
		{"unknown field", "group", `{{.Typo}}`, 10, false},
		{"empty rendered pattern", "group", `{{if false}}pong{{end}}`, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := TriggerConfig{Type: tc.kind, Template: "reply", Channels: &ChannelList{{Name: "testing"}}, FailoverPattern: tc.pattern, FailoverTimeout: tc.timeout}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestValidate_Location(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		typ  string
		loc  *FeedLocation
		ok   bool
	}{
		{"a point in NZ", "cap", fl(-41.29, 174.78, 20), true},
		{"the widest margin", "cap", fl(-41.29, 174.78, MaxLocationRadiusKm), true},
		{"only cap alerts carry shapes", "rss", fl(-41.29, 174.78, 0), false},
		{"latitude past the pole", "cap", fl(-91, 174.78, 0), false},
		{"longitude past the antimeridian", "cap", fl(-41.29, 181, 0), false},
		{"a negative margin", "cap", fl(-41.29, 174.78, -1), false},
		{"a margin past the limit", "cap", fl(-41.29, 174.78, MaxLocationRadiusKm+1), false},
		{"a field left out of a config file", "cap", &FeedLocation{Lat: ptr(-41.29), Lon: ptr(174.78)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := ChannelList{{Name: "Public"}}
			tc := TriggerConfig{Type: c.typ, URL: "https://example.com/feed", Template: "x", Channels: &ch, Location: c.loc}
			if err := tc.Validate(); (err == nil) != c.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, c.ok)
			}
			if err := tc.ValidateFeedTest(); (err == nil) != c.ok {
				t.Errorf("ValidateFeedTest() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestValidate_Regions(t *testing.T) {
	t.Parallel()
	ch := ChannelList{{Name: "Public"}}
	base := func() TriggerConfig {
		return TriggerConfig{Type: "cap", URL: "https://example.com/feed", Template: "x", Channels: &ch}
	}
	ids := func(s ...string) *[]string { return &s }

	ok := base()
	ok.Regions = ids("NZL-3398", "NZL-3404")
	if err := ok.Validate(); err != nil {
		t.Errorf("two known regions: %v", err)
	}
	for name, tc := range map[string]func(*TriggerConfig){
		"an empty list":     func(t *TriggerConfig) { t.Regions = ids() },
		"an unknown region": func(t *TriggerConfig) { t.Regions = ids("NZ-AUK") },
		"an rss trigger":    func(t *TriggerConfig) { t.Type = "rss"; t.Regions = ids("NZL-3398") },
		"regions and a point": func(t *TriggerConfig) {
			t.Regions = ids("NZL-3398")
			t.Location = fl(-36.8, 174.7, 0)
		},
	} {
		cfg := base()
		tc(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func fl(lat, lon, km float64) *FeedLocation {
	return &FeedLocation{Lat: &lat, Lon: &lon, RadiusKm: &km}
}
