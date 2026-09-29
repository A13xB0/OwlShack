# MQTT schema & radio settings

The published MQTT wire schema (shared with meshcore-bot), the TX duty cycle, and path hash size.

## MQTT wire schema

**Verify a name against the consumer, not just the firmware.** CoreScope
(<https://github.com/Kpa-clawbot/CoreScope>, default branch **master**
not `main` — a raw fetch of a `main` path 404s) is open source and its
`cmd/ingestor/main.go` is the readable record of what a real consumer parses:
`nestedOrTopLevel` checks `stats` first then top level (so a field's placement
is forgiving, its *name* is not), and `toFloat64` accepts numbers **and numeric
strings** via `ParseFloat`. Two consequences worth remembering: moving a field
into `stats` breaks nothing there, and a decimal SNR string is not rejected —
integer SNR is right because it is the firmware/bridge convention, not because
CoreScope would choke on `"4.75"`.

**"meshcoretomqtt" is two forks, so cite the one you checked.**
`Cisien/meshcoretomqtt` is the packaged layout (`bridge/message_parser.py`,
`bridge/serial_connection.py`) and is the one this doc quotes;
`Andrew-a-g/meshcoretomqtt` is a single root `mctomqtt.py` whose field set
differs — it has no `duration` at all. Paths below are Cisien's.

**meshcoretomqtt scrapes the firmware's serial output, but it does NOT forward
it whole.** The packet payload comes from the `MESH_PACKET_LOGGING` line via one
regex. The status `stats` block is built by `get_device_stats`
(`bridge/serial_connection.py`) as an **allowlist of 8 keys** copied out of the
three `stats-*` JSON replies, with one **renamed**:

| From | Copied | Dropped |
|---|---|---|
| `stats-core` | `battery_mv`, `uptime_secs`, `queue_len`, `errors` **-> `debug_flags`** | — |
| `stats-radio` | `noise_floor`, `tx_air_secs`, `rx_air_secs` | `last_rssi`, `last_snr` |
| `stats-packets` | `recv_errors` | `recv`, `sent`, `flood_tx`, `direct_tx`, `flood_rx`, `direct_rx` |

So `recv`/`sent` never appear in a meshcoretomqtt payload — they are
`stats-packets` field names the bridge discards, not names a status consumer
looks for. `errors` on the wire is `debug_flags`. `last_snr`/`last_rssi` in our
status block are an extension, not parity: no observer publisher emits them
(CoreScope reads them only on its client-RF topic).

A third publisher exists and is where `packets_recv`/`packets_sent` come from:
OffbandMesh's `src/helpers/wifi_telemetry/WifiTelemetry.cpp` publishes native
Home Assistant telemetry on `{prefix}/{node}/state` with `battery_mv`,
`battery_pct`, `uptime_seconds`, `tx_queue_len`, `noise_floor_dbm`,
`mcu_temp_c`, `last_rssi_dbm`, `last_snr_db`, `packets_recv`, `packets_sent`.
Note its `battery_pct` uses a piecewise Li-ion discharge curve — the reference
to copy if a percentage is ever wanted again (ours was a naive 3200-4200 linear
ramp).

Board readings live **inside `stats`** — `battery_mv` (millivolts, never a
percentage of our own devising), `mcu_temp_c`, `noise_floor`, `rx_air_secs`.

`statsBlock` in [`internal/mqtt/format.go`](../internal/mqtt/format.go) is a
**shared schema with meshcore-bot**: change a wire name in both repos or
downstream sees two dialects. Go shapes need not match (the bot returns
`hardware.ModemStats` directly, being KISS-only).

Our extensions, with no upstream equivalent: `tx_*`, `rx_*`, `hw_*`,
`handler_slow`, `flood_dups`, `direct_dups`.

### What a status publish reads

The heartbeat is every `StatusIntervalSeconds` (**300 s** default), so anything
cached rather than read per publish is stale for up to five minutes. Per
publish:

