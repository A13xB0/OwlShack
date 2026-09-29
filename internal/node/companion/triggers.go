package companion

import (
	"context"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/logging"
	"github.com/meshcore-go/OwlShack/internal/trigger"
)

// ReloadTriggers swaps the trigger set without touching the node, adverts or sessions; the new set is validated before the old one is stopped.
func (c *Companion) ReloadTriggers(newCfg config.CompanionConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx := c.runCtx
	if ctx == nil {
		return fmt.Errorf("companion %q not started", c.cfg.Name)
	}

	// Channels are unchanged here: a config change that alters them takes the full-restart path.
	newEntries, err := c.buildTriggers(newCfg)
	if err != nil {
		return err
	}

	for _, e := range c.triggers {
		e.trigger.Stop()
	}
	c.triggers = newEntries
	c.cfg.Triggers = newCfg.Triggers
	for _, e := range c.triggers {
		if err := e.trigger.Start(ctx, c.makeCallback(ctx, e)); err != nil {
			return fmt.Errorf("companion %q starting reloaded trigger (%s): %w", c.cfg.Name, e.config.Type, err)
		}
	}

	c.log.Info("triggers reloaded", "count", len(c.triggers))
	return nil
}

func applyTriggerDefaults(t *config.TriggerConfig) {
	if t.MaxRetries == nil {
		retries := 3
		t.MaxRetries = &retries
	}
	if t.RetryTimeout == nil {
		retry := int64(5)
		t.RetryTimeout = &retry
	}
}

// triggerChannelFilters builds a trigger's name filter only; it registers nothing on the node, where decryption uses the companion's own channels.
func triggerChannelFilters(cfg config.TriggerConfig) ([]*meshcore.ChannelEntry, error) {
	if cfg.Channels == nil {
		return nil, nil
	}
	var channels []*meshcore.ChannelEntry
	for _, chRef := range *cfg.Channels {
		ch, err := channelFromRef(chRef)
		if err != nil {
			return nil, fmt.Errorf("invalid channel %q: %w", chRef.Name, err)
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

func (c *Companion) buildTriggers(cfg config.CompanionConfig) ([]triggerEntry, error) {
	if cfg.Triggers == nil {
		return nil, nil
	}
	entries := make([]triggerEntry, 0, len(*cfg.Triggers))
	for _, trigCfg := range *cfg.Triggers {
		applyTriggerDefaults(&trigCfg)
		channels, err := triggerChannelFilters(trigCfg)
		if err != nil {
			return nil, fmt.Errorf("companion %q: %w", c.cfg.Name, err)
		}
		entry, err := c.buildTrigger(trigCfg, channels)
		if err != nil {
			return nil, fmt.Errorf("companion %q trigger %q: %w", c.cfg.Name, trigCfg.Type, err)
		}
		entries = append(entries, *entry)
	}
	return entries, nil
}

func (c *Companion) buildTrigger(cfg config.TriggerConfig, channels []*meshcore.ChannelEntry) (*triggerEntry, error) {
	var t trigger.Trigger
	var err error

	switch cfg.Type {
	case "channel", "group":
		t, err = trigger.NewChannelTrigger(c.cfg.Name, cfg, c.node, channels, c.log)
	case "dm":
		t, err = trigger.NewDMTrigger(c.cfg.Name, cfg, c.log)
	case "cron":
		t, err = trigger.NewCronTrigger(c.cfg.Name, cfg, c.log)
	case "rss":
		t, err = trigger.NewRSSTrigger(c.cfg.Name, cfg, c.log)
	case "cap":
		t, err = trigger.NewCAPTrigger(c.cfg.Name, cfg, c.log)
	default:
		return nil, fmt.Errorf("unknown trigger type %q", cfg.Type)
	}
	if err != nil {
		return nil, err
	}

	return &triggerEntry{
		trigger:  t,
		config:   cfg,
		channels: channels,
	}, nil
}

// triggerContacts is the pubkeys a broadcasting trigger DMs, in the same lowercase hex form the
// DM trigger matches senders on.
func triggerContacts(cfg config.TriggerConfig) []string {
	if cfg.Contacts == nil {
		return nil
	}
	var out []string
	for _, c := range *cfg.Contacts {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func (c *Companion) makeCallback(ctx context.Context, entry triggerEntry) trigger.Callback {
	return func(evt trigger.Event) {
		rendered, err := c.templater.Render(&evt, entry.config.Template)
		if err != nil {
			c.log.Error("template error", "error", err)
			return
		}

		c.log.Log(ctx, logging.LevelTrace, "template rendered",
			"trigger", evt.Type, "output", rendered)

		hashSize := resolvePathHashSize(entry.config.PathHashSize, evt, c.pathHashSize())

		retryTimeout := time.Duration(*entry.config.RetryTimeout) * time.Second

		switch evt.Type {
		case "channel":
			ch, _ := evt.Data["ChannelEntry"].(*meshcore.ChannelEntry)
			c.log.Debug("sending group txt", "channel", ch.Name, "pathHashSize", hashSize)
			if err := c.sendGroupReply(ch, rendered, hashSize, retryTimeout, *entry.config.MaxRetries); err != nil {
				c.log.Error("send error", "error", err)
			}

		case "dm":
			pubkey, _ := evt.Data["SenderPubKey"].(string)
			hashSize := resolvePathHashSize(entry.config.PathHashSize, evt, c.bytesPerHopHex(pubkey))
			c.log.Debug("sending dm reply", "peer", pubkey, "pathHashSize", hashSize)
			if err := c.sendDMReply(pubkey, rendered, hashSize, retryTimeout); err != nil {
				c.log.Error("send error", "error", err)
			}

		// A trigger with no incoming message to answer broadcasts instead, to every channel and
		// every contact it was configured with.
		case "cron", "rss", "cap":
			for _, ch := range entry.channels {
				c.log.Debug("sending group txt", "channel", ch.Name, "pathHashSize", hashSize)
				if err := c.sendGroupReply(ch, rendered, hashSize, retryTimeout, *entry.config.MaxRetries); err != nil {
					c.log.Error("send error", "error", err)
				}
			}
			for _, pubkey := range triggerContacts(entry.config) {
				hashSize := resolvePathHashSize(entry.config.PathHashSize, evt, c.bytesPerHopHex(pubkey))
				c.log.Debug("sending dm", "peer", pubkey, "pathHashSize", hashSize)
				if err := c.sendDMReply(pubkey, rendered, hashSize, retryTimeout); err != nil {
					c.log.Error("send error", "peer", pubkey, "error", err)
				}
			}
		}
	}
}
