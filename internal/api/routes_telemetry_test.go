package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// PUT /api/sensors/{id} also matches /api/sensors/telemetry-map; Go's mux prefers the literal, and must keep doing so.
func TestRoutes_TelemetryMapBeatsTheSensorIdPattern(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	for _, method := range []string{"GET", "PUT"} {
		req := httptest.NewRequest(method, "/api/sensors/telemetry-map", nil)
		_, pattern := s.mux.Handler(req)
		if pattern != method+" /api/sensors/telemetry-map" {
			t.Fatalf("%s /api/sensors/telemetry-map routed to %q", method, pattern)
		}
	}
}
