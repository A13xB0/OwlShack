package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Node kinds a channel map can belong to; a repeater is a singleton, a companion keys on its id.
const (
	NodeKindCompanion = "companion"
	NodeKindRepeater  = "repeater"
	RepeaterNodeID    = 1
)

// TelemetryNode names one node that can carry a channel map of its own.
type TelemetryNode struct {
	Kind string
	ID   int64
}

// TelemetryMapEntry is one published reading; the type is stored because a metric is a local name only the operator can map.
type TelemetryMapEntry struct {
	ID       int64
	Node     TelemetryNode
	Channel  int
	LPPType  int
	SensorID int64
	Metric   string
}

type TelemetryMapRepo struct{ db *sql.DB }

func (r *TelemetryMapRepo) List(ctx context.Context) ([]TelemetryMapEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, node_kind, node_id, channel, lpp_type, sensor_id, metric FROM telemetry_map
		ORDER BY node_kind ASC, node_id ASC, channel ASC, lpp_type ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying the telemetry map: %w", err)
	}
	defer rows.Close()
	var out []TelemetryMapEntry
	for rows.Next() {
		var e TelemetryMapEntry
		if err := rows.Scan(&e.ID, &e.Node.Kind, &e.Node.ID, &e.Channel, &e.LPPType, &e.SensorID, &e.Metric); err != nil {
			return nil, fmt.Errorf("scanning a telemetry map row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReplaceForNode swaps one node's map in a transaction, because a map is validated as a whole.
func (r *TelemetryMapRepo) ReplaceForNode(ctx context.Context, node TelemetryNode, entries []TelemetryMapEntry) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("opening a transaction for the telemetry map: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`DELETE FROM telemetry_map WHERE node_kind = ? AND node_id = ?`, node.Kind, node.ID)
	if err != nil {
		return fmt.Errorf("clearing the telemetry map: %w", err)
	}
	for _, e := range entries {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO telemetry_map (node_kind, node_id, channel, lpp_type, sensor_id, metric)
			VALUES (?, ?, ?, ?, ?, ?)`,
			node.Kind, node.ID, e.Channel, e.LPPType, e.SensorID, e.Metric)
		if err != nil {
			return fmt.Errorf("saving channel %d: %w", e.Channel, err)
		}
	}
	return tx.Commit()
}

// deleteTelemetryMapForNode drops a node's map when the node itself goes.
func deleteTelemetryMapForNode(ctx context.Context, db dbExecer, node TelemetryNode) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM telemetry_map WHERE node_kind = ? AND node_id = ?`, node.Kind, node.ID)
	return err
}
