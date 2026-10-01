package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type pathHopJSON struct {
	Hash      string   `json:"hash"`
	PeerNames []string `json:"peerNames,omitempty"`
}

func (s *Server) handleListEchoes(w http.ResponseWriter, r *http.Request) {
	msgIDStr := r.PathValue("messageId")
	msgID, err := strconv.ParseInt(msgIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	echoes, err := s.store.Echoes.ListByMessage(r.Context(), msgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list echoes")
		return
	}

	type echoJSON struct {
		ID           int64         `json:"id"`
		ReceivedAt   string        `json:"receivedAt"`
		Hops         int           `json:"hops"`
		PathHashSize int           `json:"pathHashSize"`
		Path         []pathHopJSON `json:"path"`
		SNR          *float64      `json:"snr,omitempty"`
		RSSI         *int8         `json:"rssi,omitempty"`
	}

	out := make([]echoJSON, 0, len(echoes))
	for _, e := range echoes {
		ej := echoJSON{
			ID:           e.ID,
			ReceivedAt:   e.ReceivedAt.UTC().Format(time.RFC3339),
			Hops:         e.Hops,
			PathHashSize: e.PathHashSize,
			SNR:          e.SNR,
			RSSI:         e.RSSI,
		}

		ej.Path = s.resolvePathHashes(r.Context(), e.PathHashes, e.PathHashSize)
		out = append(out, ej)
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetMessagePath(w http.ResponseWriter, r *http.Request) {
	msgIDStr := r.PathValue("messageId")
	msgID, err := strconv.ParseInt(msgIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	name := r.PathValue("name")
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		writeError(w, http.StatusBadRequest, "channel is required")
		return
	}
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	m, err := s.store.Messages.GetByID(r.Context(), msgID)
	if err != nil || m.CompanionID != cid || m.Channel != channel {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}

	// Hops stays a pointer: a direct-routed packet arrives with its path consumed, so an absent
	// count means "not measurable" and flattening it to 0 would claim the message came direct.
	type pathJSON struct {
		Hops         *int          `json:"hops,omitempty"`
		PathHashSize int           `json:"pathHashSize"`
		Sender       string        `json:"sender"`
		Path         []pathHopJSON `json:"path"`
	}

	result := pathJSON{Sender: m.Sender, Hops: m.Hops}
	if m.PathHashSize != nil {
		result.PathHashSize = *m.PathHashSize
	}
	result.Path = s.resolvePathHashes(r.Context(), m.PathHashes, result.PathHashSize)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolvePathHashes(ctx context.Context, pathBytes []byte, hashSize int) []pathHopJSON {
	if hashSize <= 0 {
		hashSize = 1
	}

	hops := make([]pathHopJSON, 0)

	for i := 0; i+hashSize <= len(pathBytes); i += hashSize {
		hashBytes := pathBytes[i : i+hashSize]
		hop := pathHopJSON{
			Hash: hex.EncodeToString(hashBytes),
		}

		peers, err := s.store.Peers.LookupRepeatersByHash(ctx, hashBytes)
		if err == nil {
			hop.PeerNames = peers
		}

		hops = append(hops, hop)
	}

	return hops
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	msgIDStr := r.PathValue("messageId")
	msgID, err := strconv.ParseInt(msgIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	var delErr error
	s.store.WriteSync(func() {
		delErr = s.store.Messages.Delete(r.Context(), msgID)
	})
	if delErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete message")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBlockSender(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	convoID := r.PathValue("conversationId")

	var body struct {
		Sender string `json:"sender"`
	}
	if err := readJSON(r, &body); err != nil || body.Sender == "" {
		writeError(w, http.StatusBadRequest, "sender is required")
		return
	}
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var blockErr error
	s.store.WriteSync(func() {
		blockErr = s.store.BlockedSenders.Block(r.Context(), cid, convoID, body.Sender)
	})
	if blockErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to block sender")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnblockSender(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	convoID := r.PathValue("conversationId")
	sender := r.PathValue("sender")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var unblockErr error
	s.store.WriteSync(func() {
		unblockErr = s.store.BlockedSenders.Unblock(r.Context(), cid, convoID, sender)
	})
	if unblockErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to unblock sender")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListBlockedSenders(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	convoID := r.PathValue("conversationId")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	senders, err := s.store.BlockedSenders.List(r.Context(), cid, convoID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list blocked senders")
		return
	}

	if senders == nil {
		senders = []string{}
	}

	writeJSON(w, http.StatusOK, senders)
}

func (s *Server) handleDeleteConversationMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	convoID := r.PathValue("conversationId")

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var channel string
	if strings.HasPrefix(convoID, "channel:") {
		channel = strings.TrimPrefix(convoID, "channel:")
	} else if strings.HasPrefix(convoID, "contact:") {
		channel = "dm:" + strings.TrimPrefix(convoID, "contact:")
	} else {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	var delErr error
	s.store.WriteSync(func() {
		delErr = s.store.Messages.DeleteByChannel(r.Context(), cid, channel)
	})
	if delErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete messages")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	convoID := r.PathValue("conversationId")

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var channel string
	if strings.HasPrefix(convoID, "channel:") {
		channel = strings.TrimPrefix(convoID, "channel:")
	} else if strings.HasPrefix(convoID, "contact:") {
		channel = "dm:" + strings.TrimPrefix(convoID, "contact:")
	} else {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}

	senders, err := s.store.Messages.DistinctSenders(r.Context(), cid, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list participants")
		return
	}

	if senders == nil {
		senders = []string{}
	}

	writeJSON(w, http.StatusOK, senders)
}

func (s *Server) handleRenameChannel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	channelName := r.PathValue("channel")

	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	renamer := s.ChannelRenamer()
	if renamer == nil {
		writeError(w, http.StatusServiceUnavailable, "channel management not available")
		return
	}

	if err := renamer(name, channelName, body.Name); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if persist := s.ConfigPersist(); persist != nil {
		if err := persist(r.Context()); err != nil {
			s.log.Error("config persist failed after channel rename", "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetChannelKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	channelName := r.PathValue("channel")

	provider := s.companionProvider()

	if provider == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	for _, c := range provider() {
		if c.Name == name {
			for _, ch := range c.Channels {
				if ch.Name == channelName {
					writeJSON(w, http.StatusOK, map[string]string{
						"name": ch.Name,
						"key":  hex.EncodeToString(ch.PSK),
					})
					return
				}
			}
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
	}

	writeError(w, http.StatusNotFound, "companion not found")
}

func (s *Server) handleGetContactPath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pubkey := r.PathValue("pubkey")

	ops, ok := s.repeaterOps(name)
	if !ok || ops == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	info, err := ops.PathGet(pubkey)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleResetContactPath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pubkey := r.PathValue("pubkey")

	ops, ok := s.repeaterOps(name)
	if !ok || ops == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	if err := ops.PathReset(pubkey); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
