// Command gen rebuilds regions.bin.gz from Natural Earth's admin-1 boundaries: go generate ./internal/region
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"slices"

	"github.com/meshcore-go/OwlShack/internal/region"
)

// Natural Earth is public domain; the release is pinned so a rebuild is reproducible.
const source = "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/v5.1.2/geojson/ne_10m_admin_1_states_provinces.geojson"

// Each ring is simplified to 1/detail of its width, at most maxTolerance (about 1 km), so a small city or an atoll keeps its shape.
const (
	detail       = 200
	maxTolerance = 0.01
)

type feature struct {
	Properties struct {
		Adm1Code string `json:"adm1_code"`
		Name     string `json:"name"`
		NameEn   string `json:"name_en"`
		Admin    string `json:"admin"`
	} `json:"properties"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

func main() {
	allowRemoved := flag.Bool("allow-removed", false, "write the file even though ids in the current one are gone; saved bots naming them will fail to load")
	flag.Parse()
	out := "regions.bin.gz"
	if flag.NArg() > 0 {
		out = flag.Arg(0)
	}

	resp, err := http.Get(source)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("fetching %s: %s", source, resp.Status)
	}
	var fc struct {
		Features []feature `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
		log.Fatal(err)
	}

	regions := make([]*region.Region, 0, len(fc.Features))
	points := 0
	for _, f := range fc.Features {
		var polys [][][][2]float64
		switch f.Geometry.Type {
		case "Polygon":
			var p [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &p); err != nil {
				log.Fatal(err)
			}
			polys = [][][][2]float64{p}
		case "MultiPolygon":
			if err := json.Unmarshal(f.Geometry.Coordinates, &polys); err != nil {
				log.Fatal(err)
			}
		default:
			log.Fatalf("%s: unexpected geometry %s", f.Properties.Adm1Code, f.Geometry.Type)
		}
		r := &region.Region{ID: f.Properties.Adm1Code, Name: cmp.Or(f.Properties.NameEn, f.Properties.Name, f.Properties.Admin), Country: f.Properties.Admin}
		// Holes are kept as ordinary rings, which the even-odd test reads right; a ring that collapses is retried finer.
		for _, p := range polys {
			for i, ring := range p {
				s := simplifyToFit(ring)
				if s == nil {
					continue
				}
				r.Rings = append(r.Rings, s)
				if i == 0 {
					r.Area += ringArea(s)
				} else {
					r.Area -= ringArea(s)
				}
			}
		}
		if len(r.Rings) == 0 || r.Area <= 0 {
			log.Fatalf("%s %s: no area left after simplifying", r.ID, r.Name)
		}
		for _, ring := range r.Rings {
			points += len(ring)
		}
		regions = append(regions, r)
	}
	slices.SortFunc(regions, func(a, b *region.Region) int { return cmp.Compare(a.ID, b.ID) })

	if old, err := os.ReadFile(out); err == nil && len(old) > 0 {
		prev, err := region.Decode(bytes.NewReader(old))
		if err != nil {
			log.Fatalf("reading the current %s: %v", out, err)
		}
		kept := map[string]bool{}
		for _, r := range regions {
			kept[r.ID] = true
		}
		var gone []string
		for _, r := range prev {
			if !kept[r.ID] {
				gone = append(gone, r.ID+" "+r.Name)
			}
		}
		if len(gone) > 0 && !*allowRemoved {
			log.Fatalf("%d region ids in the current file are gone, and a saved bot naming one would stop loading: %v (rerun with -allow-removed to accept)", len(gone), gone)
		}
	}

	var buf bytes.Buffer
	if err := region.Encode(&buf, regions); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d regions, %d points -> %s (%d bytes)\n", len(regions), points, out, buf.Len())
}

// simplifyToFit simplifies a ring at its own scale, retrying finer rather than letting it collapse.
func simplifyToFit(ring [][2]float64) [][2]float64 {
	base := min(maxTolerance, extent(ring)/detail)
	for _, tol := range []float64{base, base / 10, base / 100, 0} {
		if s := simplify(ring, tol); len(s) >= 4 {
			return s
		}
	}
	return nil
}

// ringArea is ∫cos(lat) in square degrees, region.SampledArea's units, as the boundary integral of sin(lat) dlon.
func ringArea(ring [][2]float64) float64 {
	sum := 0.0
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		sum += (ring[i][0] - ring[j][0]) * (math.Sin(ring[i][1]*math.Pi/180) + math.Sin(ring[j][1]*math.Pi/180)) / 2
	}
	return math.Abs(sum) * 180 / math.Pi
}

func extent(ring [][2]float64) float64 {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, c := range ring {
		minX, maxX = min(minX, c[0]), max(maxX, c[0])
		minY, maxY = min(minY, c[1]), max(maxY, c[1])
	}
	return max(maxX-minX, maxY-minY)
}

// simplify is Douglas-Peucker on a closed ring, rounded to a tenth of the tolerance, never finer than microdegrees.
func simplify(ring [][2]float64, tol float64) [][2]float64 {
	if len(ring) < 4 {
		return nil
	}
	keep := make([]bool, len(ring))
	keep[0], keep[len(ring)-1] = true, true
	// A closed ring's ends coincide, so split at the point farthest from the start first.
	far, best := 0, -1.0
	for i, p := range ring {
		if d := math.Hypot(p[0]-ring[0][0], p[1]-ring[0][1]); d > best {
			far, best = i, d
		}
	}
	keep[far] = true
	dp(ring, 0, far, tol, keep)
	dp(ring, far, len(ring)-1, tol, keep)

	scale := 1e6
	if tol > 0 {
		scale = min(scale, math.Pow(10, math.Ceil(-math.Log10(tol))+1))
	}
	out := make([][2]float64, 0, len(ring))
	for i, p := range ring {
		if !keep[i] {
			continue
		}
		q := [2]float64{math.Round(p[0]*scale) / scale, math.Round(p[1]*scale) / scale}
		if n := len(out); n > 0 && out[n-1] == q {
			continue
		}
		out = append(out, q)
	}
	return out
}

func dp(pts [][2]float64, a, b int, tol float64, keep []bool) {
	if b-a < 2 {
		return
	}
	idx, best := -1, tol
	for i := a + 1; i < b; i++ {
		if d := segDist(pts[i], pts[a], pts[b]); d > best {
			idx, best = i, d
		}
	}
	if idx < 0 {
		return
	}
	keep[idx] = true
	dp(pts, a, idx, tol, keep)
	dp(pts, idx, b, tol, keep)
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}
