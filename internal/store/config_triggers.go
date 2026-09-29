package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Trigger is a bot rule owned by a companion; ChannelIDs come from the trigger_channels join and the leaf lists are newline-encoded TEXT.
type Trigger struct {
	ID                 int64
	CompanionID        int64
	Type               string
	Template           string
	CharLimitBehaviour *string
	MatchPatterns      []string
	Contacts           []string
	FailoverPattern    string
	FailoverTimeout    int64
	RetryTimeout       *int64
	MaxRetries         *int
	PathHashSize       *int
	Schedule           *string
	URL                string
	Location           *TriggerLocation
	Regions            *[]string // region ids; nil takes alerts from anywhere
	ChannelIDs         []int64
}

// TriggerLocation is a cap trigger's point and margin; a trigger without one takes alerts from anywhere.
type TriggerLocation struct {
	Lat, Lon, RadiusKm float64
}

func regionsCol(ids *[]string) *string {
	if ids == nil {
		return nil
	}
	s := encodeList(*ids)
	return &s
}

// locationCols splits a location into its three nullable columns.
func locationCols(l *TriggerLocation) (lat, lon, km *float64) {
	if l == nil {
		return nil, nil, nil
	}
	return &l.Lat, &l.Lon, &l.RadiusKm
}

type TriggerRepo struct{ db *sql.DB }

func (r *TriggerRepo) scanRow(s interface{ Scan(...any) error }) (*Trigger, error) {
	var t Trigger
	var match, contacts string
	var regions sql.NullString
	var lat, lon, km sql.NullFloat64
	if err := s.Scan(
		&t.ID, &t.CompanionID, &t.Type, &t.Template, &t.CharLimitBehaviour,
		&match, &contacts, &t.RetryTimeout, &t.MaxRetries, &t.PathHashSize, &t.Schedule, &t.URL, &t.FailoverPattern, &t.FailoverTimeout,
		&lat, &lon, &km, &regions,
	); err != nil {
		return nil, err
	}
	switch {
	case lat.Valid && lon.Valid && km.Valid:
		t.Location = &TriggerLocation{Lat: lat.Float64, Lon: lon.Float64, RadiusKm: km.Float64}
	case lat.Valid || lon.Valid || km.Valid:
		return nil, fmt.Errorf("trigger %d has a partial location", t.ID)
	}
	t.MatchPatterns = decodeList(match)
	t.Contacts = decodeList(contacts)
	if regions.Valid {
		ids := decodeList(regions.String)
		t.Regions = &ids
	}
	return &t, nil
}

// channelIDs loads the channel ids a trigger listens on, in stable order.
func (r *TriggerRepo) channelIDs(ctx context.Context, triggerID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT channel_id FROM trigger_channels WHERE trigger_id = ? ORDER BY channel_id ASC`, triggerID)
	if err != nil {
		return nil, fmt.Errorf("querying trigger channels: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning trigger channel id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating trigger channels: %w", err)
	}
	return ids, nil
}

// List returns every trigger across all companions (for the bots page).
func (r *TriggerRepo) List(ctx context.Context) ([]Trigger, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, companion_id, type, template, char_limit_behaviour,
		       match_patterns, contacts, retry_timeout, max_retries, path_hash_size, schedule, url, failover_pattern, failover_timeout,
		       location_lat, location_lon, location_radius_km, location_regions
		FROM triggers ORDER BY companion_id ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying triggers: %w", err)
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		t, err := r.scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning trigger row: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating triggers: %w", err)
	}
	for i := range out {
		ids, err := r.channelIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ChannelIDs = ids
	}
	return out, nil
}

func (r *TriggerRepo) ListByCompanion(ctx context.Context, companionID int64) ([]Trigger, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, companion_id, type, template, char_limit_behaviour,
		       match_patterns, contacts, retry_timeout, max_retries, path_hash_size, schedule, url, failover_pattern, failover_timeout,
		       location_lat, location_lon, location_radius_km, location_regions
		FROM triggers WHERE companion_id = ? ORDER BY id ASC`, companionID)
	if err != nil {
		return nil, fmt.Errorf("querying triggers by companion: %w", err)
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		t, err := r.scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning trigger row: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating triggers: %w", err)
	}
	// Resolve channel links after the rows cursor is closed (separate queries).
	for i := range out {
		ids, err := r.channelIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ChannelIDs = ids
	}
	return out, nil
}

