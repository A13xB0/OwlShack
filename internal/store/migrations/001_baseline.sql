-- The baseline schema.

CREATE TABLE IF NOT EXISTS discovered_peers (
	pubkey             BLOB PRIMARY KEY,
	name               TEXT NOT NULL DEFAULT '',
	type               TEXT NOT NULL DEFAULT 'NONE',
	lat                INTEGER NOT NULL DEFAULT 0,
	lon                INTEGER NOT NULL DEFAULT 0,
	feat1              INTEGER NOT NULL DEFAULT 0,
	feat2              INTEGER NOT NULL DEFAULT 0,
	out_path           BLOB,
	last_advert_ts     INTEGER NOT NULL DEFAULT 0,
	last_seen          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	snr                REAL,
	rssi               INTEGER,
	out_path_hash_size INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS companions (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT    NOT NULL,
	private_key     TEXT    NOT NULL DEFAULT '',
	pubkey          TEXT    NOT NULL DEFAULT '',
	latitude        REAL,
	longitude       REAL,
	advert_interval INTEGER
);

CREATE TABLE IF NOT EXISTS companion_contacts (
	companion_id       INTEGER  NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	peer_pubkey        BLOB     NOT NULL,
	name               TEXT     NOT NULL DEFAULT '',
	type               TEXT     NOT NULL DEFAULT '',
	lat                INTEGER  NOT NULL DEFAULT 0,
	lon                INTEGER  NOT NULL DEFAULT 0,
	feat1              INTEGER  NOT NULL DEFAULT 0,
	feat2              INTEGER  NOT NULL DEFAULT 0,
	out_path           BLOB,
	out_path_hash_size INTEGER  NOT NULL DEFAULT 0,
	last_seen          DATETIME,
	last_advert_ts     INTEGER  NOT NULL DEFAULT 0,
	added_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	metadata           TEXT     NOT NULL DEFAULT '{}',
	PRIMARY KEY (companion_id, peer_pubkey)
);

CREATE TABLE IF NOT EXISTS packets (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	received_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	direction     TEXT NOT NULL,
	raw           BLOB NOT NULL,
	route_type    INTEGER,
	payload_type  INTEGER,
	snr           REAL,
	rssi          INTEGER
);

CREATE TABLE IF NOT EXISTS messages (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	companion_id   INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	channel        TEXT NOT NULL,
	channel_hash   INTEGER NOT NULL,
	sender         TEXT NOT NULL DEFAULT '',
	text           TEXT NOT NULL DEFAULT '',
	direction      TEXT NOT NULL,
	timestamp      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	snr            REAL,
	rssi           INTEGER,
	confirmed      INTEGER,
	path_hashes    BLOB,
	path_hash_size INTEGER,
	hops           INTEGER,
	status         TEXT
);

CREATE TABLE IF NOT EXISTS message_echoes (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id      INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	received_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	path_hashes     BLOB,
	path_hash_size  INTEGER NOT NULL DEFAULT 1,
	hops            INTEGER NOT NULL DEFAULT 0,
	snr             REAL,
	rssi            INTEGER
);

CREATE TABLE IF NOT EXISTS conversation_reads (
	companion_id    INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	conversation_id TEXT NOT NULL,
	last_read_id    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (companion_id, conversation_id)
);

CREATE TABLE IF NOT EXISTS blocked_senders (
	companion_id    INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	conversation_id TEXT NOT NULL,
	sender          TEXT NOT NULL,
	blocked_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (companion_id, conversation_id, sender)
);

CREATE TABLE IF NOT EXISTS node_metrics (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	ts      INTEGER NOT NULL,
	pubkey  BLOB    NOT NULL,
	metric  TEXT    NOT NULL,
	channel INTEGER NOT NULL DEFAULT 0,
	value   REAL    NOT NULL
);

CREATE TABLE IF NOT EXISTS node_state (
	pubkey       BLOB PRIMARY KEY,
	kind         TEXT    NOT NULL DEFAULT '',
	name         TEXT    NOT NULL DEFAULT '',
	last_poll_ts INTEGER NOT NULL DEFAULT 0,
	last_ok_ts   INTEGER NOT NULL DEFAULT 0,
	last_error   TEXT    NOT NULL DEFAULT '',
	state        TEXT    NOT NULL DEFAULT '{}',
	companion_id TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS node_neighbors (
	ts              INTEGER NOT NULL,
	pubkey          BLOB    NOT NULL,
	neighbor_pubkey BLOB    NOT NULL,
	snr             REAL,
	PRIMARY KEY (ts, pubkey, neighbor_pubkey)
);

CREATE TABLE IF NOT EXISTS app_config (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	config     TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	log_level       TEXT,
	connection_type TEXT NOT NULL DEFAULT 'kiss',
	connection      TEXT,
	baud_rate       INTEGER,
	freq            REAL,
	bw              REAL,
	sf              INTEGER,
	cr              INTEGER,
	tx              INTEGER,
	listen_addr     TEXT,
	setup_complete  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS companion_channels (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	companion_id INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	name         TEXT    NOT NULL,
	private_key  TEXT    NOT NULL DEFAULT '',
	UNIQUE (companion_id, name)
);

CREATE TABLE IF NOT EXISTS triggers (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	companion_id         INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	type                 TEXT    NOT NULL,
	template             TEXT    NOT NULL DEFAULT '',
	char_limit_behaviour TEXT,
	match_patterns       TEXT    NOT NULL DEFAULT '',
	contacts             TEXT    NOT NULL DEFAULT '',
	retry_timeout        INTEGER,
	max_retries          INTEGER,
	path_hash_size       INTEGER,
	schedule             TEXT
);

CREATE TABLE IF NOT EXISTS trigger_channels (
	trigger_id INTEGER NOT NULL REFERENCES triggers(id) ON DELETE CASCADE,
	channel_id INTEGER NOT NULL REFERENCES companion_channels(id) ON DELETE CASCADE,
	PRIMARY KEY (trigger_id, channel_id)
);

CREATE TABLE IF NOT EXISTS mqtt_settings (
	id                INTEGER PRIMARY KEY CHECK (id = 1),
	enabled           INTEGER,
	node_companion_id INTEGER REFERENCES companions(id) ON DELETE SET NULL,
	iata_code         TEXT,
	status_interval   INTEGER,
	owner             TEXT,
	email             TEXT
);

CREATE TABLE IF NOT EXISTS mqtt_brokers (
	id                      INTEGER PRIMARY KEY AUTOINCREMENT,
	name                    TEXT    NOT NULL,
	enabled                 INTEGER NOT NULL DEFAULT 1,
	dedup                   INTEGER NOT NULL DEFAULT 0,
	transport               TEXT    NOT NULL DEFAULT 'tcp',
	host                    TEXT    NOT NULL DEFAULT '',
	port                    INTEGER NOT NULL DEFAULT 0,
	packet_topic            TEXT,
	status_topic            TEXT,
	disallowed_packet_types TEXT    NOT NULL DEFAULT '',
	retain_status           INTEGER NOT NULL DEFAULT 0,
	tls_enabled             INTEGER NOT NULL DEFAULT 0,
	tls_insecure            INTEGER NOT NULL DEFAULT 0,
	auth_type               TEXT    NOT NULL DEFAULT 'none',
	username                TEXT    NOT NULL DEFAULT '',
	password                TEXT    NOT NULL DEFAULT '',
	path                    TEXT    NOT NULL DEFAULT '',
	audience                TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_packets_received_at ON packets(received_at);
CREATE INDEX IF NOT EXISTS idx_companion_contacts_companion ON companion_contacts(companion_id);
CREATE INDEX IF NOT EXISTS idx_messages_companion_channel ON messages(companion_id, channel);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_message_echoes_message ON message_echoes(message_id);
CREATE INDEX IF NOT EXISTS idx_node_metrics_q ON node_metrics(pubkey, metric, ts);
CREATE INDEX IF NOT EXISTS idx_node_neighbors_pubkey ON node_neighbors(pubkey, ts);
CREATE INDEX IF NOT EXISTS idx_companion_channels_companion ON companion_channels(companion_id);
CREATE INDEX IF NOT EXISTS idx_triggers_companion ON triggers(companion_id);
CREATE INDEX IF NOT EXISTS idx_trigger_channels_trigger ON trigger_channels(trigger_id);
