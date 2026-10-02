package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Peer struct {
	PubKey []byte
	Name   string
	Type   string
	Lat    int32
	Lon    int32
	Feat1  uint16
	Feat2  uint16
	// Advert path, peer's neighbour first.
	OutPath []byte
	// OutPathHashSize is the size the peer floods its adverts at; 0 is unknown, as a zero-hop advert says nothing, and an upsert of 0 keeps the one known.
	OutPathHashSize uint8
	LastAdvertTS    uint32
	LastSeen        time.Time
	SNR             *float64
	RSSI            *int8
}

// HasLocation reports a usable fix; 0,0 is the codebase's "unknown" sentinel, not a real position.
func (p *Peer) HasLocation() bool { return p.Lat != 0 || p.Lon != 0 }

type PeerRepo struct {
	db *sql.DB
}

func (r *PeerRepo) Upsert(ctx context.Context, p *Peer) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO discovered_peers (pubkey, name, type, lat, lon, feat1, feat2, out_path, out_path_hash_size, last_advert_ts, last_seen, snr, rssi)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pubkey) DO UPDATE SET
			name               = excluded.name,
			type               = excluded.type,
			lat                = excluded.lat,
			lon                = excluded.lon,
			feat1              = excluded.feat1,
			feat2              = excluded.feat2,
			out_path           = excluded.out_path,
			out_path_hash_size = CASE WHEN excluded.out_path_hash_size = 0 THEN discovered_peers.out_path_hash_size ELSE excluded.out_path_hash_size END,
			last_advert_ts     = excluded.last_advert_ts,
			last_seen          = excluded.last_seen,
			snr                = excluded.snr,
			rssi               = excluded.rssi`,
		p.PubKey, p.Name, p.Type, p.Lat, p.Lon, p.Feat1, p.Feat2,
		p.OutPath, p.OutPathHashSize, p.LastAdvertTS, p.LastSeen, p.SNR, p.RSSI,
	)
	if err != nil {
		return fmt.Errorf("upserting peer: %w", err)
	}
	return nil
}

// SetAdvert keeps a peer's last verified advert payload, which a companion app exports or shares as its card.
func (r *PeerRepo) SetAdvert(ctx context.Context, pubkey, payload []byte) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE discovered_peers SET advert_payload = ? WHERE pubkey = ?`, payload, pubkey); err != nil {
		return fmt.Errorf("storing peer advert: %w", err)
	}
	return nil
}

// Advert is the peer's last advert payload, nil when none has been kept.
func (r *PeerRepo) Advert(ctx context.Context, pubkey []byte) ([]byte, error) {
	var payload []byte
	err := r.db.QueryRowContext(ctx, `SELECT advert_payload FROM discovered_peers WHERE pubkey = ?`, pubkey).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading peer advert: %w", err)
	}
	return payload, nil
}

func (r *PeerRepo) List(ctx context.Context) ([]Peer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT pubkey, name, type, lat, lon, feat1, feat2, out_path, out_path_hash_size, last_advert_ts, last_seen, snr, rssi
		FROM discovered_peers
		ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying peers: %w", err)
	}
	defer rows.Close()
	return scanPeers(rows)
}

func (r *PeerRepo) UpdateOutPath(ctx context.Context, pubkey []byte, path []byte, hashSize uint8) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE discovered_peers SET out_path = ?, out_path_hash_size = ? WHERE pubkey = ?`,
		path, hashSize, pubkey)
	if err != nil {
		return fmt.Errorf("updating peer out_path: %w", err)
	}
	return nil
}

func (r *PeerRepo) Delete(ctx context.Context, pubkey []byte) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM discovered_peers WHERE pubkey = ?", pubkey)
	if err != nil {
		return fmt.Errorf("deleting peer: %w", err)
	}
	return nil
}

// DeleteMany removes a batch of peers, chunked under SQLite's bound-parameter limit.
func (r *PeerRepo) DeleteMany(ctx context.Context, pubkeys [][]byte) error {
	return eachInChunk(ctx, pubkeys, func(ctx context.Context, placeholders string, args []any) error {
		_, err := r.db.ExecContext(ctx,
			"DELETE FROM discovered_peers WHERE pubkey IN ("+placeholders+")", args...,
		)
		if err != nil {
			return fmt.Errorf("deleting peers: %w", err)
		}
		return nil
	})
}

func (r *PeerRepo) GetByPubKey(ctx context.Context, pubkey []byte) (*Peer, error) {
	var p Peer
	var feat1, feat2, lastAdvertTS int64
	var snr sql.NullFloat64
	var rssi sql.NullInt64
	var outPath sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT pubkey, name, type, lat, lon, feat1, feat2, out_path, out_path_hash_size, last_advert_ts, last_seen, snr, rssi
		FROM discovered_peers WHERE pubkey = ?`, pubkey,
	).Scan(&p.PubKey, &p.Name, &p.Type, &p.Lat, &p.Lon,
		&feat1, &feat2, &outPath, &p.OutPathHashSize, &lastAdvertTS, &p.LastSeen, &snr, &rssi)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting peer by pubkey: %w", err)
	}
	p.OutPath = scanOutPath(outPath)
	p.Feat1 = uint16(feat1)
	p.Feat2 = uint16(feat2)
	p.LastAdvertTS = uint32(lastAdvertTS)
	if snr.Valid {
		v := snr.Float64
		p.SNR = &v
	}
	if rssi.Valid {
		v := int8(rssi.Int64)
		p.RSSI = &v
	}
	return &p, nil
}

