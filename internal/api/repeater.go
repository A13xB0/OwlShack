package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/store"
)

func (s *Server) repeaterOps(name string) (*RepeaterOps, bool) {
	b := s.backendRef()
	if b == nil {
		return nil, false
	}
	return b.Repeater(name)
}

func (s *Server) handleRepeaterLogin(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := ops.Login(r.PathValue("pubkey"), body.Password)
	if err != nil {
		s.log.Error("repeater login", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleRoomLogin derives syncSince from the newest stored post when the body omits it.
func (s *Server) handleRoomLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ops, ok := s.repeaterOps(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}
	if ops.RoomLogin == nil {
		writeError(w, http.StatusServiceUnavailable, "room login not available")
		return
	}

	var body struct {
		Password  string `json:"password"`
		SyncSince *int64 `json:"syncSince"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	pubkey := r.PathValue("pubkey")
	var syncSince uint32
	if body.SyncSince != nil {
		if *body.SyncSince > 0 {
			syncSince = uint32(*body.SyncSince)
		}
	} else if cid, err := s.store.Companions.IDByName(r.Context(), name); err == nil {
		if latest, lerr := s.store.Messages.LatestRx(r.Context(), cid, "dm:"+pubkey); lerr == nil && latest != nil {
			syncSince = uint32(latest.Timestamp.Unix())
		}
	}

	result, err := ops.RoomLogin(pubkey, body.Password, syncSince)
	if err != nil {
		s.log.Error("room login", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRoomStatus(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}
	res, err := ops.RoomStatusReq(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("room status request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRoomKeepAlive: body {since?} in unix secs, omitted = the room's own cursor; posts arrive over the DM path.
func (s *Server) handleRoomKeepAlive(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}
	var body struct {
		Since *int64 `json:"since"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	var since uint32
	if body.Since != nil && *body.Since > 0 {
		since = uint32(*body.Since)
	}
	if err := ops.RoomKeepAlive(r.PathValue("pubkey"), since); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepeaterStatus(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	status, err := ops.StatusReq(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("repeater status request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRepeaterCLI(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Command string `json:"command"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	response, err := ops.CLI(r.PathValue("pubkey"), body.Command)
	if err != nil {
		s.log.Error("repeater cli command", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"response": response})
}

func (s *Server) handleRepeaterSession(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	sess := ops.Session(r.PathValue("pubkey"))
	if sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loggedIn": false})
		return
	}

	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleRepeaterLogout(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	ops.Logout(r.PathValue("pubkey"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRepeaterPathGet(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	info, err := ops.PathGet(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("repeater path get", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleRepeaterPathReset(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	if err := ops.PathReset(r.PathValue("pubkey")); err != nil {
		s.log.Error("repeater path reset", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRepeaterPathSet(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Route        string `json:"route"` // flood | direct | path
		Path         string `json:"path"`
		PathHashSize int    `json:"pathHashSize"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hs := body.PathHashSize
	if hs < 1 || hs > 3 {
		writeError(w, http.StatusBadRequest, "bytes per hop must be 1, 2 or 3")
		return
	}
	var path []byte
	switch body.Route {
	case "flood":
	case "direct":
		path = []byte{}
	case "path":
		var err error
		if path, err = hex.DecodeString(body.Path); err != nil || len(path) == 0 {
			writeError(w, http.StatusBadRequest, "path must be hex with at least one hop")
			return
		}
		// The length byte packs hashSize-1 into 2 bits and the hop count into 6 (Packet.h).
		switch {
		case len(path)%hs != 0:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("the path is %d bytes, which is not whole %d-byte hops", len(path), hs))
			return
		case len(path)/hs > 63 || !meshcore.IsValidPathLen(uint8((hs-1)<<6|len(path)/hs)):
			writeError(w, http.StatusBadRequest, "the path is longer than 63 hops or 64 bytes")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, `route must be "flood", "direct" or "path"`)
		return
	}

	if err := ops.PathSet(r.PathValue("pubkey"), path, uint8(hs)); err != nil {
		if errors.Is(err, store.ErrNotContact) {
			writeError(w, http.StatusNotFound, "add the node as a contact first: a path is saved on its contact")
			return
		}
		s.log.Error("repeater path update", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRepeaterNeighbors(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	// The firmware's results buffer holds ~11 entries at an 11-byte stride, so 10 is a safe page.
	count := uint8(10)
	if v := r.URL.Query().Get("count"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 8); err == nil && n > 0 {
			count = uint8(n)
		}
	}
	var offset uint16
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 16); err == nil {
			offset = uint16(n)
		}
	}

	res, err := ops.NeighborsReq(r.PathValue("pubkey"), count, offset)
	if err != nil {
		s.log.Error("repeater neighbours request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRepeaterOwnerInfo(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	res, err := ops.OwnerInfoReq(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("repeater owner info request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRepeaterAccessList(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	res, err := ops.AccessList(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("repeater access list request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRepeaterAccessSet(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Permissions uint8 `json:"permissions"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := ops.SetPerm(r.PathValue("pubkey"), r.PathValue("target"), body.Permissions); err != nil {
		s.log.Error("repeater access set", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRepeaterAccessRemove(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	if err := ops.SetPerm(r.PathValue("pubkey"), r.PathValue("target"), 0); err != nil {
		s.log.Error("repeater access remove", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRepeaterTelemetry(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	res, err := ops.TelemetryReq(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("repeater telemetry request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, res)
}

// handleRepeaterSeries: ?from=/?to= are seconds before now (from is older); the firmware ignores guest sessions.
func (s *Server) handleRepeaterSeries(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}
	from, to := uint32(86400), uint32(0)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be seconds before now")
			return
		}
		from = uint32(n)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be seconds before now")
			return
		}
		to = uint32(n)
	}
	if to >= from {
		writeError(w, http.StatusBadRequest, "from must be further back than to")
		return
	}
	res, err := ops.SeriesReq(r.PathValue("pubkey"), from, to)
	if err != nil {
		s.log.Error("sensor series request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleContactTelemetry(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterOps(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	res, err := ops.ContactTelemetryReq(r.PathValue("pubkey"))
	if err != nil {
		s.log.Error("contact telemetry request", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, res)
}