| Field | Freshness |
|---|---|
| `battery_mv`, `noise_floor`, `mcu_temp_c` | polled off the board every publish |
| `uptime_secs`, all counters, air seconds | live atomics |
| `radio` | captured at `modem.Setup`; a radio change forces a modem reconnect, which rebuilds the provider |
| `origin`, `origin_id` | captured at observer construction; a rename changes the companion block, so the companion (and its observer) is rebuilt |
| `model`, `firmware_version`, `client_version` | build-time constants |
| `repeat` | cached in an atomic, **pushed** on change |

`repeat` is the only one that needed handling. A CLI `set repeat` goes through
`repeaterReconfigurer` -> `writeConfigTx`, which persists and SIGHUPs, so
`reloadCompanions` re-applies the flag to every companion it returns — reused
instances included, since those keep their observer. `SetRelaying` then
publishes a status immediately **when the value actually changed**, so the feed
does not carry a stale relay state for up to five minutes. It stays silent when
the value is unchanged, because every reload re-applies it.

Ordering matters here and has already caused one wrong publish: `Observer.Start`
publishes its first `"online"` the instant a broker connects, so anything the
status carries must be set **before** `Start`. Setting the relay flag after it
made every restart advertise `repeat:off`, which CoreScope acts on — it drops
the node from its path-hop disambiguator.

### Consumer coverage

We publish to LetsMesh (US/EU), Waev (A/B), MeshMapper, and CoreScope. Only
CoreScope's ingest is open source; LetsMesh, Waev and MeshMapper serve 403 or
require auth, so for those the claim is field coverage against the
meshcoretomqtt contract every consumer in this ecosystem was built on, not an
observed parse.

| Payload | Their keys | We publish |
|---|---|---|
| Packet (meshcoretomqtt) | 18 | **18 — complete** |
| Status top level (meshcoretomqtt) | 8 | **8 — complete** |
| Status `stats` (meshcoretomqtt) | 8 | 6 — missing `debug_flags`, `tx_air_secs` |
| Status (CoreScope ingest) | 9 | 8 — missing `tx_air_secs` |

Plus 22 `stats` extensions of our own that no consumer reads yet.

Two coverage gaps that are not field-shaped:

- **We publish RX packets only.** `publishPacket` is only ever called with
  `"rx"`, while meshcoretomqtt publishes both directions, so a map consumer
  never sees what this node transmitted. Closing it needs an outbound handler
  on the modem (as `wirePacketLogger` has) *and* the per-broker dedup re-keyed
  to (hash, direction) — `meshcore.DedupCache.HasSeen` keys on the hash alone,
  so relaying a flood we already published as RX would be dropped as a dup.
- **`tx_air_secs` and `repeat`** are the only status fields with a known reader
  that we do not send. Both need plumbing rather than a formatting change.

### Rules the current set encodes

