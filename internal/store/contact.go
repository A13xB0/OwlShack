package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ContactMetadata struct {
	IsRepeater       bool   `json:"isRepeater,omitempty"`
	RepeaterPassword string `json:"repeaterPassword,omitempty"`
	RoomPassword     string `json:"roomPassword,omitempty"`

	// Monitor enables background polling for this node; runtime state set via the UI, not file config.
	Monitor bool `json:"monitor,omitempty"`
	// MonitorIntervalSecs overrides the poll cadence; 0 = the poller's built-in default.
	MonitorIntervalSecs int64 `json:"monitorIntervalSecs,omitempty"`
	// MonitorProbes limits which bundles the poller runs ("status", "telemetry", "neighbors"); empty = all.
	MonitorProbes []string `json:"monitorProbes,omitempty"`
	// MonitorRetrySecs overrides the delay after a failed poll; 0 = the poller's built-in default.
	MonitorRetrySecs int64 `json:"monitorRetrySecs,omitempty"`
	// MonitorMaxRetries bounds consecutive retries before the node falls back to its normal interval; 0 = the poller's built-in default.
	MonitorMaxRetries int `json:"monitorMaxRetries,omitempty"`

	// TelemPerms is which classes this contact may read (sensor.Perm* bits), read only where the mode is "selected".
	TelemPerms uint8 `json:"telemPerms,omitempty"`
}

type Contact struct {
	CompanionID int64
	PeerPubKey  []byte
	// A contact is a self-contained address-book record with no discovered_peers FK, so deleting a peer leaves it intact.
	Name  string
	Type  string
	Lat   int32
	Lon   int32
	Feat1 uint16
	Feat2 uint16
	// Send route, our neighbour first; nil = unknown (flood), empty = direct.
	OutPath         []byte
	OutPathHashSize uint8
	// PathHashSize is the bytes per hop everything sent to this contact uses, 1 to 3.
	PathHashSize uint8
	LastSeen     time.Time // zero when never heard
	LastAdvertTS uint32
	AddedAt      time.Time
	Metadata     ContactMetadata
}

const contactColumns = `companion_id, peer_pubkey, name, type, lat, lon,
	feat1, feat2, out_path, out_path_hash_size, path_hash_size, last_seen, last_advert_ts,
	added_at, metadata`

func scanContact(s interface{ Scan(...any) error }) (*Contact, error) {
	var c Contact
	var metaStr string
	var feat1, feat2, lastAdvertTS int64
	var lastSeen sql.NullTime
	var outPath sql.NullString
	if err := s.Scan(
		&c.CompanionID, &c.PeerPubKey, &c.Name, &c.Type, &c.Lat, &c.Lon,
		&feat1, &feat2, &outPath, &c.OutPathHashSize, &c.PathHashSize, &lastSeen, &lastAdvertTS,
		&c.AddedAt, &metaStr,
	); err != nil {
		return nil, err
	}
	c.OutPath = scanOutPath(outPath)
	c.Feat1 = uint16(feat1)
	c.Feat2 = uint16(feat2)
	c.LastAdvertTS = uint32(lastAdvertTS)
	if lastSeen.Valid {
		c.LastSeen = lastSeen.Time
	}
	json.Unmarshal([]byte(metaStr), &c.Metadata)
	return &c, nil
}

type ContactRepo struct {
	db *sql.DB
}

func (r *ContactRepo) Add(ctx context.Context, companionID int64, peerPubKey []byte, name, contactType string) error {
	// A re-add keeps a known name and type, and bytes per hop is set only here, from the advert heard or else the node's own size.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO companion_contacts (companion_id, peer_pubkey, name, type, path_hash_size)
		VALUES (?1, ?2, ?3, ?4, COALESCE(
			(SELECT out_path_hash_size FROM discovered_peers WHERE pubkey = ?2 AND out_path_hash_size BETWEEN 1 AND 3),
			(SELECT path_hash_size FROM companions WHERE id = ?1 AND path_hash_size BETWEEN 1 AND 3),
			(SELECT path_hash_size FROM settings WHERE id = 1 AND path_hash_size BETWEEN 1 AND 3),
			1))
		ON CONFLICT(companion_id, peer_pubkey) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE companion_contacts.name END,
			type = CASE WHEN excluded.type <> '' THEN excluded.type ELSE companion_contacts.type END`,
		companionID, peerPubKey, name, contactType,
	)
	if err != nil {
		return fmt.Errorf("adding contact: %w", err)
	}
	return nil
}

// Restore overwrites every field, for backup restore where the incoming row is authoritative; the other writers preserve fields they do not own.
func (r *ContactRepo) Restore(ctx context.Context, c *Contact) error {
	meta, err := json.Marshal(c.Metadata)
	if err != nil {
		return fmt.Errorf("encoding contact metadata: %w", err)
	}
	var lastSeen any
	if !c.LastSeen.IsZero() {
		lastSeen = c.LastSeen
	}
	addedAt := c.AddedAt
	if addedAt.IsZero() {
		addedAt = time.Now()
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO companion_contacts (
			companion_id, peer_pubkey, name, type, lat, lon, feat1, feat2,
			out_path, out_path_hash_size, path_hash_size, last_seen, last_advert_ts, added_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(companion_id, peer_pubkey) DO UPDATE SET
			name = excluded.name, type = excluded.type,
			lat = excluded.lat, lon = excluded.lon,
			feat1 = excluded.feat1, feat2 = excluded.feat2,
			out_path = excluded.out_path,
			out_path_hash_size = excluded.out_path_hash_size,
			path_hash_size = excluded.path_hash_size,
			last_seen = excluded.last_seen,
			last_advert_ts = excluded.last_advert_ts,
			added_at = excluded.added_at,
			metadata = excluded.metadata`,
		c.CompanionID, c.PeerPubKey, c.Name, c.Type, c.Lat, c.Lon, c.Feat1, c.Feat2,
		c.OutPath, c.OutPathHashSize, max(c.PathHashSize, 1), lastSeen, c.LastAdvertTS, addedAt, string(meta),
	)
	if err != nil {
		return fmt.Errorf("restoring contact: %w", err)
	}
	return nil
}

