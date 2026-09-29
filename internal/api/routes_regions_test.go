package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegions(t *testing.T) {
	t.Parallel()
	s := NewServer(nil, nil, nil)
	get := func(path string) (int, regionDTO) {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var out regionDTO
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, auckland := get("/api/regions/at?lat=-36.8485&lon=174.7633")
	if code != http.StatusOK || auckland.Name != "Auckland" || len(auckland.Rings) == 0 {
		t.Fatalf("at the CBD: %d %+v", code, auckland.Name)
	}
	if code, again := get("/api/regions/" + auckland.ID); code != http.StatusOK || again.Name != "Auckland" {
		t.Errorf("by id: %d %q", code, again.Name)
	}
	for path, want := range map[string]int{
		"/api/regions/at?lat=-40&lon=165": http.StatusNotFound,
		"/api/regions/at?lat=-95&lon=174": http.StatusBadRequest,
		"/api/regions/at?lat=abc&lon=174": http.StatusBadRequest,
		"/api/regions/at?lat=NaN&lon=174": http.StatusBadRequest,
		"/api/regions/at?lat=-36&lon=NaN": http.StatusBadRequest,
		"/api/regions/NZ-AUK":             http.StatusNotFound,
	} {
		if code, _ := get(path); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}
