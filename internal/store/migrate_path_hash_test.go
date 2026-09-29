package store

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"
)

type upgradeRow struct {
	companion int64
	pubkey    []byte
	hs        uint8
	route     []byte
}

// upgradeTo20 builds a v19 database with the given settings size, runs the store's upgrade, and checks each contact.
func upgradeTo20(t *testing.T, settingsSize string, seed func(db *sql.DB), want map[string]upgradeRow) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db := dbAt(t, path, 19)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO settings (id, path_hash_size) VALUES (1, `+settingsSize+`)`); err != nil {
		t.Fatal(err)
	}
	seed(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for name, w := range want {
		ct, err := st.Contacts.Get(t.Context(), w.companion, w.pubkey)
		if err != nil || ct == nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ct.PathHashSize != w.hs || (ct.OutPath == nil) != (w.route == nil) || !bytes.Equal(ct.OutPath, w.route) {
			t.Errorf("%s: %d bytes per hop, route %x (nil %v); want %d, %x (nil %v)",
				name, ct.PathHashSize, ct.OutPath, ct.OutPath == nil, w.hs, w.route, w.route == nil)
		}
	}
	return st
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// Each contact gets the size its peer floods adverts at, else its companion's, else the node's; a saved route at another size is dropped.
func TestStore_UpgradeContactPathHashSize(t *testing.T) {
	t.Parallel()
	k := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	st := upgradeTo20(t, "3", func(db *sql.DB) {
		exec(t, db, `INSERT INTO companions (id, name, private_key, pubkey, path_hash_size) VALUES (1, 'home', '', 'aa', NULL), (2, 'two', '', 'bb', 2)`)
		exec(t, db, `INSERT INTO discovered_peers (pubkey, name, type, out_path, out_path_hash_size) VALUES
			(?, 'r2', 'REPEATER', X'bcbc', 2), (?, 'zero-hop', 'REPEATER', X'', 1), (?, 'neighbour', 'REPEATER', X'', 2), (?, 'kept', 'REPEATER', X'bcbc', 2)`,
			k(2), k(3), k(6), k(7))
		exec(t, db, `INSERT INTO companion_contacts (companion_id, peer_pubkey, name, type, out_path, out_path_hash_size) VALUES
			(1, ?, 'r2', 'REPEATER', X'e640', 1),
			(1, ?, 'zero-hop', 'REPEATER', X'', 1),
			(2, ?, 'chat', 'CHAT', X'e640', 1),
			(1, ?, 'room', 'ROOM', X'aabbccdd', 2),
			(1, ?, 'neighbour', 'REPEATER', X'', 1),
			(1, ?, 'kept', 'REPEATER', X'e640a1b2', 2),
			(2, ?, 'legacy', 'CHAT', X'e640', 0)`,
			k(2), k(3), k(4), k(5), k(6), k(7), k(8))
		exec(t, db, `UPDATE companions SET path_hash_size = 1 WHERE id = 2`)
	}, map[string]upgradeRow{
		"advertised at 2, its 1-byte route dropped":                     {1, k(2), 2, nil},
		"zero-hop advert only: the node's 3; a 0-hop route has no size": {1, k(3), 3, []byte{}},
		"chat contact at its companion's 1 keeps its 1-byte route":      {2, k(4), 1, []byte{0xe6, 0x40}},
		"nothing advertised: node's 3, its 2-byte route dropped":        {1, k(5), 3, nil},
		"a neighbour's flood advert heard at 0 hops keeps its 2":        {1, k(6), 2, []byte{}},
		"a route already at the advertised size is kept":                {1, k(7), 2, []byte{0xe6, 0x40, 0xa1, 0xb2}},
		"a legacy size of 0 was sent at 1, so it is kept at 1":          {2, k(8), 1, []byte{0xe6, 0x40}},
	})
	for pk, want := range map[byte]uint8{3: 0, 6: 2} {
		if p, _ := st.Peers.GetByPubKey(t.Context(), k(pk)); p == nil || p.OutPathHashSize != want {
			t.Errorf("advert size for %x: %+v, want %d", pk, p, want)
		}
	}
}

// With no usable size anywhere, a working route keeps the size it was learned at rather than dropping to 1.
func TestStore_UpgradeFallsBackToTheRoutesOwnSize(t *testing.T) {
	t.Parallel()
	k := bytes.Repeat([]byte{9}, 32)
	upgradeTo20(t, "NULL", func(db *sql.DB) {
		exec(t, db, `INSERT INTO companions (id, name, private_key, pubkey) VALUES (1, 'home', '', 'aa')`)
		exec(t, db, `INSERT INTO discovered_peers (pubkey, name, type, out_path, out_path_hash_size) VALUES (?, 'r', 'REPEATER', X'aabb', 4)`, k)
		exec(t, db, `INSERT INTO companion_contacts (companion_id, peer_pubkey, name, type, out_path, out_path_hash_size) VALUES (1, ?, 'r', 'REPEATER', X'aabb', 2)`, k)
	}, map[string]upgradeRow{"bad advert size, nothing configured": {1, k, 2, []byte{0xaa, 0xbb}}})
}

// Set on add from the advert heard, kept on a re-add, and a zero-hop advert never overwrites a known size.
func TestContacts_AddTakesTheAdvertisedSize(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "add.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	comp := Companion{Name: "home"}
	if err := st.Companions.Create(t.Context(), &comp); err != nil {
		t.Fatal(err)
	}
	pk := bytes.Repeat([]byte{7}, 32)
	if err := st.Peers.Upsert(t.Context(), &Peer{PubKey: pk, Name: "r", Type: "REPEATER", OutPath: []byte{0xbc, 0xbc}, OutPathHashSize: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.Peers.Upsert(t.Context(), &Peer{PubKey: pk, Name: "r", Type: "REPEATER", OutPath: []byte{}, OutPathHashSize: 0}); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Peers.GetByPubKey(t.Context(), pk); p.OutPathHashSize != 2 {
		t.Fatalf("a zero-hop advert overwrote the flood advert's size with %d", p.OutPathHashSize)
	}
	if err := st.Contacts.Add(t.Context(), comp.ID, pk, "r", "REPEATER"); err != nil {
		t.Fatal(err)
	}
	get := func(pk []byte) uint8 {
		ct, err := st.Contacts.Get(t.Context(), comp.ID, pk)
		if err != nil || ct == nil {
			t.Fatalf("get: %v", err)
		}
		return ct.PathHashSize
	}
	if got := get(pk); got != 2 {
		t.Fatalf("added at %d bytes per hop, want the advertised 2", got)
	}
	if err := st.Contacts.SetRoute(t.Context(), comp.ID, pk, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.Add(t.Context(), comp.ID, pk, "r", "REPEATER"); err != nil {
		t.Fatal(err)
	}
	if got := get(pk); got != 1 {
		t.Errorf("a re-add reset the operator's 1 to %d", got)
	}

	// Heard only zero-hop, the size is unknown, so the node's own is used.
	zero := bytes.Repeat([]byte{8}, 32)
	if err := st.Peers.Upsert(t.Context(), &Peer{PubKey: zero, Name: "z", Type: "REPEATER", OutPath: []byte{}, OutPathHashSize: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(t.Context(), `INSERT INTO settings (id, path_hash_size) VALUES (1, 3) ON CONFLICT(id) DO UPDATE SET path_hash_size = 3`); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.Add(t.Context(), comp.ID, zero, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := get(zero); got != 3 {
		t.Errorf("heard only zero-hop, node at 3: %d, want 3", got)
	}
}

// A route or size set for a node that is not a contact would be lost at restart while reporting success.
func TestContacts_SetRouteRefusesANonContact(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "setroute.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	comp := Companion{Name: "home"}
	if err := st.Companions.Create(t.Context(), &comp); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.SetRoute(t.Context(), comp.ID, bytes.Repeat([]byte{1}, 32), nil, 2); err != ErrNotContact {
		t.Errorf("SetRoute on a stranger: %v, want ErrNotContact", err)
	}
}
