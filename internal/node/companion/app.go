package companion

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/store"
)

// AppSink hears what a MeshCore companion app is told about; methods run on the receive path and must not block.
type AppSink interface {
	Message(AppMessage)
	Advert(pkt *meshcore.Packet, adv *meshcore.Advert)
	PathUpdated(pubkey [32]byte)
	RawRX(data []byte, snr float32, rssi int8)
	Trace(pkt *meshcore.Packet, tr *meshcore.Trace)
	Control(pkt *meshcore.Packet)
	RawCustom(pkt *meshcore.Packet)
}

// AppMessage kinds, one per frame an app syncs from the offline queue.
const (
	AppMessageContact = iota
	AppMessageChannel
	AppMessageChannelData
)

// AppMessage is one received message in the shape the firmware queues it for an app.
type AppMessage struct {
	Kind int

	// Contact messages: the sender, and for a room post the author's 4-byte key prefix.
	SenderPubKey [32]byte
	SenderPrefix []byte

	// ChannelIdx is the slot of the channel it arrived on, -1 when it is not one of ours.
	ChannelIdx int

	TxtType         byte
	SenderTimestamp uint32
	// Text is the whole text; a channel message carries the "sender: " prefix as it does on air.
	Text string

	DataType uint16
	Data     []byte

	// PathLen is the received path_len byte for a flood, 0xff for a direct packet.
	PathLen byte
	SNR     float32
}

type appSinkHolder struct{ AppSink }

// SetAppSink attaches the app connection that hears this companion, or detaches it with nil.
func (c *Companion) SetAppSink(s AppSink) {
	if s == nil {
		c.appSink.Store(nil)
		return
	}
	c.appSink.Store(&appSinkHolder{s})
}

func (c *Companion) sink() AppSink {
	if h := c.appSink.Load(); h != nil {
		return h.AppSink
	}
	return nil
}

// appPathLen is the path_len byte the firmware hands an app: the flood's own, 0xff once direct routing consumed it.
func appPathLen(pkt *meshcore.Packet) byte {
	if pkt.IsRouteFlood() {
		return pkt.PathLength
	}
	return 0xff
}

func (c *Companion) emitMessage(m AppMessage, pkt *meshcore.Packet) {
	s := c.sink()
	if s == nil {
		return
	}
	m.PathLen = appPathLen(pkt)
	if pkt.HasSignalInfo {
		m.SNR = pkt.SNR
	}
	s.Message(m)
}

// ChannelIndex is the slot an app knows a channel by, -1 when the companion does not hold it.
func (c *Companion) ChannelIndex(ch *meshcore.ChannelEntry) int {
	for i, have := range c.node.Channels() {
		if have == ch {
			return i
		}
	}
	return -1
}

// SendAppText sends a plain or CLI DM once for an app, which runs its own retries; a plain one is saved to the chat like any other.
func (c *Companion) SendAppText(peer meshcore.Identity, txtType byte, attempt int, timestamp uint32, text string, onResult func(node.DMSendResult), opts ...node.SendOption) (node.TextSent, error) {
	pub := peer.PublicKey()
	path, hs, ok := c.learnedRoute(pub[:])
	if !ok {
		path, hs = nil, c.bytesPerHop(pub[:])
	} else if len(path) == 0 {
		hs = c.bytesPerHop(pub[:])
	}
	sent, err := c.node.SendTextOnce(peer, txtType, attempt, time.Unix(int64(timestamp), 0), []byte(text), path, hs, onResult, opts...)
	if err != nil || txtType != txtTypePlain || attempt > 0 {
		return sent, err
	}
	c.recordAppSend("dm:"+hex.EncodeToString(pub[:]), 0, text)
	return sent, nil
}

// SendAppChannelText posts an app's channel message once, as "name: text" with the app's timestamp, and saves it to the chat.
func (c *Companion) SendAppChannelText(idx int, timestamp uint32, text string, opts ...node.SendOption) error {
	ch := c.node.Channel(idx)
	if ch == nil {
		return fmt.Errorf("no channel in slot %d", idx)
	}
	payload := &meshcore.GroupTextPayload{Timestamp: timestamp, Sender: c.cfg.Name, Text: text}
	if err := c.node.SendGroupText(ch, payload, c.pathHashSize(), 0, 0, nil, opts...); err != nil {
		return err
	}
	c.recordAppSend(ch.Name, ch.Hash, text)
	return nil
}

// SendAppTrace sends a trace with the app's own tag and auth, so the app can match the reply.
func (c *Companion) SendAppTrace(tag, auth uint32, path []byte, pathHashSize uint8) error {
	return c.sendTracePacket(tag, auth, path, pathHashSize)
}

// recordAppSend files what an app sent beside what the UI sends, so the chat shows both.
func (c *Companion) recordAppSend(channel string, hash byte, text string) {
	msg := &store.Message{
		CompanionID: c.cfg.ID,
		Channel:     channel,
		ChannelHash: hash,
		Sender:      c.cfg.Name,
		Text:        text,
		Direction:   "tx",
		Timestamp:   time.Now(),
	}
	c.store.WriteAsync(func() {
		if err := c.store.Messages.Insert(context.Background(), msg); err != nil {
			c.log.Error("failed to persist a message sent by an app", "error", err)
			return
		}
		if c.hub != nil {
			c.hub.Broadcast("messages", map[string]any{
				"companion":   c.cfg.Name,
				"companionId": c.cfg.ID,
				"channel":     channel,
				"sender":      c.cfg.Name,
				"text":        text,
				"direction":   "tx",
				"timestamp":   msg.Timestamp.UTC().Format(time.RFC3339),
				"id":          msg.ID,
			})
		}
	})
}
