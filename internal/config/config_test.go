package config

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// validSeedHex is a known-good 64-hex (32-byte) ed25519 seed used across tests.
const validSeedHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func strPtr(s string) *string { return &s }

// validConfig returns a minimal known-good *Config; tests mutate one field per case and re-validate.
func validConfig(t *testing.T) *Config {
	t.Helper()
	conn := "serial:///dev/ttyACM0"
	return &Config{
		Connection: &conn,
		Companions: []CompanionConfig{
			{
				Name:       "alpha",
				PrivateKey: validSeedHex,
				Channels:   &ChannelList{{Name: "Public"}},
				Triggers: &[]TriggerConfig{
					{
						Type:     "group",
						Template: "hello {{.Sender}}",
						Channels: &ChannelList{{Name: "Public"}},
					},
				},
			},
		},
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	// Sanity: the baseline must pass, else every negative case below is meaningless.
	t.Run("valid config passes", func(t *testing.T) {
		t.Parallel()
		if err := validConfig(t).Validate(); err != nil {
			t.Fatalf("validConfig().Validate() = %v, want nil", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{
			name:    "unknown map provider",
			mutate:  func(c *Config) { c.MapProvider = strPtr("google") },
			wantErr: true,
		},
		{
			name:    "empty map provider",
			mutate:  func(c *Config) { c.MapProvider = strPtr("") },
			wantErr: true,
		},
		{
			name:    "unknown map dark style",
			mutate:  func(c *Config) { c.MapDarkStyle = strPtr("inverted") },
			wantErr: true,
		},
		{
			name:   "simplified map dark style",
			mutate: func(c *Config) { c.MapDarkStyle = strPtr(MapDarkSimplified) },
		},
		{
			name:   "carto map provider",
			mutate: func(c *Config) { c.MapProvider = strPtr(MapProviderCarto) },
		},
		{
			name: "duplicate companion names",
			mutate: func(c *Config) {
				c.Companions = append(c.Companions, CompanionConfig{Name: "alpha"})
			},
			wantErr: true,
		},
		{
			name: "empty companion name",
			mutate: func(c *Config) {
				c.Companions[0].Name = ""
			},
			wantErr: true,
		},
		{
			name: "duplicate companion private keys",
			mutate: func(c *Config) {
				c.Companions = append(c.Companions, CompanionConfig{
					Name:       "beta",
					PrivateKey: validSeedHex,
				})
			},
			wantErr: true,
		},
		{
			name: "non-hex companion private key",
			mutate: func(c *Config) {
				c.Companions[0].PrivateKey = "zzzz"
			},
			wantErr: true,
		},
		{
			name: "wrong-length companion private key",
			mutate: func(c *Config) {
				c.Companions[0].PrivateKey = "0102030405" // valid hex, too short
			},
			wantErr: true,
		},
		{
			name: "trigger with invalid type",
			mutate: func(c *Config) {
				(*c.Companions[0].Triggers)[0].Type = "bogus"
			},
			wantErr: true,
		},
		{
			name: "trigger with unparseable template",
			mutate: func(c *Config) {
				(*c.Companions[0].Triggers)[0].Template = "{{ .Sender " // unterminated action
			},
			wantErr: true,
		},
		{
			name: "trigger with bad regex match",
			mutate: func(c *Config) {
				bad := []string{"("}
				(*c.Companions[0].Triggers)[0].Match = &bad
			},
			wantErr: true,
		},
		{
			name: "cron trigger with bad schedule",
			mutate: func(c *Config) {
				(*c.Companions[0].Triggers)[0] = TriggerConfig{
					Type:     "cron",
					Schedule: "not a cron expr",
					Template: "tick",
				}
			},
			wantErr: true,
		},
		{
			name: "trigger channel with non-hex private key",
			mutate: func(c *Config) {
				(*(*c.Companions[0].Triggers)[0].Channels)[0].PrivateKey = "nothex!!"
			},
			wantErr: true,
		},
		{
			// A standalone channel key, referenced by no trigger, still fails companion construction.
			name: "standalone channel with non-hex private key",
			mutate: func(c *Config) {
				(*c.Companions[0].Channels)[0].PrivateKey = "nothex!!"
			},
			wantErr: true,
		},
		{
			name: "mqtt node references missing companion",
			mutate: func(c *Config) {
				c.Mqtt = &MqttConfig{Node: strPtr("ghost")}
			},
			wantErr: true,
		},
		{
			name: "mqtt node references existing companion",
			mutate: func(c *Config) {
				c.Mqtt = &MqttConfig{Node: strPtr("alpha")}
			},
			wantErr: false,
		},
		{
			name: "broker with missing host",
			mutate: func(c *Config) {
				c.Mqtt = &MqttConfig{
					Brokers: []BrokerConfig{{Name: "b", Port: 1883}},
				}
			},
			wantErr: true,
		},
		{
			name: "broker with out-of-range port",
			mutate: func(c *Config) {
				c.Mqtt = &MqttConfig{
					Brokers: []BrokerConfig{{Name: "b", Host: "h", Port: 99999}},
				}
			},
			wantErr: true,
		},
		{
			name: "invalid connection string",
			mutate: func(c *Config) {
				c.Connection = strPtr("http://nope")
			},
			wantErr: true,
		},
		{
			name: "freq out of range",
			mutate: func(c *Config) {
				f := 50.0
				c.Freq = &f
			},
			wantErr: true,
		},
		{
			name: "sf out of range",
			mutate: func(c *Config) {
				sf := uint8(99)
				c.SF = &sf
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validConfig(t)
			tt.mutate(c)
			err := c.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestTriggerConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trig    TriggerConfig
		wantErr bool
	}{
		{
			name: "valid group trigger",
			trig: TriggerConfig{
				Type:     "group",
				Template: "hi {{.Sender}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name: "valid channel trigger",
			trig: TriggerConfig{
				Type:     "channel",
				Template: "hi",
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name: "valid cron trigger",
			trig: TriggerConfig{
				Type:     "cron",
				Schedule: "*/5 * * * *",
				Template: "tick",
			},
		},
		{
			name: "valid rss trigger to a channel",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name: "valid cap trigger to a contact only",
			trig: TriggerConfig{
				Type:     "cap",
				URL:      "https://example.com/cap.atom",
				Template: "{{.Headline}}",
				Contacts: &[]string{"aabbccdd"},
			},
		},
		{
			name: "feed trigger with no url",
			trig: TriggerConfig{
				Type:     "rss",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "feed trigger with a non-http url",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "file:///etc/passwd",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "feed trigger with nowhere to deliver",
			trig: TriggerConfig{
				Type:     "cap",
				URL:      "https://example.com/cap.atom",
				Template: "{{.Headline}}",
			},
			wantErr: true,
		},
		{
			name: "feed trigger polling below the minimum",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Schedule: "@every 30s",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "feed trigger polling at the minimum",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Schedule: "@every 1m",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name: "feed trigger on a cron spec, whose finest step is already a minute",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Schedule: "*/15 * * * *",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name: "feed trigger with a field-scoped pattern",
			trig: TriggerConfig{
				Type:     "cap",
				URL:      "https://example.com/cap.atom",
				Template: "{{.Headline}}",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"severity:^(Extreme|Severe)$", "area:(?i)northland"},
			},
		},
		{
			name: "feed trigger with an unscoped pattern",
			trig: TriggerConfig{
				Type:     "cap",
				URL:      "https://example.com/cap.atom",
				Template: "{{.Headline}}",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"(?i)tsunami"},
			},
			wantErr: true,
		},
		{
			name: "feed trigger naming a field that does not exist",
			trig: TriggerConfig{
				Type:     "cap",
				URL:      "https://example.com/cap.atom",
				Template: "{{.Headline}}",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"severty:Extreme"},
			},
			wantErr: true,
		},
		{
			name: "feed trigger naming the other type's field",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"severity:Extreme"},
			},
			wantErr: true,
		},
		{
			name: "feed trigger with a bad regex on a good field",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"title:[unclosed"},
			},
			wantErr: true,
		},
		{
			name: "feed trigger with a bad schedule",
			trig: TriggerConfig{
				Type:     "rss",
				URL:      "https://example.com/feed.xml",
				Schedule: "not a cron",
				Template: "{{.Title}}",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "template using formatPathBytes stub func parses",
			trig: TriggerConfig{
				Type:     "group",
				Template: `{{ formatPathBytes .Path }}`,
				Channels: &ChannelList{{Name: "Public"}},
			},
		},
		{
			name:    "unknown type",
			trig:    TriggerConfig{Type: "private", Template: "x", Channels: &ChannelList{{Name: "Public"}}},
			wantErr: true,
		},
		{
			name: "group trigger without channels",
			trig: TriggerConfig{
				Type:     "group",
				Template: "hi",
			},
			wantErr: true,
		},
		{
			name: "cron trigger without schedule",
			trig: TriggerConfig{
				Type:     "cron",
				Template: "tick",
			},
			wantErr: true,
		},
		{
			name: "cron trigger with bad schedule",
			trig: TriggerConfig{
				Type:     "cron",
				Schedule: "@bogus",
				Template: "tick",
			},
			wantErr: true,
		},
		{
			name: "missing template",
			trig: TriggerConfig{
				Type:     "group",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "bad template",
			trig: TriggerConfig{
				Type:     "group",
				Template: "{{ .Sender ",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "unknown template function",
			trig: TriggerConfig{
				Type:     "group",
				Template: "{{ notAFunc .X }}",
				Channels: &ChannelList{{Name: "Public"}},
			},
			wantErr: true,
		},
		{
			name: "bad match regex",
			trig: TriggerConfig{
				Type:     "group",
				Template: "hi",
				Channels: &ChannelList{{Name: "Public"}},
				Match:    &[]string{"["},
			},
			wantErr: true,
		},
		{
			name: "pathHashSize too large",
			trig: TriggerConfig{
				Type:         "group",
				Template:     "hi",
				Channels:     &ChannelList{{Name: "Public"}},
				PathHashSize: func() *uint8 { v := uint8(5); return &v }(),
			},
			wantErr: true,
		},
		{
			name: "channel ref with bad hex key",
			trig: TriggerConfig{
				Type:     "group",
				Template: "hi",
				Channels: &ChannelList{{Name: "Public", PrivateKey: "xyz"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.trig.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestValidateRegionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		region  string
		wantErr bool
	}{
		{"wildcard is exempt", "*", false},
		{"named ok", "alpha", false},
		{"empty invalid", "", true},
		{"bad char invalid", "a b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRegionName(tt.region)
			if tt.wantErr && err == nil {
				t.Errorf("validateRegionName(%q) = nil, want error", tt.region)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateRegionName(%q) = %v, want nil", tt.region, err)
			}
		})
	}
}

func TestChannelRef_Validate(t *testing.T) {
	t.Parallel()

	const secret16 = "00112233445566778899aabbccddeeff" // channel PSKs are 16 bytes (NewChannelFromPSK)

	tests := []struct {
		name    string
		ref     ChannelRef
		wantErr bool
	}{
		{
			name: "name only",
			ref:  ChannelRef{Name: "Public"},
		},
		{
			name: "valid 16-byte hex secret",
			ref:  ChannelRef{Name: "secret", PrivateKey: secret16},
		},
		{
			name:    "empty name",
			ref:     ChannelRef{Name: ""},
			wantErr: true,
		},
		{
			name:    "non-hex secret",
			ref:     ChannelRef{Name: "c", PrivateKey: "nothex!!"},
			wantErr: true,
		},
		{
			name:    "odd-length hex secret",
			ref:     ChannelRef{Name: "c", PrivateKey: "abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.ref.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestBrokerConfig_Validate(t *testing.T) {
	t.Parallel()

	base := func() BrokerConfig {
		return BrokerConfig{Name: "b", Host: "broker.example", Port: 1883}
	}

	tests := []struct {
		name    string
		mutate  func(b *BrokerConfig)
		wantErr bool
	}{
		{name: "valid tcp", mutate: func(b *BrokerConfig) { b.Transport = "tcp" }},
		{name: "valid websockets", mutate: func(b *BrokerConfig) { b.Transport = "websockets" }},
		{name: "empty transport ok", mutate: func(b *BrokerConfig) { b.Transport = "" }},
		{name: "invalid transport", mutate: func(b *BrokerConfig) { b.Transport = "udp" }, wantErr: true},
		{name: "ws not yet normalized rejected", mutate: func(b *BrokerConfig) { b.Transport = "ws" }, wantErr: true},
		{name: "missing name", mutate: func(b *BrokerConfig) { b.Name = "" }, wantErr: true},
		{name: "missing host", mutate: func(b *BrokerConfig) { b.Host = "" }, wantErr: true},
		{name: "zero port", mutate: func(b *BrokerConfig) { b.Port = 0 }, wantErr: true},
		{name: "port too high", mutate: func(b *BrokerConfig) { b.Port = 70000 }, wantErr: true},
		{name: "valid auth token", mutate: func(b *BrokerConfig) { b.AuthType = "token" }},
		{name: "valid auth basic", mutate: func(b *BrokerConfig) { b.AuthType = "basic" }},
		{name: "invalid auth type", mutate: func(b *BrokerConfig) { b.AuthType = "oauth" }, wantErr: true},
		{
			name:   "valid packet topic placeholders",
			mutate: func(b *BrokerConfig) { b.PacketTopic = "meshcore/{iata}/{pubkey}/{name}" },
		},
		{
			name:    "unknown placeholder",
			mutate:  func(b *BrokerConfig) { b.PacketTopic = "meshcore/{bogus}" },
			wantErr: true,
		},
		{
			name:    "packet topic with plus wildcard",
			mutate:  func(b *BrokerConfig) { b.PacketTopic = "meshcore/+/x" },
			wantErr: true,
		},
		{
			name:    "status topic with hash wildcard",
			mutate:  func(b *BrokerConfig) { b.StatusTopic = "meshcore/#" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := base()
			tt.mutate(&b)
			err := b.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestBrokerConfig_migrateTopicPrefix(t *testing.T) {
	t.Parallel()

	t.Run("folds prefix into both topics", func(t *testing.T) {
		t.Parallel()
		b := BrokerConfig{TopicPrefix: "myfeed"}
		b.migrateTopicPrefix()

		if got, want := b.PacketTopic, "myfeed/{iata}/{pubkey}/packets"; got != want {
			t.Errorf("PacketTopic = %q, want %q", got, want)
		}
		if got, want := b.StatusTopic, "myfeed/{iata}/{pubkey}/status"; got != want {
			t.Errorf("StatusTopic = %q, want %q", got, want)
		}
		if b.TopicPrefix != "" {
			t.Errorf("TopicPrefix = %q, want cleared", b.TopicPrefix)
		}
	})

	t.Run("no prefix is a no-op", func(t *testing.T) {
		t.Parallel()
		b := BrokerConfig{}
		b.migrateTopicPrefix()
		if b.PacketTopic != "" || b.StatusTopic != "" {
			t.Errorf("topics set from empty prefix: packet=%q status=%q", b.PacketTopic, b.StatusTopic)
		}
	})

	t.Run("does not clobber explicit topics", func(t *testing.T) {
		t.Parallel()
		b := BrokerConfig{
			TopicPrefix: "myfeed",
			PacketTopic: "explicit/packets",
		}
		b.migrateTopicPrefix()
		if got, want := b.PacketTopic, "explicit/packets"; got != want {
			t.Errorf("PacketTopic = %q, want preserved %q", got, want)
		}
		// StatusTopic was empty, so it should be derived from the prefix.
		if got, want := b.StatusTopic, "myfeed/{iata}/{pubkey}/status"; got != want {
			t.Errorf("StatusTopic = %q, want %q", got, want)
		}
	})
}

func TestApplyDefaults_migrateLegacyFormat(t *testing.T) {
	t.Parallel()

	t.Run("nodeType folds into connectionType", func(t *testing.T) {
		t.Parallel()
		c := &Config{NodeType: strPtr("kiss")}
		c.ApplyDefaults()
		if c.NodeType != nil {
			t.Errorf("NodeType = %v, want cleared", c.NodeType)
		}
		if c.ConnectionType == nil || *c.ConnectionType != "kiss" {
			t.Errorf("ConnectionType = %v, want \"kiss\"", c.ConnectionType)
		}
	})

	t.Run("bot folds into companion with triggers", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Bots: []BotConfig{
				{
					Name: strPtr("botnode"),
					Triggers: []TriggerConfig{
						{Type: "group", Template: "hi", Channels: &ChannelList{{Name: "Public"}}},
					},
				},
			},
		}
		c.ApplyDefaults()
		if c.Bots != nil {
			t.Errorf("Bots = %v, want cleared", c.Bots)
		}
		if len(c.Companions) != 1 {
			t.Fatalf("len(Companions) = %d, want 1", len(c.Companions))
		}
		comp := c.Companions[0]
		if comp.Name != "botnode" {
			t.Errorf("companion name = %q, want \"botnode\"", comp.Name)
		}
		if comp.Triggers == nil || len(*comp.Triggers) != 1 {
			t.Fatalf("triggers not carried over: %v", comp.Triggers)
		}
	})

	t.Run("observer folds into top-level mqtt and ws normalizes", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Companions: []CompanionConfig{{Name: "feeder"}},
			Observers: []legacyObserver{
				{
					Name:     strPtr("feeder"),
					IataCode: strPtr("LAX"),
					Brokers: []BrokerConfig{
						{Name: "wsb", Host: "h", Port: 443, Transport: "ws"},
						{Name: "wssb", Host: "h2", Port: 8883, Transport: "wss"},
					},
				},
			},
		}
		c.ApplyDefaults()

		if c.Observers != nil {
			t.Errorf("Observers = %v, want cleared", c.Observers)
		}
		if c.Mqtt == nil {
			t.Fatal("Mqtt = nil, want hoisted from observer")
		}
		if c.Mqtt.Node == nil || *c.Mqtt.Node != "feeder" {
			t.Errorf("Mqtt.Node = %v, want \"feeder\"", c.Mqtt.Node)
		}
		if c.Mqtt.IataCode == nil || *c.Mqtt.IataCode != "LAX" {
			t.Errorf("Mqtt.IataCode = %v, want \"LAX\"", c.Mqtt.IataCode)
		}
		if len(c.Mqtt.Brokers) != 2 {
			t.Fatalf("len(Brokers) = %d, want 2", len(c.Mqtt.Brokers))
		}
		ws, wss := c.Mqtt.Brokers[0], c.Mqtt.Brokers[1]
		if ws.Transport != "websockets" {
			t.Errorf("ws broker transport = %q, want \"websockets\"", ws.Transport)
		}
		if ws.TlsEnabled {
			t.Errorf("ws broker TlsEnabled = true, want false")
		}
		if wss.Transport != "websockets" {
			t.Errorf("wss broker transport = %q, want \"websockets\"", wss.Transport)
		}
		if !wss.TlsEnabled {
			t.Errorf("wss broker TlsEnabled = false, want true")
		}
	})

	t.Run("bot and observer sharing a name become one companion", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Bots: []BotConfig{
				{Name: strPtr("shared")},
			},
			Observers: []legacyObserver{
				{
					Name:    strPtr("shared"),
					Brokers: []BrokerConfig{{Name: "b", Host: "h", Port: 1883}},
				},
			},
		}
		c.ApplyDefaults()

		count := 0
		for _, comp := range c.Companions {
			if comp.Name == "shared" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("companions named \"shared\" = %d, want 1 (bot+observer merge)", count)
		}
		if c.Mqtt == nil || c.Mqtt.Node == nil || *c.Mqtt.Node != "shared" {
			t.Errorf("Mqtt.Node = %v, want \"shared\"", c.Mqtt)
		}
	})

	t.Run("defaults applied and Public channel ensured", func(t *testing.T) {
		t.Parallel()
		c := &Config{Companions: []CompanionConfig{{Name: "x"}}}
		c.ApplyDefaults()

		if c.ConnectionType == nil || *c.ConnectionType != "kiss" {
			t.Errorf("ConnectionType = %v, want \"kiss\"", c.ConnectionType)
		}
		if c.Connection == nil {
			t.Error("Connection = nil, want default")
		}
		if c.SetupComplete == nil || *c.SetupComplete {
			t.Errorf("SetupComplete = %v, want false", c.SetupComplete)
		}
		comp := c.Companions[0]
		if comp.Channels == nil {
			t.Fatal("Channels = nil, want Public ensured")
		}
		found := false
		for _, ch := range *comp.Channels {
			if ch.Name == PublicChannelName {
				found = true
			}
		}
		if !found {
			t.Errorf("Public channel not ensured: %v", *comp.Channels)
		}
	})

	t.Run("legacy per-companion mqtt hoisted to top level", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Companions: []CompanionConfig{
				{
					Name: "feeder",
					Mqtt: &MqttConfig{IataCode: strPtr("SFO")},
				},
			},
		}
		c.ApplyDefaults()
		if c.Companions[0].Mqtt != nil {
			t.Error("per-companion Mqtt not cleared")
		}
		if c.Mqtt == nil || c.Mqtt.Node == nil || *c.Mqtt.Node != "feeder" {
			t.Errorf("Mqtt.Node = %v, want \"feeder\"", c.Mqtt)
		}
		if c.Mqtt.IataCode == nil || *c.Mqtt.IataCode != "SFO" {
			t.Errorf("Mqtt.IataCode = %v, want \"SFO\"", c.Mqtt.IataCode)
		}
	})
}

func TestConfigRoundTrip(t *testing.T) {
	t.Parallel()

	const chanSecret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

	build := func() *Config {
		conn := "serial:///dev/ttyACM0"
		return &Config{
			Connection: &conn,
			Companions: []CompanionConfig{
				{
					Name:       "alpha",
					PrivateKey: validSeedHex,
					Channels: &ChannelList{
						{Name: "Public"},
						{Name: "secret", PrivateKey: chanSecret},
					},
				},
			},
		}
	}

	// assertChannels verifies the channel name+secret survived a round-trip.
	assertChannels := func(t *testing.T, got *Config) {
		t.Helper()
		if len(got.Companions) != 1 {
			t.Fatalf("len(Companions) = %d, want 1", len(got.Companions))
		}
		chans := got.Companions[0].Channels
		if chans == nil {
			t.Fatal("Channels = nil after round-trip")
		}
		// ApplyDefaults pins Public at the front; the secret channel must survive with its key.
		var secret *ChannelRef
		for i := range *chans {
			if (*chans)[i].Name == "secret" {
				secret = &(*chans)[i]
			}
		}
		if secret == nil {
			t.Fatalf("secret channel lost: %+v", *chans)
		}
		if secret.PrivateKey != chanSecret {
			t.Errorf("secret channel key = %q, want %q", secret.PrivateKey, chanSecret)
		}
	}

	t.Run("toml", func(t *testing.T) {
		t.Parallel()
		data, err := toml.Marshal(build())
		if err != nil {
			t.Fatalf("toml.Marshal: %v", err)
		}
		got, err := UnmarshalConfigToml(data)
		if err != nil {
			t.Fatalf("UnmarshalConfigToml: %v", err)
		}
		assertChannels(t, got)
	})

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		data, err := json.Marshal(build())
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		got, err := UnmarshalConfigJson(data)
		if err != nil {
			t.Fatalf("UnmarshalConfigJson: %v", err)
		}
		assertChannels(t, got)
	})
}

func TestKeys(t *testing.T) {
	t.Parallel()

	t.Run("GenerateSeedHex produces 64-hex seed", func(t *testing.T) {
		t.Parallel()
		seed, err := GenerateSeedHex()
		if err != nil {
			t.Fatalf("GenerateSeedHex: %v", err)
		}
		if len(seed) != 64 {
			t.Errorf("len(seed) = %d, want 64", len(seed))
		}
		if _, err := hex.DecodeString(seed); err != nil {
			t.Errorf("seed not hex: %v", err)
		}
	})

	t.Run("GenerateSeedHex is non-deterministic", func(t *testing.T) {
		t.Parallel()
		a, _ := GenerateSeedHex()
		b, _ := GenerateSeedHex()
		if a == b {
			t.Errorf("two generated seeds identical: %q", a)
		}
	})

	t.Run("PubKeyHexFromSeed is deterministic and 64-hex", func(t *testing.T) {
		t.Parallel()
		pub1, err := PubKeyHexFromSeed(validSeedHex)
		if err != nil {
			t.Fatalf("PubKeyHexFromSeed: %v", err)
		}
		if len(pub1) != 64 {
			t.Errorf("len(pubkey) = %d, want 64", len(pub1))
		}
		if _, err := hex.DecodeString(pub1); err != nil {
			t.Errorf("pubkey not hex: %v", err)
		}
		pub2, _ := PubKeyHexFromSeed(validSeedHex)
		if pub1 != pub2 {
			t.Errorf("PubKeyHexFromSeed not deterministic: %q vs %q", pub1, pub2)
		}
	})

	t.Run("PubKeyHexFromSeed matches fixed known vector", func(t *testing.T) {
		t.Parallel()
		// A fixed seed -> pubkey pair, computed once with crypto/ed25519.
		const wantPub = "79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664"
		pub, err := PubKeyHexFromSeed(validSeedHex)
		if err != nil {
			t.Fatalf("PubKeyHexFromSeed: %v", err)
		}
		if pub != wantPub {
			t.Errorf("PubKeyHexFromSeed(validSeedHex) = %q, want %q", pub, wantPub)
		}
	})

	t.Run("PubKeyHexFromSeed empty seed returns empty", func(t *testing.T) {
		t.Parallel()
		pub, err := PubKeyHexFromSeed("")
		if err != nil {
			t.Fatalf("PubKeyHexFromSeed(\"\"): %v", err)
		}
		if pub != "" {
			t.Errorf("pubkey = %q, want empty", pub)
		}
	})

	t.Run("PubKeyHexFromSeed rejects bad seed", func(t *testing.T) {
		t.Parallel()
		if _, err := PubKeyHexFromSeed("notvalidhex"); err == nil {
			t.Error("PubKeyHexFromSeed(bad) = nil error, want error")
		}
		if _, err := PubKeyHexFromSeed("0102"); err == nil {
			t.Error("PubKeyHexFromSeed(short) = nil error, want error")
		}
	})
}

// TestRepeaterAdvertIntervalRange pins the firmware CommonCLI ranges: zero-hop 0 (off) or 60-240 minutes, flood 0 or 3-168 hours, stored in seconds.
func TestRepeaterAdvertIntervalRange(t *testing.T) {
	secs := func(v int) *int { return &v }
	cases := []struct {
		name          string
		advert, flood *int
		wantErr       bool
	}{
		{"nil = default", nil, nil, false},
		{"both off", secs(0), secs(0), false},
		{"at minimums", secs(60 * 60), secs(3 * 3600), false},
		{"at maximums", secs(240 * 60), secs(168 * 3600), false},
		{"advert 60 seconds", secs(60), nil, true},
		{"advert below minimum", secs(59 * 60), nil, true},
		{"advert above maximum", secs(241 * 60), nil, true},
		{"flood below minimum", nil, secs(2 * 3600), true},
		{"flood above maximum", nil, secs(169 * 3600), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{Repeater: &RepeaterConfig{
				Name: "rp", AdvertInterval: c.advert, FloodAdvertInterval: c.flood,
			}}
			if err := cfg.Validate(); (err != nil) != c.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// TestPathHashSizeRange: 1-3 bytes (the firmware's `path.hash.mode` takes this minus one), enforced globally and per node.
func TestPathHashSizeRange(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, c := range []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{"global unset", &Config{}, false},
		{"global 1", &Config{PathHashSize: n(1)}, false},
		{"global 3", &Config{PathHashSize: n(3)}, false},
		{"global 0", &Config{PathHashSize: n(0)}, true},
		{"global 4", &Config{PathHashSize: n(4)}, true},
		{"companion 2", &Config{Companions: []CompanionConfig{{Name: "a", PathHashSize: n(2)}}}, false},
		{"companion 5", &Config{Companions: []CompanionConfig{{Name: "a", PathHashSize: n(5)}}}, true},
		{"repeater 2", &Config{Repeater: &RepeaterConfig{Name: "rp", PathHashSize: n(2)}}, false},
		{"repeater 0", &Config{Repeater: &RepeaterConfig{Name: "rp", PathHashSize: n(0)}}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cfg.Validate(); (err != nil) != c.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// TestChannelRefKeyLength: a wrong-length hex key used to pass Validate and then fail companion construction on every start.
func TestChannelRefKeyLength(t *testing.T) {
	for _, c := range []struct {
		key     string
		wantErr bool
	}{
		{"", false},
		{"00112233445566778899aabbccddeeff", false},  // 16 bytes
		{"00112233445566778899aabbccddee", true},     // 15 bytes
		{"00112233445566778899aabbccddeeff00", true}, // 17 bytes
		{"zz112233445566778899aabbccddeeff", true},   // not hex
	} {
		err := (&ChannelRef{Name: "ch", PrivateKey: c.key}).Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("key %q: err = %v, wantErr %v", c.key, err, c.wantErr)
		}
	}
}

// The board's wiring has no safe default, so absence must be rejected rather than guessed.
func TestValidate_SPIConnectionRequiresBoard(t *testing.T) {
	board := "ultrapeaterzero-e22p"
	for _, tc := range []struct {
		name    string
		conn    string
		board   *string
		wantErr bool
	}{
		{"spi without board", "spi://SPI0.0", nil, true},
		{"spi with empty board", "spi://SPI0.0", strPtr(""), true},
		{"spi with board", "spi://SPI0.0", &board, false},
		{"serial needs no board", "serial:///dev/ttyACM0", nil, false},
		{"tcp needs no board", "tcp://10.0.0.5:5000", nil, false},
		{"unknown scheme", "usb://whatever", &board, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Connection: &tc.conn, SPIBoard: tc.board}
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error for %q", tc.conn)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for %q", err, tc.conn)
			}
		})
	}
}

func TestParseConnection_SPI(t *testing.T) {
	scheme, addr, ok := ParseConnection("spi://SPI0.0")
	if !ok || scheme != "spi" || addr != "SPI0.0" {
		t.Errorf("ParseConnection(spi://SPI0.0) = (%q, %q, %v), want (spi, SPI0.0, true)", scheme, addr, ok)
	}
}

func TestConfig_MapProviderOr(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		provider, key *string
		want          string
	}{
		{nil, nil, MapProviderOSM},
		{nil, strPtr(""), MapProviderOSM},
		{nil, strPtr("k"), MapProviderCarto},
		{strPtr(MapProviderOSM), strPtr("k"), MapProviderOSM},
	} {
		if got := (&Config{MapProvider: c.provider, MapTileKey: c.key}).MapProviderOr(); got != c.want {
			t.Errorf("provider %v key %v: got %q, want %q", c.provider, c.key, got, c.want)
		}
	}
}
