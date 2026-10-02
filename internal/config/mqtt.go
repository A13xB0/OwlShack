package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type MqttConfig struct {
	// Node names the companion that runs the observer; empty = the first companion.
	Node *string `json:"node,omitempty" yaml:"node,omitempty" toml:"node,omitempty"`
	// Identity is who the feed is published as: the node companion (the default) or the repeater,
	// which is how a firmware repeater running meshcoretomqtt reports and keeps an existing observer's history.
	Identity string `json:"identity,omitempty" yaml:"identity,omitempty" toml:"identity,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty" toml:"enabled,omitempty"` // nil = enabled

	IataCode       *string        `json:"iataCode" yaml:"iataCode" toml:"iataCode"`
	StatusInterval *int           `json:"statusInterval" yaml:"statusInterval" toml:"statusInterval"`
	Owner          *string        `json:"owner" yaml:"owner" toml:"owner"`
	Email          *string        `json:"email" yaml:"email" toml:"email"`
	Brokers        []BrokerConfig `json:"brokers" yaml:"brokers" toml:"broker"`

	// Origin is set on the copy handed to the node companion when Identity is repeater; JSON-tagged
	// so a repeater rename or key change rebuilds the observer.
	Origin *MqttOrigin `json:"origin,omitempty" yaml:"-" toml:"-"`
}

// MQTT identities: the node companion's, or the repeater's.
const (
	MqttIdentityCompanion = "companion"
	MqttIdentityRepeater  = "repeater"
)

// MqttIdentities lists the values Identity takes; empty means companion.
var MqttIdentities = []string{MqttIdentityCompanion, MqttIdentityRepeater}

// AsRepeater reports whether the feed is published as the repeater.
func (c *MqttConfig) AsRepeater() bool {
	return c != nil && c.Identity == MqttIdentityRepeater
}

// MqttOrigin is the identity the observer publishes as when it is not the companion it runs on.
// It is filled in at run time from the repeater block, never read from or written to a config file.
type MqttOrigin struct {
	Name       string `json:"name"`
	PrivateKey string `json:"privateKey"`
}

func (c *MqttConfig) IsEnabled() bool {
	return c != nil && (c.Enabled == nil || *c.Enabled)
}

type BrokerConfig struct {
	Name      string `json:"name" yaml:"name" toml:"name"`
	Enabled   bool   `json:"enabled" yaml:"enabled" toml:"enabled"`
	Dedup     bool   `json:"dedup" yaml:"dedup" toml:"dedup"`             // Do we enable dedup checks
	Transport string `json:"transport" yaml:"transport" toml:"transport"` // websockets or tcp
	Host      string `json:"host" yaml:"host" toml:"host"`
	Port      int    `json:"port" yaml:"port" toml:"port"`
	// Deprecated: folded into the topic templates by ApplyDefaults.
	TopicPrefix string `json:"topicPrefix,omitempty" yaml:"topicPrefix,omitempty" toml:"topicPrefix,omitempty"`
	// Placeholders {iata} {pubkey} {name}, plus meshcoretomqtt's uppercase tokens; empty = "meshcore/{iata}/{pubkey}/packets" (resp. "/status").
	PacketTopic           string   `json:"packetTopic,omitempty" yaml:"packetTopic,omitempty" toml:"packetTopic,omitempty"`
	StatusTopic           string   `json:"statusTopic,omitempty" yaml:"statusTopic,omitempty" toml:"statusTopic,omitempty"`
	DisallowedPacketTypes []string `json:"disallowedPacketTypes" yaml:"disallowedPacketTypes" toml:"disallowedPacketTypes"`
	RetainStatus          bool     `json:"retainStatus" yaml:"retainStatus" toml:"retainStatus"`
	TlsEnabled            bool     `json:"tlsEnabled" yaml:"tlsEnabled" toml:"tlsEnabled"`
	TlsInsecure           bool     `json:"tlsInsecure" yaml:"tlsInsecure" toml:"tlsInsecure"`
	AuthType              string   `json:"authType" yaml:"authType" toml:"authType"` // token, basic, or none
	Username              string   `json:"username" yaml:"username" toml:"username"`
	Password              string   `json:"password" yaml:"password" toml:"password"`
	Path                  string   `json:"path" yaml:"path" toml:"path"` // WebSocket path (default: /)
	Audience              string   `json:"audience" yaml:"audience" toml:"audience"`
}

func (b *BrokerConfig) Validate() error {
	if b.Name == "" {
		return fmt.Errorf("name is required")
	}
	if b.Host == "" {
		return fmt.Errorf("host is required")
	}
	if b.Port <= 0 || b.Port > 65535 {
		return fmt.Errorf("port must be 1-65535")
	}
	switch b.Transport {
	case "", "tcp", "websockets":
	default:
		return fmt.Errorf("transport must be \"tcp\" or \"websockets\"")
	}
	switch strings.ToLower(b.AuthType) {
	case "", "none", "token", "basic":
	default:
		return fmt.Errorf("authType must be \"token\", \"basic\", or \"none\"")
	}
	if err := ValidateTopicTemplate(b.PacketTopic); err != nil {
		return fmt.Errorf("packetTopic: %w", err)
	}
	if err := ValidateTopicTemplate(b.StatusTopic); err != nil {
		return fmt.Errorf("statusTopic: %w", err)
	}
	return nil
}

// TopicPlaceholders are the tokens a topic template may use, lowercase or meshcoretomqtt's uppercase.
var TopicPlaceholders = []string{
	"iata", "IATA",
	"pubkey", "PUBKEY", "publicKey", "PUBLIC_KEY",
	"name", "NAME", "origin",
}

var topicTokenRe = regexp.MustCompile(`\{([^{}]*)\}`)

// ValidateTopicTemplate rejects unknown placeholders and MQTT wildcards (publish topics may not contain + or #).
func ValidateTopicTemplate(t string) error {
	if t == "" {
		return nil
	}
	if strings.ContainsAny(t, "+#") {
		return fmt.Errorf("publish topics may not contain MQTT wildcards (+/#)")
	}
	for _, m := range topicTokenRe.FindAllStringSubmatch(t, -1) {
		if !slices.Contains(TopicPlaceholders, m[1]) {
			return fmt.Errorf("unknown placeholder {%s} (supported: {iata} {pubkey} {name})", m[1])
		}
	}
	return nil
}

// migrateTopicPrefix folds the deprecated topicPrefix into explicit topic templates.
func (b *BrokerConfig) migrateTopicPrefix() {
	if b.TopicPrefix == "" {
		return
	}
	if b.PacketTopic == "" {
		b.PacketTopic = b.TopicPrefix + "/{iata}/{pubkey}/packets"
	}
	if b.StatusTopic == "" {
		b.StatusTopic = b.TopicPrefix + "/{iata}/{pubkey}/status"
	}
	b.TopicPrefix = ""
}

func (c *MqttConfig) StatusIntervalSeconds() int {
	if c.StatusInterval != nil && *c.StatusInterval > 0 {
		return *c.StatusInterval
	}
	return 300
}
