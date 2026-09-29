-- The signal-test runner and link-monitor tables.

CREATE TABLE IF NOT EXISTS signal_tests (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	companion_id   INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	label          TEXT    NOT NULL DEFAULT '',
	notes          TEXT    NOT NULL DEFAULT '',
	path           BLOB    NOT NULL,
	path_hash_size INTEGER NOT NULL DEFAULT 1,
	count          INTEGER NOT NULL,
	interval_secs  INTEGER NOT NULL,
	status         TEXT    NOT NULL DEFAULT 'running',
	started_at     INTEGER NOT NULL,
	finished_at    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS signal_test_runs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	test_id    INTEGER NOT NULL REFERENCES signal_tests(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL,
	sent_at    INTEGER NOT NULL,
	ok         INTEGER NOT NULL DEFAULT 0,
	hop_snrs   TEXT    NOT NULL DEFAULT '[]',
	snr        REAL,
	elapsed_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_signal_test_runs_test ON signal_test_runs(test_id, seq);

CREATE TABLE IF NOT EXISTS link_monitors (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	key              BLOB    NOT NULL UNIQUE,
	companion_id     INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	label            TEXT    NOT NULL DEFAULT '',
	path             BLOB    NOT NULL,
	path_hash_size   INTEGER NOT NULL DEFAULT 1,
	interval_secs    INTEGER NOT NULL DEFAULT 900,
	enabled          INTEGER NOT NULL DEFAULT 1,
	-- Display-only UI toggles; the collector keeps recording both readings.
	ignore_first_hop INTEGER NOT NULL DEFAULT 0,
	hide_last_snr    INTEGER NOT NULL DEFAULT 0,
	-- 0 = use the node-monitoring poller's built-in default.
	retry_secs       INTEGER NOT NULL DEFAULT 0,
	max_retries      INTEGER NOT NULL DEFAULT 0
);
