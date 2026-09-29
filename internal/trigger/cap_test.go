package trigger

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
)

func TestCAPTrigger_DecoratesFromLinkedAlert(t *testing.T) {
	t.Parallel()
	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	fs := newFeedServer(t)
	body := string(alert)
	fs.alertBody.Store(&body)

	tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL()})
	tr.poll(context.Background())

	fs.publish("severe-weather")
	tr.poll(context.Background())

	if len(*fired) != 1 {
		t.Fatalf("fired %d events, want 1", len(*fired))
	}
	data := (*fired)[0].Data
	for field, want := range map[string]string{
		"Headline":   "Heavy Rain Warning",
		"Severity":   "Moderate",
		"Urgency":    "Immediate",
		"Certainty":  "Likely",
		"MsgType":    "Update",
		"Status":     "Actual",
		"Event":      "rain",
		"SenderName": "Meteorological Service of New Zealand Limited",
	} {
		if got, _ := data[field].(string); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
	if areas, _ := data["Areas"].(string); !strings.Contains(areas, "Northland") {
		t.Errorf("Areas = %q, want it to name Northland", areas)
	}
	if expires, _ := data["Expires"].(time.Time); expires.IsZero() {
		t.Error("Expires is zero; templates cannot show when an alert lapses")
	}
}

func TestCAPTrigger_UnfetchableAlertIsNotRetriedForever(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	fs.alertCode.Store(http.StatusNotFound)

	tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL()})
	tr.poll(context.Background())

	fs.publish("gone")
	tr.poll(context.Background())
	tr.poll(context.Background())
	tr.poll(context.Background())

	if len(*fired) != 0 {
		t.Fatalf("fired %d events for an alert that cannot be read", len(*fired))
	}
	if hits := fs.alertHits.Load(); hits != 1 {
		t.Fatalf("fetched the dead alert %d times, want 1: a 404 will not become an alert", hits)
	}
}

func TestCAPTrigger_TransientFailureIsRetried(t *testing.T) {
	t.Parallel()
	fs := newFeedServer(t)
	fs.alertCode.Store(http.StatusServiceUnavailable)

	tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL()})
	tr.poll(context.Background())

	fs.publish("flaky")
	tr.poll(context.Background())
	if len(*fired) != 0 {
		t.Fatalf("fired despite a 503")
	}

	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(alert)
	fs.alertBody.Store(&body)
	fs.alertCode.Store(http.StatusOK)

	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Fatalf("fired %d events once the publisher recovered, want 1", len(*fired))
	}
}

// The fixture is a *Moderate* alert whose instruction text reads "A Severe Weather Warning ...
// favourable for severe weather". Matching one regex over every field joined would let a
// severity:Severe filter through on that prose alone, which is why patterns name a field.
func TestCAPTrigger_SeverityFilterIgnoresTheWordInProse(t *testing.T) {
	t.Parallel()
	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(alert), "Severe Weather Warning") {
		t.Fatal("fixture no longer contains the prose this test turns on")
	}
	body := string(alert)

	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"its own severity", `severity:^Moderate$`, true},
		{"a higher severity must not leak through the instruction text", `severity:(?i)severe`, false},
		{"its urgency", `urgency:^Immediate$`, true},
		{"its message type", `msgtype:^(Alert|Update)$`, true},
		{"the word in the field that really holds it", `instruction:(?i)severe weather`, true},
		{"its event", `event:(?i)rain`, true},
		{"an event it is not", `event:(?i)tsunami`, false},
		{"its area", `area:(?i)northland`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := newFeedServer(t)
			fs.alertBody.Store(&body)
			match := []string{c.pattern}
			tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
			tr.poll(context.Background())
			fs.publish("wx")
			tr.poll(context.Background())

			if got := len(*fired) == 1; got != c.want {
				t.Errorf("pattern %s fired=%v, want %v", c.pattern, got, c.want)
			}
		})
	}
}

