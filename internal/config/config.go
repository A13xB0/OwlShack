package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/meshcore-go/meshcore-go/node"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

type ChannelRef struct {
	Name       string `json:"name" yaml:"name" toml:"name"`
	PrivateKey string `json:"privateKey,omitempty" yaml:"privateKey,omitempty" toml:"privateKey,omitempty"`
}

func (cr *ChannelRef) UnmarshalText(text []byte) error {
	cr.Name = string(text)
	return nil
}

type ChannelList []ChannelRef

func (cl *ChannelList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("channels must be an array: %w", err)
	}

	result := make(ChannelList, 0, len(raw))
	for _, item := range raw {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			result = append(result, ChannelRef{Name: s})
			continue
		}
		// ChannelRef's TextUnmarshaler (for TOML strings) makes encoding/json reject the object form.
		var ref struct {
			Name       string `json:"name"`
			PrivateKey string `json:"privateKey"`
		}
		if err := json.Unmarshal(item, &ref); err != nil {
			return fmt.Errorf("channel entry must be a string or {name, privateKey} object: %w", err)
		}
		result = append(result, ChannelRef{Name: ref.Name, PrivateKey: ref.PrivateKey})
	}
	*cl = result
	return nil
}

func (cl *ChannelList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("channels must be a sequence")
	}

	result := make(ChannelList, 0, len(value.Content))
	for _, node := range value.Content {
		switch node.Kind {
		case yaml.ScalarNode:
			result = append(result, ChannelRef{Name: node.Value})
		case yaml.MappingNode:
			var ref ChannelRef
			if err := node.Decode(&ref); err != nil {
				return fmt.Errorf("channel entry decode error: %w", err)
			}
			result = append(result, ref)
		default:
			return fmt.Errorf("channel entry must be a string or mapping")
		}
	}
	*cl = result
	return nil
}

type Config struct {
	LogLevel *string `json:"logLevel" yaml:"logLevel" toml:"logLevel"`

	// ConnectionType labels the backend for the UI: "kiss", "openhop" or "spi". It is derived from
	// Connection on every write, never read to choose a driver.
	ConnectionType *string `json:"connectionType,omitempty" yaml:"connectionType,omitempty" toml:"connectionType,omitempty"`

	// SPIBoard names the hat when Connection is spi://, selected from modem.Boards() rather than by pin.
	SPIBoard *string `json:"spiBoard,omitempty" yaml:"spiBoard,omitempty" toml:"spiBoard,omitempty"`

	// Connection Settings (KISS modem)
	Connection *string `json:"connection" yaml:"connection" toml:"connection"` // serial://<path> or tcp://<host:port>
	BaudRate   *int    `json:"baudRate" yaml:"baudRate" toml:"baudRate"`       // Default 115200 if using serial

	Freq *float64 `json:"freq" yaml:"freq" toml:"freq"` // e.g. 917.375
	Bw   *float64 `json:"bw" yaml:"bw" toml:"bw"`       // e.g. 62.50
	SF   *uint8   `json:"sf" yaml:"sf" toml:"sf"`       // e.g. 7
	CR   *uint8   `json:"cr" yaml:"cr" toml:"cr"`       // e.g. 8
	TX   *uint8   `json:"tx" yaml:"tx" toml:"tx"`       // TX Power e.g. 22

	// Default per-hop path hash width in BYTES for floods we originate; nil == 1. The firmware's `path.hash.mode` is this minus one.
	PathHashSize *int `json:"pathHashSize,omitempty" yaml:"pathHashSize,omitempty" toml:"pathHashSize,omitempty"`

	// Web UI
	ListenAddr *string `json:"listenAddr" yaml:"listenAddr" toml:"listenAddr"`
	// MapProvider is the basemap, one of MapProviders; nil takes CARTO when MapTileKey is set, else OpenStreetMap.
	MapProvider *string `json:"mapProvider,omitempty" yaml:"mapProvider,omitempty" toml:"mapProvider,omitempty"`
	// MapDarkStyle is how OSM is recoloured in dark mode, one of MapDarkStyles; nil is original.
	MapDarkStyle *string `json:"mapDarkStyle,omitempty" yaml:"mapDarkStyle,omitempty" toml:"mapDarkStyle,omitempty"`
	// https://carto.com/basemaps/apikey/
	MapTileKey *string `json:"mapTileKey" yaml:"mapTileKey" toml:"mapTileKey"`

	// ModemToken authenticates this host to an openHop modem over the network. A password: it is
	// stored on its own rather than inside Connection, which config reads return in full.
	ModemToken *string `json:"modemToken,omitempty" yaml:"modemToken,omitempty" toml:"modemToken,omitempty"`

	// PERCENTAGE, as the firmware's `set dutycycle` takes it; the library's inverted airtime factor is derived in AirtimeFactorOr. nil = 50%.
	DutyCycle *float64 `json:"dutyCycle,omitempty" yaml:"dutyCycle,omitempty" toml:"dutyCycle,omitempty"`

	// nil/false until the first-run wizard finishes: tells "never configured" from "deliberately observer-only".
	SetupComplete *bool `json:"setupComplete,omitempty" yaml:"setupComplete,omitempty" toml:"setupComplete,omitempty"`

	// MQTT observer. Exactly one node feeds MQTT (Mqtt.Node selects it).
	Mqtt *MqttConfig `json:"mqtt,omitempty" yaml:"mqtt,omitempty" toml:"mqtt,omitempty"`

	Companions []CompanionConfig `json:"companions" yaml:"companions" toml:"companion"`

	// At most one: two repeaters sharing one radio would just relay each other. nil == no repeater.
	Repeater *RepeaterConfig `json:"repeater,omitempty" yaml:"repeater,omitempty" toml:"repeater,omitempty"`

	// Legacy pre-relational aliases; migrateLegacyFormat folds and clears them (legacy.go).
	NodeType  *string          `json:"nodeType,omitempty" yaml:"nodeType,omitempty" toml:"nodeType,omitempty"`
	Bots      []BotConfig      `json:"bots,omitempty" yaml:"bots,omitempty" toml:"bot,omitempty"`
	Observers []legacyObserver `json:"observers,omitempty" yaml:"observers,omitempty" toml:"observer,omitempty"`
}

