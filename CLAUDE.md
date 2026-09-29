# CLAUDE.md — project handover

A MeshCore companion + observer + repeater-admin tool: one static Go binary that
speaks the MeshCore mesh protocol, optionally bridges to MQTT (LetsMesh,
CoreScope), persists to SQLite, and serves a React SPA on `:8080`.

## Reference docs — read the one you're touching

| Doc | When |
|---|---|
| [docs/firmware-protocol.md](./docs/firmware-protocol.md) | Anything on the wire: CLI replies, REQ types, path routing, DM/room plaintexts, sensors, rooms |
| [docs/config-and-storage.md](./docs/config-and-storage.md) | Config tables, `/api/config/*`, backup/restore, monitoring, REST endpoint list |
| [docs/frontend.md](./docs/frontend.md) | Styling system, page patterns, mobile, PWA |
| [docs/mqtt-and-radio.md](./docs/mqtt-and-radio.md) | MQTT wire schema, duty cycle, path hash size |

Also: [README.md](./README.md) (public intro), `s.routes()` in
[`internal/api/server.go`](./internal/api/server.go) (**authoritative** endpoint
list), [`internal/store/migrations/`](./internal/store/migrations/) (the schema, one file per version),
`~/Data/wesley/MeshCore` (firmware source — the tiebreaker for any protocol
dispute), <https://api.meshcore.nz/api/v1/config> (regenerate
`web/frontend/src/data/radio-presets.json` from this, don't hand-edit).

## Tech stack

| Layer | Tech |
|---|---|
| Backend | Go 1.26+, **no CGO**, `modernc.org/sqlite`, `embed.FS` for the SPA |
| Frontend | **React 19** (not Preact) + Vite 6 + TS 5.7, Tailwind v4, shadcn/ui (new-york), `react-router-dom@7`, `sonner`, Leaflet |
| Mesh proto | `github.com/meshcore-go/meshcore-go` v1.6.0 (plus `hardware/transport`, `hardware/sx12xx` and `hardware/openhop` at the same tag), pinned in `go.mod`. **No `go.work`** — add one only for lockstep library work and delete it before pushing; `GOWORK=off go build ./...` is the check |
| Real-time | WS `/api/ws`, topics `peers` `packets` `messages` `traces` `repeaterNeighbors` `discovered` `sensors` |
| Local sensors | `periph.io/x/conn` + `periph.io/x/host` for I2C; `github.com/expr-lang/expr` for derived sensors |
| Config | SQLite relational tables; config files are one-time imports |

## Build / run / test

```bash
./build.sh           # from the repo root; SPA then version-stamped Go binary, mirrors CI
go test -race ./...  # what CI runs (23 packages)
screen -S meshcore -X quit; sleep 12
screen -dmS meshcore bash -c 'exec ./OwlShack -vvv 2>&1 | tee /tmp/OwlShack2.log'
python3 -c "import sqlite3; c=sqlite3.connect('file:meshcore.db?mode=ro', uri=True); print(c.execute('PRAGMA user_version').fetchone())"
```

- **Never run `vite dev`** — no API server beside it; the WS and MeshCore endpoints only exist in the Go binary.
- `build.sh` skips `npm ci` when `node_modules` is in sync (`FORCE_INSTALL=1` forces). Warm build ~5 s. Type errors fail it.
- Frontend checks are ad-hoc via the Playwright MCP server against the running binary (there is no playwright config in the repo, despite what older docs claimed). No SPA CI, no frontend tests.

## Hard rules

**All SQLite writes go through `store.WriteAsync(fn)`** (RX/hot path, never
blocks) or `store.WriteSync(fn)` (HTTP handlers, blocks so the caller sees
generated ids). A direct `c.store.Peers.Upsert(...)` races the writer goroutine
and gives `SQLITE_BUSY`. Never `WriteSync` inside a writer closure — the writer
would wait on itself. On the RX path prefer `WriteAsync`: `WriteSync` blocks the
node's single dispatch goroutine and stalls all RX when `writerCh` is full.

