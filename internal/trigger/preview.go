package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/mmcdole/gofeed"
)

const (
	// previewTTL keeps a fetched feed and its decoded items for re-rendering, so editing a template does not hit the publisher on every keystroke.
	previewTTL = 10 * time.Minute
	// previewMaxItems is how many of the newest items a Test lists.
	previewMaxItems = 20
	// ponytail: the cache is dropped whole past this many entries, an LRU if editors ever share one server hard.
	previewMaxEntries = 64
)

// PreviewItem is one feed item a Test can render against.
type PreviewItem struct {
	ID        string
	Title     string
	Link      string
	Published time.Time
}

// PreviewResult is what the bot would send for one item, and whether its patterns would let it through at all.
type PreviewResult struct {
	Message     string
	RenderError string
	Matched     bool
	Placement   Placement
	Captures    map[string]string
}

// FeedPreview tries a feed bot without saving it: the same fetch, decode and match a poll does, with nothing sent.
type FeedPreview struct {
	templater *Templater

	mu    sync.Mutex
	feeds map[string]previewFeed
	items map[string]previewItem
}

type previewFeed struct {
	feed *gofeed.Feed
	at   time.Time
}

type previewItem struct {
	data   map[string]any
	fields map[string]string
	at     time.Time
}

func NewFeedPreview() *FeedPreview {
	return &FeedPreview{templater: NewTemplater(), feeds: map[string]previewFeed{}, items: map[string]previewItem{}}
}

// previewPoller builds the poller a saved bot would run, so decoding and matching cannot drift from the real thing.
func previewPoller(cfg config.TriggerConfig) (*feedPoller, error) {
	if err := cfg.ValidateFeedTest(); err != nil {
		return nil, err
	}
	log := slog.New(slog.DiscardHandler)
	if cfg.Type == "cap" {
		t, err := NewCAPTrigger("", cfg, log)
		if err != nil {
			return nil, err
		}
		return t.feedPoller, nil
	}
	t, err := NewRSSTrigger("", cfg, log)
	if err != nil {
		return nil, err
	}
	return t.feedPoller, nil
}

// Items fetches the feed afresh and lists its newest items, keeping the feed for Render.
func (f *FeedPreview) Items(ctx context.Context, cfg config.TriggerConfig) ([]PreviewItem, error) {
	p, err := previewPoller(cfg)
	if err != nil {
		return nil, err
	}
	feed, err := f.feed(ctx, p, true)
	if err != nil {
		return nil, err
	}
	out := []PreviewItem{}
	listed := map[string]bool{} // entries linking to one document are one alert, listed once
	for i := len(feed.Items) - 1; i >= 0 && len(out) < previewMaxItems; i-- {
		item := feed.Items[i]
		if p.documentKey != nil {
			if k := p.documentKey(item); k != "" {
				if listed[k] {
					continue
				}
				listed[k] = true
			}
		}
		if id := itemID(item); id != "" {
			out = append(out, PreviewItem{ID: id, Title: previewTitle(item), Link: item.Link, Published: itemTime(item)})
		}
	}
	return out, nil
}

// previewTitle names an item in the list; a CAP entry may carry no title, and a bare link tells an operator nothing.
func previewTitle(item *gofeed.Item) string {
	if t := strings.TrimSpace(item.Title); t != "" {
		return t
	}
	return truncateRunes(strings.Join(strings.Fields(item.Description), " "), 120)
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Render renders the template against one item as the bot would send it; a template that fails is reported in the result, not as an error.
func (f *FeedPreview) Render(ctx context.Context, cfg config.TriggerConfig, botName, id string) (PreviewResult, error) {
	p, err := previewPoller(cfg)
	if err != nil {
		return PreviewResult{}, err
	}
	decoded, err := f.item(ctx, p, id)
	if err != nil {
		return PreviewResult{}, err
	}
	captures := p.matcher.match(decoded.fields)
	res := PreviewResult{Matched: captures != nil, Placement: p.place(decoded.data), Captures: captures}
	if captures == nil {
		captures = map[string]string{}
	}
	data := maps.Clone(decoded.data)
	data["Match"] = captures
	res.Message, err = f.templater.Render(&Event{Type: cfg.Type, BotName: botName, Data: data}, cfg.Template)
	if err != nil {
		res.RenderError = templateErrorText(err)
	}
	return res, nil
}

// templateErrorText drops the wrappers text/template and Render put round a message, leaving "line 1: unclosed action".
func templateErrorText(err error) string {
	s := err.Error()
	for _, p := range []string{"parsing template: ", "executing template: "} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.Replace(s, "template: trigger:", "line ", 1)
}

func (f *FeedPreview) feed(ctx context.Context, p *feedPoller, fresh bool) (*gofeed.Feed, error) {
	key := p.kind + " " + p.url
	f.mu.Lock()
	cached, ok := f.feeds[key]
	f.mu.Unlock()
	if ok && !fresh && time.Since(cached.at) < previewTTL {
		return cached.feed, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, feedPollTimeout)
	defer cancel()
	feed, err := p.parser.ParseURLWithContext(p.url, fetchCtx)
	if err != nil {
		return nil, fmt.Errorf("fetching the feed: %w", err)
	}
	sort.Sort(feed)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.prune()
	f.feeds[key] = previewFeed{feed: feed, at: time.Now()}
	if fresh {
		// A fresh fetch may carry edited alerts under the same ids, so their decodes go too.
		for k := range f.items {
			if strings.HasPrefix(k, key+"\x00") {
				delete(f.items, k)
			}
		}
	}
	return feed, nil
}

func (f *FeedPreview) item(ctx context.Context, p *feedPoller, id string) (previewItem, error) {
	key := p.kind + " " + p.url + "\x00" + id
	f.mu.Lock()
	cached, ok := f.items[key]
	f.mu.Unlock()
	if ok && time.Since(cached.at) < previewTTL {
		return cached, nil
	}

	feed, err := f.feed(ctx, p, false)
	if err != nil {
		return previewItem{}, err
	}
	var item *gofeed.Item
	for _, it := range feed.Items {
		if itemID(it) == id {
			item = it
			break
		}
	}
	if item == nil {
		return previewItem{}, fmt.Errorf("that item is no longer in the feed; fetch the items again")
	}

	decodeCtx, cancel := context.WithTimeout(ctx, feedPollTimeout)
	defer cancel()
	data, fields, err := p.decode(decodeCtx, feed, item)
	if err != nil {
		return previewItem{}, fmt.Errorf("reading the item: %w", err)
	}
	decoded := previewItem{data: data, fields: fields, at: time.Now()}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.prune()
	f.items[key] = decoded
	return decoded, nil
}

// prune drops what has expired, and everything once the cache is full; the caller holds f.mu.
func (f *FeedPreview) prune() {
	for k, v := range f.feeds {
		if time.Since(v.at) >= previewTTL {
			delete(f.feeds, k)
		}
	}
	for k, v := range f.items {
		if time.Since(v.at) >= previewTTL {
			delete(f.items, k)
		}
	}
	if len(f.feeds)+len(f.items) >= previewMaxEntries {
		clear(f.feeds)
		clear(f.items)
	}
}
