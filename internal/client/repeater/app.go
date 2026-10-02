package repeater

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

// AppHooks are what a companion app listening through this client hears; each is optional.
type AppHooks struct {
	// CLIText is a CLI reply from a node we hold a session with.
	CLIText func(sender [32]byte, timestamp uint32, text string, pkt *meshcore.Packet)
	// PathLearned is a route to peer learned from its path return.
	PathLearned func(peer [32]byte)
}

func (rm *Client) SetAppHooks(h AppHooks) { rm.appHooks.Store(&h) }

func (rm *Client) hooks() AppHooks {
	if h := rm.appHooks.Load(); h != nil {
		return *h
	}
	return AppHooks{}
}

// PathReply is the route a path discovery learned: the path out to the peer and the path its reply came in on.
type PathReply struct {
	OutPathLen byte
	OutPath    []byte
	InPathLen  byte
	InPath     []byte
}

// Pending is a request on the air: the tag its reply carries, how it was sent, and the reply to wait for.
type Pending struct {
	Tag     uint32
	Flood   bool
	Timeout time.Duration

	reply chan []byte
	paths chan PathReply
	// settle runs once the wait ends, with the reply or nil on a timeout.
	settle func(data []byte)
}

// Wait blocks for the reply until the request's timeout or ctx ends.
func (p *Pending) Wait(ctx context.Context) ([]byte, error) {
	t := time.NewTimer(p.Timeout)
	defer t.Stop()
	select {
	case data := <-p.reply:
		p.settle(data)
		return data, nil
	case <-t.C:
		p.settle(nil)
		return nil, fmt.Errorf("no reply within %s", p.Timeout)
	case <-ctx.Done():
		p.settle(nil)
		return nil, ctx.Err()
	}
}

// Path is the route a path discovery learned, once its reply has arrived.
func (p *Pending) Path() (PathReply, bool) {
	select {
	case pr := <-p.paths:
		return pr, true
	default:
		return PathReply{}, false
	}
}

// RequestOptions shape StartRequest: an anon request carries our key, and a forced flood ignores any route we hold.
type RequestOptions struct {
	Anon       bool
	ForceFlood bool
	// Floor is the least time to wait, whatever the airtime estimate says.
	Floor time.Duration
}

// StartRequest sends body to peer led by a fresh tag and returns at once; the caller waits on the Pending for the reply.
func (rm *Client) StartRequest(peer [32]byte, body []byte, opts RequestOptions) (*Pending, error) {
	peerIdentity := meshcore.NewIdentity(peer)
	route := rm.node.Peers().Lookup(peer)
	if route == nil || opts.ForceFlood {
		route = &node.Peer{Identity: peerIdentity}
	}

	self := rm.node.Identity()
	secret, err := rm.node.SharedSecret(peerIdentity)
	if err != nil {
		return nil, fmt.Errorf("deriving shared secret: %w", err)
	}
	if sess := rm.Session(hex.EncodeToString(peer[:])); sess != nil && sess.sharedSecret != nil && !opts.Anon {
		secret = sess.sharedSecret
	}

	tag := rm.UniqueTimestamp()
	plaintext := binary.LittleEndian.AppendUint32(nil, tag)
	plaintext = append(plaintext, body...)
	encrypted, err := meshcore.EncryptThenMAC(secret, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting request: %w", err)
	}
	mac := [2]byte{encrypted[0], encrypted[1]}

	var payload []byte
	payloadType := meshcore.PayloadTypeReq
	if opts.Anon {
		payloadType = meshcore.PayloadTypeAnonReq
		payload, err = (&meshcore.AnonReq{Destination: peer[0], EphemeralPubKey: self.PublicKey(), MAC: mac, EncryptedPayload: encrypted[2:]}).ToBytes()
	} else {
		payload, err = (&meshcore.Request{Destination: peer[0], Source: self.PublicKey()[0], MAC: mac, EncryptedPayload: encrypted[2:]}).ToBytes()
	}
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	pr := &pendingRequest{
		ch: make(chan []byte, 1), paths: make(chan PathReply, 1), created: time.Now(),
		sharedSecret: secret, peerPubKeyByte: peer[0], peerPubKey: peer,
	}
	rm.pendingMu.Lock()
	rm.pending[tag] = pr
	rm.pendingMu.Unlock()
	drop := func([]byte) {
		rm.pendingMu.Lock()
		delete(rm.pending, tag)
		rm.pendingMu.Unlock()
	}

	pkt, outPath, hashSize := rm.routedPacket(route, payloadType, payload)
	if err := rm.node.SendPacket(pkt); err != nil {
		drop(nil)
		return nil, fmt.Errorf("sending request: %w", err)
	}
	return &Pending{
		Tag: tag, Flood: outPath == nil,
		Timeout: rm.replyTimeout(len(payload), outPath, hashSize, opts.Floor),
		reply:   pr.ch, paths: pr.paths, settle: drop,
	}, nil
}