func (r *PeerRepo) LoadAll(ctx context.Context) ([]Peer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT pubkey, name, type, lat, lon, feat1, feat2, out_path, out_path_hash_size, last_advert_ts, last_seen, snr, rssi
		FROM discovered_peers`)
	if err != nil {
		return nil, fmt.Errorf("querying peers: %w", err)
	}
	defer rows.Close()
	return scanPeers(rows)
}

func scanPeers(rows *sql.Rows) ([]Peer, error) {
	var peers []Peer
	for rows.Next() {
		var p Peer
		var feat1, feat2 int64
		var snr sql.NullFloat64
		var rssi sql.NullInt64
		var lastAdvertTS int64
		var outPath sql.NullString
		if err := rows.Scan(
			&p.PubKey, &p.Name, &p.Type, &p.Lat, &p.Lon,
			&feat1, &feat2, &outPath, &p.OutPathHashSize, &lastAdvertTS,
			&p.LastSeen, &snr, &rssi,
		); err != nil {
			return nil, err
		}
		p.OutPath = scanOutPath(outPath)
		p.Feat1 = uint16(feat1)
		p.Feat2 = uint16(feat2)
		p.LastAdvertTS = uint32(lastAdvertTS)
		if snr.Valid {
			v := snr.Float64
			p.SNR = &v
		}
		if rssi.Valid {
			v := int8(rssi.Int64)
			p.RSSI = &v
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}

// scanOutPath keeps nil (unknown, flood) distinct from empty (direct neighbour), which a plain []byte scan collapses.
func scanOutPath(ns sql.NullString) []byte {
	if !ns.Valid {
		return nil
	}
	return []byte(ns.String)
}

// prefixUpperBound returns the smallest byte string greater than every value with prefix; nil means none exists (prefix is all 0xFF).
func prefixUpperBound(prefix []byte) []byte {
	upper := append([]byte(nil), prefix...)
	for i := len(upper) - 1; i >= 0; i-- {
		if upper[i] != 0xFF {
			upper[i]++
			return upper[:i+1]
		}
	}
	return nil
}

// FindByPrefix resolves a pubkey prefix as a half-open range so the query rides the PRIMARY KEY index; a collision returns the first row.
func (r *PeerRepo) FindByPrefix(ctx context.Context, prefix []byte) (*Peer, error) {
	if len(prefix) == 0 {
		return nil, nil
	}
	var rows *sql.Rows
	var err error
	const cols = `SELECT pubkey, name, type, lat, lon, feat1, feat2, out_path, out_path_hash_size, last_advert_ts, last_seen, snr, rssi FROM discovered_peers`
	if upper := prefixUpperBound(prefix); upper != nil {
		rows, err = r.db.QueryContext(ctx, cols+` WHERE pubkey >= ? AND pubkey < ? LIMIT 1`, prefix, upper)
	} else {
		// prefix is all 0xFF: no upper bound exists, just match the tail.
		rows, err = r.db.QueryContext(ctx, cols+` WHERE pubkey >= ? LIMIT 1`, prefix)
	}
	if err != nil {
		return nil, fmt.Errorf("querying peer by prefix: %w", err)
	}
	defer rows.Close()

	peers, err := scanPeers(rows)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, nil
	}
	return &peers[0], nil
}

func (r *PeerRepo) LookupByHash(ctx context.Context, hash []byte) ([]string, error) {
	return r.namesByHash(ctx, hash, "")
}

// LookupRepeatersByHash names the repeaters a path hop could be, most recently heard first, as the map resolves one.
func (r *PeerRepo) LookupRepeatersByHash(ctx context.Context, hash []byte) ([]string, error) {
	return r.namesByHash(ctx, hash, " AND type = 'REPEATER' ORDER BY last_seen DESC")
}

func (r *PeerRepo) namesByHash(ctx context.Context, hash []byte, filter string) ([]string, error) {
	if len(hash) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT name FROM discovered_peers
		WHERE substr(pubkey, 1, ?) = ?`+filter, len(hash), hash)
	if err != nil {
		return nil, fmt.Errorf("querying peers by hash: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning peer name row: %w", err)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating peer names: %w", err)
	}
	return names, nil
}
