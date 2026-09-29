package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

var defaultConfigNames = []string{
	"config.toml",
	"config.yaml",
	"config.yml",
	"config.json",
}

// Load reads the config at path, or the first default-named file in the working directory when path is empty.
func Load(path string) (*Config, string, error) {
	if path == "" {
		return loadFromCwd()
	}
	return LoadFromPath(path)
}

func loadFromCwd() (*Config, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("getting working directory: %w", err)
	}

	for _, name := range defaultConfigNames {
		p := filepath.Join(cwd, name)
		if _, err := os.Stat(p); err == nil {
			slog.Info("using config", "path", p)
			return LoadFromPath(p)
		}
	}

	return nil, "", fmt.Errorf("no config file found in %s (tried %s)", cwd, strings.Join(defaultConfigNames, ", "))
}

// FindDefaultConfig returns the path of a default-named config file in the working directory, or "".
func FindDefaultConfig() string {
	for _, name := range defaultConfigNames {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

// LoadFromPath parses the config at path, selecting the decoder from its extension.
func LoadFromPath(path string) (*Config, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("reading config: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".toml":
		cfg, err := UnmarshalConfigToml(data)
		return cfg, path, err
	case ".yaml", ".yml":
		cfg, err := UnmarshalConfigYaml(data)
		return cfg, path, err
	case ".json":
		cfg, err := UnmarshalConfigJson(data)
		return cfg, path, err
	default:
		return nil, path, fmt.Errorf("unsupported config format %q", ext)
	}
}

// Marshal encodes cfg using the format implied by path's extension.
func Marshal(path string, cfg *Config) ([]byte, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".toml":
		return toml.Marshal(cfg)
	case ".yaml", ".yml":
		return yaml.Marshal(cfg)
	case ".json":
		return json.MarshalIndent(cfg, "", "  ")
	default:
		return nil, fmt.Errorf("unsupported config format %q", ext)
	}
}

// ParseConnection splits "serial://<addr>", "tcp://<addr>", "spi://<addr>" or "openhop://<addr>" into scheme and address.
func ParseConnection(conn string) (scheme, addr string, ok bool) {
	for _, prefix := range []string{"serial://", "tcp://", "spi://", "openhop://"} {
		if strings.HasPrefix(conn, prefix) {
			return strings.TrimSuffix(prefix, "://"), conn[len(prefix):], true
		}
	}
	return "", "", false
}

// Validate checks field ranges; nil pointers mean "use default" and are skipped.
func (c *Config) Validate() error {
	if c.Connection != nil {
		scheme, _, ok := ParseConnection(*c.Connection)
		if !ok {
			return fmt.Errorf("invalid connection string %q: must start with serial://, tcp://, spi:// or openhop://", *c.Connection)
		}
		// This process drives the radio, so the pins are unknown rather than defaultable.
		if scheme == "spi" && (c.SPIBoard == nil || *c.SPIBoard == "") {
			return fmt.Errorf("connection %q needs spiBoard set", *c.Connection)
		}
	}

	if c.BaudRate != nil && *c.BaudRate <= 0 {
		return fmt.Errorf("baudRate must be positive")
	}

	if c.Freq != nil && (*c.Freq < 100 || *c.Freq > 1000) {
		return fmt.Errorf("freq must be between 100 and 1000 MHz")
	}

	if c.Bw != nil && *c.Bw <= 0 {
		return fmt.Errorf("bw must be positive")
	}

	if c.SF != nil && (*c.SF < 5 || *c.SF > 12) {
		return fmt.Errorf("sf must be between 5 and 12")
	}

	if c.CR != nil && (*c.CR < 5 || *c.CR > 8) {
		return fmt.Errorf("cr must be between 5 and 8")
	}

	if c.TX != nil && *c.TX > 22 {
		return fmt.Errorf("tx must be between 0 and 22 dBm")
	}

	if v := c.PathHashSize; v != nil && (*v < MinPathHashSize || *v > MaxPathHashSize) {
		return fmt.Errorf("pathHashSize must be %d-%d bytes", MinPathHashSize, MaxPathHashSize)
	}

	// Zero companions is valid: a fresh install and an observer-only setup both have none.

	// A startCompanions failure after a reload exits the process, so anything that would fail companion construction must be rejected here.
	seen := make(map[string]bool, len(c.Companions))
	seenKeys := make(map[string]string, len(c.Companions))
	for i, comp := range c.Companions {
		if comp.Name == "" {
			return fmt.Errorf("companion[%d]: name is required", i)
		}
		// The store keys all persisted state (messages, contacts) by name.
		if seen[comp.Name] {
			return fmt.Errorf("duplicate companion name %q", comp.Name)
		}
		seen[comp.Name] = true
		if comp.PrivateKey != "" {
			if err := validateSeedHex(comp.PrivateKey); err != nil {
				return fmt.Errorf("companion %q: %w", comp.Name, err)
			}
			if other, dup := seenKeys[comp.PrivateKey]; dup {
				return fmt.Errorf("companions %q and %q share the same privateKey", other, comp.Name)
			}
			seenKeys[comp.PrivateKey] = comp.Name
		}
		if v := comp.PathHashSize; v != nil && (*v < MinPathHashSize || *v > MaxPathHashSize) {
			return fmt.Errorf("companion %q: pathHashSize must be %d-%d bytes", comp.Name, MinPathHashSize, MaxPathHashSize)
		}
		switch comp.DMPolicyOrDefault() {
		case DMPolicyContacts, DMPolicyAllowlist, DMPolicyAnyone:
		default:
			return fmt.Errorf("companion %q: dmPolicy must be %q, %q or %q", comp.Name, DMPolicyContacts, DMPolicyAllowlist, DMPolicyAnyone)
		}
		if comp.DMAllow != nil {
			for _, k := range *comp.DMAllow {
				if err := validateDMAllowKey(k); err != nil {
					return fmt.Errorf("companion %q dmAllow: %w", comp.Name, err)
				}
			}
		}
		if comp.Triggers != nil {
			for j, trig := range *comp.Triggers {
				if err := trig.Validate(); err != nil {
					return fmt.Errorf("companion %q trigger[%d]: %w", comp.Name, j, err)
				}
			}
		}
		// Standalone channels too: a bad key still fails companion construction.
		if comp.Channels != nil {
			for j, ch := range *comp.Channels {
				if err := ch.Validate(); err != nil {
					return fmt.Errorf("companion %q channel[%d] %q: %w", comp.Name, j, ch.Name, err)
				}
			}
		}
	}

	// The repeater's name shares the companion namespace, and it can't reuse a companion's private key.
	if r := c.Repeater; r != nil {
		if r.Name == "" {
			return fmt.Errorf("repeater: name is required")
		}
		if seen[r.Name] {
			return fmt.Errorf("repeater name %q clashes with a companion name", r.Name)
		}
		if r.PrivateKey != "" {
			if err := validateSeedHex(r.PrivateKey); err != nil {
				return fmt.Errorf("repeater %q: %w", r.Name, err)
			}
			if other, dup := seenKeys[r.PrivateKey]; dup {
				return fmt.Errorf("repeater %q and companion %q share the same privateKey", r.Name, other)
			}
		}
		if r.LoopDetect != nil && *r.LoopDetect != "" && !loopDetectLevels[*r.LoopDetect] {
			return fmt.Errorf("repeater %q: invalid loopDetect %q (want off|minimal|moderate|strict)", r.Name, *r.LoopDetect)
		}
		if r.FloodMax != nil && (*r.FloodMax < 1 || *r.FloodMax > 64) {
			return fmt.Errorf("repeater %q: floodMax must be between 1 and 64", r.Name)
		}
		if r.FloodMaxAdvert != nil && (*r.FloodMaxAdvert < 1 || *r.FloodMaxAdvert > 64) {
			return fmt.Errorf("repeater %q: floodMaxAdvert must be between 1 and 64", r.Name)
		}
		if r.FloodMaxUnscoped != nil && (*r.FloodMaxUnscoped < 0 || *r.FloodMaxUnscoped > 64) {
			return fmt.Errorf("repeater %q: floodMaxUnscoped must be between 0 and 64", r.Name)
		}
		if r.DefaultRegion != "" {
			if r.DefaultRegion == WildcardRegion {
				return fmt.Errorf(`repeater %q: defaultRegion cannot be "*"`, r.Name)
			}
			found := false
			for _, rg := range r.Regions {
				if rg.Name == r.DefaultRegion {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("repeater %q: defaultRegion %q is not a configured region", r.Name, r.DefaultRegion)
			}
		}
		if r.HomeRegion != "" {
			found := false
			for _, rg := range r.Regions {
				if rg.Name == r.HomeRegion {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("repeater %q: homeRegion %q is not a configured region", r.Name, r.HomeRegion)
			}
		}
		if v := r.PathHashSize; v != nil && (*v < MinPathHashSize || *v > MaxPathHashSize) {
			return fmt.Errorf("repeater %q: pathHashSize must be %d-%d bytes", r.Name, MinPathHashSize, MaxPathHashSize)
		}
		if v := r.TxDelayFactor; v != nil && !(*v >= 0 && *v <= MaxTxDelayFactor) {
			return fmt.Errorf("repeater %q: txDelayFactor must be 0-%g", r.Name, MaxTxDelayFactor)
		}
		if v := r.DirectTxDelayFactor; v != nil && !(*v >= 0 && *v <= MaxTxDelayFactor) {
			return fmt.Errorf("repeater %q: directTxDelayFactor must be 0-%g", r.Name, MaxTxDelayFactor)
		}
		if v := r.RxDelayBase; v != nil && !(*v >= 0 && *v <= MaxRxDelayBase) {
			return fmt.Errorf("repeater %q: rxDelayBase must be 0-%g", r.Name, MaxRxDelayBase)
		}
		if v := r.MultiAcks; v != nil && (*v < 0 || *v > 255) {
			return fmt.Errorf("repeater %q: multiAcks must be 0-255", r.Name)
		}
		if v := r.AdvertInterval; v != nil && *v != 0 && (*v < MinAdvertIntervalSecs || *v > MaxAdvertIntervalSecs) {
			return fmt.Errorf("repeater %q: advertInterval must be 0 (off) or %d-%d seconds (%d-%d minutes)",
				r.Name, MinAdvertIntervalSecs, MaxAdvertIntervalSecs, MinAdvertIntervalSecs/60, MaxAdvertIntervalSecs/60)
		}
		if v := r.FloodAdvertInterval; v != nil && *v != 0 && (*v < MinFloodAdvertIntervalSecs || *v > MaxFloodAdvertIntervalSecs) {
			return fmt.Errorf("repeater %q: floodAdvertInterval must be 0 (off) or %d-%d seconds (%d-%d hours)",
				r.Name, MinFloodAdvertIntervalSecs, MaxFloodAdvertIntervalSecs, MinFloodAdvertIntervalSecs/3600, MaxFloodAdvertIntervalSecs/3600)
		}
		if len(r.Regions) > meshcore.MaxRegions {
			return fmt.Errorf("repeater %q: at most %d regions", r.Name, meshcore.MaxRegions)
		}
		seenRegion := make(map[string]bool, len(r.Regions))
		for i, rg := range r.Regions {
			if err := validateRegionName(rg.Name); err != nil {
				return fmt.Errorf("repeater %q region[%d]: %w", r.Name, i, err)
			}
			if seenRegion[rg.Name] {
				return fmt.Errorf("repeater %q: duplicate region %q", r.Name, rg.Name)
			}
			seenRegion[rg.Name] = true
		}
	}

	if c.MapProvider != nil && !slices.Contains(MapProviders, *c.MapProvider) {
		return fmt.Errorf("mapProvider %q: must be one of %s", *c.MapProvider, strings.Join(MapProviders, ", "))
	}

	if c.MapDarkStyle != nil && !slices.Contains(MapDarkStyles, *c.MapDarkStyle) {
		return fmt.Errorf("mapDarkStyle %q: must be one of %s", *c.MapDarkStyle, strings.Join(MapDarkStyles, ", "))
	}

	if c.DutyCycle != nil && (*c.DutyCycle <= 0 || *c.DutyCycle > 100) {
		// Deliberate divergence from the firmware's 1-100: some EU868 sub-bands are 0.1%.
		return fmt.Errorf("dutyCycle is a percentage: must be greater than 0 and at most 100")
	}

	if c.Mqtt != nil {
		if c.Mqtt.Node != nil && *c.Mqtt.Node != "" && !seen[*c.Mqtt.Node] {
			return fmt.Errorf("mqtt node %q does not match any companion", *c.Mqtt.Node)
		}
		// Broker names must be unique: the observer's health map and /api/mqtt/status key on them.
		seenBroker := make(map[string]bool, len(c.Mqtt.Brokers))
		for i, b := range c.Mqtt.Brokers {
			if err := b.Validate(); err != nil {
				return fmt.Errorf("mqtt broker[%d] %q: %w", i, b.Name, err)
			}
			if seenBroker[b.Name] {
				return fmt.Errorf("mqtt broker[%d]: duplicate name %q", i, b.Name)
			}
			seenBroker[b.Name] = true
		}
	}

	return nil
}

// ModemSettingsChanged reports whether a reload altered a field that requires reconnecting the modem.
func ModemSettingsChanged(old, new_ *Config) bool {
	return derefStr(old.Connection) != derefStr(new_.Connection) ||
		derefInt(old.BaudRate) != derefInt(new_.BaudRate) ||
		derefFloat(old.Freq) != derefFloat(new_.Freq) ||
		derefFloat(old.Bw) != derefFloat(new_.Bw) ||
		derefUint8(old.SF) != derefUint8(new_.SF) ||
		derefUint8(old.CR) != derefUint8(new_.CR) ||
		derefUint8(old.TX) != derefUint8(new_.TX) ||
		// The airtime factor is baked into the RadioMux at modem.Setup, which is only rebuilt on reconnect; compare the RESOLVED factor so a no-op budget doesn't churn one.
		// ponytail: exact float compare, safe only while DefaultAirtimeFactor is 1.0; wants an epsilon if that moves.
		old.AirtimeFactorOr() != new_.AirtimeFactorOr()
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefUint8(p *uint8) uint8 {
	if p == nil {
		return 0
	}
	return *p
}
