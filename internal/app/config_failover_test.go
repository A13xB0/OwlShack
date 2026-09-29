package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func TestFailoverConfigRoundTrip(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	triggers := []config.TriggerConfig{{Type: "group", Template: "reply", Channels: &config.ChannelList{{Name: "Public"}},
		FailoverPattern: `^@\[{{.Sender | reQuote}}\].+`, FailoverTimeout: 10}}
	cfg := &config.Config{Companions: []config.CompanionConfig{{Name: "backup", PrivateKey: strings.Repeat("01", 32), Triggers: &triggers}}}
	for _, enabled := range []bool{true, false} {
		if !enabled {
			triggers[0].FailoverPattern = ""
			triggers[0].FailoverTimeout = 0
		}
		st.WriteSync(func() { err = writeConfigToTables(t.Context(), st, cfg) })
		if err != nil {
			t.Fatal(err)
		}
		got, err := readConfigFromTables(t.Context(), st)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Companions) != 1 || got.Companions[0].Triggers == nil || len(*got.Companions[0].Triggers) != 1 {
			t.Fatalf("lost trigger: %+v", got.Companions)
		}
		actual := (*got.Companions[0].Triggers)[0]
		if actual.FailoverPattern != triggers[0].FailoverPattern || actual.FailoverTimeout != triggers[0].FailoverTimeout {
			t.Fatalf("failover did not survive config round trip: %+v", actual)
		}
	}
}

func TestLocationConfigRoundTrip(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	triggers := []config.TriggerConfig{{Type: "cap", Template: "{{.Headline}}", URL: "https://example.com/cap",
		Channels: &config.ChannelList{{Name: "Public"}}, Location: &config.FeedLocation{Lat: f64(-40.9511), Lon: f64(175.6573), RadiusKm: f64(25)}}}
	cfg := &config.Config{Companions: []config.CompanionConfig{{Name: "backup", PrivateKey: strings.Repeat("01", 32), Triggers: &triggers}}}
	for _, want := range []*config.FeedLocation{triggers[0].Location, nil} {
		triggers[0].Location = want
		st.WriteSync(func() { err = writeConfigToTables(t.Context(), st, cfg) })
		if err != nil {
			t.Fatal(err)
		}
		got, err := readConfigFromTables(t.Context(), st)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Companions) != 1 || got.Companions[0].Triggers == nil || len(*got.Companions[0].Triggers) != 1 {
			t.Fatalf("lost trigger: %+v", got.Companions)
		}
		actual := (*got.Companions[0].Triggers)[0].Location
		if (actual == nil) != (want == nil) || (want != nil && fmt.Sprint(actual.Values()) != fmt.Sprint(want.Values())) {
			t.Fatalf("location = %+v, want %+v", actual, want)
		}
	}
}

func TestRegionsConfigRoundTrip(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids := []string{"NZL-3398", "NZL-5468"}
	triggers := []config.TriggerConfig{{Type: "cap", Template: "{{.Headline}}", URL: "https://example.com/cap",
		Channels: &config.ChannelList{{Name: "Public"}}, Regions: &ids}}
	cfg := &config.Config{Companions: []config.CompanionConfig{{Name: "backup", PrivateKey: strings.Repeat("01", 32), Triggers: &triggers}}}
	for _, want := range []*[]string{&ids, nil} {
		triggers[0].Regions = want
		st.WriteSync(func() { err = writeConfigToTables(t.Context(), st, cfg) })
		if err != nil {
			t.Fatal(err)
		}
		got, err := readConfigFromTables(t.Context(), st)
		if err != nil {
			t.Fatal(err)
		}
		actual := (*got.Companions[0].Triggers)[0].Regions
		if (actual == nil) != (want == nil) || (want != nil && strings.Join(*actual, ",") != strings.Join(*want, ",")) {
			t.Fatalf("regions = %v, want %v", actual, want)
		}
	}
}

func TestLocationFromAPI_RefusesAMissingField(t *testing.T) {
	v := 1.0
	for _, l := range []api.TriggerLocation{{Lat: &v}, {Lat: &v, Lon: &v}, {Lon: &v, RadiusKm: &v}} {
		if _, err := locationFromAPI(&l); err == nil {
			t.Errorf("%+v was accepted", l)
		}
	}
	if got, err := locationFromAPI(&api.TriggerLocation{Lat: &v, Lon: &v, RadiusKm: &v}); err != nil || got == nil {
		t.Errorf("a whole location: %v %v", got, err)
	}
	if got, err := locationFromAPI(nil); err != nil || got != nil {
		t.Errorf("no location: %v %v", got, err)
	}
}

func f64(v float64) *float64 { return &v }
