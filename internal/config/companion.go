package config

import (
	"encoding/hex"
	"fmt"
	"strings"
)

type CompanionConfig struct {
	// ID is the store's surrogate key, never serialized; 0 means not yet persisted.
	ID int64 `json:"-" yaml:"-" toml:"-"`

	Name string `json:"name" yaml:"name" toml:"name"`

	// PrivateKey is a hex ed25519 seed (64 hex chars); empty = generated when persisted.
	PrivateKey string `json:"privateKey,omitempty" yaml:"privateKey,omitempty" toml:"privateKey,omitempty"`

	// Deprecated: read and inlined into PrivateKey when a file config is imported.
	KeyFile string `json:"keyFile,omitempty" yaml:"keyFile,omitempty" toml:"keyFile,omitempty"`

	// Advert Data
	Latitude       *float64 `json:"latitude" yaml:"latitude" toml:"latitude"`
	Longitude      *float64 `json:"longitude" yaml:"longitude" toml:"longitude"`
	AdvertInterval *int     `json:"advertInterval,omitempty" yaml:"advertInterval,omitempty" toml:"advertInterval,omitempty"` // nil == default, 0 == off

	// Overrides Config.PathHashSize for this companion (bytes); nil == inherit, resolved at startup.
	PathHashSize *int `json:"pathHashSize,omitempty" yaml:"pathHashSize,omitempty" toml:"pathHashSize,omitempty"`

	// Standalone channels (not tied to triggers)
	Channels *ChannelList `json:"channels,omitempty" yaml:"channels,omitempty" toml:"channels,omitempty"`

	Triggers *[]TriggerConfig `json:"triggers,omitempty" yaml:"triggers,omitempty" toml:"trigger,omitempty"`

	// DMPolicy is who may DM this companion: contacts (default), allowlist or anyone.
	DMPolicy *string `json:"dmPolicy,omitempty" yaml:"dmPolicy,omitempty" toml:"dmPolicy,omitempty"`
	// DMAllow is the allowlist policy's peer pubkeys, full or prefix hex.
	DMAllow *[]string `json:"dmAllow,omitempty" yaml:"dmAllow,omitempty" toml:"dmAllow,omitempty"`

	// Telemetry* is who may read each class: nobody, contacts granted it, or every contact. Only contacts are answered.
	TelemetryBase        *string `json:"telemetryBase,omitempty" yaml:"telemetryBase,omitempty" toml:"telemetryBase,omitempty"`
	TelemetryLocation    *string `json:"telemetryLocation,omitempty" yaml:"telemetryLocation,omitempty" toml:"telemetryLocation,omitempty"`
	TelemetryEnvironment *string `json:"telemetryEnvironment,omitempty" yaml:"telemetryEnvironment,omitempty" toml:"telemetryEnvironment,omitempty"`

	// Deprecated: mqtt lives at the top level of Config; legacy blocks here are hoisted by ApplyDefaults.
	Mqtt *MqttConfig `json:"mqtt,omitempty" yaml:"mqtt,omitempty" toml:"mqtt,omitempty"`
}

func (c *CompanionConfig) HasLatLon() bool {
	if c.Latitude == nil || c.Longitude == nil {
		return false
	}

	return *c.Latitude != 0 && *c.Longitude != 0
}

// DM acceptance policies: who may open a DM conversation with this companion.
const (
	DMPolicyContacts  = "contacts"  // only peers already in companion_contacts
	DMPolicyAllowlist = "allowlist" // only the pubkeys in DMAllow
	DMPolicyAnyone    = "anyone"    // any peer we can decrypt, which is any advert we have heard
)

// Telemetry read policies, one per firmware permission class; an unset mode denies, as the firmware does.
const (
	TelemetryDeny     = "deny"     // no one
	TelemetrySelected = "selected" // only contacts granted this class
	TelemetryContacts = "contacts" // every contact
)

// TelemetryModeOrDefault resolves an unset or unrecognised class to deny, never to a grant.
func TelemetryModeOrDefault(mode *string) string {
	if mode == nil {
		return TelemetryDeny
	}
	switch *mode {
	case TelemetrySelected, TelemetryContacts:
		return *mode
	}
	return TelemetryDeny
}

// DMPolicyOrDefault resolves an unset policy to the behaviour every install had before the setting existed.
func (c *CompanionConfig) DMPolicyOrDefault() string {
	if c.DMPolicy == nil || *c.DMPolicy == "" {
		return DMPolicyContacts
	}
	return *c.DMPolicy
}

// AllowsDMFrom decides whether a decrypted plain DM is accepted; pubkeyHex is the sender's full key.
func (c *CompanionConfig) AllowsDMFrom(pubkeyHex string, isContact bool) bool {
	switch c.DMPolicyOrDefault() {
	case DMPolicyAnyone:
		return true
	case DMPolicyAllowlist:
		if c.DMAllow == nil {
			return false
		}
		for _, allowed := range *c.DMAllow {
			// Stored keys may be a prefix, which is what the UI shows and what an operator pastes.
			if a := strings.ToLower(strings.TrimSpace(allowed)); a != "" && strings.HasPrefix(strings.ToLower(pubkeyHex), a) {
				return true
			}
		}
		return false
	default:
		return isContact
	}
}

// validateDMAllowKey accepts a full or prefix pubkey; an odd or non-hex entry would never match a sender.
func validateDMAllowKey(k string) error {
	k = strings.TrimSpace(k)
	if k == "" {
		return fmt.Errorf("empty pubkey")
	}
	if len(k)%2 != 0 || len(k) > 64 {
		return fmt.Errorf("pubkey %q must be an even number of hex chars, at most 64", k)
	}
	if _, err := hex.DecodeString(k); err != nil {
		return fmt.Errorf("pubkey %q must be hex: %w", k, err)
	}
	return nil
}
