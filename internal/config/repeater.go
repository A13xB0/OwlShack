package config

import (
	"fmt"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// RepeaterConfig is a repeater node we RUN on the mesh; fields mirror the firmware NodePrefs subset (examples/simple_repeater), nil == default resolved by the *Or accessors.
type RepeaterConfig struct {
	Name string `json:"name" yaml:"name" toml:"name"`

	// PrivateKey is a hex ed25519 seed (64 hex chars); empty == generated when persisted.
	PrivateKey string `json:"privateKey,omitempty" yaml:"privateKey,omitempty" toml:"privateKey,omitempty"`

	// Advert data; the zero-hop and flood advert periods are SECONDS, nil == default, 0 == off.
	Latitude            *float64 `json:"latitude" yaml:"latitude" toml:"latitude"`
	Longitude           *float64 `json:"longitude" yaml:"longitude" toml:"longitude"`
	AdvertInterval      *int     `json:"advertInterval,omitempty" yaml:"advertInterval,omitempty" toml:"advertInterval,omitempty"`
	FloodAdvertInterval *int     `json:"floodAdvertInterval,omitempty" yaml:"floodAdvertInterval,omitempty" toml:"floodAdvertInterval,omitempty"`

	// Relay policy (mirrors firmware allowPacketForward / NodePrefs).
	DisableFwd       *bool   `json:"disableFwd,omitempty" yaml:"disableFwd,omitempty" toml:"disableFwd,omitempty"`
	FloodMax         *int    `json:"floodMax,omitempty" yaml:"floodMax,omitempty" toml:"floodMax,omitempty"`                         // max flood path hops to relay (firmware default 64)
	FloodMaxUnscoped *int    `json:"floodMaxUnscoped,omitempty" yaml:"floodMaxUnscoped,omitempty" toml:"floodMaxUnscoped,omitempty"` // extra hop cap for PLAIN floods only, 0 = never relay them (firmware default 64)
	FloodMaxAdvert   *int    `json:"floodMaxAdvert,omitempty" yaml:"floodMaxAdvert,omitempty" toml:"floodMaxAdvert,omitempty"`       // advert-specific hop cap (firmware default 8)
	LoopDetect       *string `json:"loopDetect,omitempty" yaml:"loopDetect,omitempty" toml:"loopDetect,omitempty"`                   // off|minimal|moderate|strict

	// Scope for our own flood adverts (firmware default_scope); empty = unscoped, which is valid.
	DefaultRegion string `json:"defaultRegion,omitempty" yaml:"defaultRegion,omitempty" toml:"defaultRegion,omitempty"`

	// Labels this node's home region (firmware home_id): stored and reported, never routed on.
	HomeRegion string `json:"homeRegion,omitempty" yaml:"homeRegion,omitempty" toml:"homeRegion,omitempty"`

	// Overrides Config.PathHashSize (BYTES); nil == inherit. The firmware's `path.hash.mode` is this minus one.
	PathHashSize *int `json:"pathHashSize,omitempty" yaml:"pathHashSize,omitempty" toml:"pathHashSize,omitempty"`

	// Relay timing (firmware NodePrefs tx_delay_factor / direct_tx_delay_factor / rx_delay_base / multi_acks); nil == firmware default.
	TxDelayFactor       *float64 `json:"txDelayFactor,omitempty" yaml:"txDelayFactor,omitempty" toml:"txDelayFactor,omitempty"`
	DirectTxDelayFactor *float64 `json:"directTxDelayFactor,omitempty" yaml:"directTxDelayFactor,omitempty" toml:"directTxDelayFactor,omitempty"`
	RxDelayBase         *float64 `json:"rxDelayBase,omitempty" yaml:"rxDelayBase,omitempty" toml:"rxDelayBase,omitempty"`
	MultiAcks           *int     `json:"multiAcks,omitempty" yaml:"multiAcks,omitempty" toml:"multiAcks,omitempty"`

	// A blank admin password is allowed — some repeaters have none.
	AdminPassword string `json:"adminPassword,omitempty" yaml:"adminPassword,omitempty" toml:"adminPassword,omitempty"`
	GuestPassword string `json:"guestPassword,omitempty" yaml:"guestPassword,omitempty" toml:"guestPassword,omitempty"`
	OwnerInfo     string `json:"ownerInfo,omitempty" yaml:"ownerInfo,omitempty" toml:"ownerInfo,omitempty"`

	// Transport-flood scopes the repeater relays; the key is SHA256(name)[:16] (firmware getAutoKeyFor), not a hashtag.
	Regions []RepeaterRegion `json:"regions,omitempty" yaml:"regions,omitempty" toml:"region,omitempty"`
}

// RepeaterRegion is one transport scope the repeater serves; DenyFlood keeps it known but not re-flooded.
type RepeaterRegion struct {
	Name      string `json:"name" yaml:"name" toml:"name"`
	DenyFlood bool   `json:"denyFlood,omitempty" yaml:"denyFlood,omitempty" toml:"denyFlood,omitempty"`
}

// WildcardRegion ("*") is the unscoped flood scope, modelled as an editable Regions entry; its absence means unscoped flood is not relayed.
const WildcardRegion = "*"

// validateRegionName mirrors the firmware's RegionMap::is_name_char / MAX_REGION_NAME; the wildcard "*" is exempt.
// ScopeRegion is the region a scope name stands for: a bare name is the "#name" hashtag region, as RegionMap::getTransportKeysFor has it.
func ScopeRegion(name string) *meshcore.Region {
	return meshcore.NewRegionFromKey(name, meshcore.NewRegionFromHashtag(name).Key)
}

func validateRegionName(name string) error {
	if name == WildcardRegion {
		return nil
	}
	if name == "" {
		return fmt.Errorf("region name is required")
	}
	if len(name) > meshcore.MaxRegionName {
		return fmt.Errorf("region name %q exceeds %d chars", name, meshcore.MaxRegionName)
	}
	for i := 0; i < len(name); i++ {
		if !meshcore.IsValidRegionNameChar(name[i]) {
			return fmt.Errorf("region name %q has an invalid character %q", name, name[i])
		}
	}
	return nil
}

// Firmware defaults for relay policy (examples/simple_repeater MyMesh.cpp ctor).
const (
	DefaultFloodMax         = 64
	DefaultFloodMaxUnscoped = 64
	DefaultFloodMaxAdvert   = 8
	DefaultLoopDetect       = "off"
	// Firmware default (MyMesh.cpp flood_advert_interval = 47 hours).
	DefaultFloodAdvertIntervalSecs = 47 * 60 * 60

	// Firmware CommonCLI ranges, in seconds: zero-hop 0 (off) or 60-240 minutes, flood 0 (off) or 3-168 hours.
	MinAdvertIntervalSecs      = 60 * 60
	MaxAdvertIntervalSecs      = 240 * 60
	MinFloodAdvertIntervalSecs = 3 * 60 * 60
	MaxFloodAdvertIntervalSecs = 168 * 60 * 60

	// The firmware's `set path.hash.mode` accepts 0-2 and uses mode+1 as the width, so 1-3 bytes.
	DefaultPathHashSize = 1
	MinPathHashSize     = 1
	MaxPathHashSize     = 3

	// Firmware simple_repeater defaults and CommonCLI set-ranges for the timing knobs.
	DefaultTxDelayFactor       = 0.5
	DefaultDirectTxDelayFactor = 0.3
	MaxTxDelayFactor           = 2.0
	MaxRxDelayBase             = 20.0
)

// Valid loop-detect levels, matching the firmware's LOOP_DETECT_* enum.
var loopDetectLevels = map[string]bool{"off": true, "minimal": true, "moderate": true, "strict": true}

// IsValidLoopDetect reports whether s is a known loop-detect level.
func IsValidLoopDetect(s string) bool { return loopDetectLevels[s] }

func (c *RepeaterConfig) HasLatLon() bool {
	if c.Latitude == nil || c.Longitude == nil {
		return false
	}
	return *c.Latitude != 0 && *c.Longitude != 0
}

// FloodMaxOr / FloodMaxAdvertOr / LoopDetectOr / IsFwdDisabled apply firmware defaults for nil fields.
func (c *RepeaterConfig) FloodMaxOr() int {
	if c.FloodMax == nil {
		return DefaultFloodMax
	}
	return *c.FloodMax
}

func (c *RepeaterConfig) FloodMaxAdvertOr() int {
	if c.FloodMaxAdvert == nil {
		return DefaultFloodMaxAdvert
	}
	return *c.FloodMaxAdvert
}

func (c *RepeaterConfig) FloodMaxUnscopedOr() int {
	if c.FloodMaxUnscoped == nil {
		return DefaultFloodMaxUnscoped
	}
	return *c.FloodMaxUnscoped
}

func (c *RepeaterConfig) LoopDetectOr() string {
	if c.LoopDetect == nil || *c.LoopDetect == "" {
		return DefaultLoopDetect
	}
	return *c.LoopDetect
}

func (c *RepeaterConfig) IsFwdDisabled() bool {
	return c.DisableFwd != nil && *c.DisableFwd
}

// PathHashSizeOr resolves the effective path hash width in bytes (1 by default).
func (c *RepeaterConfig) PathHashSizeOr() int {
	if c.PathHashSize == nil {
		return DefaultPathHashSize
	}
	return *c.PathHashSize
}

func (c *RepeaterConfig) TxDelayFactorOr() float64 {
	if c.TxDelayFactor == nil {
		return DefaultTxDelayFactor
	}
	return *c.TxDelayFactor
}

func (c *RepeaterConfig) DirectTxDelayFactorOr() float64 {
	if c.DirectTxDelayFactor == nil {
		return DefaultDirectTxDelayFactor
	}
	return *c.DirectTxDelayFactor
}

// RxDelayBaseOr is the firmware rx_delay_base; 0 (the default) disables the receive delay.
func (c *RepeaterConfig) RxDelayBaseOr() float64 {
	if c.RxDelayBase == nil {
		return 0
	}
	return *c.RxDelayBase
}

func (c *RepeaterConfig) MultiAcksOr() int {
	if c.MultiAcks == nil {
		return 0
	}
	return *c.MultiAcks
}

// AdvertIntervalOr / FloodAdvertIntervalOr resolve the effective self-advert periods in SECONDS.
func (c *RepeaterConfig) AdvertIntervalOr() int {
	if c.AdvertInterval == nil {
		return 0 // zero-hop advert default: off
	}
	return *c.AdvertInterval
}

func (c *RepeaterConfig) FloodAdvertIntervalOr() int {
	if c.FloodAdvertInterval == nil {
		return DefaultFloodAdvertIntervalSecs
	}
	return *c.FloodAdvertInterval
}
