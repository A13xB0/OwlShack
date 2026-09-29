package companion

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/store"
)

// MaxDMTextBytes is the firmware's MAX_TEXT_LEN (10 * CIPHER_BLOCK_SIZE), less the 2 bytes a
// retry past attempt 3 appends, so a message that sends can also be retried.
const MaxDMTextBytes = 10*16 - 2

// uniqueTimestamp mirrors the firmware's getCurrentTimeUnique(): a remote node drops a second post sharing a timestamp as a retry.
func (c *Companion) uniqueTimestamp() uint32 { return c.repeaters.UniqueTimestamp() }

func (c *Companion) SendChannelMessage(channelName, text string) error {
	ch := c.findChannel(channelName)
	if ch == nil {
		return fmt.Errorf("channel %q not found", channelName)
	}
	return c.sendGroupReply(ch, text, c.pathHashSize(), 5*time.Second, 3)
}

// sendGroupReply is the shared send path for the chat API and trigger replies, so bot replies are persisted, broadcast and echo-tracked like manual sends.
func (c *Companion) sendGroupReply(ch *meshcore.ChannelEntry, text string, hashSize uint8, retryTimeout time.Duration, maxRetries int) error {
	payload := &meshcore.GroupTextPayload{
		Timestamp: c.uniqueTimestamp(),
		Sender:    c.cfg.Name,
		Text:      text,
	}

	msg := &store.Message{
		CompanionID: c.cfg.ID,
		Channel:     ch.Name,
		ChannelHash: ch.Hash,
		Sender:      c.cfg.Name,
		Text:        text,
		Direction:   "tx",
		Timestamp:   time.Now(),
	}

	c.store.WriteSync(func() {
		if insertErr := c.store.Messages.Insert(context.Background(), msg); insertErr != nil {
			c.log.Error("failed to persist outgoing message", "error", insertErr)
		}
	})

	msgID := msg.ID

	if c.hub != nil {
		c.hub.Broadcast("messages", map[string]any{
			"companion":   c.cfg.Name,
			"companionId": c.cfg.ID,
			"channel":     ch.Name,
			"sender":      c.cfg.Name,
			"text":        text,
			"direction":   "tx",
			"timestamp":   msg.Timestamp.UTC().Format(time.RFC3339),
			"id":          msgID,
		})
	}

	c.pendingOutbound.Lock()
	c.pendingOutbound.msgID = msgID
	c.pendingOutbound.channel = ch.Name
	c.pendingOutbound.Unlock()

	return c.node.SendGroupText(
		ch, payload, hashSize, retryTimeout, maxRetries,
		func(gsr node.GroupSendResult) {
			c.log.Debug("group reply result", "channel", ch.Name, "confirmed", gsr.Confirmed)
		},
	)
}

// SendContactMessage is the chat API's DM send: a flood or 0-hop DM goes out at the contact's bytes per hop.
func (c *Companion) SendContactMessage(pubkeyHex, text string) error {
	return c.sendDM(pubkeyHex, text, c.bytesPerHopHex(pubkeyHex), 5*time.Second)
}

// sendDMReply is a DM trigger's answer: the trigger's pathHashSize frames it only when no route is stored, since a stored path already fixes its own hash width.
func (c *Companion) sendDMReply(pubkeyHex, text string, hashSize uint8, ackTimeout time.Duration) error {
	return c.sendDM(pubkeyHex, text, hashSize, ackTimeout)
}

// dmAckTimeout mirrors the firmware's calcFloodTimeoutMillisFor / calcDirectTimeoutMillisFor
// (MyMesh.cpp:851-858): the wait has to scale with airtime and hop count or a slow preset gives up
// before the ack can physically arrive. floor keeps the caller's configured value as a minimum,
// and is used outright when the radio params are unknown and airtime reads 0.
// Library limitation: SendTextMessage applies one timeout to every attempt, while the firmware
// recomputes per attempt, so a 0-hop neighbour that goes out of range mid-conversation runs its
// flood fallback attempts on the shorter direct timeout.
func (c *Companion) dmAckTimeout(textLen int, outPath []byte, hashSize uint8, floor time.Duration) time.Duration {
	if c.stats == nil {
		return floor
	}
	// [4 ts][1 flags][text] padded to an AES block, plus dest+src+MAC, header and path-length byte.
	cipherLen := 5 + textLen
	if rem := cipherLen % 16; rem != 0 {
		cipherLen += 16 - rem
	}
	airtime := c.stats.EstAirtimeMs(2 + len(outPath) + 4 + cipherLen)
	if airtime == 0 {
		return floor
	}

	timeout := node.CalcFloodTimeout(airtime)
	if outPath != nil { // non-nil, even empty, routes direct
		hops := 0
		if hashSize > 0 {
			hops = len(outPath) / int(hashSize)
		}
		timeout = node.CalcDirectTimeout(airtime, uint8(min(hops, 255)))
	}
	return max(timeout, floor)
}

