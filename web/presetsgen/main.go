// Command presetsgen rebuilds radio-presets.json from MeshCore's suggested radio settings: go generate ./web
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/meshcore-go/meshcore-go/hardware/sx12xx"

	"github.com/meshcore-go/OwlShack/internal/config"
)

const source = "https://api.meshcore.nz/api/v1/config"

// Settings offers only these, and an SX12xx radio takes no others.
var loraBandwidths = []sx12xx.Bandwidth{
	sx12xx.BW7800, sx12xx.BW10400, sx12xx.BW15600, sx12xx.BW20800, sx12xx.BW31250,
	sx12xx.BW41700, sx12xx.BW62500, sx12xx.BW125000, sx12xx.BW250000, sx12xx.BW500000,
}

// json.Number takes a quoted number too, since the feed says its format may change without notice.
type entry struct {
	Title           string      `json:"title"`
	Frequency       json.Number `json:"frequency"`
	SpreadingFactor json.Number `json:"spreading_factor"`
	Bandwidth       json.Number `json:"bandwidth"`
	CodingRate      json.Number `json:"coding_rate"`
	NetworkSettings struct {
		PathHashSize json.Number `json:"path_hash_size"`
	} `json:"network_settings"`
}

type preset struct {
	name         string
	freq, bw     float64
	sf, cr       int
	pathHashSize int
}

// build turns the feed into presets, skipping any entry the Settings page would refuse; skipped says why each was left out.
func build(body []byte) (presets []preset, skipped []string, err error) {
	var feed struct {
		Config struct {
			SuggestedRadioSettings struct {
				Entries []json.RawMessage `json:"entries"`
			} `json:"suggested_radio_settings"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil, nil, fmt.Errorf("decoding the feed: %w", err)
	}
	seen := map[string]bool{}
	for i, raw := range feed.Config.SuggestedRadioSettings.Entries {
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			skipped = append(skipped, fmt.Sprintf("entry %d: %v", i, err))
			continue
		}
		p, err := toPreset(e)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("entry %d %q: %v", i, e.Title, err))
			continue
		}
		if seen[p.name] {
			skipped = append(skipped, fmt.Sprintf("entry %d %q: a second preset with this name", i, p.name))
			continue
		}
		seen[p.name] = true
		presets = append(presets, p)
	}
	return presets, skipped, nil
}

func toPreset(e entry) (preset, error) {
	p := preset{name: strings.TrimSpace(e.Title)}
	if p.name == "" {
		return p, errors.New("no title")
	}
	var err error
	if p.freq, err = strconv.ParseFloat(string(e.Frequency), 64); err != nil {
		return p, fmt.Errorf("frequency %q", e.Frequency)
	}
	if p.bw, err = strconv.ParseFloat(string(e.Bandwidth), 64); err != nil {
		return p, fmt.Errorf("bandwidth %q", e.Bandwidth)
	}
	if hz := math.Round(p.bw * 1000); math.Abs(p.bw*1000-hz) > 1e-6 || !slices.Contains(loraBandwidths, sx12xx.Bandwidth(hz)) {
		return p, fmt.Errorf("bandwidth %q kHz is not a LoRa bandwidth", e.Bandwidth)
	}
	if p.sf, err = strconv.Atoi(string(e.SpreadingFactor)); err != nil {
		return p, fmt.Errorf("spreading factor %q", e.SpreadingFactor)
	}
	if p.cr, err = strconv.Atoi(string(e.CodingRate)); err != nil {
		return p, fmt.Errorf("coding rate %q", e.CodingRate)
	}
	sf, cr := uint8(p.sf), uint8(p.cr)
	cfg := config.Config{Freq: &p.freq, Bw: &p.bw, SF: &sf, CR: &cr}
	if e.NetworkSettings.PathHashSize != "" {
		if p.pathHashSize, err = strconv.Atoi(string(e.NetworkSettings.PathHashSize)); err != nil {
			return p, fmt.Errorf("path_hash_size %q", e.NetworkSettings.PathHashSize)
		}
		cfg.PathHashSize = &p.pathHashSize
	}
	if p.sf != int(sf) || p.cr != int(cr) {
		return p, fmt.Errorf("out of range")
	}
	if err := cfg.Validate(); err != nil {
		return p, err
	}
	return p, nil
}

// render writes one preset per line, in the feed's order, so a regenerated file diffs by preset.
func render(presets []preset) []byte {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, p := range presets {
		name, _ := json.Marshal(p.name)
		fmt.Fprintf(&b, `  { "name": %s, "freq": %s, "sf": %d, "bw": %s, "cr": %d`,
			name, strconv.FormatFloat(p.freq, 'f', -1, 64), p.sf, strconv.FormatFloat(p.bw, 'f', -1, 64), p.cr)
		if p.pathHashSize != 0 {
			fmt.Fprintf(&b, `, "pathHashSize": %d`, p.pathHashSize)
		}
		b.WriteString(" }")
		if i < len(presets)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	return b.Bytes()
}

func fetch() ([]byte, error) {
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(source)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", source, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", source, err)
	}
	return body, nil
}

// generate renders the feed against the current file, refusing a feed that suddenly lost most of its entries, which is more likely broken than pruned.
func generate(body, current []byte) (next []byte, skipped []string, err error) {
	presets, skipped, err := build(body)
	if err != nil {
		return nil, skipped, err
	}
	if have := bytes.Count(current, []byte(`"name"`)); len(presets) == 0 || len(presets) < have/2 {
		return nil, skipped, fmt.Errorf("the feed gave %d usable presets against %d in the file", len(presets), have)
	}
	return render(presets), skipped, nil
}

func main() {
	check := flag.Bool("check", false, "exit 1 if the file differs from the feed, without writing it; a feed that is down or unusable only warns")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: presetsgen [-check] <radio-presets.json>")
	}
	out := flag.Arg(0)
	current, err := os.ReadFile(out)
	if err != nil {
		log.Fatal(err)
	}

	body, err := fetch()
	var next []byte
	var skipped []string
	if err == nil {
		next, skipped, err = generate(body, current)
	}
	if *check {
		// Regenerating cannot fix the feed, so only a real difference may stop a release.
		if err != nil {
			fmt.Printf("::warning::radio presets not checked: %v\n", err)
			return
		}
		for _, s := range skipped {
			fmt.Printf("::warning::radio preset skipped: %s\n", s)
		}
		if !bytes.Equal(current, next) {
			fmt.Printf("::error::%s is out of date with %s: run go generate ./web and commit it\n", out, source)
			os.Exit(1)
		}
		fmt.Printf("%s matches the feed (%d skipped)\n", out, len(skipped))
		return
	}
	for _, s := range skipped {
		log.Printf("skipped %s", s)
	}
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(out, next, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", out)
}