func DefaultConfig() Config {
	connection := "serial:///dev/ttyACM0"
	baudRate := 115200
	freq := 917.375
	bw := 62.50
	sf := uint8(7)
	cr := uint8(8)
	tx := uint8(22)

	return Config{
		Connection: &connection,
		BaudRate:   &baudRate,
		Freq:       &freq,
		Bw:         &bw,
		SF:         &sf,
		CR:         &cr,
		TX:         &tx,
	}
}

const (
	MapProviderOSM   = "osm"
	MapProviderCarto = "carto"
)

var MapProviders = []string{MapProviderOSM, MapProviderCarto}

const (
	MapDarkOriginal   = "original"
	MapDarkSimplified = "simplified"
)

var MapDarkStyles = []string{MapDarkOriginal, MapDarkSimplified}

func (c *Config) MapDarkStyleOr() string {
	if c.MapDarkStyle != nil {
		return *c.MapDarkStyle
	}
	return MapDarkOriginal
}

// MapProviderOr is the basemap to store, choosing for a config file that predates the field as migration 019 does.
func (c *Config) MapProviderOr() string {
	switch {
	case c.MapProvider != nil:
		return *c.MapProvider
	case c.MapTileKey != nil && *c.MapTileKey != "":
		return MapProviderCarto
	}
	return MapProviderOSM
}

// PublicChannelName is the well-known public channel every companion joins.
const PublicChannelName = "Public"

// ensureTriggerChannels copies trigger-referenced channels (and their keys) into the companion's list, which owns them.
func ensureTriggerChannels(comp *CompanionConfig) {
	if comp.Triggers == nil {
		return
	}
	// Channel names are case-sensitive: the key is SHA256 of the exact "#name".
	have := map[string]bool{}
	if comp.Channels != nil {
		for _, ch := range *comp.Channels {
			have[ch.Name] = true
		}
	}
	for _, t := range *comp.Triggers {
		if t.Channels == nil {
			continue
		}
		for _, ref := range *t.Channels {
			if strings.TrimSpace(ref.Name) == "" || have[ref.Name] {
				continue
			}
			have[ref.Name] = true
			if comp.Channels == nil {
				comp.Channels = &ChannelList{}
			}
			*comp.Channels = append(*comp.Channels, ChannelRef{Name: ref.Name, PrivateKey: ref.PrivateKey})
		}
	}
}