- **`sent` and `queue_len` are PROCESS-wide.** One `RadioMux` serves the companion, the repeater and the observer, so `TxStats().Sent` is everything this process transmitted. Both were declared-but-never-assigned for a long time and published `0` — don't declare a field before it can be populated.
- **Busy and queue drops stay split** (`tx_dropped_busy` / `tx_dropped_queue`). Busy = the radio kept reporting congestion and we gave up, nothing the operator can change. Queue = we enqueued faster than the radio drains, so send less. Summing them gives a number nobody can act on; `format_test.go` pins the mapping because a swapped wire is silent and inverts the diagnosis.
- **RX-only fields stay absent on a TX row** — `SNR`, `RSSI` and `score` measure a *received* packet, so on a TX row they would report a measured 0 dB for our own send. A relaying node publishes MORE tx rows than rx (every relayed flood is one of each, plus its own adverts, ACKs and replies), so **most SNR-less rows in a consumer's list are ours and correct** — check `direction` before treating it as data loss. `hash` and `duration` stay on both (computed, not measured; note the firmware's TX log line carries neither, so that is a deliberate extension).
- **An rx packet with no signal metadata also carries no measurement.** Signal info arrives in a separate KISS frame paired to the data frame; when the pairing fails, `HasSignalInfo` is false and `rx_meta_timeouts` counts it. `SNR`, `RSSI` and `score` are then omitted rather than published as 0 — a 0 dB reading looks real and we feed other people's link budgets. `duration` and `path` survive, being frame-derived. Not yet observed on this hardware: every rx row in the local packet log has signal info, which is why it is a guard and not a comment.
- **`score` and `duration` are derived, and so are the firmware's**: `score=(int)(packetScore*1000)`, `time=getEstAirtimeFor(len)`. `modem.StatsProvider` mirrors them as `PacketScore` / `EstAirtimeMs` so `internal/mqtt` takes no dependency on the driver package.
- **`path` is not a route.** It reproduces `Dispatcher.cpp`'s `[%02X -> %02X]` trailer — `payload[1] -> payload[0]`, i.e. source then destination hash prefix — for the four addressed payload types on a direct route only. Published without brackets, RX only.
- **Packet counters ship under two names, and both have a reader.** `recv`/`sent` are the firmware's `stats-packets` names, also the vocabulary of CoreScope's client-RF topic; `packets_recv`/`packets_sent` are what CoreScope's *observer-status* ingest reads (`extractObserverMeta`, `cmd/ingestor/main.go`), with no alternative accepted. Publishing only one set silently drops the counters for one of them. The pre-release name `packets_received` was read by nothing — the key that consumer needs is `packets_recv`, so our RX count had never been ingested. `format_test.go` asserts all four keys and that the aliases agree.
- **Absent on purpose**, because nothing here can populate them: `errors` (firmware `_err_flags`), `flood_tx` / `direct_tx`, `tx_air_secs` — the observer taps the mux's RX side only and the mux exposes no airtime total. A permanent 0 reads as a silent radio. **CoreScope does read `tx_air_secs`**, so this one costs a real consumer a real field; populating it needs an outbound handler on the modem, the way `wirePacketLogger` does. CoreScope's own client-RF spec takes the same position we do ("Absent stays SQL NULL, never 0 — storing 0 would read as a perfectly clean channel").
- **`repeat` is published** top-level from `obs.Relaying` (`SetRelaying`, pushed before `Start` and re-pushed after a SIGHUP reload), matching firmware 1.16 and CoreScope's `CanRelay`. It must stay at the top level, never inside `stats`: CoreScope reads `msg` directly, and a missing field leaves `CanRelay` nil so the prior value persists. Tests pin both halves. Near-collision: our `hw_errors` is the KISS driver's HW_RESP_ERROR frame count, **not** the firmware's `errors`.
- `rx_meta_misattributed` is the one to watch: signal metadata matched to the wrong packet means the `snr`/`rssi` we published was wrong, and we feed LetsMesh/CoreScope — that corrupts other people's link budgets.
- `handler_slow` is non-zero only because `modem.Setup` passes `hardware.WithHandlerWatchdog(500ms)` — a constant, not a knob. With `WithRxDelay` set (the repeater does) the real work runs on the library's `runInbound` goroutine, so it measures the companion's and observer's handlers, not the repeater's.

**`LinkStats` is KISS-shaped despite the neutral name** — a known ceiling. Only
`InboundDropped*` and `HandlerSlow` are driver-neutral; the other six are KISS
protocol concepts with no SPI analogue. If a direct SX126x/SPI driver lands,
those six must become **omittable rather than 0** (the `HaveMCUTemp` precedent),
and that driver's own counters will need a home. Don't design that seam before
the driver exists.

## TX duty cycle

How much of each hour the radio may spend transmitting. Stored as a
**percentage** (`Config.DutyCycle`, `settings.duty_cycle_pct`, Settings -> RF)
because that is the unit the firmware shows operators; meshcore-go wants an
inverted "airtime factor", converted **only** in `Config.AirtimeFactorOr`.

