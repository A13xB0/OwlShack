package companion

import (
	"encoding/hex"
	"fmt"
	"strings"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
)

func (c *Companion) AddChannel(ref config.ChannelRef) error {
	ch, err := channelFromRef(ref)
	if err != nil {
		return fmt.Errorf("invalid channel %q: %w", ref.Name, err)
	}

	for i := range node.DefaultMaxChannels {
		if existing := c.node.Channel(i); existing != nil && existing.Name == ch.Name {
			return fmt.Errorf("channel %q already exists", ch.Name)
		}
	}

	idx := c.nextFreeChannelIndex()
	if idx < 0 {
		return fmt.Errorf("no free channel slots")
	}

	if !c.node.SetChannel(idx, ch) {
		return fmt.Errorf("failed to set channel at index %d", idx)
	}

	c.log.Info("channel added", "channel", ch.Name, "index", idx)
	return nil
}

func (c *Companion) RemoveChannel(name string) error {
	if used := c.channelTriggerUsage(name); used != "" {
		return fmt.Errorf("channel %q is in use by the %s; remove that trigger usage first", name, used)
	}
	for i := range node.DefaultMaxChannels {
		ch := c.node.Channel(i)
		if ch != nil && ch.Name == name {
			c.node.RemoveChannel(i)
			c.log.Info("channel removed", "channel", name, "index", i)
			return nil
		}
	}
	return fmt.Errorf("channel %q not found", name)
}

func (c *Companion) RenameChannel(oldName, newName string) error {
	if used := c.channelTriggerUsage(oldName); used != "" {
		return fmt.Errorf("channel %q is in use by the %s; remove that trigger usage first", oldName, used)
	}
	for i := range node.DefaultMaxChannels {
		ch := c.node.Channel(i)
		if ch != nil && ch.Name == oldName {
			ch.Name = newName
			c.log.Info("channel renamed", "old", oldName, "new", newName, "index", i)
			return nil
		}
	}
	return fmt.Errorf("channel %q not found", oldName)
}

// channelTriggerUsage describes the first trigger referencing the named channel, or "" if none do; matching is case-sensitive, as elsewhere in the channel code.
func (c *Companion) channelTriggerUsage(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.triggers {
		for _, ch := range e.channels {
			if ch.Name == name {
				if e.config.Type == "cron" {
					return "cron bot"
				}
				return "group bot"
			}
		}
	}
	return ""
}

func (c *Companion) nextFreeChannelIndex() int {
	for i := 0; i < node.DefaultMaxChannels; i++ {
		if c.node.Channel(i) == nil {
			return i
		}
	}
	return -1
}

// StandaloneChannels returns every channel registered on the node for config persistence; hashtag/Public channels omit their derived key.
func (c *Companion) StandaloneChannels() []config.ChannelRef {
	allChs := c.node.Channels()
	var refs []config.ChannelRef
	for _, ch := range allChs {
		if ch == nil {
			continue
		}
		ref := config.ChannelRef{Name: ch.Name}
		if !isHashtagChannel(ch) {
			ref.PrivateKey = hex.EncodeToString(ch.PSK[:])
		}
		refs = append(refs, ref)
	}
	return refs
}

func isHashtagChannel(ch *meshcore.ChannelEntry) bool {
	if strings.HasPrefix(ch.Name, "#") {
		derived := meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag(ch.Name))
		return derived.Hash == ch.Hash
	}
	if strings.EqualFold(ch.Name, "Public") {
		pub, err := meshcore.NewChannelFromBase64("Public", "izOH6cXN6mrJ5e26oRXNcg==")
		if err != nil {
			return false
		}
		return pub.Hash == ch.Hash
	}
	return false
}

// SetChannels makes the node's channel slots exactly list, in place: slot i holds list[i] and every
// later slot is cleared. Nothing restarts, so a change an app makes is live before it is answered.
func (c *Companion) SetChannels(list config.ChannelList) error {
	entries := make([]*meshcore.ChannelEntry, len(list))
	for i, ref := range list {
		ch, err := channelFromRef(ref)
		if err != nil {
			return fmt.Errorf("channel %q: %w", ref.Name, err)
		}
		entries[i] = ch
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ch := range entries {
		if !c.node.SetChannel(i, ch) {
			return fmt.Errorf("channel %q: slot %d is past the node's last", list[i].Name, i)
		}
	}
	// SetChannel refuses a slot past the table, which is where clearing stops.
	for i := len(entries); c.node.SetChannel(i, nil); i++ {
	}
	kept := append(config.ChannelList(nil), list...)
	c.cfg.Channels = &kept
	return nil
}
