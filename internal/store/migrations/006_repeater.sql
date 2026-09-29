-- The repeater tables; an absent "*" region means unscoped flood is not relayed, so it is seeded to keep the prior behaviour.

CREATE TABLE IF NOT EXISTS repeater (
	id                    INTEGER PRIMARY KEY CHECK (id = 1),
	name                  TEXT    NOT NULL DEFAULT '',
	private_key           TEXT    NOT NULL DEFAULT '',
	pubkey                TEXT    NOT NULL DEFAULT '',
	latitude              REAL,
	longitude             REAL,
	advert_interval       INTEGER,
	flood_advert_interval INTEGER,
	disable_fwd           INTEGER,
	flood_max             INTEGER,
	flood_max_advert      INTEGER,
	loop_detect           TEXT,
	path_hash_mode        INTEGER,
	owner_info            TEXT    NOT NULL DEFAULT '',
	admin_password        TEXT    NOT NULL DEFAULT '',
	guest_password        TEXT    NOT NULL DEFAULT '',
	regions               TEXT    NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS repeater_acl (
	pubkey         TEXT    PRIMARY KEY,
	permissions    INTEGER NOT NULL DEFAULT 0,
	last_timestamp INTEGER NOT NULL DEFAULT 0,
	last_seen      INTEGER NOT NULL DEFAULT 0
);
UPDATE repeater SET regions = '[{"name":"*"}]'
	WHERE id = 1 AND (regions IS NULL OR regions = '' OR regions = '[]');
