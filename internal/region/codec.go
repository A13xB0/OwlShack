package region

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// The gzipped file is magic, a count, then per region its strings, area bits and zigzag varint microdegree deltas.
const (
	magic = "OWLRGN1\n"
	micro = 1e6

	// Bounds far past the real data, so a corrupt length fails rather than allocating wildly.
	maxRegions = 1 << 16
	maxRings   = 1 << 16
	maxPoints  = 1 << 22
	maxString  = 1 << 10
)

// Encode writes regions in the embedded format.
func Encode(w io.Writer, regions []*Region) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(gz)
	buf := make([]byte, binary.MaxVarintLen64)
	uvarint := func(v uint64) { bw.Write(buf[:binary.PutUvarint(buf, v)]) }
	varint := func(v int64) { bw.Write(buf[:binary.PutVarint(buf, v)]) }
	str := func(s string) { uvarint(uint64(len(s))); bw.WriteString(s) }

	bw.WriteString(magic)
	uvarint(uint64(len(regions)))
	for _, r := range regions {
		str(r.ID)
		str(r.Name)
		str(r.Country)
		uvarint(math.Float64bits(r.Area))
		uvarint(uint64(len(r.Rings)))
		for _, ring := range r.Rings {
			uvarint(uint64(len(ring)))
			var px, py int64
			for _, p := range ring {
				x, y := int64(math.Round(p[0]*micro)), int64(math.Round(p[1]*micro))
				varint(x - px)
				varint(y - py)
				px, py = x, y
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return gz.Close()
}

// Decode reads regions written by Encode.
func Decode(r io.Reader) ([]*Region, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(gz)
	head := make([]byte, len(magic))
	if _, err := io.ReadFull(br, head); err != nil || string(head) != magic {
		return nil, errors.New("not a region file")
	}
	var rerr error
	uvarint := func() uint64 {
		v, err := binary.ReadUvarint(br)
		if err != nil && rerr == nil {
			rerr = err
		}
		return v
	}
	varint := func() int64 {
		v, err := binary.ReadVarint(br)
		if err != nil && rerr == nil {
			rerr = err
		}
		return v
	}
	bounded := func(limit uint64) uint64 {
		v := uvarint()
		if v > limit && rerr == nil {
			rerr = fmt.Errorf("length %d over %d", v, limit)
		}
		return min(v, limit)
	}
	str := func() string {
		b := make([]byte, bounded(maxString))
		if _, err := io.ReadFull(br, b); err != nil && rerr == nil {
			rerr = err
		}
		return string(b)
	}

	n := bounded(maxRegions)
	out := make([]*Region, 0, n)
	for range n {
		r := &Region{ID: str(), Name: str(), Country: str(), Area: math.Float64frombits(uvarint())}
		r.Rings = make([][][2]float64, bounded(maxRings))
		for i := range r.Rings {
			ring := make([][2]float64, bounded(maxPoints))
			var x, y int64
			for j := range ring {
				x += varint()
				y += varint()
				ring[j] = [2]float64{float64(x) / micro, float64(y) / micro}
			}
			r.Rings[i] = ring
		}
		if rerr != nil {
			return nil, fmt.Errorf("region %d: %w", len(out), rerr)
		}
		out = append(out, r)
	}
	// Reading to the end is what makes gzip check its CRC, and anything left over is not this format.
	if n, err := io.Copy(io.Discard, br); err != nil || n > 0 {
		return nil, fmt.Errorf("region file: %d trailing bytes, %v", n, err)
	}
	return out, nil
}
