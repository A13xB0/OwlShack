package sensor

import (
	"bytes"
	"slices"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// selfTypes are what channel 1 carries: the node's battery and board temperature.
var selfTypes = []byte{meshcore.LPPVoltage, meshcore.LPPTemperature}

// SelfTypes are the LPP types a channel map may put on the node's own channel.
func SelfTypes() []byte { return slices.Clone(selfTypes) }

// isSelfType reports whether the node's own channel may carry this type.
func isSelfType(t byte) bool { return bytes.IndexByte(selfTypes, t) >= 0 }

// SelfReadings is what a node knows of itself without a sensor: a nil temperature is unmeasurable, and 0 V is no cell, as the firmware reports.
type SelfReadings struct {
	BatteryVolts float64
	TempC        *float64
}

// BuildReply builds a reply body in the firmware's order, battery first and board temperature last; a mapped channel-1 row replaces the built-in reading, and a map too big for the packet is dropped whole, as truncated LPP decodes as garbage.
func BuildReply(perms byte, self SelfReadings, entries []ChannelEntry, statuses []Status, maxBody int) (body []byte, dropped bool) {
	mapped := map[byte]bool{}
	for _, e := range entries {
		if e.Channel == ChannelSelf {
			mapped[e.Type] = true
		}
	}
	// A failing mapped sensor publishes nothing, since falling back would report the board's battery as the one the operator pointed at.
	own := func(enc *meshcore.LPPEncoder, typ byte) {
		encodeEntries(enc, entries, statuses, func(e ChannelEntry) bool { return e.Channel == ChannelSelf && e.Type == typ })
	}
	build := func(sensors bool) []byte {
		enc := meshcore.NewLPPEncoder()
		if perms&PermBase != 0 {
			// Always a voltage, as the firmware always sends one: a mains-powered node says 0, not nothing.
			if mapped[meshcore.LPPVoltage] {
				own(enc, meshcore.LPPVoltage)
			} else {
				enc.AddVoltage(ChannelSelf, self.BatteryVolts)
			}
		}
		if sensors && perms&PermEnvironment != 0 {
			encodeEntries(enc, entries, statuses, func(e ChannelEntry) bool { return e.Channel != ChannelSelf })
		}
		if perms&PermBase != 0 {
			if mapped[meshcore.LPPTemperature] {
				own(enc, meshcore.LPPTemperature)
			} else if self.TempC != nil {
				enc.AddTemperature(ChannelSelf, *self.TempC)
			}
		}
		return enc.Bytes()
	}
	if body = build(true); len(body) > maxBody {
		return build(false), true
	}
	return body, false
}
