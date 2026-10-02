package config

import "testing"

func TestValidate_MqttIdentity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		identity string
		repeater bool
		ok       bool
	}{
		{"", false, true},
		{"companion", false, true},
		{"repeater", true, true},
		// Publishing as a repeater that does not exist would sign the feed with nothing.
		{"repeater", false, false},
		{"Repeater", true, false},
		{"observer", true, false},
	} {
		cfg := Config{Companions: []CompanionConfig{{Name: "home"}}, Mqtt: &MqttConfig{Identity: tt.identity}}
		if tt.repeater {
			cfg.Repeater = &RepeaterConfig{Name: "rp"}
		}
		if err := cfg.Validate(); (err == nil) != tt.ok {
			t.Errorf("identity %q, repeater %v: Validate() = %v, want ok=%v", tt.identity, tt.repeater, err, tt.ok)
		}
	}
}
