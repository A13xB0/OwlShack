-- A companion's connection for MeshCore companion apps, and the preferences an app sets on it.
CREATE TABLE companion_app (
	companion_id       INTEGER PRIMARY KEY REFERENCES companions(id) ON DELETE CASCADE,
	port               INTEGER NOT NULL DEFAULT 0 CHECK (port BETWEEN 0 AND 65535),
	bind               TEXT    NOT NULL DEFAULT '',
	allow_key_export   INTEGER NOT NULL DEFAULT 0,
	manual_add         INTEGER NOT NULL DEFAULT 0,
	autoadd_config     INTEGER NOT NULL DEFAULT 0,
	autoadd_max_hops   INTEGER NOT NULL DEFAULT 0,
	adv_loc_policy     INTEGER NOT NULL DEFAULT 0,
	multi_acks         INTEGER NOT NULL DEFAULT 0,
	rx_delay_base_ms   INTEGER NOT NULL DEFAULT 0,
	airtime_factor_ms  INTEGER NOT NULL DEFAULT 1000,
	ble_pin            INTEGER NOT NULL DEFAULT 0
);

-- When a contact last changed, as an app's GET_CONTACTS since-filter reads it, and the app's flags byte.
ALTER TABLE companion_contacts ADD COLUMN lastmod INTEGER NOT NULL DEFAULT 0;
ALTER TABLE companion_contacts ADD COLUMN flags INTEGER NOT NULL DEFAULT 0;
UPDATE companion_contacts SET lastmod = COALESCE(
	CAST(strftime('%s', last_seen) AS INTEGER),
	CAST(strftime('%s', added_at) AS INTEGER),
	0);

-- The last verified advert heard from each peer, which an app exports or re-shares as the contact's card.
ALTER TABLE discovered_peers ADD COLUMN advert_payload BLOB;
