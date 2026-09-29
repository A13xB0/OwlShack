package region

import (
	"bytes"
	"errors"
	"testing"
)

func TestAt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"Auckland CBD", -36.8485, 174.7633, "Auckland"},
		{"Masterton", -40.9511, 175.6573, "Wellington"},
		{"Christchurch", -43.532, 172.636, "Canterbury"},
		{"the Chathams, east of the antimeridian", -43.95, -176.55, "Chatham Islands"},
		{"Munich", 48.137, 11.575, "Bavaria"},
		{"Tokyo", 35.68, 139.76, "Tokyo"},
		{"Los Angeles", 34.05, -118.24, "California"},
		{"Monaco, a city-state a coarse tolerance blurs", 43.7384, 7.4246, "Monaco"},
		{"Funafuti atoll", -8.50408, 179.20558, "Tuvalu"},
		{"Tokelau, atolls across the antimeridian from NZ", -9.34751, -171.19348, "Tokelau"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := At(c.lat, c.lon)
			if err != nil {
				t.Fatal(err)
			}
			if r == nil {
				t.Fatalf("no region at %v,%v", c.lat, c.lon)
			}
			if got, _ := ByID(r.ID); got != r {
				t.Errorf("ByID(%q) does not return the same region", r.ID)
			}
			t.Logf("%s -> %s %s, %s", c.name, r.ID, r.Name, r.Country)
			if r.Name != c.want {
				t.Errorf("At = %s (%s), want %s", r.Name, r.ID, c.want)
			}
		})
	}
	if r, _ := At(-40, 165); r != nil {
		t.Errorf("the Tasman Sea is in %s", r.Name)
	}
}

func TestShare(t *testing.T) {
	t.Parallel()
	auckland, err := At(-36.8485, 174.7633)
	if err != nil || auckland == nil {
		t.Fatal("no Auckland")
	}
	box := func(minLon, minLat, maxLon, maxLat float64) Shape {
		b := Box{minLon, minLat, maxLon, maxLat}
		in := func(lon, lat float64) bool { return lon >= minLon && lon <= maxLon && lat >= minLat && lat <= maxLat }
		return NewShape([]Part{{Box: b, Contains: in}})
	}
	cases := []struct {
		name     string
		shape    Shape
		min, max float64
	}{
		{"a small box inland at Mt Eden is wholly inside", box(174.75, -36.89, 174.78, -36.87), 0.99, 1},
		{"a box over the whole North Island covers all of Auckland", box(172, -42, 179, -34), 0.95, 1},
		{"a box out at sea touches nothing", box(170, -40, 171, -39), 0, 0},
		{"a box straddling the Waikato border is part in", box(175.0, -37.4, 175.6, -37.0), 0.1, 0.9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := auckland.Share(c.shape); got < c.min || got > c.max {
				t.Errorf("Share = %.3f, want %.2f-%.2f", got, c.min, c.max)
			}
		})
	}
}

func TestEveryRegionIsFindable(t *testing.T) {
	t.Parallel()
	idx, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.all) < 4500 {
		t.Fatalf("only %d regions", len(idx.all))
	}
	for _, r := range idx.all {
		if r.ID == "" || r.Name == "" || len(r.Rings) == 0 || !(r.Area > 0) {
			t.Errorf("region %q %q: no id, name, outline or area (%v)", r.ID, r.Name, r.Area)
		}
	}
	if r, err := ByID("MDV-5230"); err != nil || r.Name != "Kaafu Atoll" {
		t.Errorf("an atoll that 1 km simplification loses: %v %v", r, err)
	}
}

// The band index must agree with a plain ray cast over every ring, including at band edges.
func TestContainsMatchesRayCast(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"NZL-3398", "NZL-5470", "CAN-634", "FJI-2619"} {
		r, err := ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		plain := func(lon, lat float64) bool {
			in := false
			for _, ring := range r.Rings {
				if InRing(ring, lon, lat) {
					in = !in
				}
			}
			return in
		}
		box := r.box
		for y := range 97 {
			for x := range 97 {
				lon := box.MinLon + (box.MaxLon-box.MinLon)*float64(x)/96
				lat := box.MinLat + (box.MaxLat-box.MinLat)*float64(y)/96
				if r.contains(lon, lat) != plain(lon, lat) {
					t.Fatalf("%s at %v,%v: index says %v, ray cast %v", r.Name, lat, lon, r.contains(lon, lat), plain(lon, lat))
				}
			}
		}
	}
}

func TestByIDNotFound(t *testing.T) {
	t.Parallel()
	if _, err := ByID("NZ-AUK"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByID of an unknown id = %v, want ErrNotFound", err)
	}
}

func BenchmarkDecode(b *testing.B) {
	for b.Loop() {
		if _, err := Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// Nunavut is the most detailed region, the worst case for one alert check.
func BenchmarkShareNunavut(b *testing.B) {
	r, err := ByID("CAN-634")
	if err != nil {
		b.Fatal(err)
	}
	box := Box{-100, 60, -80, 70}
	in := func(lon, lat float64) bool {
		return lon >= box.MinLon && lon <= box.MaxLon && lat >= box.MinLat && lat <= box.MaxLat
	}
	s := NewShape([]Part{{Box: box, Contains: in}})
	for b.Loop() {
		r.Share(s)
	}
}

// Areas are in cos(lat)-weighted square degrees; one is about 111.19² km².
func TestAreasMatchKnownSizes(t *testing.T) {
	t.Parallel()
	for id, km2 := range map[string]float64{
		"NZL-3398": 4_900,     // Auckland, Natural Earth's outline takes in the islands
		"NZL-3400": 45_000,    // Canterbury
		"USA-3563": 1_720_000, // Alaska, far enough north that one cosine per ring was 10% out
	} {
		r, err := ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		got := r.Area * 111.19 * 111.19
		if got < km2*0.85 || got > km2*1.15 {
			t.Errorf("%s: %.0f km², want about %.0f", r.Name, got, km2)
		}
	}
}

func TestDecodeRefusesDamage(t *testing.T) {
	t.Parallel()
	for name, b := range map[string][]byte{
		"truncated": data[:len(data)/2],
		"not gzip":  []byte("hello"),
		"empty":     nil,
	} {
		if _, err := Decode(bytes.NewReader(b)); err == nil {
			t.Errorf("%s data decoded without error", name)
		}
	}
}
