package api

import (
	"net/http"
)

// AdvertSender broadcasts a self-advert: flood when true, zero-hop (direct neighbours only) when false.
type AdvertSender func(flood bool) error
type AdvertSenderLookup func(name string) (AdvertSender, bool)

type sendAdvertRequest struct {
	Mode string `json:"mode"` // "flood" | "zerohop"
}

func (s *Server) handleSendAdvert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	lookup := s.advertSenderLookup()
	if lookup == nil {
		writeError(w, http.StatusServiceUnavailable, "advert sender not configured")
		return
	}

	sender, ok := lookup(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var req sendAdvertRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var flood bool
	switch req.Mode {
	case "flood":
		flood = true
	case "zerohop":
		flood = false
	default:
		writeError(w, http.StatusBadRequest, `mode must be "flood" or "zerohop"`)
		return
	}

	if err := sender(flood); err != nil {
		s.log.Error("advert send", "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