// ensurePublicChannel guarantees a companion is a member of the public channel.
func ensurePublicChannel(comp *CompanionConfig) {
	if comp.Channels == nil {
		comp.Channels = &ChannelList{{Name: PublicChannelName}}
		return
	}
	for _, ch := range *comp.Channels {
		if ch.Name == PublicChannelName {
			return
		}
	}
	*comp.Channels = append(ChannelList{{Name: PublicChannelName}}, *comp.Channels...)
}

func (c *Config) ApplyDefaults() {
	// Fold legacy keys first so the folded companions/mqtt go through the normalization below.
	c.migrateLegacyFormat()

	defaults := DefaultConfig()
	// Non-nil so JSON serialises companions as [] not null; the TS contract expects an array.
	if c.Companions == nil {
		c.Companions = []CompanionConfig{}
	}
	if c.ConnectionType == nil || *c.ConnectionType == "" {
		kiss := "kiss"
		c.ConnectionType = &kiss
	}
	// Pin nil to false so the value is representable in the relational schema.
	if c.SetupComplete == nil {
		f := false
		c.SetupComplete = &f
	}
	if c.Connection == nil {
		c.Connection = defaults.Connection
	}
	if c.BaudRate == nil {
		c.BaudRate = defaults.BaudRate
	}
	if c.Freq == nil {
		c.Freq = defaults.Freq
	}
	if c.Bw == nil {
		c.Bw = defaults.Bw
	}
	if c.SF == nil {
		c.SF = defaults.SF
	}
	if c.CR == nil {
		c.CR = defaults.CR
	}
	if c.TX == nil {
		c.TX = defaults.TX
	}

	// Hoist legacy per-companion mqtt: the first wins and its companion becomes the selected node.
	for i := range c.Companions {
		comp := &c.Companions[i]
		ensureTriggerChannels(comp)
		ensurePublicChannel(comp)
		if comp.Mqtt == nil {
			continue
		}
		if c.Mqtt == nil {
			hoisted := *comp.Mqtt
			name := comp.Name
			hoisted.Node = &name
			c.Mqtt = &hoisted
		}
		comp.Mqtt = nil
	}

	if c.Mqtt != nil {
		for i := range c.Mqtt.Brokers {
			c.Mqtt.Brokers[i].migrateTopicPrefix()
		}
	}
}

func unmarshalConfig(data []byte, fn func([]byte, any) error) (*Config, error) {
	var cfg Config
	if err := fn(data, &cfg); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return &cfg, nil
}

func UnmarshalConfigJson(data []byte) (*Config, error) { return unmarshalConfig(data, json.Unmarshal) }
func UnmarshalConfigYaml(data []byte) (*Config, error) { return unmarshalConfig(data, yaml.Unmarshal) }
func UnmarshalConfigToml(data []byte) (*Config, error) { return unmarshalConfig(data, toml.Unmarshal) }

// AirtimeFactorOr mirrors the firmware's `airtime_factor = (100/dc) - 1` (CommonCLI.cpp handleSetCmd "dutycycle").
func (c *Config) AirtimeFactorOr() float64 {
	if c.DutyCycle == nil || *c.DutyCycle <= 0 {
		return node.DefaultAirtimeFactor
	}
	return (100.0 / *c.DutyCycle) - 1.0
}

// DutyCyclePercentOr is the effective percentage, for display and logging.
func (c *Config) DutyCyclePercentOr() float64 {
	return 100.0 / (c.AirtimeFactorOr() + 1.0)
}

// PathHashSizeOr resolves the global default flood path hash width in bytes.
func (c *Config) PathHashSizeOr() int {
	if c.PathHashSize == nil {
		return DefaultPathHashSize
	}
	return *c.PathHashSize
}
