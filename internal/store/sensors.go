package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Sensor is one configured local sensor; Options are JSON because only its provider knows what they mean, and everything else passes them through.
type Sensor struct {
	ID       int64
	Provider string
	Kind     string
	Name     string
	Options  map[string]string
	// Bindings are the sensors this one reads; their own column because the hub orders a pass by them.
	Bindings []SensorBinding
}

// SensorBinding names one reading of one sensor, under the name an expression calls it.
type SensorBinding struct {
	Name     string `json:"name"`
	SensorID int64  `json:"sensorId"`
	Metric   string `json:"metric"`
}

type SensorRepo struct{ db *sql.DB }

func (r *SensorRepo) List(ctx context.Context) ([]Sensor, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, provider, kind, name, options, bindings FROM sensors ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying sensors: %w", err)
	}
	defer rows.Close()
	var out []Sensor
	for rows.Next() {
		var s Sensor
		var opts, binds string
		if err := rows.Scan(&s.ID, &s.Provider, &s.Kind, &s.Name, &opts, &binds); err != nil {
			return nil, fmt.Errorf("scanning sensor row: %w", err)
		}
		if s.Options, err = decodeOptions(opts); err != nil {
			return nil, fmt.Errorf("sensor %d: %w", s.ID, err)
		}
		if s.Bindings, err = decodeBindings(binds); err != nil {
			return nil, fmt.Errorf("sensor %d: %w", s.ID, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating sensors: %w", err)
	}
	return out, nil
}

func (r *SensorRepo) Create(ctx context.Context, s *Sensor) error {
	opts, err := encodeOptions(s.Options)
	if err != nil {
		return err
	}
	binds, err := encodeBindings(s.Bindings)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO sensors (provider, kind, name, options, bindings) VALUES (?, ?, ?, ?, ?)`,
		s.Provider, s.Kind, s.Name, opts, binds)
	if err != nil {
		return fmt.Errorf("inserting sensor: %w", err)
	}
	if s.ID, err = res.LastInsertId(); err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	return nil
}

func (r *SensorRepo) Update(ctx context.Context, s *Sensor) error {
	opts, err := encodeOptions(s.Options)
	if err != nil {
		return err
	}
	binds, err := encodeBindings(s.Bindings)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE sensors SET provider=?, kind=?, name=?, options=?, bindings=? WHERE id=?`,
		s.Provider, s.Kind, s.Name, opts, binds, s.ID)
	if err != nil {
		return fmt.Errorf("updating sensor: %w", err)
	}
	// A silent no-op would leave the caller believing an edit landed on a sensor that is gone.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading update result: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no sensor with id %d: %w", s.ID, sql.ErrNoRows)
	}
	return nil
}

func (r *SensorRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sensors WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting sensor: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading delete result: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no sensor with id %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// LoadState returns what a sensor learned on an earlier run, or nil when it stored none; the state is opaque here and carries its own version.
func (r *SensorRepo) LoadState(ctx context.Context, sensorID int64) ([]byte, error) {
	var state string
	err := r.db.QueryRowContext(ctx,
		`SELECT state FROM sensor_state WHERE sensor_id = ?`, sensorID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading sensor %d state: %w", sensorID, err)
	}
	return []byte(state), nil
}

// SaveState replaces a sensor's one row, and writes nothing for a sensor deleted while the save was queued.
func (r *SensorRepo) SaveState(ctx context.Context, sensorID int64, state []byte) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sensor_state (sensor_id, state, updated_at)
		SELECT ?, ?, CURRENT_TIMESTAMP WHERE EXISTS (SELECT 1 FROM sensors WHERE id = ?)
		ON CONFLICT(sensor_id) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		sensorID, string(state), sensorID)
	if err != nil {
		return fmt.Errorf("saving sensor %d state: %w", sensorID, err)
	}
	return nil
}

// encodeOptions stores an absent map as {} rather than NULL, so a read never has to decide what a missing value meant.
func encodeOptions(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encoding sensor options: %w", err)
	}
	return string(b), nil
}

func decodeOptions(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("decoding sensor options: %w", err)
	}
	return m, nil
}

// encodeBindings stores an absent list as [] rather than NULL, for the same reason options store {}.
func encodeBindings(bs []SensorBinding) (string, error) {
	if len(bs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(bs)
	if err != nil {
		return "", fmt.Errorf("encoding sensor bindings: %w", err)
	}
	return string(b), nil
}

func decodeBindings(s string) ([]SensorBinding, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var bs []SensorBinding
	if err := json.Unmarshal([]byte(s), &bs); err != nil {
		return nil, fmt.Errorf("decoding sensor bindings: %w", err)
	}
	return bs, nil
}
