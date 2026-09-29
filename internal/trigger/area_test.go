package trigger

import (
	"context"
	"encoding/xml"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/region"
	"github.com/tuzzmaniandevil/cap-go"
)

func TestPlaceAlert(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	var alert cap.Alert
	if err := xml.Unmarshal(raw, &alert); err != nil {
		t.Fatal(err)
	}
	northland := primaryInfo(&alert).Area
	if len(northland) == 0 || len(northland[0].Polygons) == 0 {
		t.Fatal("fixture no longer carries a polygon")
	}
	circle := []*cap.Area{{Circles: cap.Circles{{Coordinates: []float64{174.7633, -36.8485}, Radius: 10_000}}}}
	// A box over the Chathams that crosses the antimeridian.
	chathams := []*cap.Area{{Polygons: cap.Polygons{{{{179, -44}, {-179, -44}, {-179, -43}, {179, -43}, {179, -44}}}}}}

	cases := []struct {
		name  string
		areas []*cap.Area
		loc   point
		want  Placement
	}{
		{"Whangarei is in Northland", northland, point{Lat: -35.725, Lon: 174.323}, PlacementInside},
		{"Wellington is not", northland, point{Lat: -41.29, Lon: 174.78, RadiusKm: 100}, PlacementOutside},
		{"Auckland is near enough with a wide margin", northland, point{Lat: -36.8485, Lon: 174.7633, RadiusKm: 200}, PlacementInside},
		{"Auckland is outside with none", northland, point{Lat: -36.8485, Lon: 174.7633}, PlacementOutside},
		{"inside a circle", circle, point{Lat: -36.9, Lon: 174.8}, PlacementInside},
		{"beyond a circle", circle, point{Lat: -37.8, Lon: 175.3}, PlacementOutside},
		{"beyond a circle, within the margin", circle, point{Lat: -37.8, Lon: 175.3, RadiusKm: 110}, PlacementInside},
		{"west of the antimeridian", chathams, point{Lat: -43.5, Lon: 179.9}, PlacementInside},
		{"east of the antimeridian", chathams, point{Lat: -43.5, Lon: -179.9}, PlacementInside},
		{"an alert with only a description", []*cap.Area{{AreaDesc: "Northland"}}, point{Lat: -35.725, Lon: 174.323, RadiusKm: 500}, PlacementNoShape},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := placeAlert(c.loc, c.areas); got != c.want {
				t.Errorf("placeAlert = %s, want %s", got, c.want)
			}
		})
	}
}

func TestCAPTrigger_LocationFilter(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	shaped := string(raw)
	start, end := strings.Index(shaped, "<polygon>"), strings.Index(shaped, "</polygon>")
	if start < 0 || end < 0 {
		t.Fatal("fixture no longer carries a polygon")
	}
	shapeless := shaped[:start] + shaped[end+len("</polygon>"):]

	whangarei := at(-35.725, 174.323, 0)
	cases := []struct {
		name  string
		alert string
		loc   *config.FeedLocation
		want  bool
	}{
		{"no location takes it", shaped, nil, true},
		{"a point it covers", shaped, whangarei, true},
		{"a point it does not", shaped, at(-41.29, 174.78, 0), false},
		{"no shape never matches a location", shapeless, whangarei, false},
		{"no shape, no location", shapeless, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := newFeedServer(t)
			fs.alertBody.Store(&c.alert)
			tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL(), Location: c.loc})
			tr.poll(context.Background())
			fs.publish("wx")
			tr.poll(context.Background())
			if got := len(*fired) == 1; got != c.want {
				t.Errorf("fired=%v, want %v", got, c.want)
			}
		})
	}
}

