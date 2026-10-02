# Config, storage & API reference

Relational config tables, the per-resource config REST, backup/restore, node monitoring, and the REST endpoint summary (`s.routes()` in `internal/api/server.go` is authoritative).

## Config management (web UI)

The sidebar has **Bots** / **Repeater** / **MQTT** (Comms) and **Settings**
(System) plus add/edit/delete on the Companions page (the chat header's
"edit" action deep-links there via `?edit=<name>`). Each edits **one resource** through
the per-resource config REST (`/api/config/*`): pages fetch only the slice
they need (`hooks/useApiObject` for single-row settings/mqtt, `hooks/useApiList`
for the lists) and write through `lib/configApi.ts`. There is **no
whole-document fetch or PUT** — the old `GET/PUT /api/config` (and
`hooks/useConfig.ts`) were removed because the GET shipped every secret to the
browser. Read DTOs are **secret-redacted** (`privateKeySet` / `passwordSet` /
`modemTokenSet` booleans); a write **omits** a secret field to keep the stored
value, sends it to set, sends `""` to clear. The openHop modem token is one of
these: it lives in `settings.modem_token`, never inside the connection string,
because `GET /api/config/settings` returns that string in full.

Server-side, every write goes through `backend.configMutate`
(`internal/app/config_rest.go`): inside one `WriteSync` it loads the current
row snapshot (`configRows`), applies the change in memory, **assembles +
`Validate()`s the whole config BEFORE persisting** (by surrogate id), then
reloads. So an invalid edit is rejected before it touches the DB, and a
rename/key-rotation keeps the row's id. Reloads are diff-based
(`reloadCompanions` in `internal/app/app.go`): only companions whose
*effective* block changed are restarted — unchanged ones keep running,
sessions intact, no re-advert; log-level changes apply with zero restarts. A
radio/connection change still restarts everything (modem reconnect);
`listenAddr` needs a process restart.

- **Local sensors are not config.** `sensors` (provider, kind, name, options
  JSON, bindings JSON; the name unique ignoring case), `sensor_state` (one row per
  sensor: what a part like the BME680 has learned, replaced in place) and
  `telemetry_map` (per node: `node_kind`, `node_id`, channel, LPP type, sensor and
  metric; unique on node, channel and type) live outside `config.Config` and
  bypass `configMutate`. `node_kind` and the three `telem_*` columns are CHECKed
  to their few values.
  Sensor and map writes go through `internal/app/sensors.go` and
  `telemetry_map.go`, which hold `sensorWrites` from their checks to the hub
  reload so racing requests cannot each pass the checks the other breaks.
  The `radio` provider reads the board's battery and MCU temperature from what
  the liveness probe already cached (`liveRadio` in `internal/app/sensors.go`),
  so it adds no link traffic; its sample time is the board's last answer, kept
  across a reconnect like health's, and a 0 mV battery (the firmware's "not
  supported") reads 0 V, as the firmware sends it. SPI has no board, so the
  provider is unavailable there.
  `companions.telem_base/loc/env` hold who may read each telemetry class
  (`deny`, `selected`, `contacts`), written only by
  `PUT /api/config/companions/{id}/telemetry`, so every other companion edit
  carries them through. The map keys on node kind and id with no foreign key
  (a row may be the repeater's), so deleting a companion or the repeater deletes
  its rows, and a pruned backup drops rows whose node is gone.
- **Config is stored relationally** (the config tables: `settings`,
  `mqtt_settings`, `mqtt_brokers`, `companions`, `companion_channels`,
  `triggers`, `trigger_channels` — all with surrogate INTEGER ids so name /
  pubkey / private_key are mutable columns nothing references). The
  assemble/disassemble seam between these rows and the in-memory
  `*config.Config` the runtime consumes lives in `internal/app/config_tables.go`
  (`loadConfigRows` → `assembleFromRows`; `writeConfigToTables` disassembles).
  The legacy `app_config` JSON blob table is **dormant**: the baseline schema
  still creates it, but `initConfigTables` reads it only on a never-initialized
  DB (no `settings` row) to import a pre-relational blob, and no live DB has one
  left to import.
- **The database is the source of truth.** Config files are one-time imports
  (`internal/app/config.go` `resolveConfig`): an explicit `-config path` flag
  imports and **overwrites** the stored config; otherwise the DB wins; a first
  run imports a default-named file from the cwd or bootstraps a quiet default
  (zero companions — the first-run wizard creates one). SIGHUP reloads from
  the DB.
- **Legacy config-file format is import-compatible.** The pre-relational ("main"
  deployment) format used `nodeType`, `[[bot]]`/`[[bot.trigger]]`, and
  `[[observer]]`/`[observer.broker]`; `migrateLegacyFormat` (in
  `internal/config/legacy.go`, run first in `ApplyDefaults`) folds those into
  `connectionType` / `[[companion]]` / a single top-level `[mqtt]` so an
  existing deployed config imports without dropping the bot or MQTT feed. A
  bot and an observer **sharing a name are the same node**: the observer is
  matched to that companion (else a companion is created), its `keyFile` seed
  becomes the companion's identity (via the `KeyFile`→`PrivateKey`
  `MigrateKeyFiles` path) and its `[advert]` its position. Legacy broker
  `ws`/`wss` transports normalize to `websockets` (`wss` implies TLS); the
  canonical schema and `BrokerConfig.Validate` only know `tcp`/`websockets`.
