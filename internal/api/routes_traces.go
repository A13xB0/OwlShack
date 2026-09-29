package api

import (
	"encoding/hex"
	"net/http"
)

type TraceSender func(path []byte, pathHashSize uint8) (uint32, error)
type TraceSenderLookup func(name string) (TraceSender, bool)

type sendTraceRequest struct {
	Path         string `json:"path"`
	PathHashSize uint8  `json:"pathHashSize"`
}

func (s *Server) handleSendTrace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	lookup := s.traceSenderLookup()

	if lookup == nil {
		writeError(w, http.StatusServiceUnavailable, "trace sender not configured")
		return
	}

	sender, ok := lookup(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var req sendTraceRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	if req.PathHashSize != 1 && req.PathHashSize != 2 && req.PathHashSize != 4 {
		writeError(w, http.StatusBadRequest, "pathHashSize must be 1, 2, or 4")
		return
	}

	pathBytes, err := hex.DecodeString(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "path must be valid hex")
		return
	}

	if len(pathBytes)%int(req.PathHashSize) != 0 {
		writeError(w, http.StatusBadRequest, "path length must be divisible by pathHashSize")
		return
	}

	tag, err := sender(pathBytes, req.PathHashSize)
	if err != nil {
		s.log.Error("trace send", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"tag": tag})
}
