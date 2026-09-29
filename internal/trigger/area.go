package trigger

import (
	"math"

	"github.com/meshcore-go/OwlShack/internal/region"
	"github.com/tuzzmaniandevil/cap-go"
)

// Placement is where an alert falls against a trigger's location.
type Placement string

const (
	PlacementAnywhere Placement = "anywhere" // the trigger has no location
	PlacementInside   Placement = "inside"
	PlacementOutside  Placement = "outside"
	// PlacementNoShape is an alert with no polygon or circle, which a location or region never matches.
	PlacementNoShape Placement = "noShape"
)

func (p Placement) sends() bool { return p == PlacementAnywhere || p == PlacementInside }

const earthRadiusKm = 6371.0

// point is a validated config.FeedLocation.
type point struct{ Lat, Lon, RadiusKm float64 }

// placeAlert reports whether any polygon or circle in areas covers loc, or comes within its radius.
func placeAlert(loc point, areas []*cap.Area) Placement {
	shaped := false
	for _, a := range areas {
		if a == nil {
			continue
		}
		for _, poly := range a.Polygons {
			if len(poly) == 0 || len(poly[0]) < 4 {
				continue
			}
			shaped = true
			if ringDistanceKm(loc, poly[0]) <= loc.RadiusKm {
				return PlacementInside
			}
		}
		for _, c := range a.Circles {
			if len(c.Coordinates) != 2 {
				continue
			}
			shaped = true
			if haversineKm(loc.Lat, loc.Lon, c.Coordinates[1], c.Coordinates[0]) <= c.Radius/1000+loc.RadiusKm {
				return PlacementInside
			}
		}
	}
	if !shaped {
		return PlacementNoShape
	}
	return PlacementOutside
}

// ringDistanceKm is 0 inside the ring, else the distance to its nearest edge, projected flat around loc.
func ringDistanceKm(loc point, ring [][]float64) float64 {
	cosLat := math.Cos(loc.Lat * math.Pi / 180)
	pts := make([][2]float64, len(ring))
	for i, c := range ring {
		dLon := math.Remainder(c[0]-loc.Lon, 360)
		pts[i] = [2]float64{
			earthRadiusKm * dLon * math.Pi / 180 * cosLat,
			earthRadiusKm * (c[1] - loc.Lat) * math.Pi / 180,
		}
	}

	if region.InRing(pts, 0, 0) {
		return 0
	}
	best := math.Inf(1)
	for i := 0; i+1 < len(pts); i++ {
		best = min(best, segmentDistance(pts[i], pts[i+1]))
	}
	return best
}

func segmentDistance(a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, -(a[0]*dx+a[1]*dy)/l))
	}
	return math.Hypot(a[0]+t*dx, a[1]+t*dy)
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
}

// regionShareMin: on live MetService alerts, district-line slivers came to about 1% and the smallest real overlap to 17%.
const regionShareMin = 0.10

// placeInRegions reports whether the alert's shapes, taken together, cover enough of any region.
func placeInRegions(regions []*region.Region, areas []*cap.Area) Placement {
	shape, ok := alertShape(areas)
	if !ok {
		return PlacementNoShape
	}
	for _, r := range regions {
		if r.Share(shape) >= regionShareMin {
			return PlacementInside
		}
	}
	return PlacementOutside
}

// minCircleKm stands in for a CAP circle of radius 0, which is a point but needs some area to overlap.
const minCircleKm = 0.1

// alertShape is every polygon and circle in the alert, false when it has none, unwrapped into one longitude frame.
func alertShape(areas []*cap.Area) (region.Shape, bool) {
	var parts []region.Part
	frame := math.NaN() // the first part's longitude; every later part is moved within 180° of it
	into := func(lon float64) float64 {
		if math.IsNaN(frame) {
			frame = lon
		}
		return frame + math.Remainder(lon-frame, 360)
	}
	for _, a := range areas {
		if a == nil {
			continue
		}
		for _, poly := range a.Polygons {
			if len(poly) == 0 || len(poly[0]) < 4 {
				continue
			}
			ring := make([][2]float64, len(poly[0]))
			ring[0] = [2]float64{into(poly[0][0][0]), poly[0][0][1]}
			for i := 1; i < len(ring); i++ {
				prev := ring[i-1][0]
				ring[i] = [2]float64{prev + math.Remainder(poly[0][i][0]-prev, 360), poly[0][i][1]}
			}
			parts = append(parts, region.Part{
				Box:      region.RingsBox([][][2]float64{ring}),
				Contains: func(lon, lat float64) bool { return region.InRing(ring, lon, lat) },
			})
		}
		for _, c := range a.Circles {
			if len(c.Coordinates) != 2 {
				continue
			}
			lat, lon, km := c.Coordinates[1], into(c.Coordinates[0]), max(c.Radius/1000, minCircleKm)
			dLat := km / earthRadiusKm * 180 / math.Pi
			dLon := min(180, dLat/max(math.Cos(lat*math.Pi/180), 0.01))
			parts = append(parts, region.Part{
				Box:      region.Box{MinLon: lon - dLon, MinLat: lat - dLat, MaxLon: lon + dLon, MaxLat: lat + dLat},
				Contains: func(x, y float64) bool { return haversineKm(y, x, lat, lon) <= km },
			})
		}
	}
	if len(parts) == 0 {
		return region.Shape{}, false
	}
	return region.NewShape(parts), true
}