- **Validation is the crash guard.** A `startCompanions` failure after reload
  **exits the process**, so `Config.Validate()` must reject anything that
  would fail companion construction: trigger type/template/regex/cron,
  channel privateKey hex, duplicate companion names (the API routes and the
  runtime registry still key by name, so names must be unique even though
  storage keys by id), mqtt node references, broker fields.
  `configMutate` runs this on the assembled config before persisting.
  `TriggerConfig.Validate` parse-checks templates with stubbed trigger funcs
  (`formatPathBytes`) — extend the stubs if the templater gains functions.
- **`GET /api/health` is the monitoring endpoint**, shaped for Uptime Kuma and
  friends rather than for a person. It **always answers 200 while the process is
  alive**, including when the radio is not: a monitor that cannot reach OwlShack
  already fails the request, so the status code is not spent on a second
  opinion. `problems` is a possibly-empty array of binary faults in what the
  node cannot work without, and `status` is `ok` exactly when it is empty. The
  whole list: the modem not connected, or a companion or the repeater failing to
  start (which closes a modem that was fine, so it is named as such); the board
  not answering for `modem.AnswerDeadline()`; transmit failing (at least three
  sends in a row, for two minutes, the shape a lost `TX_DONE` leaves); no node
  running; and the database's disk under 32 MiB free. MQTT has its own verdict in `mqtt`: `status` is `ok`
  when every enabled broker is connected, `degraded` with a `problems` entry per
  one that is not, and `off` when none is enabled, because a broker being down
  loses uploads to the map, not the node, and should not page whoever watches
  the radio. Everything else is a fact to threshold externally, never a verdict. Three
  radio ages are kept apart on purpose: `lastReplySecs` is the liveness probe's
  own signal (the board answering a query, an error reply included, which a quiet
  mesh does not move, carried across a reconnect since reconnecting never waits
  for an answer; a board that has not answered yet counts from when the first
  unanswered link connected, so one hung before its first answer is caught too;
  KISS answers status queries, openHop its STATUS, and SPI has no board to ask),
  `lastRxSecs` is mesh traffic, and `lastTxSecs` is our own sends (a frame
  written to the modem, whatever the board then made of it; `txFailedInARow` and
  `txFailingSecs` say whether sends are actually going out). Each is
  `null` rather than `0` when there is nothing to measure from, so "cannot say"
  is distinguishable from "just now". `database.writesDroppedLastSecs` is the
  database signal to threshold. The `WriteAsync` overflow is otherwise silent —
  the write vanishes and every surface downstream still looks healthy — but the
  raw `writesDropped` count only ever rises, so it cannot tell "failing now"
  from "had a bad minute last Tuesday". A past loss is deliberately **not** put
  in `problems`, which would pin the endpoint to `degraded` for the life of the
  process after one transient overflow. `writeQueueLen` is a point sample of a
  queue that normally drains in microseconds, so it reads 0 unless the writer is
  *sustainedly* behind and will not catch a brief spike. `walBytes` is the
  write-ahead log's size on disk: a checkpoint trims it back to 4 MiB, so a
  value that keeps climbing means checkpoints are not completing, which nothing
  else would show. `diskFreeBytes` is there because a full disk fails every
  write inside the writer, which drops nothing and so shows nowhere else; it is
  `null` where the platform cannot say. **It is written on the
  assumption it may be public**: the running nodes are not listed at all — a
  name or pubkey is on-air already, but published on the internet it ties a
  hostname to a mesh identity that public maps resolve to coordinates — there is
  no position, and a broker reports
  `connectedSecs` / `lastErrorSecs` rather than the transport error, which would
  name a private broker's host and port. Both are ages for the same reason the
  radio's are: `connected` is a sample, so a broker reconnecting every thirty
  seconds reads `true` on nearly every scrape and only a connection age resetting
  to near zero reveals the flapping, while the observer never clears its last
  error on reconnect, so a boolean built from it would stay true for the life of
  the process. Leaving the nodes out costs
  nothing: peer counts are hydrated from SQLite and only grow, so they read the
  same with the antenna unplugged, and a node that fails to start exits the
  process rather than quietly leaving a list. "No node is running at all" is the
  only state worth reporting and it is a `problems` entry. That
  is a property of this endpoint alone — the rest of the API is unauthenticated
  and must not be exposed with it. It also never polls the board: board readings
  come from `StatsProvider.CachedStats`, not `Stats`, so a scrape costs no
  airtime and no 500ms wait. `GET /api/radio/status` still polls, which is why
  it takes ~500ms — that one is a diagnostics page a person is looking at.
