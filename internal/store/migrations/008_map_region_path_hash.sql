-- Map tile key, home region, advert interval clamps, path hash size in bytes, and relay timing.

-- CARTO basemap API key.
ALTER TABLE settings ADD COLUMN map_tile_key TEXT;

-- Repeater home region (stored and reported, never routed on).
ALTER TABLE repeater ADD COLUMN home_region TEXT NOT NULL DEFAULT '';

-- Clamp advert intervals to the firmware's legal ranges; below the minimum becomes 0 (off), matching savePrefs.
UPDATE repeater SET
	advert_interval = CASE
		WHEN advert_interval > 0 AND advert_interval < 3600 THEN 0
		WHEN advert_interval > 14400 THEN 14400
		ELSE advert_interval END,
	flood_advert_interval = CASE
		WHEN flood_advert_interval > 0 AND flood_advert_interval < 10800 THEN 0
		WHEN flood_advert_interval > 604800 THEN 604800
		ELSE flood_advert_interval END;

-- Path hash width is bytes internally; the shipped path_hash_mode column (bytes-1) must be read here before it is dropped.
ALTER TABLE settings ADD COLUMN path_hash_size INTEGER;

ALTER TABLE companions ADD COLUMN path_hash_size INTEGER;

ALTER TABLE repeater ADD COLUMN path_hash_size INTEGER;

UPDATE repeater SET path_hash_size = path_hash_mode + 1 WHERE path_hash_mode IS NOT NULL;

ALTER TABLE repeater DROP COLUMN path_hash_mode;

-- Relay timing (firmware txdelay / direct.txdelay / rxdelay / multi.acks).
ALTER TABLE repeater ADD COLUMN tx_delay_factor REAL;

ALTER TABLE repeater ADD COLUMN direct_tx_delay_factor REAL;

ALTER TABLE repeater ADD COLUMN rx_delay_base REAL;

ALTER TABLE repeater ADD COLUMN multi_acks INTEGER;