// RefreshFromAdvert updates every companion's contact row for this peer; location only when hasLocation, so a no-GPS advert never wipes a hand-set one.
func (r *ContactRepo) RefreshFromAdvert(
	ctx context.Context,
	peerPubKey []byte, name, contactType string,
	lat, lon int32, feat1, feat2 uint16, lastSeen time.Time, lastAdvertTS uint32,
	hasLocation bool,
) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE companion_contacts SET
			name           = CASE WHEN ? <> '' THEN ? ELSE name END,
			type           = CASE WHEN ? <> '' THEN ? ELSE type END,
			lat            = CASE WHEN ? THEN ? ELSE lat END,
			lon            = CASE WHEN ? THEN ? ELSE lon END,
			feat1          = ?,
			feat2          = ?,
			last_seen      = ?,
			last_advert_ts = ?
		WHERE peer_pubkey = ?`,
		name, name, contactType, contactType,
		hasLocation, lat, hasLocation, lon,
		feat1, feat2, lastSeen, lastAdvertTS,
		peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("refreshing contact from advert: %w", err)
	}
	return nil
}

// UpdateOutPath scopes a learned route by companion_id: companions never share a route to a peer.
func (r *ContactRepo) UpdateOutPath(ctx context.Context, companionID int64, peerPubKey []byte, path []byte, hashSize uint8) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE companion_contacts SET out_path = ?, out_path_hash_size = ?
		WHERE companion_id = ? AND peer_pubkey = ?`,
		path, hashSize, companionID, peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("updating contact out_path: %w", err)
	}
	return nil
}

// ErrNotContact is a write to a node that is not the companion's contact, which has no row to hold it.
var ErrNotContact = errors.New("not a contact of this companion")

// SetRoute saves an operator's route and bytes per hop together, so the two can never disagree.
func (r *ContactRepo) SetRoute(ctx context.Context, companionID int64, peerPubKey []byte, path []byte, hashSize uint8) error {
	outHashSize := hashSize
	if path == nil {
		outHashSize = 0
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE companion_contacts SET out_path = ?, out_path_hash_size = ?, path_hash_size = ?
		WHERE companion_id = ? AND peer_pubkey = ?`,
		path, outHashSize, hashSize, companionID, peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("setting contact route: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotContact
	}
	return nil
}

// SetLocation hand-sets a location; a later advert carrying a position overwrites it (RefreshFromAdvert).
func (r *ContactRepo) SetLocation(ctx context.Context, companionID int64, peerPubKey []byte, lat, lon int32) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE companion_contacts SET lat = ?, lon = ?
		WHERE companion_id = ? AND peer_pubkey = ?`,
		lat, lon, companionID, peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("setting contact location: %w", err)
	}
	return nil
}

func (r *ContactRepo) List(ctx context.Context, companionID int64) ([]Contact, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+contactColumns+`
		FROM companion_contacts
		WHERE companion_id = ?
		ORDER BY added_at DESC`, companionID)
	if err != nil {
		return nil, fmt.Errorf("querying contacts: %w", err)
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning contact row: %w", err)
		}
		contacts = append(contacts, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating contacts: %w", err)
	}
	return contacts, nil
}

func (r *ContactRepo) UpdateMetadata(ctx context.Context, companionID int64, peerPubKey []byte, meta ContactMetadata) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshaling contact metadata: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		UPDATE companion_contacts SET metadata = ?
		WHERE companion_id = ? AND peer_pubkey = ?`,
		string(metaJSON), companionID, peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("updating contact metadata: %w", err)
	}
	return nil
}

func (r *ContactRepo) Get(ctx context.Context, companionID int64, peerPubKey []byte) (*Contact, error) {
	c, err := scanContact(r.db.QueryRowContext(ctx, `
		SELECT `+contactColumns+`
		FROM companion_contacts
		WHERE companion_id = ? AND peer_pubkey = ?`,
		companionID, peerPubKey,
	))
	if err != nil {
		return nil, fmt.Errorf("getting contact: %w", err)
	}
	return c, nil
}

func (r *ContactRepo) Delete(ctx context.Context, companionID int64, peerPubKey []byte) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM companion_contacts
		WHERE companion_id = ? AND peer_pubkey = ?`,
		companionID, peerPubKey,
	)
	if err != nil {
		return fmt.Errorf("deleting contact: %w", err)
	}
	return nil
}

func (r *ContactRepo) DeleteAll(ctx context.Context, companionID int64) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM companion_contacts WHERE companion_id = ?", companionID)
	if err != nil {
		return fmt.Errorf("deleting all contacts: %w", err)
	}
	return nil
}