func TestPlaceInRegions(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	var alert cap.Alert
	if err := xml.Unmarshal(raw, &alert); err != nil {
		t.Fatal(err)
	}
	northlandAlert := primaryInfo(&alert).Area
	at := func(lat, lon float64) *region.Region {
		r, err := region.At(lat, lon)
		if err != nil || r == nil {
			t.Fatalf("no region at %v,%v", lat, lon)
		}
		return r
	}
	northland, auckland, canterbury := at(-35.725, 174.323), at(-36.8485, 174.7633), at(-43.532, 172.636)
	aucklandCircle := []*cap.Area{{Circles: cap.Circles{{Coordinates: []float64{174.7633, -36.8485}, Radius: 5_000}}}}

	cases := []struct {
		name    string
		regions []*region.Region
		areas   []*cap.Area
		want    Placement
	}{
		{"a Northland warning is in Northland", []*region.Region{northland}, northlandAlert, PlacementInside},
		{"and not in Canterbury", []*region.Region{canterbury}, northlandAlert, PlacementOutside},
		{"any one of the regions is enough", []*region.Region{canterbury, northland}, northlandAlert, PlacementInside},
		{"a circle in the CBD is in Auckland", []*region.Region{auckland}, aucklandCircle, PlacementInside},
		{"an alert with only a description", []*region.Region{northland}, []*cap.Area{{AreaDesc: "Northland"}}, PlacementNoShape},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := placeInRegions(c.regions, c.areas); got != c.want {
				t.Errorf("placeInRegions = %s, want %s", got, c.want)
			}
		})
	}
}

func TestCAPTrigger_RegionFilter(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cap_alert.xml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, c := range []struct {
		name    string
		regions []string
		want    bool
	}{
		{"its own region", []string{"NZL-3404"}, true},
		{"another region", []string{"NZL-3400"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := newFeedServer(t)
			fs.alertBody.Store(&body)
			regions := c.regions
			tr, fired := newTestCAP(t, config.TriggerConfig{URL: fs.feedURL(), Regions: &regions})
			tr.poll(context.Background())
			fs.publish("wx")
			tr.poll(context.Background())
			if got := len(*fired) == 1; got != c.want {
				t.Errorf("fired=%v, want %v", got, c.want)
			}
		})
	}
}

func boxArea(minLon, minLat, maxLon, maxLat float64) *cap.Area {
	return &cap.Area{Polygons: cap.Polygons{{{{minLon, minLat}, {maxLon, minLat}, {maxLon, maxLat}, {minLon, maxLat}, {minLon, minLat}}}}}
}

func TestPlaceInRegions_Edges(t *testing.T) {
	t.Parallel()
	byID := func(id string) *region.Region {
		r, err := region.ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	auckland, waikato, canterbury := byID("NZL-3398"), byID("NZL-5468"), byID("NZL-3400")
	chathams, northernFiji := byID("NZL-5470"), byID("FJI-2619")

	// Mostly Waikato, spilling about 4% into Auckland: the threshold is what keeps it out.
	spill := boxArea(175.0, -38.0, 175.8, -37.08)
	speck := boxArea(168.30, -46.42, 168.33, -46.40) // far away in Southland
	acrossTheLine := &cap.Area{Polygons: cap.Polygons{{{{179.5, -17}, {-179.5, -17}, {-179.5, -16}, {179.5, -16}, {179.5, -17}}}}}
	point := &cap.Area{Circles: cap.Circles{{Coordinates: []float64{174.7633, -36.8485}, Radius: 0}}}

	cases := []struct {
		name   string
		region *region.Region
		areas  []*cap.Area
		want   Placement
	}{
		{"a spill of a few percent is not the neighbour's", auckland, []*cap.Area{spill}, PlacementOutside},
		{"it is the region it covers", waikato, []*cap.Area{spill}, PlacementInside},
		{"a far-off speck does not inflate the spill", auckland, []*cap.Area{spill, speck}, PlacementOutside},
		{"a box across the antimeridian covers Fiji", northernFiji, []*cap.Area{acrossTheLine}, PlacementInside},
		{"and not a band round the world", canterbury, []*cap.Area{acrossTheLine}, PlacementOutside},
		{"nor the Chathams beside it", chathams, []*cap.Area{acrossTheLine}, PlacementOutside},
		{"a circle of radius 0 is a point", auckland, []*cap.Area{point}, PlacementInside},
		{"a small part is found beside a far one", canterbury, []*cap.Area{boxArea(172.620, -43.540, 172.647, -43.525), speck}, PlacementInside},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := placeInRegions([]*region.Region{c.region}, c.areas); got != c.want {
				t.Errorf("placeInRegions = %s, want %s", got, c.want)
			}
		})
	}
	s, _ := alertShape([]*cap.Area{spill})
	if share := auckland.Share(s); share < 0.01 || share >= regionShareMin {
		t.Errorf("the spill's share of Auckland is %.3f; the case no longer tests the threshold", share)
	}
}

func at(lat, lon, km float64) *config.FeedLocation {
	return &config.FeedLocation{Lat: &lat, Lon: &lon, RadiusKm: &km}
}