func TestCAPTrigger_FieldsNarrowTogether(t *testing.T) {
	t.Parallel()
	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(alert)

	// Severity holds, area does not: narrowing by both must reject the alert.
	fs := newFeedServer(t)
	fs.alertBody.Store(&body)
	match := []string{`severity:^Moderate$`, `area:(?i)canterbury`}
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
	tr.poll(context.Background())
	fs.publish("wx")
	tr.poll(context.Background())

	if len(*fired) != 0 {
		t.Fatalf("fired with only one of two fields satisfied")
	}
}

// Meteoalarm lists a web page first and the CAP document second, typed; the typed one is the alert.
func TestCAPTrigger_PrefersTheCAPTypedLink(t *testing.T) {
	t.Parallel()
	alert, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	entries := ""
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>t</title>`+entries+`</feed>`)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<!doctype html><p>a web page</p>") })
	mux.HandleFunc("/cap", func(w http.ResponseWriter, _ *http.Request) { w.Write(alert) })

	tr, fired := newTestCAP(t, config.TriggerConfig{URL: srv.URL + "/feed"})
	tr.poll(context.Background())
	entries = `<entry><id>urn:test:1</id><title>Gale</title><updated>2026-09-25T14:45:30Z</updated>` +
		`<link href="` + srv.URL + `/page"/>` +
		`<link type="application/cap+xml" href="` + srv.URL + `/cap"/></entry>`
	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Fatalf("fired %d events, want 1: the alert was fetched from the web page link", len(*fired))
	}
}

// Publishers that name areas by code (the NWS, Meteoalarm) are filtered on the codes themselves.
func TestCAPTrigger_GeocodeField(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "</areaDesc>", "</areaDesc>"+
		"<geocode><valueName>UGC</valueName><value>NMZ001</value></geocode>"+
		"<geocode><valueName>UGC</valueName><value>TXC369</value></geocode>"+
		"<geocode><valueName>SAME</valueName><value>048369</value></geocode>", 1)
	if !strings.Contains(body, "TXC369") {
		t.Fatal("fixture no longer has an areaDesc to add geocodes after")
	}
	for _, c := range []struct {
		pattern string
		want    bool
	}{
		{`geocode:(?m)^UGC=TX`, true},
		{`geocode:(?m)^UGC=NM`, true},
		{`geocode:(?m)^UGC=OK`, false},
		{`geocode:(?m)^SAME=048`, true},
		{`geocode:^UGC=TX`, false}, // without (?m), ^ is the start of the whole field, the first code
	} {
		t.Run(c.pattern, func(t *testing.T) {
			fs := newFeedServer(t)
			fs.alertBody.Store(&body)
			match := []string{c.pattern}
			tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL(), Match: &match})
			tr.poll(context.Background())
			fs.publish("wx")
			tr.poll(context.Background())
			if got := len(*fired) == 1; got != c.want {
				t.Errorf("fired=%v, want %v", got, c.want)
			}
		})
	}
}

// capFeed serves an Atom feed whose entries each point, by a CAP-typed link, at one of several alert
// documents, the way Meteoalarm posts an alert once per area.
type capFeed struct {
	*httptest.Server
	entries []string // the document each entry links to, oldest first
	docs    map[string]string
	fetches map[string]int
	mu      sync.Mutex
}

func newCAPFeed(t *testing.T, docs map[string]string) *capFeed {
	t.Helper()
	f := &capFeed{docs: docs, fetches: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>t</title>`)
		for i, doc := range f.entries {
			fmt.Fprintf(&b, `<entry><id>urn:entry:%d</id><title>entry %d</title><updated>%s</updated>`+
				`<link href="%s/page"/><link type="application/cap+xml" href="%s/cap/%s"/></entry>`,
				i, i, time.Date(2026, 9, 25, 0, i, 0, 0, time.UTC).Format(time.RFC3339), f.URL, f.URL, doc)
		}
		b.WriteString(`</feed>`)
		io.WriteString(w, b.String())
	})
	mux.HandleFunc("/cap/{doc}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetches[r.PathValue("doc")]++
		f.mu.Unlock()
		io.WriteString(w, f.docs[r.PathValue("doc")])
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *capFeed) publish(entries ...string) {
	f.mu.Lock()
	f.entries = entries
	f.mu.Unlock()
}

