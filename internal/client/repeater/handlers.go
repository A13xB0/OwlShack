package repeater

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/meshpath"
)

func (rm *Client) HandlePathPacket(pkt *meshcore.Packet) bool {
	path, err := meshcore.PathFromBytes(pkt.Payload)
	if err != nil {
		rm.log.Debug("HandlePathPacket: failed to parse", "error", err)
		return false
	}

	rm.loginMu.Lock()
	rm.log.Debug("HandlePathPacket: checking pending logins",
		"pathDest", fmt.Sprintf("%02x", path.Destination),
		"pathSrc", fmt.Sprintf("%02x", path.Source),
		"pendingCount", len(rm.pendingLogins))
	for i, pl := range rm.pendingLogins {
		if path.Source != pl.peerPubKeyByte {
			rm.log.Debug("HandlePathPacket: source mismatch",
				"pathSrc", fmt.Sprintf("%02x", path.Source),
				"expected", fmt.Sprintf("%02x", pl.peerPubKeyByte))
			continue
		}
		plaintext := path.Decrypt(pl.sharedSecret)
		if plaintext == nil {
			rm.log.Debug("HandlePathPacket: MAC verify failed")
			continue
		}
		pp, err := meshcore.ParsePathPayload(plaintext)
		if err != nil {
			rm.log.Debug("HandlePathPacket: bad path payload", "error", err)
			continue
		}
		if pp.ExtraType != meshcore.PayloadTypeResponse || !isLoginReply(pp.Extra) {
			continue
		}
		rm.pendingLogins = append(rm.pendingLogins[:i], rm.pendingLogins[i+1:]...)
		rm.loginMu.Unlock()

		rm.log.Debug("HandlePathPacket: login path decrypted",
			"plaintextLen", len(plaintext),
			"plaintextHex", hex.EncodeToString(plaintext))
		returnPath, extraType, extraData := pp.Path, pp.ExtraType, pp.Extra
		pathHashSize := int(pp.PathHashSize())

		rm.log.Debug("HandlePathPacket: parsed path return",
			"hops", pp.PathHashCount(), "hashSize", pathHashSize,
			"pathHex", hex.EncodeToString(returnPath),
			"extraType", fmt.Sprintf("%02x", extraType),
			"extraDataLen", len(extraData))

		rm.node.Peers().SetOutPath(pl.peerPubKey, returnPath, uint8(pathHashSize))
		rm.persistOutPath(pl.peerPubKey[:], returnPath, uint8(pathHashSize))
		rm.sendReciprocalPath(pkt, pl.peerPubKey[:], pl.sharedSecret, returnPath, uint8(pathHashSize))

		select {
		case pl.ch <- extraData:
		default:
			rm.log.Debug("HandlePathPacket: login channel full, dropped response")
		}
		return true
	}
	rm.loginMu.Unlock()

	rm.mu.Lock()
	sessions := make([]*Session, 0, len(rm.sessions))
	for _, s := range rm.sessions {
		sessions = append(sessions, s)
	}
	rm.mu.Unlock()

	for _, sess := range sessions {
		if path.Destination != sess.localPubKey[0] {
			continue
		}
		if !path.VerifyMAC(sess.sharedSecret) {
			continue
		}
		pp, err := meshcore.ParsePathPayload(path.Decrypt(sess.sharedSecret))
		if err != nil {
			return true
		}
		returnPath, extraType, extraData := pp.Path, pp.ExtraType, pp.Extra
		pathHashSize := int(pp.PathHashSize())

		rm.log.Debug("path return received (session)", "hops", pp.PathHashCount(), "hashSize", pathHashSize, "pathHex", hex.EncodeToString(returnPath))
		if pubkeyBytes, derr := hex.DecodeString(sess.PubKeyHex); derr == nil && len(pubkeyBytes) == 32 {
			var pubkey [32]byte
			copy(pubkey[:], pubkeyBytes)
			rm.node.Peers().SetOutPath(pubkey, returnPath, uint8(pathHashSize))
			rm.persistOutPath(pubkeyBytes, returnPath, uint8(pathHashSize))
			rm.sendReciprocalPath(pkt, pubkeyBytes, sess.sharedSecret, returnPath, uint8(pathHashSize))
		}

		if extraType == meshcore.PayloadTypeResponse && len(extraData) >= 4 {
			tag := binary.LittleEndian.Uint32(extraData[:4])
			data := extraData[4:]

			rm.pendingMu.Lock()
			pr, ok := rm.pending[tag]
			rm.pendingMu.Unlock()
			if ok {
				select {
				case pr.ch <- data:
				default:
				}
			}
		}
		return true
	}

	// Sessionless PATH-wrapped responses match by carried secret and deliver by tag.
	rm.pendingMu.Lock()
	pendings := make([]*pendingRequest, 0, len(rm.pending))
	for _, pr := range rm.pending {
		if pr.sharedSecret != nil {
			pendings = append(pendings, pr)
		}
	}
	rm.pendingMu.Unlock()

	for _, pr := range pendings {
		if path.Source != pr.peerPubKeyByte {
			continue
		}
		if !path.VerifyMAC(pr.sharedSecret) {
			continue
		}
		pp, err := meshcore.ParsePathPayload(path.Decrypt(pr.sharedSecret))
		if err != nil {
			return true
		}
		returnPath, extraType, extraData := pp.Path, pp.ExtraType, pp.Extra

		rm.node.Peers().SetOutPath(pr.peerPubKey, returnPath, pp.PathHashSize())
		rm.persistOutPath(pr.peerPubKey[:], returnPath, pp.PathHashSize())
		rm.sendReciprocalPath(pkt, pr.peerPubKey[:], pr.sharedSecret, returnPath, pp.PathHashSize())

		if extraType == meshcore.PayloadTypeResponse && len(extraData) >= 4 {
			tag := binary.LittleEndian.Uint32(extraData[:4])
			data := extraData[4:]

			rm.pendingMu.Lock()
			target, ok := rm.pending[tag]
			rm.pendingMu.Unlock()
			if ok {
				select {
				case target.ch <- data:
				default:
				}
			}
		}
		return true
	}
	return false
}

