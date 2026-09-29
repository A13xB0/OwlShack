package trigger

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/region"
	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/atom"
	"github.com/tuzzmaniandevil/cap-go"
)

// CAPTrigger fires once per new alert in a CAP feed: the feed entry only announces the alert, so
// each new one is fetched and decoded as a CAP 1.2 document before the template sees it.
type CAPTrigger struct {
	*feedPoller

	// docs holds one parsed feed's fetched documents: one fetch per alert, and a new poll sees an update at the same link.
	docsMu   sync.Mutex
	docsFeed *gofeed.Feed
	docs     map[string]fetched
	fetches  int
}

const (
	// capFetchTimeout bounds one alert document's fetch, apart from the feed's own.
	capFetchTimeout = 15 * time.Second
	// capMaxFetchesPerPoll bounds a burst: a filtered bot that seldom sends would otherwise fetch every new alert at once.
	capMaxFetchesPerPoll = 20
)

type fetched struct {
	alert *cap.Alert
	err   error
}

func NewCAPTrigger(botName string, cfg config.TriggerConfig, log *slog.Logger) (*CAPTrigger, error) {
	p, err := newFeedPoller("cap", botName, cfg, log)
	if err != nil {
		return nil, err
	}
	t := &CAPTrigger{feedPoller: p}
	p.decode = t.decodeAlert
	p.parser.AtomTranslator = capAtomTranslator{}
	p.documentKey = func(item *gofeed.Item) string { return cmp.Or(item.Custom[capLinkKey], item.Link) }
	p.alertKey = alertKey
	var place func(areas []*cap.Area) Placement
	switch {
	case cfg.Location != nil:
		if cfg.Location.Lat == nil || cfg.Location.Lon == nil || cfg.Location.RadiusKm == nil {
			return nil, fmt.Errorf("location needs lat, lon and radiusKm")
		}
		var loc point
		loc.Lat, loc.Lon, loc.RadiusKm = cfg.Location.Values()
		place = func(areas []*cap.Area) Placement { return placeAlert(loc, areas) }
	case cfg.Regions != nil:
		regions := make([]*region.Region, 0, len(*cfg.Regions))
		for _, id := range *cfg.Regions {
			r, err := region.ByID(id)
			if err != nil {
				return nil, err
			}
			regions = append(regions, r)
		}
		place = func(areas []*cap.Area) Placement { return placeInRegions(regions, areas) }
	}
	if place != nil {
		p.place = func(data map[string]any) Placement {
			info, _ := data["Info"].(*cap.Info)
			if info == nil {
				return PlacementNoShape
			}
			return place(info.Area)
		}
	}
	return t, nil
}

// capMaxBytes bounds one alert document. Real CAP alerts run to tens of kilobytes; anything past
// this is not an alert and should not be read into memory.
const capMaxBytes = 4 << 20

// decodeAlert fetches the alert document a feed item links to and overlays its fields onto the
// entry's data. The CAP fields win where they collide: a CAP trigger's template is written against
// the alert, not the entry that announced it.
func (t *CAPTrigger) decodeAlert(ctx context.Context, feed *gofeed.Feed, item *gofeed.Item) (map[string]any, map[string]string, error) {
	data, _, err := decodeEntry(ctx, feed, item)
	if err != nil {
		return nil, nil, err
	}
	link := cmp.Or(item.Custom[capLinkKey], item.Link)
	if link == "" {
		return nil, nil, fmt.Errorf("%w: item has no link to an alert document", errItemPermanent)
	}

	alert, err := t.fetchOnce(ctx, feed, link)
	if err != nil {
		return nil, nil, err
	}

	info := primaryInfo(alert)
	if info == nil {
		return nil, nil, fmt.Errorf("%w: alert %s carries no info block", errItemPermanent, alert.Identifier)
	}

	areas := make([]string, 0, len(info.Area))
	for _, a := range info.Area {
		if a != nil && a.AreaDesc != "" {
			areas = append(areas, a.AreaDesc)
		}
	}

	data["Alert"] = alert
	data["Info"] = info
	data["Identifier"] = alert.Identifier
	data["Sender"] = alert.Sender
	data["Sent"] = alert.Sent.Time
	data["Status"] = alert.Status.String()
	data["MsgType"] = alert.MsgType.String()
	data["Event"] = info.Event
	data["Headline"] = deref(info.Headline)
	data["Severity"] = info.Severity.String()
	data["Urgency"] = info.Urgency.String()
	data["Certainty"] = info.Certainty.String()
	data["Description"] = deref(info.Description)
	data["Instruction"] = deref(info.Instruction)
	data["SenderName"] = deref(info.SenderName)
	data["Web"] = deref(info.Web)
	data["Areas"] = strings.Join(areas, ", ")
	data["Effective"] = capTime(info.Effective)
	data["Onset"] = capTime(info.Onset)
	data["Expires"] = capTime(info.Expires)
	data["Categories"] = categoryNames(info.Categories)

	fields := map[string]string{
		"event":       info.Event,
		"headline":    deref(info.Headline),
		"description": deref(info.Description),
		"instruction": deref(info.Instruction),
		"severity":    info.Severity.String(),
		"urgency":     info.Urgency.String(),
		"certainty":   info.Certainty.String(),
		"msgtype":     alert.MsgType.String(),
		"status":      alert.Status.String(),
		"area":        strings.Join(areas, "\n"),
		"geocode":     geocodeText(info.Area),
		"sender":      deref(info.SenderName),
		"category":    strings.Join(categoryNames(info.Categories), "\n"),
	}
	return data, fields, nil
}

