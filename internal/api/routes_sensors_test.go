package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type discoverBackend struct {
	Backend
	provider string
}

func (d *discoverBackend) DiscoverSensors(_ context.Context, provider string) (SensorScan, error) {
	d.provider = provider
	return SensorScan{}, nil
}

// A chunked request has no ContentLength, so a length test scans every provider instead of the one asked for.
func TestSensorDiscover_ReadsTheBodyWithoutAContentLength(t *testing.T) {
	for _, tc := range []struct {
		name          string
		body          string
		chunked, want bool
		provider      string
	}{
		{name: "chunked", body: `{"provider":"i2c"}`, chunked: true, provider: "i2c"},
		{name: "measured", body: `{"provider":"i2c"}`, provider: "i2c"},
		{name: "empty", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &discoverBackend{}
			s := &Server{mux: http.NewServeMux()}
			s.routes()
			s.SetBackend(b)

			req := httptest.NewRequest(http.MethodPost, "/api/sensors/discover", strings.NewReader(tc.body))
			if tc.chunked {
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
			}
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if b.provider != tc.provider {
				t.Errorf("scanned provider %q, want %q", b.provider, tc.provider)
			}
		})
	}
}

type createBackend struct {
	Backend
	created int
}

func (c *createBackend) CreateSensor(context.Context, SensorInput) (int64, error) {
	c.created++
	return 1, nil
}

// A body far past any real request is refused before the backend sees it, or a 20 MB name is saved, logged every poll and pushed to every client.
func TestReadJSON_RefusesAnOversizedBody(t *testing.T) {
	b := &createBackend{}
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	s.SetBackend(b)
	post := func(name string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/sensors", strings.NewReader(`{"provider":"i2c","kind":"shtc3","name":"`+name+`"}`))
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post(strings.Repeat("x", 2<<20)); code != http.StatusBadRequest || b.created != 0 {
		t.Fatalf("a 2 MB body got %d and %d creates, want 400 and none", code, b.created)
	}
	if code := post("air"); code != http.StatusOK || b.created != 1 {
		t.Fatalf("an ordinary body got %d and %d creates, so the refusal proves nothing", code, b.created)
	}
}

// errBackend answers every sensor write with the one error it holds.
type errBackend struct {
	Backend
	err error
}

func (b errBackend) DiscoverSensors(context.Context, string) (SensorScan, error) {
	return SensorScan{}, b.err
}
func (b errBackend) SensorKinds(string) ([]SensorKindInfo, error)             { return nil, b.err }
func (b errBackend) CreateSensor(context.Context, SensorInput) (int64, error) { return 0, b.err }
func (b errBackend) UpdateSensor(context.Context, int64, SensorInput) error   { return b.err }
func (b errBackend) DeleteSensor(context.Context, int64) error                { return b.err }
func (b errBackend) SetTelemetryMap(context.Context, TelemetryNode, []TelemetryMapEntry) error {
	return b.err
}

// A refusal is the request's fault and says why, a missing sensor is a 404, and a failed database is the server's: a 500 whose text stays in the log.
func TestSensorRoutes_TellARefusalFromAFailure(t *testing.T) {
	type route struct{ method, path, body string }
	byID := []route{{"PUT", "/api/sensors/7", `{}`}, {"DELETE", "/api/sensors/7", ""}}
	all := append([]route{
		{"POST", "/api/sensors/discover", `{}`},
		{"GET", "/api/sensors/kinds", ""},
		{"POST", "/api/sensors", `{}`},
		{"PUT", "/api/sensors/telemetry-map", `{"node":{"kind":"repeater","id":1},"entries":[]}`},
	}, byID...)
	for _, tc := range []struct {
		name   string
		err    error
		routes []route
		status int
		shown  string
	}{
		{"a refusal", Invalid(errors.New("another sensor is already called air")), all, http.StatusUnprocessableEntity, "already called air"},
		{"a missing sensor", fmt.Errorf("no sensor with id 7: %w", sql.ErrNoRows), byID, http.StatusNotFound, "no such sensor"},
		{"a database failure", errors.New("disk I/O error in /var/lib/owlshack/meshcore.db"), all, http.StatusInternalServerError, ""},
	} {
		for _, r := range tc.routes {
			s := &Server{mux: http.NewServeMux(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			s.routes()
			s.SetBackend(errBackend{err: tc.err})
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, strings.NewReader(r.body)))
			if rec.Code != tc.status {
				t.Errorf("%s on %s %s: %d, want %d", tc.name, r.method, r.path, rec.Code, tc.status)
			}
			if body := rec.Body.String(); !strings.Contains(body, tc.shown) || strings.Contains(body, "/var/lib") {
				t.Errorf("%s on %s %s answered %q", tc.name, r.method, r.path, body)
			}
		}
	}
}

type mapBackend struct {
	Backend
	sets int
}

func (m *mapBackend) SetTelemetryMap(context.Context, TelemetryNode, []TelemetryMapEntry) error {
	m.sets++
	return nil
}

func (m *mapBackend) TelemetryMap(context.Context) (TelemetryMap, error) { return TelemetryMap{}, nil }

// A body with no entries is a mistake, not a request to clear the map; clearing one says so with [].
func TestSetTelemetryMap_RequiresEntries(t *testing.T) {
	for body, want := range map[string]int{
		`{"node":{"kind":"repeater","id":1}}`:                http.StatusBadRequest,
		`{"node":{"kind":"repeater","id":1},"entries":null}`: http.StatusBadRequest,
		`{"node":{"kind":"repeater","id":1},"entries":[]}`:   http.StatusOK,
	} {
		b := &mapBackend{}
		s := &Server{mux: http.NewServeMux()}
		s.routes()
		s.SetBackend(b)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/sensors/telemetry-map", strings.NewReader(body)))
		if rec.Code != want || (want != http.StatusOK && b.sets != 0) {
			t.Errorf("%s: %d with %d saves, want %d", body, rec.Code, b.sets, want)
		}
	}
}
