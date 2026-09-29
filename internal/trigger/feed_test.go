package trigger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
)

// feedServer serves an Atom feed whose entries are named by titles, each linking to /alert/<n>.
// The entry list is swapped between polls to stand in for a publisher adding or dropping items.
type feedServer struct {
	*httptest.Server
	titles    atomic.Pointer[[]string]
	alertBody atomic.Pointer[string]
	alertCode atomic.Int32
	alertHits atomic.Int32
}

func newFeedServer(t *testing.T, titles ...string) *feedServer {
	t.Helper()
	fs := &feedServer{}
	fs.publish(titles...)
	fs.alertCode.Store(http.StatusOK)
	empty := ""
	fs.alertBody.Store(&empty)

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		io.WriteString(w, fs.atom())
	})
	mux.HandleFunc("/alert/", func(w http.ResponseWriter, _ *http.Request) {
		fs.alertHits.Add(1)
		if code := int(fs.alertCode.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		w.Header().Set("Content-Type", "application/cap+xml")
		io.WriteString(w, *fs.alertBody.Load())
	})

	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func (fs *feedServer) publish(titles ...string) {
	fs.titles.Store(&titles)
}

func (fs *feedServer) feedURL() string { return fs.URL + "/feed" }

// atom renders the current entry list, oldest first, with distinct timestamps so the trigger's
// sort is deterministic.
func (fs *feedServer) atom() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom"><title>Test Feed</title>`)
	base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for i, title := range *fs.titles.Load() {
		fmt.Fprintf(&b, `<entry><id>urn:test:%s</id><title>%s</title>`+
			`<summary>summary of %s</summary><updated>%s</updated>`+
			`<link rel="alternate" href="%s/alert/%d"/></entry>`,
			title, title, title, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), fs.URL, i)
	}
	b.WriteString(`</feed>`)
	return b.String()
}

// newTestPoller wires a trigger's callback without starting its cron, so a test drives polls
// itself and every assertion happens on a settled state.
func newTestPoller(t *testing.T, p *feedPoller, err error) (*feedPoller, *[]Event) {
	t.Helper()
	if err != nil {
		t.Fatalf("building trigger: %v", err)
	}
	var fired []Event
	p.callback = func(e Event) { fired = append(fired, e) }
	return p, &fired
}

func newTestRSS(t *testing.T, cfg config.TriggerConfig) (*feedPoller, *[]Event) {
	t.Helper()
	cfg.Type = "rss"
	tr, err := NewRSSTrigger("bot", cfg, testLogger())
	if tr == nil {
		return newTestPoller(t, nil, err)
	}
	return newTestPoller(t, tr.feedPoller, err)
}

func newTestCAP(t *testing.T, cfg config.TriggerConfig) (*feedPoller, *[]Event) {
	t.Helper()
	cfg.Type = "cap"
	tr, err := NewCAPTrigger("bot", cfg, testLogger())
	if tr == nil {
		return newTestPoller(t, nil, err)
	}
	return newTestPoller(t, tr.feedPoller, err)
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func titlesOf(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i], _ = e.Data["Title"].(string)
	}
	return out
}

func TestFeedPoller_FirstPollPrimesAndDoesNotReplay(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t, "old-a", "old-b", "old-c")
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL()})

	tr.poll(context.Background())
	if len(*fired) != 0 {
		t.Fatalf("first poll fired %d events, want 0 (a restart must not replay the backlog): %v",
			len(*fired), titlesOf(*fired))
	}

	tr.poll(context.Background())
	if len(*fired) != 0 {
		t.Fatalf("second poll on an unchanged feed fired %v, want none", titlesOf(*fired))
	}

	fs.publish("old-a", "old-b", "old-c", "brand-new")
	tr.poll(context.Background())
	if got := titlesOf(*fired); len(got) != 1 || got[0] != "brand-new" {
		t.Fatalf("after a new item, fired %v, want [brand-new]", got)
	}

	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Fatalf("item fired again on a later poll: %v", titlesOf(*fired))
	}
}