**Migrations are append-only and shipped files are frozen.** Add
`internal/store/migrations/NNN_what.sql`, where NNN is the `user_version` it
sets; never edit, rename or renumber a file listed in `migrations.sum` — a
released DB has stamped that version and will skip it.
`TestMigrations_ShippedFilesAreFrozen` checks the listed checksums; a failure
there means add a file instead, **never** edit the sum. Unlisted files are
unreleased and may change, but a DB that ran a different draft of one is
refused at open (and as a backup), naming the file; a DB migrated before
checksums were kept is trusted as it stands. At release, record the new files with
`go test ./internal/store -run TestMigrations -release-migrations <tag>` and
commit the sum before tagging; the release workflow fails on an unlisted file.
What SQL cannot do goes in a Go step called from `migration.apply`, run after
its file in the same transaction (004's packet backfill is the only one), and
it is frozen like the files. Each file runs in one transaction with its version bump, so it must
not `BEGIN`. The store copies the DB to `meshcore.db.pre-v<from>-to-v<to>`
before upgrading, keeps an unfinished upgrade's copy rather than copying a
half-upgraded DB, and deletes older copies only once an upgrade finishes; a copy
that cannot be made (a full disk) is a warning, not a refusal to start. Going
back is moving the copy into place, and there are no down migrations.

**At release, check the region data** (`internal/region/regions.bin.gz`) is on
Natural Earth's latest release; the steps are in the regions entry of
[docs/config-and-storage.md](./docs/config-and-storage.md).

**Every timestamp on outgoing admin traffic comes from
`Client.UniqueTimestamp()`** (the firmware's `getCurrentTimeUnique()`), never
`time.Now().Unix()`. The companion's DMs and room posts share that counter; the
repeater node has its own `Repeater.uniqueTimestamp()`. Remote firmware drops a
timestamp `<=` the last one it saw as a replay, and treats an equal CLI
timestamp as a retry it never answers, so two sends in one second silently lose
the second.

**`internal/api` never imports the domain.** The seam is the `api.Backend`
interface ([`internal/api/backend.go`](./internal/api/backend.go)), implemented
by `internal/app` and swapped atomically via `SetBackend`. Add a method to
`Backend`; do **not** reintroduce per-feature `Set*`/mutex pairs. The one
exception is `internal/region`, stateless embedded data like `store`, which the
region endpoints read directly.

**Driver pragmas use modernc syntax** —
`?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)`,
plus `journal_size_limit` and `synchronous(NORMAL)` (see `store.Open`).
The mattn-style `_journal_mode=WAL` form is silently ignored, which once left
the DB in rollback-journal mode with no busy timeout. Under NORMAL a commit is
only synced at a checkpoint, so `WriteSync` checkpoints after its closure: an
HTTP save is on disk when it returns, and the RX path's `WriteAsync` is not.

**SNR is real dB end-to-end** (`snr REAL` / `*float64` / JSON number); RSSI is
raw `int8` dBm. The wire is quarter-dB (x4) and meshcore-go converts at ingest;
`meshcore.PathSNRdB(b)` decodes raw trace path bytes. No consumer divides by 4.

**OutPath semantics** on `node.Peer`: `nil` = unknown (send flood), `[]byte{}` =
direct neighbour (0 hops), non-empty = multi-hop. Route from
`peer.OutPathHashSize`, never a hardcoded `meshcore.PathHashSize`.

## Repo layout

Two independent axes: **`internal/node/<type>`** = personalities we *run* (own
identity, lifecycle, handlers — `companion`, `repeater`); **`internal/client/<type>`**
= protocol clients driving *remote* nodes (`repeater`, which also serves sensors
and rooms). A running node uses clients; never the reverse.

`main -> internal/app -> {node/*, client/*, modem, echo, mqtt, trigger, config} -> {api, store}`

```
main.go                 flags, logging, app.Run (~50 ln)
internal/
  app/                  supervisor loop (SIGHUP reload, reconnect+backoff), node
                        lifecycle, packet logger, the api.Backend impl
  config/               Config types, load/marshal/validate/defaults, legacy import
  logging/ buildinfo/   slog setup; Version/Date set by build.sh
  node/companion/       channels, DMs, triggers, templater, identity
  node/repeater/        the repeater we run: CLI, regions, ACL, adverts, delays
  client/repeater/      remote admin: login, status, CLI, path, neighbours, telemetry, ACL
  echo/ modem/ monitor/ tx echoes; KISS setup + stats; type-agnostic node poller
  mqtt/ trigger/        observer + wire formatting + JWT; triggers
  telemetry/            CayenneLPP series decoding (sensor 0x04 history)
  sensor/               local sensors: the hub, providers (I2C, PiSugar, web, derived), drivers, LPP replies
  api/                  HTTP+WS server, routes, hub; the Backend seam
  store/                SQLite persistence + backup/restore
web/embed.go            go:embed of web/frontend/dist (must stay at root)
web/frontend/           React SPA
```

**Two-tier peer model**: `discovered_peers` (every peer ever seen, shared)
vs `companion_contacts` (per-companion, with `isRepeater` / `repeaterPassword` /
monitoring metadata). Every per-companion table keys on the surrogate
`companions.id`, so renaming a companion is safe; `node_state.companion_id` is
the lone exception (name-keyed, self-heals each poll).

Two big files remain (`internal/client/repeater/client.go`,
`internal/node/companion/companion.go`); splitting them within their package is
safe future cleanup.

## WebSocket payloads

- `peers` — `{pubkey, name, type, lat, lon, lastSeen, lastAdvertTs, snr, rssi, outPath, outPathHashSize}`; hops = `len(outPath)/outPathHashSize`.
- `packets` — `{id?, receivedAt, direction, raw, payloadType, route, pathHashSize, hops, packetHash, summary, snr, rssi}`
- `messages` — **flat, no `.message` wrapper**. Re-adding one silently drops every message (it was a real bug). Action variants: `{action:"repeatCount"...}`, `{action:"status"...}`.
- `traces` — `{companion, tag, hops, path, hopSNRs, snr}`; `repeaterNeighbors` — `{pubkey, name, snr, secsAgo}`.
- `discovered` — one discovery answer, `{pubkey, name, type, snr, reportedSnr, heard}`; `sensors` — every sensor's `api.SensorStatus`, pushed whole after each poll pass.

The hub pings every 50 s with a 75 s read deadline. Clients may send
`{"action":"ping"}` and get `{"topic":"pong"}` — `useWebSocket` uses it to spot
half-open sockets on mobile resume.

## Conventions / don'ts

- **No CGO ever** — no `mattn/go-sqlite3`, nothing needing a C toolchain.
- Don't reintroduce DaisyUI, PicoCSS, preact-router, `@preact/preset-vite`, lucide-preact.
- Don't auto-refresh a tab on switch — it spams the radio. Use the fetch-once pattern (`forceMount` + an `active` prop + `fetchedRef`).
- Use canonical Tailwind classes; brackets only where no built-in exists. These are editor-only warnings — the build does NOT catch them. See [docs/frontend.md](./docs/frontend.md).
- Don't add per-component `cursor` classes — `index.css` has a global rule.
- Chat ordering is by row **id**, never timestamp (a remote node's clock can be years out).
- Comments: one line or none, and never a comment that only restates the code.

## Where to look first when debugging

**Go looking for state that would be indistinguishable from healthy if it were
broken.** Every serious defect found here shared that shape, not carelessness:
`packets_sent`/`queue_len` declared and never assigned published `0` — which
looks like a quiet radio; `dutyCycle` missing from `ModemSettingsChanged`
persisted and never applied — which looks applied; no handler watchdog looked
like nothing at all; a battery percentage defaulting to 100 looked like a
healthy board; and a test asserting the buggy behaviour *defended* it while
looking like coverage. Prefer "what would I see if this were wrong?" over
reading for obvious errors — if the answer is "the same thing I see now", that
is the place to look.

| Symptom | Look at |
|---|---|
| WS message missing in the UI | `useWebSocket.ts` parsing + the page's `handleWsMessage`. Flat shape, no `.message`. |
| Repeater login succeeds but stays on the login screen | `loggedIn` derivation in `RepeaterDetailPage.tsx` — presence of `pubkeyHex`. Don't change it without `api/repeater.go`. |
| Settings load shows a blank value | Wrong CLI command name. Check `CommonCLI.cpp`; there are no combined commands like `get coords`. |
| Threads list shows repeaters | `isRepeater` on the conversation row, set by `routes_conversations.go`. |
| Room posts stop mid-backlog | The push stream is ACK-gated — `handleRoomPush` must ACK hashing **our** pubkey. |
| "Works on one repeater but not another" | Different admin passwords; the Settings tab is admin-only. |
| Wrong password vs unreachable | Indistinguishable: ~10 s timeout either way, the firmware drops both. |
| `kiss: tx failed` while RX and stats work | The **radio board**, not the link. `completeTx` raises it only when the firmware's TX-outcome frame says the transmit failed, so the serial side is healthy. Confirmed once by swapping the radio. |

## Known limitations / tech debt

- Status/Neighbors auto-fetch is once per page mount; after a long absence, hit Refresh.
- Repeater and room sessions are in-memory — a restart drops every login.
- Settings-section CLI `get`s run sequentially on purpose (half-duplex radio), though the client *could* correlate concurrent commands.
- Go tests cover 23 packages; radio-facing paths are still manual. No frontend tests.
- `MESHCORE_APP_FEATURES.md` is referenced by the docs but does not exist in the repo.
- MQTT publishes RX packets only, never TX — see [docs/mqtt-and-radio.md](./docs/mqtt-and-radio.md).

## Feature ideas not yet started

No auth on the REST/UI at all (the biggest real gap for a tool that can
reconfigure repeaters) - push notifications (the PWA is installability-only) -
hosting a room server or sensor node - OTA firmware update (`start ota`) -
multi-repeater batch operations - room auto re-join on startup.