func (c *Companion) sendDM(pubkeyHex, text string, fallbackHashSize uint8, ackTimeout time.Duration) error {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("invalid pubkey: %w", err)
	}

	// The UI counts characters; the wire counts bytes, and a retry past attempt 3 appends 2 more.
	if len(text) > MaxDMTextBytes {
		return fmt.Errorf("message is %d bytes, over the %d-byte limit (multibyte characters cost more than one)", len(text), MaxDMTextBytes)
	}

	// SendTextMessage treats a nil path as a flood, so an unrouted contact still sends.
	outPath, hashSize, haveRoute := c.learnedRoute(pubkeyBytes)
	if !haveRoute {
		outPath = nil // flood
	}
	// Nothing to frame on a flood or a 0-hop neighbour, so the caller's width decides the header.
	if len(outPath) == 0 {
		hashSize = fallbackHashSize
	}

	ackTimeout = c.dmAckTimeout(len(text), outPath, hashSize, ackTimeout)

	channelKey := "dm:" + pubkeyHex
	statusSending := "sending"

	msg := &store.Message{
		CompanionID: c.cfg.ID,
		Channel:     channelKey,
		ChannelHash: 0,
		Sender:      c.cfg.Name,
		Text:        text,
		Direction:   "tx",
		Timestamp:   time.Now(),
		Status:      &statusSending,
	}

	c.store.WriteSync(func() {
		if insertErr := c.store.Messages.Insert(context.Background(), msg); insertErr != nil {
			c.log.Error("failed to persist outgoing DM", "error", insertErr)
		}
	})

	if c.hub != nil {
		c.hub.Broadcast("messages", map[string]any{
			"companion":   c.cfg.Name,
			"companionId": c.cfg.ID,
			"channel":     channelKey,
			"sender":      c.cfg.Name,
			"text":        text,
			"direction":   "tx",
			"timestamp":   msg.Timestamp.UTC().Format(time.RFC3339),
			"id":          msg.ID,
			"status":      "sending",
		})
	}

	msgID := msg.ID
	return c.node.SendTextMessage(
		peerIdentity,
		[]byte(text),
		0,
		time.Unix(int64(c.uniqueTimestamp()), 0),
		outPath,
		hashSize,
		ackTimeout,
		func(result node.DMSendResult) {
			var status string
			if result.Confirmed {
				status = "delivered"
				c.log.Debug("DM delivered", "peer", pubkeyHex[:12], "roundTrip", result.RoundTrip)
			} else {
				status = "failed"
				c.log.Warn("DM delivery failed", "peer", pubkeyHex[:12])
				// Every attempt failed on a route we believed in, so stop believing in it: the
				// official app issues CMD_RESET_PATH here. The next path return re-learns it.
				if haveRoute {
					c.node.Peers().ResetOutPath(peerIdentity.PublicKey())
					c.store.WriteAsync(func() {
						if err := c.store.Contacts.UpdateOutPath(context.Background(), c.cfg.ID, pubkeyBytes, nil, 0); err != nil {
							c.log.Error("failed to clear stale route", "peer", pubkeyHex[:12], "error", err)
						}
					})
					c.log.Info("cleared stale route after failed delivery", "peer", pubkeyHex[:12])
				}
			}

			c.store.WriteAsync(func() {
				if err := c.store.Messages.UpdateStatus(context.Background(), msgID, status); err != nil {
					c.log.Error("failed to update message status", "id", msgID, "error", err)
				}
			})

			if c.hub != nil {
				c.hub.Broadcast("messages", map[string]any{
					"action":      "status",
					"companion":   c.cfg.Name,
					"companionId": c.cfg.ID,
					"channel":     channelKey,
					"id":          msgID,
					"status":      status,
				})
			}
		},
	)
}

func (c *Companion) SendTrace(path []byte, pathHashSize uint8) (uint32, error) {
	if len(path) == 0 {
		return 0, fmt.Errorf("path is required")
	}
	if pathHashSize != 1 && pathHashSize != 2 && pathHashSize != 4 {
		return 0, fmt.Errorf("pathHashSize must be 1, 2, or 4")
	}
	if len(path)%int(pathHashSize) != 0 {
		return 0, fmt.Errorf("path length %d is not divisible by pathHashSize %d", len(path), pathHashSize)
	}

	var tagBytes [4]byte
	if _, err := rand.Read(tagBytes[:]); err != nil {
		return 0, fmt.Errorf("generating trace tag: %w", err)
	}
	tag := binary.LittleEndian.Uint32(tagBytes[:])

	var authBytes [4]byte
	if _, err := rand.Read(authBytes[:]); err != nil {
		return 0, fmt.Errorf("generating trace auth: %w", err)
	}
	auth := binary.LittleEndian.Uint32(authBytes[:])

	if err := c.sendTracePacket(tag, auth, path, pathHashSize); err != nil {
		return 0, err
	}
	return tag, nil
}

// sendTracePacket is split out of SendTrace so RunTrace can register its waiter under the tag before the packet hits the radio.
func (c *Companion) sendTracePacket(tag, auth uint32, path []byte, pathHashSize uint8) error {
	var flags byte
	switch pathHashSize {
	case 1:
		flags = 0
	case 2:
		flags = 1
	case 4:
		flags = 2
	}

	trace := &meshcore.Trace{
		Tag:        tag,
		AuthCode:   auth,
		Flags:      flags,
		PathHashes: path,
	}

	payload, err := trace.ToBytes()
	if err != nil {
		return fmt.Errorf("encoding trace: %w", err)
	}

	pkt := meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTrace, 0),
		PathLength: 0,
		Payload:    payload,
	}

	if err := c.node.SendPacket(&pkt); err != nil {
		return fmt.Errorf("sending trace: %w", err)
	}

	c.log.Debug("trace sent", "tag", fmt.Sprintf("%08x", tag), "hops", len(path)/int(pathHashSize), "pathHashSize", pathHashSize)
	return nil
}

func (c *Companion) findChannel(name string) *meshcore.ChannelEntry {
	for _, ch := range c.node.Channels() {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}