func (rm *Client) HandleResponsePacket(pkt *meshcore.Packet) {
	resp, err := meshcore.ResponseFromBytes(pkt.Payload)
	if err != nil {
		rm.log.Debug("failed to parse response", "error", err)
		return
	}

	rm.loginMu.Lock()
	for i, pl := range rm.pendingLogins {
		if resp.Source != pl.peerPubKeyByte {
			continue
		}
		plaintext := resp.Decrypt(pl.sharedSecret)
		if !isLoginReply(plaintext) {
			continue
		}
		rm.pendingLogins = append(rm.pendingLogins[:i], rm.pendingLogins[i+1:]...)
		rm.loginMu.Unlock()
		rm.retryReciprocalPath(pkt, pl.peerPubKey, pl.sharedSecret)
		select {
		case pl.ch <- plaintext:
		default:
		}
		return
	}
	rm.loginMu.Unlock()

	// Sessionless requests: match by carried secret + source, confirm echoed tag.
	rm.pendingMu.Lock()
	for tag, pr := range rm.pending {
		if pr.sharedSecret == nil || resp.Source != pr.peerPubKeyByte {
			continue
		}
		if !resp.VerifyMAC(pr.sharedSecret) {
			continue
		}
		plaintext := resp.Decrypt(pr.sharedSecret)
		if len(plaintext) < 4 || binary.LittleEndian.Uint32(plaintext[:4]) != tag {
			continue
		}
		ch := pr.ch
		rm.pendingMu.Unlock()
		rm.retryReciprocalPath(pkt, pr.peerPubKey, pr.sharedSecret)
		select {
		case ch <- plaintext[4:]:
		default:
		}
		return
	}
	rm.pendingMu.Unlock()

	rm.mu.Lock()
	sessions := make([]*Session, 0, len(rm.sessions))
	for _, s := range rm.sessions {
		sessions = append(sessions, s)
	}
	rm.mu.Unlock()

	for _, sess := range sessions {
		if resp.Destination != sess.localPubKey[0] {
			continue
		}
		if !resp.VerifyMAC(sess.sharedSecret) {
			continue
		}
		plaintext := resp.Decrypt(sess.sharedSecret)
		if len(plaintext) < 4 {
			continue
		}

		tag := binary.LittleEndian.Uint32(plaintext[:4])
		data := plaintext[4:]

		rm.pendingMu.Lock()
		pr, ok := rm.pending[tag]
		rm.pendingMu.Unlock()
		if ok {
			select {
			case pr.ch <- data:
			default:
			}
		}
		// The branch every in-session status, telemetry and CLI reply takes, so it is where a
		// remote still flooding at us despite our route is most likely to show up.
		if pubkeyBytes, derr := hex.DecodeString(sess.PubKeyHex); derr == nil && len(pubkeyBytes) == 32 {
			var pubkey [32]byte
			copy(pubkey[:], pubkeyBytes)
			rm.retryReciprocalPath(pkt, pubkey, sess.sharedSecret)
		}
		return
	}
}

