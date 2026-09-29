// Package region holds Natural Earth's first-level regions (states, provinces, NZ's regions) for every country.
package region

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"sync"
)

//go:generate go run ./gen regions.bin.gz

//go:embed regions.bin.gz
var data []byte

// ErrNotFound is a region id the data does not have.
var ErrNotFound = errors.New("no such region")

// Region's ID is Natural Earth's adm1_code (ISO 3166-2 codes repeat); Rings are [lon, lat], read even-odd; Area is in SampledArea's units.
type Region struct {
	ID      string
	Name    string
	Country string
	Area    float64
	Rings   [][][2]float64

	box       Box
	bandsOnce sync.Once
	bands     *bandIndex
}

type Box struct{ MinLon, MinLat, MaxLon, MaxLat float64 }

func (b Box) intersect(o Box) (Box, bool) {
	r := Box{max(b.MinLon, o.MinLon), max(b.MinLat, o.MinLat), min(b.MaxLon, o.MaxLon), min(b.MaxLat, o.MaxLat)}
	return r, r.MinLon < r.MaxLon && r.MinLat < r.MaxLat
}

func (b Box) shift(dLon float64) Box {
	return Box{b.MinLon + dLon, b.MinLat, b.MaxLon + dLon, b.MaxLat}
}

// Part is one piece of a Shape, in the shape's longitude frame, which may run past ±180.
type Part struct {
	Box      Box
	Contains func(lon, lat float64) bool
}

// Shape is an area to compare, kept as parts so small ones far apart are each sampled at their own scale.
type Shape struct {
	Parts []Part
	Area  float64
}

// NewShape measures the union of parts, counting each point once however many parts cover it.
func NewShape(parts []Part) Shape {
	area := 0.0
	for i, p := range parts {
		area += SampledArea(p.Box, func(lon, lat float64) bool { return p.Contains(lon, lat) && !inAny(parts[:i], lon, lat) })
	}
	return Shape{Parts: parts, Area: area}
}

func inAny(parts []Part, lon, lat float64) bool {
	for _, q := range parts {
		if q.Contains(lon, lat) {
			return true
		}
	}
	return false
}

type index struct {
	byID map[string]*Region
	all  []*Region
}

// The data is decoded on first use, so a node that never picks a region never pays for it.
var load = sync.OnceValues(func() (*index, error) {
	all, err := Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("region data: %w", err)
	}
	idx := &index{byID: make(map[string]*Region, len(all)), all: all}
	for _, r := range all {
		r.box = RingsBox(r.Rings)
		idx.byID[r.ID] = r
	}
	return idx, nil
})

func ByID(id string) (*Region, error) {
	idx, err := load()
	if err != nil {
		return nil, err
	}
	r, ok := idx.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return r, nil
}

// At is the region containing the point, or nil over the sea.
func At(lat, lon float64) (*Region, error) {
	idx, err := load()
	if err != nil {
		return nil, err
	}
	for _, r := range idx.all {
		if lon >= r.box.MinLon && lon <= r.box.MaxLon && lat >= r.box.MinLat && lat <= r.box.MaxLat && r.contains(lon, lat) {
			return r, nil
		}
	}
	return nil, nil
}

func RingsBox(rings [][][2]float64) Box {
	b := Box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, ring := range rings {
		for _, p := range ring {
			b.MinLon, b.MaxLon = min(b.MinLon, p[0]), max(b.MaxLon, p[0])
			b.MinLat, b.MaxLat = min(b.MinLat, p[1]), max(b.MaxLat, p[1])
		}
	}
	return b
}

// InRing is the even-odd ray cast on one [lon, lat] ring.
func InRing(ring [][2]float64, lon, lat float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[i], ring[j]
		if (a[1] > lat) != (b[1] > lat) && lon < a[0]+(lat-a[1])*(b[0]-a[0])/(b[1]-a[1]) {
			in = !in
		}
	}
	return in
}

// bandIndex buckets a region's edges by latitude, so a point test reads only the edges near it.
type bandIndex struct {
	minLat, step float64
	bands        [][][4]float64
}

const bandCount = 256

func (r *Region) index() *bandIndex {
	r.bandsOnce.Do(func() {
		step := (r.box.MaxLat - r.box.MinLat) / bandCount
		if step <= 0 {
			step = 1
		}
		b := &bandIndex{minLat: r.box.MinLat, step: step, bands: make([][][4]float64, bandCount)}
		for _, ring := range r.Rings {
			for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
				e := [4]float64{ring[i][0], ring[i][1], ring[j][0], ring[j][1]}
				lo, hi := b.band(min(e[1], e[3])), b.band(max(e[1], e[3]))
				for k := lo; k <= hi; k++ {
					b.bands[k] = append(b.bands[k], e)
				}
			}
		}
		r.bands = b
	})
	return r.bands
}

func (b *bandIndex) band(lat float64) int {
	return max(0, min(bandCount-1, int((lat-b.minLat)/b.step)))
}

// contains is the even-odd test across every ring, through the band index.
func (r *Region) contains(lon, lat float64) bool {
	in := false
	b := r.index()
	for _, e := range b.bands[b.band(lat)] {
		if (e[1] > lat) != (e[3] > lat) && lon < e[0]+(lat-e[1])*(e[2]-e[0])/(e[3]-e[1]) {
			in = !in
		}
	}
	return in
}

// grid is the samples per side of a sampled area; 64x64 finds a 10 km² road warning in its box.
const grid = 64

// SampledArea samples contains over box, weighting each sample by the cosine of its latitude.
func SampledArea(box Box, contains func(lon, lat float64) bool) float64 {
	w, h := (box.MaxLon-box.MinLon)/grid, (box.MaxLat-box.MinLat)/grid
	sum := 0.0
	for y := range grid {
		lat := box.MinLat + (float64(y)+0.5)*h
		c := math.Cos(lat * math.Pi / 180)
		for x := range grid {
			if contains(box.MinLon+(float64(x)+0.5)*w, lat) {
				sum += c
			}
		}
	}
	return sum * w * h
}

// Share is the overlap as a fraction of the smaller of region and shape: a border sliver is near 0, a nationwide alert 1.
func (r *Region) Share(s Shape) float64 {
	smaller := min(r.Area, s.Area)
	if smaller <= 0 {
		return 0
	}
	overlap := 0.0
	for i, p := range s.Parts {
		// The region is tried at ±360 too, for a part past the antimeridian.
		for _, d := range []float64{0, 360, -360} {
			box, ok := r.box.shift(d).intersect(p.Box)
			if !ok {
				continue
			}
			overlap += SampledArea(box, func(lon, lat float64) bool {
				return p.Contains(lon, lat) && r.contains(lon-d, lat) && !inAny(s.Parts[:i], lon, lat)
			})
		}
	}
	return min(1, overlap/smaller)
}