func TestFeedPoller_BurstClampedToNewest(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL()})
	tr.poll(context.Background()) // prime on an empty feed

	titles := make([]string, 0, feedMaxPerPoll+3)
	for i := range feedMaxPerPoll + 3 {
		titles = append(titles, fmt.Sprintf("item-%d", i))
	}
	fs.publish(titles...)
	tr.poll(context.Background())

	got := titlesOf(*fired)
	if len(got) != feedMaxPerPoll {
		t.Fatalf("fired %d events, want the radio-safe cap of %d: %v", len(got), feedMaxPerPoll, got)
	}
	want := titles[len(titles)-feedMaxPerPoll:]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("clamped to the wrong items: got %v, want the newest %v", got, want)
		}
	}

	// The clamped-away items are recorded, not deferred, so they never arrive late.
	tr.poll(context.Background())
	if len(*fired) != feedMaxPerPoll {
		t.Fatalf("clamped items fired on a later poll: %v", titlesOf(*fired))
	}
}

func TestFeedPoller_MatchScopesToOneField(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	match := []string{`title:(?i)rain`}
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())

	// "summary of Road Closure" is the description; only one item has rain in its *title*.
	fs.publish("Heavy Rain Warning", "Road Closure")
	tr.poll(context.Background())

	if got := titlesOf(*fired); len(got) != 1 || got[0] != "Heavy Rain Warning" {
		t.Fatalf("fired %v, want only the item whose title matched", got)
	}
}

func TestFeedPoller_FieldsAreRequiredTogether(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	// Both fields must hit: the title says rain, the description must name the region.
	match := []string{`title:(?i)rain`, `description:(?i)summary of Heavy`}
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())

	fs.publish("Heavy Rain Warning", "Light Rain Watch")
	tr.poll(context.Background())

	if got := titlesOf(*fired); len(got) != 1 || got[0] != "Heavy Rain Warning" {
		t.Fatalf("fired %v, want only the item satisfying both fields", got)
	}
}

func TestFeedPoller_PatternsOnOneFieldAreAlternatives(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	match := []string{`title:(?i)^flood`, `title:(?i)^slip`}
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())

	fs.publish("Flood Warning", "Slip Closure", "Sunny Day")
	tr.poll(context.Background())

	if got := titlesOf(*fired); len(got) != 2 {
		t.Fatalf("fired %v, want both items — two patterns on one field are alternatives, not a contradiction", got)
	}
}

func TestFeedPoller_UnknownFieldMatchesNothing(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	match := []string{`severity:(?i)extreme`} // a CAP field, on an rss trigger
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())

	fs.publish("Extreme Weather")
	tr.poll(context.Background())

	if len(*fired) != 0 {
		t.Fatalf("fired %v on a field the rss decoder does not offer", titlesOf(*fired))
	}
}

func TestFeedPoller_CapturesReachTheTemplate(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	match := []string{`title:(?i)magnitude (?P<mag>[0-9.]+)`}
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())

	fs.publish("Magnitude 5.2 quake")
	tr.poll(context.Background())

	if len(*fired) != 1 {
		t.Fatalf("fired %d events, want 1", len(*fired))
	}
	captures, _ := (*fired)[0].Data["Match"].(map[string]string)
	if captures["mag"] != "5.2" {
		t.Fatalf("Match.mag = %q, want 5.2", captures["mag"])
	}
}

func TestFeedPoller_ForgetsLongAbsentItems(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL()})
	tr.poll(context.Background())

	fs.publish("alert")
	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Fatalf("new item fired %d times, want 1", len(*fired))
	}

	fs.publish()
	for range feedForgetAfter + 1 {
		tr.poll(context.Background())
	}
	if n := len(tr.seen); n != 0 {
		t.Fatalf("seen set still holds %d ids after the items left the feed, want 0", n)
	}
}

// The cap counts what is sent: newer items the patterns skip do not use up the sends older matches need.
func TestRSSTrigger_CapCountsSends(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	match := []string{"title:^match"}
	tr, fired := newTestRSS(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())
	fs.publish("match one", "match two", "skip 1", "skip 2", "skip 3", "skip 4", "skip 5", "skip 6")
	tr.poll(context.Background())
	var titles []string
	for _, ev := range *fired {
		titles = append(titles, ev.Data["Title"].(string))
	}
	if strings.Join(titles, ",") != "match one,match two" {
		t.Errorf("sent %v, want both matches, oldest first", titles)
	}
}
