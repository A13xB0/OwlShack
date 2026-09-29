package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func triggerTestConfig(in api.TriggerTestInput) (config.TriggerConfig, error) {
	loc, err := locationFromAPI(in.Location)
	if err != nil {
		return config.TriggerConfig{}, api.Invalid(err)
	}
	cfg := config.TriggerConfig{Type: in.Type, URL: in.URL, Template: in.Template, Location: loc, Regions: in.Regions}
	if len(in.Match) > 0 {
		cfg.Match = &in.Match
	}
	return cfg, nil
}

// locationFromAPI refuses a location missing a field, which would otherwise read as 0.
func locationFromAPI(l *api.TriggerLocation) (*config.FeedLocation, error) {
	if l != nil && (l.Lat == nil || l.Lon == nil || l.RadiusKm == nil) {
		return nil, errors.New("location needs lat, lon and radiusKm")
	}
	return (*config.FeedLocation)(l), nil
}

func locationToStore(l *config.FeedLocation) *store.TriggerLocation {
	if l == nil || l.Lat == nil || l.Lon == nil || l.RadiusKm == nil {
		return nil
	}
	lat, lon, km := l.Values()
	return &store.TriggerLocation{Lat: lat, Lon: lon, RadiusKm: km}
}

func locationFromStore(l *store.TriggerLocation) *config.FeedLocation {
	if l == nil {
		return nil
	}
	lat, lon, km := l.Lat, l.Lon, l.RadiusKm
	return &config.FeedLocation{Lat: &lat, Lon: &lon, RadiusKm: &km}
}

func (b *backend) TestTriggerItems(ctx context.Context, in api.TriggerTestInput) ([]api.TriggerTestItem, error) {
	cfg, err := triggerTestConfig(in)
	if err != nil {
		return nil, err
	}
	items, err := b.feedPreview.Items(ctx, cfg)
	if err != nil {
		return nil, api.Invalid(err)
	}
	out := make([]api.TriggerTestItem, 0, len(items))
	for _, it := range items {
		row := api.TriggerTestItem{ID: it.ID, Title: it.Title, Link: it.Link}
		if !it.Published.IsZero() {
			at := it.Published.UTC().Format(time.RFC3339)
			row.Published = &at
		}
		out = append(out, row)
	}
	return out, nil
}

func (b *backend) TestTriggerRender(ctx context.Context, in api.TriggerTestInput) (api.TriggerTestRender, error) {
	// The companion sends it, so its name is both {{.BotName}} and the prefix a channel message spends bytes on.
	comp, err := b.db.Companions.Get(ctx, in.CompanionID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.TriggerTestRender{}, api.Invalid(fmt.Errorf("no companion %d to send as", in.CompanionID))
	}
	if err != nil {
		return api.TriggerTestRender{}, err
	}
	if in.ItemID == "" {
		return api.TriggerTestRender{}, api.Invalid(errors.New("itemId is required: pick an item to render against"))
	}
	cfg, err := triggerTestConfig(in)
	if err != nil {
		return api.TriggerTestRender{}, err
	}
	res, err := b.feedPreview.Render(ctx, cfg, comp.Name, in.ItemID)
	if err != nil {
		return api.TriggerTestRender{}, api.Invalid(err)
	}
	limit := max(0, meshcore.MaxTextLen-len(comp.Name+": "))
	out := api.TriggerTestRender{
		Message: res.Message, RenderError: res.RenderError, Matched: res.Matched, Placement: string(res.Placement), Captures: res.Captures,
		Bytes: len(res.Message), ChannelText: meshcore.TruncateUTF8(res.Message, limit), ChannelLimit: limit,
		DMLimit: companion.MaxDMTextBytes,
	}
	if out.Captures == nil {
		out.Captures = map[string]string{}
	}
	return out, nil
}