- **Feed triggers (`rss`, `cap`) poll `triggers.url`** on `triggers.schedule`,
  which unlike `cron` may be blank and then defaults to `@every 5m`. The bot
  editor writes that column as a number and a unit (`@every 15m`) rather than a
  cron expression, and `validateFeedSchedule` floors any `@every` at one
  minute — a crontab spec has no seconds field, so a minute is already its
  finest step and only the descriptor form can ask for less. Each new
  item goes to every channel in `trigger_channels` *and* as a DM to every
  pubkey in `triggers.contacts` — the same column the `dm` type uses to filter
  senders, read here as recipients. At least one of the two is required: with
  neither, the trigger can never say anything. The first poll after a start only
  records what is already published, so a restart never replays a backlog onto
  the mesh, and one poll sends at most five items, the newest, counted as they
  are sent — a feed that republishes itself would otherwise queue dozens of
  transmissions onto a duty-cycled radio. A `cap` trigger fetches the alert
  document each entry links to; a 4xx or unparseable document is recorded as
  seen rather than refetched every poll. It sends an alert once however many
  entries post it (Meteoalarm posts one per area: 57 entries for 2 alerts in
  Germany's feed), skipping an entry whose message it has already handled, until
  a day past the alert's expiry. A message is named as CAP 1.2's `<references>`
  names one, by sender, identifier and sent, so an Update or Cancel (a new
  identifier) is sent even when it is served at the same link. Documents are
  cached for one poll only, so 47 entries cost one fetch but an update at the
  same link on a later poll is fetched fresh. A message is marked handled only
  once sent, so a copy that fails the filters cannot hide one that passes. One
  poll fetches at most 20 alert documents, each with its own 15 s timeout, and
  leaves the rest for the next poll. An entry whose document was already in the
  feed when the bot started, for a message sent before then, is backlog and is
  not sent. The Test lists each alert once.
  Templates get flattened conveniences (`.Title`/`.Link` for rss,
  `.Severity`/`.Headline`/`.Areas` for cap) plus the parsed structs themselves:
  `.Item` (`*gofeed.Item`) on both, and `.Alert`/`.Info` (`*cap.Alert`) on cap.
  Match patterns on a feed trigger are **field-scoped**, written
  `<field>:<regex>` (`severity:^(Extreme|Severe)$`). Patterns naming the same
  field are alternatives; different fields must all match, which is the pairing
  regex alone cannot express — alternation already says OR inside one field.
  `matchFields` in `internal/config/feedfields.go` is the vocabulary and
  `TriggerConfig.Validate` rejects an unscoped or misspelt field rather than
  compiling it as a bare regex that silently never matches. A field holding
  several values (`area`, `category`, `geocode`) joins them one per line, so an
  anchor needs `(?m)`. `geocode` is every area's `<valueName>=<value>`, for
  publishers that name areas by code instead of drawing them: on the NWS feed 89%
  of alerts (marine, coastal, wind) carry only zone codes, so
  `geocode:(?m)^UGC=TX` is Texas, and Meteoalarm's German, Spanish and Italian
  feeds carry only `EMMA_ID`/`WARNCELLID` codes. A cap entry's link typed
  `application/cap+xml` is fetched in preference to its first link, which on
  Meteoalarm is a web page.
- **Where a `cap` trigger's alerts must be** is a point or a set of regions,
  never both, checked before the patterns against the polygons and circles in
  the rendered info block. A point is `triggers.location_lat`/`_lon`/`_radius_km`
  (`location: {lat, lon, radiusKm}`): an alert passes when it covers the point or
  comes within `radiusKm` (0-500). Regions are `triggers.location_regions`
  (`regions: [id, ...]`, newline-encoded): an alert passes when its overlap with
  a region is at least 10% of whichever is smaller, the alert or the region,
  which keeps a warning drawn along a district line out of the neighbour it
  spills a sliver into. `null` for both takes alerts from anywhere, and `[]` is
  refused. An alert with no shape never passes either: a publisher that sends
  none never will. The bot Test reports it as `placement` (`anywhere`, `inside`,
  `outside`, `noShape`).
- **Regions are Natural Earth's admin-1 boundaries** (public domain, every
  country), each ring simplified to 1/200 of its width but never past about
  1 km, so a city-state or an atoll keeps its shape, and embedded as
  `internal/region/regions.bin.gz` (2.2 MB, delta-encoded microdegrees, each
  region's exact area precomputed); `go generate ./internal/region`
  rebuilds it from the pinned release. A region's id is Natural Earth's
  `adm1_code` (`NZL-3398` is Auckland), since ISO 3166-2 codes repeat within it.
  The generator refuses to drop an id the current file has, since a bot naming
  it would stop loading. The data is decoded on first use (about 30 ms), and a
  region's edges are indexed by latitude band the first time it is tested, so
  checking an alert against Nunavut, the most detailed region, takes under 1 ms.
  **At release**, compare the pinned tag (`source` in `internal/region/gen/main.go`)
  with Natural Earth's latest release (`gh api
  repos/nvkelso/natural-earth-vector/releases/latest --jq .tag_name`; v5.1.2 of
  May 2022 was the latest when this was written). To move to a newer one, change
  the tag, run `go generate ./internal/region`, and commit the new file with it.
  If the generator stops because ids are gone, those regions were renumbered or
  removed: a saved bot naming one would stop loading, so check whether any bot
  uses them before accepting with `go run ./gen -allow-removed regions.bin.gz`.
  The output is deterministic (sorted ids, fixed rounding, no gzip timestamp), so
  rerunning the generator on an unchanged tag must leave `git status` clean; if it
  does not, the committed file is stale.
- **DM acceptance is `companions.dm_policy`** (`contacts` | `allowlist` |
  `anyone`, default `contacts`) with `companions.dm_allow` holding the
  allowlist's pubkeys newline-encoded, the same encoding `triggers.contacts`
  uses. The decision is `CompanionConfig.AllowsDMFrom`, applied in the
  companion's TxtMsg handler to plain DMs only: a CLI reply or room push is a
  response to a session we opened, so it is never gated. Changing either field
  restarts the companion, since `triggersOnlyChange` only spares a trigger-only
  edit — so the RX path reads them without a lock.
- **A companion's flood scope is `companions.flood_scope`**, a region name or
  `""` for unscoped. `config.ScopeRegion` turns it into the region the way
  firmware's `RegionMap::getTransportKeysFor` does (a bare `sco` is the `#sco`
  hashtag region), and the repeater's regions go through the same function so
  both sides agree on keys. It becomes the companion node's `WithFloodScope`,
  so the library's own floods (group text, DMs, adverts) are scoped there.
  Everything OwlShack builds itself goes through `meshpath.ScopeFlood` before it
  is sent: ACKs, path returns, request replies and the repeater client's
  requests, which mirrors the firmware sending every flood through
  `sendFloodScoped`. A direct packet is never scoped. `"*"` and `"$"` names are
  refused: one is no scope and the other's key cannot be derived from its
  name. The companion edit carries the column through when `floodScope` is
  omitted, as forms that do not show it must not clear it.
