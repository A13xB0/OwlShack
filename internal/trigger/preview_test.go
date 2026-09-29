package trigger

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/mmcdole/gofeed"
)

func capServer(t *testing.T, titles ...string) *feedServer {
	t.Helper()
	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	fs := newFeedServer(t, titles...)
	body := string(alert)
	fs.alertBody.Store(&body)
	return fs
}

// The newest item is the one an operator is most likely checking a template against, so it leads.
func TestFeedPreview_ListsTheNewestFirst(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t, "oldest", "middle", "newest")
	items, err := NewFeedPreview().Items(context.Background(), config.TriggerConfig{Type: "rss", URL: fs.feedURL()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Title)
	}
	if strings.Join(got, ",") != "newest,middle,oldest" {
		t.Errorf("items %v, want newest first", got)
	}
}

// A CAP template is written against the alert the entry links to, so the preview must fetch and decode it as a poll does.
func TestFeedPreview_RendersACAPAlertAsTheBotWould(t *testing.T) {
	t.Parallel()
	fs := capServer(t, "severe-weather")
	cfg := config.TriggerConfig{
		Type: "cap", URL: fs.feedURL(),
		Template: "{{.BotName}}: {{.Severity}} {{.Event}}: {{.Headline}} ({{.Match.area}})",
		Match:    &[]string{`area:(?P<area>Northland)`},
	}
	p := NewFeedPreview()
	items, err := p.Items(context.Background(), cfg)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %v, %v", items, err)
	}
	res, err := p.Render(context.Background(), cfg, "Pi Hat", items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Pi Hat: Moderate rain: Heavy Rain Warning (Northland)"; res.Message != want || !res.Matched || res.RenderError != "" {
		t.Errorf("got %+v, want %q, matched, no error", res, want)
	}
}

// Re-rendering while the operator types must not refetch the feed and alert every time.
func TestFeedPreview_ReRendersFromWhatItFetched(t *testing.T) {
	t.Parallel()
	fs := capServer(t, "severe-weather")
	cfg := config.TriggerConfig{Type: "cap", URL: fs.feedURL(), Template: "{{.Event}}"}
	p := NewFeedPreview()
	items, err := p.Items(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range []string{"{{.Event}}", "{{.Headline}}", "{{.Severity}}"} {
		cfg.Template = tmpl
		if _, err := p.Render(context.Background(), cfg, "bot", items[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if hits := fs.alertHits.Load(); hits != 1 {
		t.Errorf("the alert was fetched %d times for three renders, want once", hits)
	}
}

// An item the patterns skip still renders, marked as not sending, so the operator can see why.
func TestFeedPreview_SaysWhenThePatternsSkipAnItem(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t, "quiet day")
	cfg := config.TriggerConfig{Type: "rss", URL: fs.feedURL(), Template: "{{.Title}}", Match: &[]string{"title:(?i)warning"}}
	p := NewFeedPreview()
	items, err := p.Items(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Render(context.Background(), cfg, "bot", items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Message != "quiet day" {
		t.Errorf("got %+v, want the message rendered and marked as skipped", res)
	}
}

// A half-typed template is the normal state while editing: it is reported in the result, not as a failed request.
func TestFeedPreview_ReportsATemplateErrorInTheResult(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t, "item")
	cfg := config.TriggerConfig{Type: "rss", URL: fs.feedURL(), Template: "{{.Title"}
	p := NewFeedPreview()
	items, err := p.Items(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Render(context.Background(), cfg, "bot", items[0].ID)
	if err != nil {
		t.Fatalf("Render = %v, want the template error in the result", err)
	}
	if res.RenderError != "line 1: unclosed action" {
		t.Errorf("render error %q, want it without text/template's wrappers", res.RenderError)
	}
}

// The Test must refuse what a save would, or it tries a bot that could never be stored.
func TestFeedPreview_RefusesWhatASaveWould(t *testing.T) {
	t.Parallel()
	p := NewFeedPreview()
	for name, cfg := range map[string]config.TriggerConfig{
		"a chat type":   {Type: "group", URL: "http://x"},
		"no url":        {Type: "rss"},
		"ftp":           {Type: "rss", URL: "ftp://x"},
		"unknown field": {Type: "cap", URL: "http://x", Match: &[]string{"colour:red"}},
	} {
		if _, err := p.Items(context.Background(), cfg); err == nil {
			t.Errorf("%s was tried", name)
		}
	}
}

// A CAP entry may have no title, and the list must still say what the item is.
func TestPreviewTitle_FallsBackToTheSummary(t *testing.T) {
	t.Parallel()
	got := previewTitle(&gofeed.Item{Description: "  Severe thunderstorm\n warning for Montgomery  "})
	if got != "Severe thunderstorm warning for Montgomery" {
		t.Errorf("title %q, want the summary on one line", got)
	}
}

// Meteoalarm posts an alert once per area; the Test lists it once, as the bot sends it once.
func TestFeedPreview_ListsAnAlertOnce(t *testing.T) {
	t.Parallel()
	f := newCAPFeed(t, map[string]string{"fog": alertDoc(t, "fog"), "gale": alertDoc(t, "gale")})
	f.publish("fog", "gale", "fog", "fog")
	items, err := NewFeedPreview().Items(context.Background(), config.TriggerConfig{Type: "cap", URL: f.URL + "/feed"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Title)
	}
	if strings.Join(got, ",") != "entry 3,entry 1" {
		t.Errorf("items %v, want the newest entry of each alert", got)
	}
}
