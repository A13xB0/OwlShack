package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"

	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/node/companion"
	"github.com/meshcore-go/OwlShack/internal/store"
)

type silentModem struct{}

func (silentModem) SendData([]byte) error                            { return nil }
func (silentModem) SetDataHandler(func([]byte, float32, int8, bool)) {}
func (silentModem) AddOutboundHandler(func([]byte))                  {}

// The repeater page read Flood after every restart while sends went direct: the table came up without the routes the contact rows held.
func TestHydratePeerTables_SeedsLearnedRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	k := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	comp := store.Companion{Name: "home"}
	// neverHeard is a contact added by key, or whose peer was deleted: no discovered_peers row to seed it from.
	direct, twoHop, unknown, neverHeard, learned := k(1), k(2), k(3), k(4), k(5)
	st.WriteSync(func() {
		if err = st.Companions.Create(ctx, &comp); err != nil {
			return
		}
		for _, pk := range [][]byte{direct, twoHop, unknown, learned} {
			if err = st.Peers.Upsert(ctx, &store.Peer{PubKey: pk, Name: "r", Type: "REPEATER"}); err != nil {
				return
			}
		}
		for _, pk := range [][]byte{direct, twoHop, unknown, neverHeard, learned} {
			if err = st.Contacts.Add(ctx, comp.ID, pk, "r", "REPEATER"); err != nil {
				return
			}
		}
		for _, r := range []struct {
			pk    []byte
			route []byte
			hs    uint8
		}{{direct, []byte{}, 1}, {twoHop, []byte{0xaa, 0xbb, 0xcc, 0xdd}, 2}, {neverHeard, []byte{0xe6}, 1}, {learned, []byte{0x11}, 1}} {
			if err = st.Contacts.UpdateOutPath(ctx, comp.ID, r.pk, r.route, r.hs); err != nil {
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	c, err := companion.NewCompanion(config.CompanionConfig{ID: comp.ID, Name: "home", PrivateKey: strings.Repeat("11", 32)},
		node.NewRadioMux(silentModem{}), st, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := func(pk []byte) [meshcore.PubKeySize]byte {
		var out [meshcore.PubKeySize]byte
		copy(out[:], pk)
		return out
	}
	// RX is live before hydrate runs, and what it learned there is fresher than the row.
	id, _ := meshcore.NewIdentityFromBytes(learned)
	c.Node().Peers().Insert(&node.Peer{Identity: id, Name: "r"})
	c.Node().Peers().SetOutPath(key(learned), []byte{0x22, 0x33}, 1)

	hydratePeerTables(ctx, st, []*companion.Companion{c})

	for name, tc := range map[string]struct {
		pk    []byte
		route []byte
		hs    uint8
	}{
		"direct neighbour":               {direct, []byte{}, 1},
		"two 2-byte hops":                {twoHop, []byte{0xaa, 0xbb, 0xcc, 0xdd}, 2},
		"no route saved floods":          {unknown, nil, 0},
		"never heard advertising":        {neverHeard, []byte{0xe6}, 1},
		"learned before hydrate is kept": {learned, []byte{0x22, 0x33}, 1},
	} {
		p := c.Node().Peers().Lookup(key(tc.pk))
		if p == nil {
			t.Errorf("%s: not in the peer table", name)
			continue
		}
		if (p.OutPath == nil) != (tc.route == nil) || !bytes.Equal(p.OutPath, tc.route) || (tc.route != nil && p.OutPathHashSize != tc.hs) {
			t.Errorf("%s: route %x (nil %v) at %d, want %x (nil %v) at %d", name, p.OutPath, p.OutPath == nil, p.OutPathHashSize, tc.route, tc.route == nil, tc.hs)
		}
	}
}
