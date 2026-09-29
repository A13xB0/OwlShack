-- The sensor framework: sensors, their learned state, the channel map, and who may read a companion's telemetry.

CREATE TABLE IF NOT EXISTS sensors (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	kind     TEXT NOT NULL,
	name     TEXT NOT NULL,
	options  TEXT NOT NULL DEFAULT '{}',
	bindings TEXT NOT NULL DEFAULT '[]'
);

-- NOCASE, as the hub compares names; the hub's check gives the reason, this holds against any other writer.
CREATE UNIQUE INDEX IF NOT EXISTS sensors_name ON sensors (name COLLATE NOCASE);

-- Keyed by node kind and id rather than a foreign key, because a row may belong to the repeater singleton.
CREATE TABLE IF NOT EXISTS telemetry_map (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	node_kind TEXT    NOT NULL CHECK (node_kind IN ('companion', 'repeater')),
	node_id   INTEGER NOT NULL,
	channel   INTEGER NOT NULL,
	lpp_type  INTEGER NOT NULL,
	sensor_id INTEGER NOT NULL REFERENCES sensors(id) ON DELETE CASCADE,
	metric    TEXT NOT NULL,
	UNIQUE (node_kind, node_id, channel, lpp_type)
);

-- A sensor's delete cascades here, which is a scan per row without it.
CREATE INDEX IF NOT EXISTS telemetry_map_sensor ON telemetry_map (sensor_id);

-- One row per sensor, replaced in place: what a sensor has learned, never a series of it.
CREATE TABLE IF NOT EXISTS sensor_state (
	sensor_id  INTEGER PRIMARY KEY REFERENCES sensors(id) ON DELETE CASCADE,
	state      TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE companions ADD COLUMN telem_base TEXT NOT NULL DEFAULT 'deny' CHECK (telem_base IN ('deny', 'selected', 'contacts'));

ALTER TABLE companions ADD COLUMN telem_loc TEXT NOT NULL DEFAULT 'deny' CHECK (telem_loc IN ('deny', 'selected', 'contacts'));

ALTER TABLE companions ADD COLUMN telem_env TEXT NOT NULL DEFAULT 'deny' CHECK (telem_env IN ('deny', 'selected', 'contacts'));