func (r *TriggerRepo) Get(ctx context.Context, id int64) (*Trigger, error) {
	t, err := r.scanRow(r.db.QueryRowContext(ctx, `
		SELECT id, companion_id, type, template, char_limit_behaviour,
		       match_patterns, contacts, retry_timeout, max_retries, path_hash_size, schedule, url, failover_pattern, failover_timeout,
		       location_lat, location_lon, location_radius_km, location_regions
		FROM triggers WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("getting trigger: %w", err)
	}
	if t.ChannelIDs, err = r.channelIDs(ctx, t.ID); err != nil {
		return nil, err
	}
	return t, nil
}

func (r *TriggerRepo) Create(ctx context.Context, t *Trigger) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer tx.Rollback()

	lat, lon, km := locationCols(t.Location)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO triggers
			(companion_id, type, template, char_limit_behaviour, match_patterns, contacts, retry_timeout, max_retries, path_hash_size, schedule, url, failover_pattern, failover_timeout,
			 location_lat, location_lon, location_radius_km, location_regions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.CompanionID, t.Type, t.Template, t.CharLimitBehaviour,
		encodeList(t.MatchPatterns), encodeList(t.Contacts),
		t.RetryTimeout, t.MaxRetries, t.PathHashSize, t.Schedule, t.URL, t.FailoverPattern, t.FailoverTimeout,
		lat, lon, km, regionsCol(t.Regions))
	if err != nil {
		return fmt.Errorf("inserting trigger: %w", err)
	}
	if t.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	if err := setTriggerChannels(ctx, tx, t.ID, t.ChannelIDs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing tx: %w", err)
	}
	return nil
}

func (r *TriggerRepo) Update(ctx context.Context, t *Trigger) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer tx.Rollback()

	lat, lon, km := locationCols(t.Location)
	if _, err := tx.ExecContext(ctx, `
		UPDATE triggers SET type=?, template=?, char_limit_behaviour=?, match_patterns=?, contacts=?,
			retry_timeout=?, max_retries=?, path_hash_size=?, schedule=?, url=?, failover_pattern=?, failover_timeout=?,
			location_lat=?, location_lon=?, location_radius_km=?, location_regions=?
		WHERE id=?`,
		t.Type, t.Template, t.CharLimitBehaviour, encodeList(t.MatchPatterns), encodeList(t.Contacts),
		t.RetryTimeout, t.MaxRetries, t.PathHashSize, t.Schedule, t.URL, t.FailoverPattern, t.FailoverTimeout,
		lat, lon, km, regionsCol(t.Regions), t.ID); err != nil {
		return fmt.Errorf("updating trigger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trigger_channels WHERE trigger_id = ?`, t.ID); err != nil {
		return fmt.Errorf("deleting trigger channels: %w", err)
	}
	if err := setTriggerChannels(ctx, tx, t.ID, t.ChannelIDs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing tx: %w", err)
	}
	return nil
}

func (r *TriggerRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM triggers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting trigger: %w", err)
	}
	return nil
}

func setTriggerChannels(ctx context.Context, tx *sql.Tx, triggerID int64, channelIDs []int64) error {
	for _, cid := range channelIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trigger_channels (trigger_id, channel_id) VALUES (?, ?)`, triggerID, cid); err != nil {
			return fmt.Errorf("inserting trigger channel: %w", err)
		}
	}
	return nil
}
