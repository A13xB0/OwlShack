package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/store"
	"github.com/meshcore-go/OwlShack/internal/trigger"
)

func previewBackend(t *testing.T) (*backend, int64) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &store.Companion{Name: "Pi Hat"}
	if err := db.Companions.Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return &backend{db: db, feedPreview: trigger.NewFeedPreview()}, c.ID
}

func feedWithTitle(t *testing.T, title string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		io.WriteString(w, fmt.Sprintf(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>F</title>`+
			`<entry><id>urn:1</id><title>%s</title><updated>2026-09-24T00:00:00Z</updated></entry></feed>`, title))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A channel keeps 160 bytes including "Pi Hat: ", so the preview must cut where the radio would, not at 160.
func TestTestTriggerRender_CutsWhereAChannelWould(t *testing.T) {
	t.Parallel()
	b, id := previewBackend(t)
	title := strings.Repeat("x", 200)
	in := api.TriggerTestInput{CompanionID: id, Type: "rss", URL: feedWithTitle(t, title), Template: "{{.Title}}"}
	items, err := b.TestTriggerItems(t.Context(), in)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %v, %v", items, err)
	}
	in.ItemID = items[0].ID
	res, err := b.TestTriggerRender(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := meshcore.MaxTextLen - len("Pi Hat: ")
	if res.ChannelLimit != want || len(res.ChannelText) != want || res.Bytes != 200 || !strings.HasPrefix(title, res.ChannelText) {
		t.Errorf("limit %d, kept %d of %d bytes; want %d kept of 200", res.ChannelLimit, len(res.ChannelText), res.Bytes, want)
	}
	if res.DMLimit <= 0 || res.DMLimit >= meshcore.MaxTextLen {
		t.Errorf("dmLimit %d, want the firmware's limit less the retry bytes", res.DMLimit)
	}
}

// The companion is who sends it; one that does not exist is the request's fault, not a server error.
func TestTestTriggerRender_RefusesAnUnknownCompanion(t *testing.T) {
	t.Parallel()
	b, _ := previewBackend(t)
	_, err := b.TestTriggerRender(t.Context(), api.TriggerTestInput{CompanionID: 999, Type: "rss", URL: "http://x", ItemID: "a"})
	var verr *api.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error", err)
	}
}