- `dutyCycle = 1/(1 + factor)`, so a **smaller factor is a HIGHER duty cycle**: 0 = 100%, 1.0 = 50%, 99 = 1%. Reaching for "0.01 for 1%" gets you 99%. Nothing but `AirtimeFactorOr` speaks factor, and `modem.Setup` logs both at startup.
- nil resolves to `node.DefaultAirtimeFactor` = 1.0 = **50%**, which is firmware parity (`Dispatcher::getAirtimeBudgetFactor()` and `_prefs.airtime_factor = 1.0` in all four roles). Don't "fix" it.
- Fractions of a percent are accepted (some EU868 sub-bands are 0.1%); firmware's `set dutycycle` validates 1-100. There is deliberately **no raw-factor key** — firmware's `set af` exists only because its `set dutycycle` is capped, so no second setting can disagree with this one.
- The budget is **process-wide**: one shared mux, one cap for companion + repeater + observer. It is one radio.
- `dutyCycle` is in `config.ModemSettingsChanged` — the factor is baked into the mux at `modem.Setup` and the mux is only rebuilt on reconnect, so leaving it out means the setting persists and silently never applies. Compare the **resolved** factor (`AirtimeFactorOr()`), not the stored percentage: unset and an explicit 50 are the same budget, and a reconnect drops the serial link and every session.
- The firmware's `constrain(airtime_factor, 0, 9.0f)` is **not** a 10% floor — it lives only in `loadPrefsInt`, the legacy `/com_prefs` loader.
- The budget model is a faithful port: firmware seeds `tx_budget_ms = window * dutyCycle` and refills `elapsed * dutyCycle` capped at max; Go's `newAirtimeBudget` does the same, bucket starting full. A low-rate sender never hitting the budget is the design.

> **Trap:** `PUT /api/config/settings` writes every non-secret field **as sent**,
> so a partial body resets what it omits to defaults — a `{"dutyCycle":1}` PUT
> also moves `cr` to 8. Only secret-ish fields (`mapTileKey`) and `mapProvider` keep their
> stored value on nil. Always send the whole settings object; the UI does.

## Path hash size

The per-hop path hash width for the flood packets we originate. It is a
**regional convention** (the official radio presets carry it — NZ Narrow and
Hungary run 2 bytes), so it lives as a default on Settings with per-node
overrides:

- `Config.PathHashSize` (Settings → RF section) is the default, in **BYTES**.
  nil = 1. `CompanionConfig.PathHashSize` and `RepeaterConfig.PathHashSize`
  override it; nil = inherit.
- **Everything internal speaks bytes.** The firmware's `path.hash.mode` is
  bytes-1 and its `set` handler checks `mode < 3`, so 0-2 = 1-3 bytes. Convert
  only in `cliGet`/`setMutation` — never store the mode. (The old
  `RepeaterConfig.PathHashMode` was removed; `008_map_region_path_hash.sql` converted the column
  to `path_hash_size` with `mode + 1`.) `RepeaterDetailPage` still speaks mode,
  correctly: it drives *remote* firmware over the CLI.
- **Inheritance is resolved at startup, not read at use.**
  `effectiveCompanionConfigs` / `effectiveRepeaterConfig` (internal/app) copy
  the global into a node's block when it has none, so node code reads only its
  own field *and* the reload diff notices a global change — inheriting nodes
  restart, overriding ones keep running. Neither helper mutates the source
  config, so a node's stored nil keeps following the global.
- Consumers: `advert.SendSelf` (takes bytes, writes size-1 into PathLength's
  top 2 bits), `Companion.sendGroupReply`, and the repeater's adverts.
  Anything sent to a contact uses that contact's own bytes per hop instead (`companion_contacts.path_hash_size`, see
  [firmware-protocol.md](./firmware-protocol.md)), flooded or direct; a
  learned route keeps the size it was learned at. Trigger `pathHashSize` still wins
  over the contact's or companion's default (`resolvePathHashSize`, where 0 = mirror the
  incoming packet).