// alertDoc is the fixture alert under another identifier.
func alertDoc(t *testing.T, identifier string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`<identifier>[^<]*</identifier>`)
	if !re.Match(raw) {
		t.Fatal("fixture has no identifier")
	}
	return re.ReplaceAllString(string(raw), "<identifier>"+identifier+"</identifier>")
}

func TestCAPTrigger_OneAlertUnderManyEntriesSendsOnce(t *testing.T) {
	t.Parallel()
	f := newCAPFeed(t, map[string]string{"fog": alertDoc(t, "fog-1"), "gale": alertDoc(t, "gale-1")})
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	tr.poll(context.Background())

	// One gale entry, then the fog alert under enough entries to have filled the old per-poll cap.
	entries := []string{"gale"}
	for range 47 {
		entries = append(entries, "fog")
	}
	f.publish(entries...)
	tr.poll(context.Background())

	got := map[string]int{}
	for _, ev := range *fired {
		got[ev.Data["Identifier"].(string)]++
	}
	if got["fog-1"] != 1 || got["gale-1"] != 1 || len(*fired) != 2 {
		t.Errorf("sent %v, want each alert once", got)
	}
	if f.fetches["fog"] != 1 {
		t.Errorf("fetched the fog alert %d times, want once", f.fetches["fog"])
	}

	// Another area added to an alert already sent does not send it again.
	f.publish(append(entries, "fog")...)
	tr.poll(context.Background())
	if len(*fired) != 2 {
		t.Errorf("a later entry for a sent alert sent it again: %d events", len(*fired))
	}
}

func TestCAPTrigger_CapCountsAlertsNotEntries(t *testing.T) {
	t.Parallel()
	docs := map[string]string{}
	var entries []string
	for i := range 7 {
		name := fmt.Sprintf("a%d", i)
		docs[name] = alertDoc(t, name)
		entries = append(entries, name, name) // two entries each
	}
	f := newCAPFeed(t, docs)
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	tr.poll(context.Background())
	f.publish(entries...)
	tr.poll(context.Background())

	var ids []string
	for _, ev := range *fired {
		ids = append(ids, ev.Data["Identifier"].(string))
	}
	if want := []string{"a2", "a3", "a4", "a5", "a6"}; !slices.Equal(ids, want) {
		t.Errorf("sent %v, want the newest %d alerts oldest first: %v", ids, feedMaxPerPoll, want)
	}
}

// The same alert behind a second link (a mirror, another language) is still the same alert.
func TestCAPTrigger_SameIdentifierUnderAnotherLinkSendsOnce(t *testing.T) {
	t.Parallel()
	doc := alertDoc(t, "same")
	f := newCAPFeed(t, map[string]string{"en": doc, "mirror": doc})
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	tr.poll(context.Background())
	f.publish("en", "mirror")
	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Errorf("sent %d events for one alert under two links, want 1", len(*fired))
	}
}

// Meteoalarm keeps one link per warning; an Update served there later is a new message and is sent.
func TestCAPTrigger_AnUpdateAtTheSameLinkIsSent(t *testing.T) {
	t.Parallel()
	f := newCAPFeed(t, map[string]string{"warning": alertDoc(t, "w-1")})
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	tr.poll(context.Background())
	f.publish("warning")
	tr.poll(context.Background())

	f.mu.Lock()
	f.docs["warning"] = alertDoc(t, "w-2")
	f.mu.Unlock()
	f.publish("warning", "warning") // a new entry, the same link, the updated document
	tr.poll(context.Background())

	var ids []string
	for _, ev := range *fired {
		ids = append(ids, ev.Data["Identifier"].(string))
	}
	if !slices.Equal(ids, []string{"w-1", "w-2"}) {
		t.Errorf("sent %v, want the alert and then its update", ids)
	}
}

