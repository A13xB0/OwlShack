package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/meshcore-go/OwlShack/internal/region"
)

// regionDTO is one region with its outline, [lon, lat] rings read even-odd.
type regionDTO struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Country string         `json:"country"`
	Rings   [][][2]float64 `json:"rings"`
}

func toRegionDTO(r *region.Region) regionDTO {
	return regionDTO{ID: r.ID, Name: r.Name, Country: r.Country, Rings: r.Rings}
}

// handleRegionAt answers a map click with the region under it; 404 is the sea.
func (s *Server) handleRegionAt(w http.ResponseWriter, r *http.Request) {
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if err1 != nil || err2 != nil || !(lat >= -90 && lat <= 90) || !(lon >= -180 && lon <= 180) {
		writeError(w, http.StatusBadRequest, "lat and lon must be a latitude of -90 to 90 and a longitude of -180 to 180")
		return
	}
	reg, err := region.At(lat, lon)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reg == nil {
		writeError(w, http.StatusNotFound, "no region there")
		return
	}
	writeJSON(w, http.StatusOK, toRegionDTO(reg))
}

func (s *Server) handleGetRegion(w http.ResponseWriter, r *http.Request) {
	reg, err := region.ByID(r.PathValue("id"))
	if errors.Is(err, region.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRegionDTO(reg))
}