func (t *CAPTrigger) fetchOnce(ctx context.Context, feed *gofeed.Feed, link string) (*cap.Alert, error) {
	t.docsMu.Lock()
	defer t.docsMu.Unlock()
	if t.docsFeed != feed {
		t.docsFeed, t.docs, t.fetches = feed, map[string]fetched{}, 0
	}
	if f, ok := t.docs[link]; ok {
		return f.alert, f.err
	}
	if t.fetches == capMaxFetchesPerPoll {
		return nil, errDeferred
	}
	t.fetches++
	fetchCtx, cancel := context.WithTimeout(ctx, capFetchTimeout)
	defer cancel()
	alert, err := t.fetchAlert(fetchCtx, link)
	t.docs[link] = fetched{alert, err}
	return alert, err
}

func (t *CAPTrigger) fetchAlert(ctx context.Context, url string) (*cap.Alert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errItemPermanent, err)
	}
	req.Header.Set("User-Agent", t.parser.UserAgent)
	req.Header.Set("Accept", "application/cap+xml, application/xml;q=0.9, */*;q=0.1")

	resp, err := t.parser.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching alert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A 4xx is the publisher saying this URL will never be an alert; a 5xx may pass.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, fmt.Errorf("%w: alert fetch returned %s", errItemPermanent, resp.Status)
		}
		return nil, fmt.Errorf("alert fetch returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, capMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading alert: %w", err)
	}
	if len(body) > capMaxBytes {
		return nil, fmt.Errorf("%w: alert document exceeds %d bytes", errItemPermanent, capMaxBytes)
	}

	var alert cap.Alert
	if err := xml.Unmarshal(body, &alert); err != nil {
		return nil, fmt.Errorf("%w: parsing alert: %v", errItemPermanent, err)
	}
	return &alert, nil
}

// primaryInfo picks the info block a template renders. CAP repeats the whole block per language;
// English is preferred because that is what the mesh reads.
// ponytail: no per-trigger language setting until someone runs a bot that needs one.
func primaryInfo(alert *cap.Alert) *cap.Info {
	var first *cap.Info
	for _, info := range alert.Info {
		if info == nil {
			continue
		}
		if first == nil {
			first = info
		}
		if info.Language == nil || strings.HasPrefix(strings.ToLower(*info.Language), "en") {
			return info
		}
	}
	return first
}

// geocodeText is every area's geocodes as sorted "valueName=value" lines (UGC=TXZ123, EMMA_ID=DE028).
func geocodeText(areas []*cap.Area) string {
	var lines []string
	for _, a := range areas {
		if a == nil {
			continue
		}
		for name, values := range a.Geocodes {
			for _, v := range values {
				lines = append(lines, name+"="+v)
			}
		}
	}
	slices.Sort(lines)
	return strings.Join(slices.Compact(lines), "\n")
}

func categoryNames(categories []cap.Category) []string {
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = c.String()
	}
	return names
}

func capTime(t *cap.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// alertKeep is how long past its expiry an alert is remembered, against a feed that keeps an expired alert up.
const alertKeep = 24 * time.Hour

// alertKey is CAP 1.2's name for a message, the sender,identifier,sent triple <references> uses.
func alertKey(data map[string]any) (string, time.Time, time.Time) {
	alert, _ := data["Alert"].(*cap.Alert)
	info, _ := data["Info"].(*cap.Info)
	if alert == nil || alert.Identifier == "" {
		return "", time.Time{}, time.Time{}
	}
	until := time.Now()
	if info != nil && info.Expires != nil && info.Expires.Time.After(until) {
		until = info.Expires.Time
	}
	return alert.Sender + "," + alert.Identifier + "," + alert.Sent.Time.UTC().Format(time.RFC3339), alert.Sent.Time, until.Add(alertKeep)
}

// capLinkKey holds an Atom entry's application/cap+xml link, which Meteoalarm lists after a web page.
const capLinkKey = "capLink"

type capAtomTranslator struct{ gofeed.DefaultAtomTranslator }

func (t capAtomTranslator) Translate(feed any) (*gofeed.Feed, error) {
	f, err := t.DefaultAtomTranslator.Translate(feed)
	af, ok := feed.(*atom.Feed)
	if err != nil || !ok || len(af.Entries) != len(f.Items) {
		return f, err
	}
	for i, e := range af.Entries {
		for _, l := range e.Links {
			if strings.HasPrefix(l.Type, "application/cap+xml") {
				if f.Items[i].Custom == nil {
					f.Items[i].Custom = map[string]string{}
				}
				f.Items[i].Custom[capLinkKey] = l.Href
				break
			}
		}
	}
	return f, nil
}

var _ Trigger = (*CAPTrigger)(nil)