// CAP names a message by sender, identifier and sent; the same identifier sent again is another message.
func TestCAPTrigger_SameIdentifierSentAgainIsAnotherMessage(t *testing.T) {
	t.Parallel()
	doc := alertDoc(t, "same")
	re := regexp.MustCompile(`<sent>[^<]*</sent>`)
	if !re.MatchString(doc) {
		t.Fatal("fixture has no sent")
	}
	later := re.ReplaceAllString(doc, "<sent>2026-09-26T09:00:00+12:00</sent>")
	f := newCAPFeed(t, map[string]string{"first": doc, "again": later})
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	tr.poll(context.Background())
	f.publish("first", "again")
	tr.poll(context.Background())
	if len(*fired) != 2 {
		t.Errorf("sent %d events, want 2: the sent time is part of a message's identity", len(*fired))
	}
}

// Two documents under one message key (a language each, say) are judged apart: one that fails the
// patterns must not hide one that passes.
func TestCAPTrigger_AFilteredCopyDoesNotHideOneThatPasses(t *testing.T) {
	t.Parallel()
	english := alertDoc(t, "same")
	other := strings.Replace(english, "<event>rain</event>", "<event>ua</event>", 1)
	if other == english {
		t.Fatal("fixture has no rain event to swap")
	}
	f := newCAPFeed(t, map[string]string{"en": english, "mi": other})
	match := []string{"event:^rain$"}
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed", Match: &match})
	tr.poll(context.Background())
	f.publish("en", "mi") // the one that fails is the newer, so it is judged first
	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Errorf("sent %d events, want the copy that matches", len(*fired))
	}
}

// A filtered bot fetches a bounded number of new alerts per poll and reads the rest on the next.
func TestCAPTrigger_ABurstIsReadOverSeveralPolls(t *testing.T) {
	t.Parallel()
	docs := map[string]string{}
	var entries []string
	for i := range capMaxFetchesPerPoll + 5 {
		name := fmt.Sprintf("a%02d", i)
		docs[name] = alertDoc(t, name)
		entries = append(entries, name)
	}
	docs["a00"] = strings.Replace(docs["a00"], "<event>rain</event>", "<event>tsunami</event>", 1)
	f := newCAPFeed(t, docs)
	match := []string{"event:^tsunami$"} // only the oldest alert
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed", Match: &match})
	tr.poll(context.Background())
	f.publish(entries...)

	tr.poll(context.Background())
	total := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, c := range f.fetches {
			n += c
		}
		return n
	}
	if got := total(); got != capMaxFetchesPerPoll || len(*fired) != 0 {
		t.Fatalf("first poll fetched %d and sent %d, want %d and 0", got, len(*fired), capMaxFetchesPerPoll)
	}
	tr.poll(context.Background())
	if got := total(); got != capMaxFetchesPerPoll+5 || len(*fired) != 1 {
		t.Errorf("second poll: fetched %d in all and sent %d, want %d and the oldest alert", got, len(*fired), capMaxFetchesPerPoll+5)
	}
}

// Meteoalarm adds an area to an alert already up when the bot started: that is backlog, not news.
// An update served at the same link after the start is news.
func TestCAPTrigger_AnAlertUpAtStartIsBacklog(t *testing.T) {
	t.Parallel()
	f := newCAPFeed(t, map[string]string{"fog": alertDoc(t, "fog")}) // sent in 2018, before the start
	tr, fired := newTestCAP(t, config.TriggerConfig{URL: f.URL + "/feed"})
	f.publish("fog")
	tr.poll(context.Background()) // the start: records what is up
	f.publish("fog", "fog")       // another area of the same alert
	tr.poll(context.Background())
	if len(*fired) != 0 {
		t.Fatalf("an alert up at the start went out when a new area was added: %d events", len(*fired))
	}

	re := regexp.MustCompile(`<sent>[^<]*</sent>`)
	f.mu.Lock()
	f.docs["fog"] = re.ReplaceAllString(alertDoc(t, "fog-update"), "<sent>"+time.Now().Add(time.Minute).Format(time.RFC3339)+"</sent>")
	f.mu.Unlock()
	f.publish("fog", "fog", "fog")
	tr.poll(context.Background())
	if len(*fired) != 1 {
		t.Errorf("an update sent after the start was not sent: %d events", len(*fired))
	}
}
