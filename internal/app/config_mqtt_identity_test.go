package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/store"
)

// Only the node companion's block carries the origin, it is the repeater's, and one that came in with the config is dropped.
func TestEffectiveCompanionConfigs_MqttOrigin(t *testing.T) {
	broker := []config.BrokerConfig{{Name: "map", Enabled: true, Host: "mqtt.example", Port: 1883}}
	node := "feeder"
	cfg := &config.Config{
		Companions: []config.CompanionConfig{{Name: "other"}, {Name: "feeder"}},
		Repeater:   &config.RepeaterConfig{Name: "Cadham Village", PrivateKey: strings.Repeat("22", 32)},
		Mqtt: &config.MqttConfig{Node: &node, Identity: config.MqttIdentityRepeater, Brokers: broker,
			Origin: &config.MqttOrigin{Name: "planted", PrivateKey: strings.Repeat("33", 32)}},
	}

	blocks := effectiveCompanionConfigs(cfg)
	if blocks[0].Mqtt != nil {
		t.Error("a companion that is not the node got the mqtt block")
	}
	o := blocks[1].Mqtt.Origin
	if o == nil || o.Name != "Cadham Village" || o.PrivateKey != cfg.Repeater.PrivateKey {
		t.Fatalf("node origin = %+v, want the repeater's name and key", o)
	}

	cfg.Mqtt.Identity = ""
	if o := effectiveCompanionConfigs(cfg)[1].Mqtt.Origin; o != nil {
		t.Errorf("publishing as the companion kept origin %+v", o)
	}
	// Validate refuses this; a database edited by hand still feeds, as the companion.
	cfg.Mqtt.Identity, cfg.Repeater = config.MqttIdentityRepeater, nil
	if o := effectiveCompanionConfigs(cfg)[1].Mqtt.Origin; o != nil {
		t.Errorf("with no repeater the origin is %+v, want none", o)
	}
	if cfg.Mqtt.Origin == nil || cfg.Mqtt.Origin.Name != "planted" {
		t.Error("effectiveCompanionConfigs mutated the source mqtt block")
	}
}

func TestMqttIdentity_SavedKeptAndReset(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{
		Companions: []config.CompanionConfig{{Name: "home", PrivateKey: strings.Repeat("11", 32)}},
		Repeater:   &config.RepeaterConfig{Name: "rp", PrivateKey: strings.Repeat("22", 32)},
		Mqtt:       &config.MqttConfig{Identity: config.MqttIdentityRepeater},
	}
	if err := persistToTables(ctx, db, cfg); err != nil {
		t.Fatal(err)
	}
	b := &backend{db: db}
	identity := func() string {
		t.Helper()
		got, err := readConfigFromTables(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if got.Mqtt == nil {
			return ""
		}
		return got.Mqtt.Identity
	}
	str := func(s string) *string { return &s }

	if got := identity(); got != config.MqttIdentityRepeater {
		t.Fatalf("imported identity read back as %q, want repeater", got)
	}
	if err := b.SaveMqtt(ctx, api.MqttInput{IataCode: str("EDI")}); err != nil {
		t.Fatal(err)
	}
	if got := identity(); got != config.MqttIdentityRepeater {
		t.Errorf("after a save without identity it is %q, want repeater kept", got)
	}
	if err := b.SaveMqtt(ctx, api.MqttInput{Identity: str("companion")}); err != nil {
		t.Fatal(err)
	}
	if got := identity(); got != "" {
		t.Errorf("after choosing companion it is %q, want the default", got)
	}
	if err := b.SaveMqtt(ctx, api.MqttInput{Identity: str("observer")}); err == nil {
		t.Error(`identity "observer" was saved`)
	}
	if err := b.SaveMqtt(ctx, api.MqttInput{Identity: str("repeater")}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteRepeater(ctx); err != nil {
		t.Fatalf("deleting the repeater the feed is published as: %v", err)
	}
	if got := identity(); got != "" {
		t.Errorf("after deleting the repeater the identity is %q, want companion", got)
	}
}