func (rm *Client) HandleCLIResponse(senderPubKey [32]byte, text string) {
	if len(text) < cliPrefixLen || text[2] != '|' {
		return
	}
	prefix := text[:2]
	response := text[3:]

	rm.cliMu.Lock()
	ch, ok := rm.cliPending[prefix]
	rm.cliMu.Unlock()

	if ok {
		select {
		case ch <- response:
		default:
		}
	}
}

func (rm *Client) HandleTextPacket(pkt *meshcore.Packet) bool {
	txtMsg, err := meshcore.TextMessageFromBytes(pkt.Payload)
	if err != nil {
		return false
	}

	rm.mu.Lock()
	sessions := make([]*Session, 0, len(rm.sessions))
	for _, s := range rm.sessions {
		sessions = append(sessions, s)
	}
	rm.mu.Unlock()

	for _, sess := range sessions {
		if txtMsg.Destination != sess.localPubKey[0] {
			continue
		}
		if !txtMsg.VerifyMAC(sess.sharedSecret) {
			continue
		}
		plaintext := txtMsg.Decrypt(sess.sharedSecret)
		if len(plaintext) < 5 {
			continue
		}

		flags := plaintext[4] >> 2
		if flags != txtTypeCliData {
			continue
		}

		text := strings.TrimRight(string(plaintext[5:]), "\x00")
		rm.HandleCLIResponse([32]byte{}, text)
		return true
	}
	return false
}

// reciprocalPathDelay is the firmware's 500 ms on a reciprocal path return (Mesh.cpp:177).
const reciprocalPathDelay = 500 * time.Millisecond

// sendReciprocalPath teaches a peer its route back to us after it taught us ours, sent direct down
// that fresh route. Firmware Mesh.cpp:173-178 does this for any flood PATH; without it the remote
// keeps flooding every response at us.
func (rm *Client) sendReciprocalPath(pkt *meshcore.Packet, peerPubKey, secret, learnedPath []byte, hashSize uint8) {
	if !pkt.IsRouteFlood() {
		return
	}
	rpath, err := meshpath.BuildReturn(rm.node.Identity().PublicKey(), peerPubKey, secret, pkt.Path, pkt.PathLength, 0, nil)
	if err != nil {
		rm.log.Debug("failed to build reciprocal path return", "error", err)
		return
	}
	meshpath.Direct(rpath, learnedPath, hashSize)
	meshpath.ScopeFlood(rm.node, rpath)
	if err := rm.node.SendPacketDelayed(rpath, node.PriorityFloodRelay, reciprocalPathDelay); err != nil {
		rm.log.Debug("failed to send reciprocal path return", "error", err)
		return
	}
	rm.log.Debug("sent reciprocal path return", "peer", hex.EncodeToString(peerPubKey[:min(6, len(peerPubKey))]), "hops", len(learnedPath)/int(max(hashSize, 1)))
}

// returnPathRetryDelay is the firmware's 3 s on handleReturnPathRetry (BaseChatMesh.cpp:364).
const returnPathRetryDelay = 3 * time.Second

// retryReciprocalPath answers a FLOOD response from a peer we already hold a route to: they are
// not using the route we taught them, so BaseChatMesh::handleReturnPathRetry resends it direct.
func (rm *Client) retryReciprocalPath(pkt *meshcore.Packet, peerPubKey [32]byte, secret []byte) {
	if !pkt.IsRouteFlood() || secret == nil {
		return
	}
	outPath, hashSize := learnedRoute(rm.node.Peers().Lookup(peerPubKey))
	if outPath == nil {
		return // no route of ours for them to be ignoring
	}
	rpath, err := meshpath.BuildReturn(rm.node.Identity().PublicKey(), peerPubKey[:], secret, pkt.Path, pkt.PathLength, 0, nil)
	if err != nil {
		rm.log.Debug("failed to build return path retry", "error", err)
		return
	}
	meshpath.Direct(rpath, outPath, hashSize)
	meshpath.ScopeFlood(rm.node, rpath)
	if err := rm.node.SendPacketDelayed(rpath, node.PriorityFloodRelay, returnPathRetryDelay); err != nil {
		rm.log.Debug("failed to send return path retry", "error", err)
		return
	}
	rm.log.Debug("sent return path retry", "peer", hex.EncodeToString(peerPubKey[:6]))
}