- **MQTT is top-level, one node.** `Config.Mqtt` (`mqtt.node` selects the
  feeding companion, empty = first; `enabled` nil = on). Stored as
  `mqtt_settings.node_companion_id` (a real FK, ON DELETE SET NULL) and
  resolved to the companion **name** in `assembleFromRows` — so renaming the
  feed node no longer breaks the reference. The MqttPage node selector is by
  companion id. Legacy per-companion `[companion.mqtt]` blocks are hoisted by
  `ApplyDefaults` on load — first one wins. At runtime `startCompanions` copies
  the block into the selected companion's config; `CompanionConfig.Mqtt` is
  otherwise deprecated.
- **Broker topics are templates.** `broker.packetTopic` / `broker.statusTopic`
  take placeholders `{iata} {pubkey} {name}` (meshcoretomqtt's `{IATA}` /
  `{PUBLIC_KEY}` uppercase forms also resolve); empty = the old hardcoded
  `meshcore/{iata}/{pubkey}/<kind>`. The deprecated `topicPrefix` field is
  folded into explicit templates by `ApplyDefaults` (`migrateTopicPrefix`).
  Resolution is `resolveTopics` in `internal/mqtt/observer.go` — the LWT in
  `connectBroker` must use the same resolution as the live status publisher.
  Templates are validated in `BrokerConfig.Validate` (unknown placeholders,
  MQTT wildcards). **Broker connection state is surfaced**:
  `GET /api/mqtt/status` returns every *configured* enabled broker with
  `connected` (read from paho's own `IsConnected`, so its auto-reconnect needs
  no tracking), `lastError`, `connectedTs`, and publish/drop counts; the
  MqttPage polls it every 5 s for the per-broker pill. It lists configured
  brokers rather than live clients on purpose — a broker that never connected
  must stay visible. **A failed initial connect retries** (`retryConnect`,
  5 s doubling to 5 min, guarded by `brokerClient.retrying`): paho's
  `SetAutoReconnect` only covers a client that connected at least once, so
  without it a broker that was down at startup stayed down until the process
  restarted. The same loop rescues a failed token-refresh reconnect, which
  otherwise stranded the broker until the next refresh tick (0.8 x token
  lifetime). `doPublish` skips a disconnected client and counts a drop —
  publishing to one would block for `publishWaitTimeout` per job and stall the
  worker. Per-broker auth: `token` (Ed25519 JWT from the node
  identity, username `v1_<pubkey>`, 10-min lifetime, minted fresh on every connect/reconnect via paho's `CredentialsProvider` plus a refresh tick) or `basic`
  (user/pass). The Add Broker modal has presets (`BROKER_PRESETS` in
  `MqttPage.tsx`) mirroring meshcoretomqtt's: LetsMesh US/EU, Waev A/B,
  MeshMapper — update there if upstream endpoints change.
- **`ChannelRef` JSON gotcha**: it implements `UnmarshalText` (TOML string
  form), which makes encoding/json demand a string — `ChannelList`'s
  `UnmarshalJSON` therefore decodes objects into a tag-equivalent anonymous
  struct. Don't "simplify" it back to unmarshalling `ChannelRef` directly;
  that breaks JSON config-file import and the legacy-blob migration. (The API
  no longer round-trips `ChannelRef` — triggers reference channels by id via
  `trigger_channels`.)
- **Companion identity** is the `companions.private_key` column (64-hex
  ed25519 seed; `pubkey` is a derived, cached column). On create with a blank
  key, `SaveCompanion` generates one (`config.GenerateSeedHex`); on update it
  inherits the stored key from the row snapshot when the write omits it, so an
  edit can never silently rotate a running identity (no more
  `inheritCompanionKeys` — each resource owns its secret-keep logic). Keys are
  never sent to the browser (`privateKeySet` boolean only). Legacy `keyFile`
  entries are read and inlined at file import (`MigrateKeyFiles`). **Renaming a
  companion is fully safe**: every per-companion table — config references
  (mqtt node id, trigger channels) *and* history (messages, contacts,
  conversation reads, blocked senders) — keys on the surrogate
  `companions.id`, not the name. The history tables carry an INTEGER
  `companion_id` FK (`ON DELETE CASCADE`) and the runtime threads the id via
  `CompanionConfig.ID` / `Companion.ID()`. The
  API keeps `{name}` in its URLs and resolves it to the id per request
  (`CompanionRepo.IDByName`, `Server.companionID`). `node_state.companion_id`
  is the lone exception: it stays a name (node-keyed, self-heals each poll).

## SPI board registry (`internal/modem/boards.json`)

One entry per radio hat, keyed by the name stored in `settings.spi_board`.
**Every entry is drivable** — a pin map nobody can complete or test does not go
in the file, because a listed board reads as a supported one. 15 boards today,
all SX1262 on a Raspberry Pi header.

**`verified`** is declared per board and required: `"hardware"` (run here on
the physical hat) or `"community"` (wiring transcribed from a vendor or
community list, never tested). The UI labels anything but `"hardware"` as
**unverified**, and `modem_sx12xx.go` logs a warning at startup. 4 of 15 are
`"hardware"`.

Two invariants back that up, and neither is a runtime rule:

- `TestBoardRegistryIsComplete` fails on an entry with no RF-switch control
  (neither `use_dio2_rf` nor `txen_pin`), which would transmit into a
  terminated switch and read as a quiet mesh.
- `gpio_chip` is a hard load error: periph resolves pins by name on the default
  chip, so a banked pin map would drive whatever header line happens to share
  the number. It is how a hat for a LuckFox Pico or another non-Pi board fails,
  loudly, instead of silently driving Pi pins.

## Backup & restore

Export/import so a non-technical operator can move a node to new hardware
without hand-copying `meshcore.db`. UI is `BackupWizard` (opened from
`BackupPanel` on Settings) for export, and `RestoreFromBackup` inside
`SetupWizard` for restore. Logic: `internal/app/backup.go` over
`internal/store/backup.go`.

- **Restore is first-run only, by design.** Applying a backup to a node that is
  already configured and on the air is what breaks: identities collide,
  contacts half-overwrite, live sessions dangle. So the Settings panel is
  export-only and says where restore lives; the wizard's welcome step offers
  "Restore a backup" beside "Begin". **Don't add a restore button to Settings.**
- **A backup is a pruned copy of the database**, not a per-table dump:
  `VACUUM INTO` a temp file, `store.PruneBackup` deletes what the operator did
  not select, then `VACUUM` so the file actually shrinks (3.4 MB → 208 KB with
  history excluded). One format, and it restores through the whole-file swap.
  Companion pruning relies on `ON DELETE CASCADE`, so `PRAGMA foreign_keys`
  must be on in the copy — deleting a companion is what removes its channels,
  contacts and messages.
- **Restoring cannot overwrite the live file** (this process holds it open), so
  `StageRestore` writes `meshcore.db.restore` and `AdoptPendingRestore` (called
  from `Run` *before* `store.Open`) renames it into place on the next start,
  keeping the outgoing DB as `meshcore.db.replaced` and deleting the stale
  `-wal`/`-shm` — leaving those would corrupt the adopted DB. The API returns
  `restartRequired: true`.
- **The companion selection is always explicit.** `companionIds` is exactly
  what to keep: `[]` is none (a settings-only backup), every id is all. It is
  **required** — an omitted or `null` field is a 400, not a default, which is
  why the API DTO types it `*[]int64` and `decodeBackupOptions` fills in no
  defaults at all. Absent must never mean "all": a caller that forgets the
  field would silently get the largest possible backup, and the nil-vs-empty
  distinction does not survive re-serialization or a non-Go client. It keys on
  the surrogate id, not the name, like the rest of the config REST. An unknown
  id simply matches nothing.
- **Identity keys are opt-in** and govern `companions.private_key` /
  `repeater.private_key` only; channel keys are shared channel secrets, not
  identities, so they always travel. When excluded, `PruneBackup` blanks the
  columns and **`mintMissingKeys` (in `resolveConfig`) generates and persists
  new ones at startup** — required, because `Config.Validate` treats two blank
  keys as duplicates and would stop the process from starting.
- `POST /api/backup/estimate` counts what a selection captures using the same
  predicates `PruneBackup` deletes by, so the wizard can show what including
  the packet log costs before building the file. `Bytes` is the live DB size,
  i.e. an upper bound.
- **Day windows** are `-1` all, `0` none, or N days (`store.DaysAll` /
  `DaysNone`). They apply to `packets.received_at` and `messages.timestamp`
  (DATETIME) and `node_metrics.ts` / `node_neighbors.ts` (unix seconds) — hence
  the `datetime('now',?)` vs `unixepoch('now',?)` split.
- **Import sniffs the upload**: the `SQLite format 3\0` magic means a backup
  (staged); anything else goes through `importConfigFile`, so an operator can
  also bring an old `.toml`/`.yaml`/`.json` config across through the UI. The
  filename only picks the parser.
- **A backup carries secrets** — channel keys, broker credentials, saved
  repeater passwords — so the export sets `Cache-Control: no-store` and both
  the wizard and the panel say so.
- **Import validates before touching anything**: `InspectBackup` requires a
  readable SQLite file with a `settings` table and `user_version <=`
  `LatestSchemaVersion()` (migrations never run backwards); a rejected upload
  leaves nothing staged.
- **An upgrade keeps a copy of the database first**: before migrating from
  version N to M, the store writes `meshcore.db.pre-vN-to-vM` with `VACUUM
  INTO` and syncs it, so going back to an older build is stopping OwlShack and
  moving the copy into place. An upgrade that fails part way keeps its copy,
  and the next start reuses it rather than copying the half-upgraded database;
  older copies are deleted only once an upgrade finishes. If the copy cannot be
  made (a full disk), the upgrade goes ahead without one and logs a warning, so
  an updated node still starts. Builds from this one
  on name the copy when they refuse a newer database (v1.4.x does not). Nothing
  is copied for a fresh database.

## Node monitoring

A type-agnostic poller (`internal/monitor`) polls monitored contacts on a staggered schedule → `node_metrics` (time-series) + `node_state` (latest snapshot, JSON `metric→value`). Gotchas:

- **Kind resolution is peer-type-first** (`monitorKind` in `internal/app/monitor_collector.go`): the advertised peer type wins when known; `metadata.isRepeater` is only the fallback for repeaters whose advert hasn't been heard. A stray `isRepeater` flag must never route a chat node to the repeater collector — companion firmware has no login handler, so that poll can only time out.
- **Companions are sessionless, telemetry-only.** `companionCollector` sends one `REQ_TYPE_GET_TELEMETRY_DATA` contact request (ECDH with the static identity — no login, no password). What comes back is gated by the *remote* node's telemetry-sharing prefs (base/location/environment, optionally per-contact flags) and requires the bot to be in its contacts. `MonitoringSettings` takes a `kind` prop ("repeater" | "companion") and hides password/probes for companions; its save only writes `isRepeater`/`repeaterPassword`/`monitorProbes` for the repeater kind. Chat contacts enrol via the monitoring panel on `ContactDetailPage`.

- **Metric keys are channel-faithful.** A node's *own* readings keep clean names (`mcu_temperature`, `battery`); *external* sensors carry their LPP channel (`temperature_ch2`, `humidity_ch3`) — the channel is the firmware's stable per-sensor identity on 1.16+. Status fields are fixed names (`last_snr`, `battery_mv`). Don't strip the channel to "unify" sensors — that makes multi-sensor identity order-dependent. Frontend tiles pattern-match by type (`metricKeysOfType` in `NodeStatTiles.tsx`), so they render whatever channel a board uses; `metricDef`/`metricOrderIndex` resolve `_chN` keys back to the base catalogue.
- **Firmware channel split:** ≤1.15 lumps all sensors on the self channel (ch1); 1.16+ gives each its own (ch2+). `parseRepeaterTelemetry` marks only the FIRST self-type reading on the self channel as the node's own (MCU temp/battery/location), the rest external — otherwise 1.15's external temp collides with the MCU temp under `mcu_temperature` and is lost. A 1.15→1.16 upgrade changes a board's channel keys (accepted one-off history discontinuity).
- **Snapshot merges, never blanks:** `UpsertNodeState` merges new readings onto the stored snapshot, so a partial poll (status OK, telemetry failed) keeps last-known values; a fully-failed poll uses `MarkPollFailure` (touches only `last_poll_ts`/`last_error`). Telemetry/neighbours probes retry once in-poll (`monitorProbeAttempts`).
- **Manual poll:** `monitor.Service.PollNow` (behind `POST /api/nodes/{pubkey}/poll`) runs an out-of-band poll, serialized against scheduled polls via `pollMu.TryLock` (returns "a poll is already in progress" rather than racing the radio).
- **Overview staleness dot** derives from each node's configured interval, not a constant: stale when `age > interval + max(25%, 5min)`. The interval is exposed per-node via `intervalSecs` on `/api/nodes/monitored`.
- **List membership is toggle-driven, not data-driven:** `/api/nodes/monitored` lists the poller's current target set (`monitor.Service.Targets()`, i.e. contacts with the monitor flag), merged with `node_state` snapshots where they exist. A freshly enrolled node appears immediately with `lastPollTs: 0` (UI shows a muted dot + "waiting for first poll"); toggling monitoring off hides a node even though its `node_state` row is retained. Don't go back to listing `node_state` rows directly — that hid new nodes until their first poll and showed unmonitored leftovers forever.

## REST endpoints (summary — see `internal/api/server.go` for the canonical list)

Every JSON body is capped at 1 MiB (`readJSON`); backup uploads have their own, larger limit.

```
GET  /api/peers
DELETE /api/peers/{pubkey}
GET  /api/packets?limit=N

GET  /api/companions
GET  /api/companions/{name}/contacts
GET  /api/companions/{name}/contacts/{pubkey}                (single contact; 404 if absent)
POST /api/companions/{name}/contacts                         { pubkey }   (also registers the peer with the running nodes)
DELETE /api/companions/{name}/contacts/{pubkey}
PATCH /api/companions/{name}/contacts/{pubkey}               { isRepeater?, repeaterPassword?, telemPerms?, ... }   (merges: only named fields change; an unknown or null field, or a value the form does not offer, is a 400; 404 if absent)

GET|POST|DELETE /api/companions/{name}/channels[/{channel}]

GET  /api/companions/{name}/conversations
POST /api/companions/{name}/conversations/{id}/read          { lastReadId }
POST /api/companions/{name}/conversations/{id}/block         { sender }
DELETE /api/companions/{name}/conversations/{id}/block/{sender}
GET  /api/companions/{name}/conversations/{id}/block
GET  /api/companions/{name}/conversations/{id}/participants
DELETE /api/companions/{name}/conversations/{id}/messages

GET  /api/companions/{name}/channels/{channel}/key
PATCH /api/companions/{name}/channels/{channel}               { name }

GET  /api/companions/{name}/contacts/{pubkey}/path
DELETE /api/companions/{name}/contacts/{pubkey}/path
PUT  /api/companions/{name}/contacts/{pubkey}/path             same body as the repeaters PUT below

GET  /api/companions/{name}/messages?channel=&limit=&afterId=
POST /api/companions/{name}/messages                         { channel, text }
DELETE /api/messages/{id}
GET  /api/messages/{id}/echoes
GET  /api/companions/{name}/messages/{id}/path?channel=

POST /api/companions/{name}/trace                            { path, pathHashSize }

POST /api/companions/{name}/repeaters/{pubkey}/login         { password }
GET  /api/companions/{name}/repeaters/{pubkey}/status
POST /api/companions/{name}/repeaters/{pubkey}/cli           { command }
GET|DELETE /api/companions/{name}/repeaters/{pubkey}/session
GET|DELETE /api/companions/{name}/repeaters/{pubkey}/path       GET: { outPath, hops, hasPath, directNeighbor, pathHashSize (the route's), bytesPerHop }
PUT  /api/companions/{name}/repeaters/{pubkey}/path            { route: flood|direct|path, path?, pathHashSize }

POST /api/companions/{name}/rooms/{pubkey}/login             { password, syncSince? }
GET|DELETE /api/companions/{name}/rooms/{pubkey}/session

# Per-resource config REST. Reads are scoped + secret-redacted (privateKeySet /
# passwordSet booleans, never the raw secret). Writes validate the whole
# assembled config before persisting (by surrogate id) and reload. There is NO
# whole-document /api/config endpoint — it was removed (it leaked secrets);
# unmatched /api/* paths 404.
GET  /api/config/settings                                    (radio/connection/log + setupComplete; modemTokenSet, never the token)
PUT  /api/config/settings
GET  /api/config/mqtt                                        (feed settings; node by companion id)
PUT  /api/config/mqtt
GET  /api/config/mqtt/brokers
POST /api/config/mqtt/brokers          PUT|DELETE /api/config/mqtt/brokers/{id}
GET  /api/config/companions                                  (id, name, pubkey, privateKeySet, …)
POST /api/config/companions            PUT|DELETE /api/config/companions/{id}
PUT  /api/config/companions/{id}/telemetry                   { base, location, environment }   (each deny | selected | contacts)
GET  /api/config/companions/{id}/channels
GET  /api/health                                             (monitoring snapshot; see below)
GET  /api/config/channels                                    (all channels; for trigger name resolution)
POST /api/config/companions/{id}/channels    PUT|DELETE /api/config/channels/{id}
GET  /api/config/triggers[?companionId=N]
POST /api/config/triggers              PUT|DELETE /api/config/triggers/{id}
POST /api/config/triggers/test/items   { companionId, type, url, match, template, location, regions }   (an unsaved rss/cap bot's newest feed items; sends nothing)
POST /api/config/triggers/test/render  { ...same, itemId }   (the message for one item, whether the patterns pass it, where it falls, and what a channel keeps)
GET  /api/regions/at?lat=&lon=         (the region under a point, with its outline; 404 at sea)
GET  /api/regions/{id}                 (one region and its outline)

GET  /api/mqtt/status                                        (live broker connection state; runtime, not config)
GET  /api/radio/status                                       (link counters; packetsRecv/packetsSent/crcErrors/driverErrors/recvRecoveries are SPI-only, inboundDroppedOldest/rxMetaTimeouts/rxMetaMisattributed/hwErrors/txOutcomeLost KISS-only, each absent rather than 0 on the other transport)
POST /api/radio/reset                                        (202; drops the modem and reconnects in the background)
GET  /api/spi/boards                                         (the board registry; [] when the backend is not up yet)
GET  /api/serial/ports                                       (host serial devices; `path` is the /dev/serial/by-id name where Linux has one, `device` the tty it resolves to, `stable` false when only the tty exists)

GET  /api/sensors                                            (every sensor with its last readings; also the `sensors` WS topic)
GET  /api/sensors/providers
POST /api/sensors/discover                                   { provider? }   (candidates beside problems, such as a bus that would not open)
GET  /api/sensors/kinds                                      (the parts catalogue)
POST /api/sensors                      PUT|DELETE /api/sensors/{id}   (refused 422 when it would strand a published channel or a derived sensor; 404 for an id not configured; secret options never read back, kept on edit by keepSecrets)
POST /api/sensors/test                                      { id, ...sensor }   (one try of an unsaved sensor of a testable kind: status, reply, each value; saves nothing)
GET  /api/sensors/telemetry-map                              (every node's map, the LPP catalogue and defaults, the 145-byte budget)
PUT  /api/sensors/telemetry-map                              { node, entries }   (entries required, each naming that node; [] clears the node's map)

POST /api/backup                                             (options JSON -> .db file download)
POST /api/backup/estimate                                    (same body; row counts, no file built)
POST /api/backup/import                                      (multipart "file", or a raw body)

GET  /api/nodes/monitored
GET  /api/nodes/neighbor-links                               (repeater-to-repeater SNR topology, last 24h, both ends pre-resolved)
GET  /api/nodes/{pubkey}/metrics
GET  /api/nodes/{pubkey}/history?metric=&from=&to=&bucket=
POST /api/nodes/{pubkey}/poll                                (on-demand poll; 502 on failure)

WS   /api/ws
```

`?afterId=` was added in the recent UI rewrite. `internal/store/message.go` exports `ListAfter(companionID, channel, afterID, limit)` and `routes_messages.go` honours `?afterId=` to return messages with `id > afterID` ordered ASC. The frontend uses this for delta backfill on cache re-open and on WS reconnect.
