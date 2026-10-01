# Firmware & protocol reference

Device/firmware facts, repeater CLI parity, wire formats, and the sensor and room-server roles. Verify every claim here against `~/Data/wesley/MeshCore` before relying on it.

## Notes on the device / firmware

- The status payload has two distinct error counters — don't conflate them:
  - `errEvents` (uint16, bytes 40-42) — `_err_flags` bitmask of fatal events (queue-full, etc).
  - `recvErrors` (uint32, bytes 52-56) — `radio_driver.getPacketsRecvErrors()`, the actual receive-errors counter.
  Both are surfaced as separate StatTiles in the Status tab.
- Settings tab is **admin-only**. Logging in as guest hides it.
- Repeater sessions live in `RepeaterManager` memory; a binary restart drops them all and login is required again.
- Firmware does not support semicolon-chained commands. Sequential `set …` calls only.
- `freq` (set) and `prv.key` are serial-only commands — sending them via `/cli` yields an error.
- `radio.rxgain` is SX126x/LR1110 only. `bridge.*` are gated by compile-time flags. `pwrmgt.*` and `bootloader.ver` are NRF52-only.
- Advert intervals: zero-hop/direct is `0` (off) or `60-240 minutes`
  (`MIN_LOCAL_ADVERT_INTERVAL = 60`; the firmware stores it in 2-minute units,
  so odd minutes round down, and `savePrefs` forces anything under the minimum
  to 0). Flood is `0` or `3-168 hours`. **We store both in SECONDS** — the
  advert loop schedules on them — but every firmware-facing surface uses
  minutes/hours: the CLI `get`/`set` handlers convert, and the Repeater page's
  fields are labelled in those units so what you type is what `get
  advert.interval` reports. `Config.Validate` enforces the ranges and
  `008_map_region_path_hash.sql` clamped existing rows into them (below minimum → off), so a
  legacy out-of-range value can't fail validation at load and stop startup. `flood.max` ≤ 64. `dutycycle` is `1-100` and writes `airtime_factor` indirectly.
- **Wrong password / unreachable repeater**: client sees a silent ~10 s timeout — the firmware drops the packet either way. The current UX doesn't distinguish the two cases. Two things the client does reject immediately: a CLI send from a non-admin session (all three roles drop `CLI_DATA` from non-admins silently) and a login reply whose byte 4 isn't `RESP_SERVER_LOGIN_OK` (`isLoginReply`, so a late status response can't be mistaken for the login). ACL rows with `permissions == 0` are block padding and are never surfaced.
- **Logout is local-only**: deletes the session from server memory, no radio traffic.
- `meshcore.PathHashSize = 1` (1 byte per hop hash). Direct neighbour = 0 hops.
- Firmware `get` responses are prefixed with `> ` (e.g., `> 917.375`).
- **Reply formats are part of the contract** — clients parse them, so match the
  firmware's own `sprintf`. The ones that bite: `clock` and `clock sync` return
  a DateTime (`HH:MM - D/M/YYYY UTC`, only the time zero-padded), **not** a unix
  timestamp; floats go through `StrHelper::ftoa`, which always emits a decimal
  point (`915.0`, `0.0`) — our `floatOrZero` appends `.0` for this; `ver` is
  `"<version> (Build: <date>)"` (`buildinfo.Date`, stamped by build.sh — keep it
  space-free, the linker splits `-ldflags` on whitespace); the `neighbors` text
  reply is capped at 134 bytes, matching the firmware's reply buffer.
- **Repeater login uses the companion's static identity**, not an ephemeral keypair, despite the protocol field being named `AnonReq.EphemeralPubKey`. This lets the repeater's ACL recognise the companion across sessions (blank-password reauth) and avoids orphan contact entries on the repeater. Internal field names in `RepeaterSession` are `localPubKey`/`sharedSecret` to reflect this.
- **Repeater ACL surface** (admin-only):
  - `REQ_TYPE_GET_ACCESS_LIST` (0x05) returns `(6-byte pubkey prefix, 1-byte permissions)` entries — *prefix only*, not full pubkey.
  - `setperm <hex-pubkey> <perms-int>` CLI command writes; perms=0 deletes. Firmware matches a **pubkey prefix** when deleting (`ClientACL::applyPermissions` → `getClient` byte-compare) and requires the full 32 bytes only to grant a role, so a prefix-only ACL row is still removable — our repeater node and the remote Access tab both follow this.
  - Permission lower-2-bits: 0=GUEST (not persisted), 1=READ_ONLY, 2=READ_WRITE, 3=ADMIN.
  - MAX_CLIENTS = 20.
  - Resolve prefix→name in UI by cross-referencing both `discovered_peers` AND `companions` (the companion's own pubkey won't appear in its own peers list).
- **Telemetry permissions** follow the requester's ACL role:
  - Guest: base only (battery voltage + MCU temperature)
  - Any other role (read-only, read-write, admin): base + location + environment sensors
  Base goes out whatever the request's `inverse_perm_mask` says; the mask narrows the other two. We send `0x00` to ask for everything the role allows.

### Repeater CLI / OTA parity with the firmware

The node we run (`internal/node/repeater`) is measured against MeshCore 1.17.1
`src/helpers/CommonCLI.cpp` + `examples/simple_repeater/MyMesh.cpp`.

- **`sender_timestamp == 0` is the firmware's "came from serial" flag.** Anything
  behind it is unreachable over the air on real firmware too, so it is never a
  parity gap: `erase`, bare `log`, `stats-core`, `stats-packets`, `stats-radio`,
  `set freq`, `get prv.key`, `get acl`. `isUnsupportedCmd` answers the commands
  and `unsupportedGetKeys`/`unsupportedSetKeys` the keys with "ERR: not
  supported on this node". Unknown input gets the firmware's own strings:
  `Unknown command`, `??: <key>`, `unknown config: <rest>`. Only `set freq` is
  serial-gated — `get freq` answers OTA. Argument parsing mirrors C `atoi`/`atof`
  leniency and `uint8_t` truncation (`set flood.max 300` → 44, `abc` → 0),
  `set repeat` is ON for anything but `off`, and `StrHelper::strncpy` limits
  apply (name 31, passwords 15, owner.info 119). The CLI catalog still lists
  the serial-only commands, hint-tagged "(serial only)".
- **`set radio` / `set tx` are OTA commands upstream; we deliberately refuse
  them** — the modem is shared with the companion and its settings live in app
  config. This is our one intentional divergence.
- **Regions are flat** (name + denyFlood), not the firmware's parent tree, so
  `region def` is unsupported and `region get` never prints a parent.
  `region save` returns OK because `reconfigure` already persisted; `region load`
  replies nothing, as the firmware's async reload does. `region list` and
  `regionsExport` are comma-separated (`region list` includes `*` and prints
  `-none-` when empty); bare `region` is the newline-terminated tree
  (`regionTree`, mirroring `RegionMap::exportTo` — wildcard first, children
  indented one space, `^` = home (on `*` when unset), ` F` = flood allowed).
  Lookups are exact-then-prefix (`findByNamePrefix`); `region home` replies
  `" home is now X"` / `" home is X"`; `put` on an existing region re-allows
  flood (`OK - (flood allowed)`); `remove *` → `Err - not empty`. `region
  default *` can't be represented in config and replies `Err - region table
  full`.
- **`HomeRegion` is inert**, exactly as in firmware: `home_id` is stored and
  reported but never routed on. It rides the live-apply path (`ApplyRegions`),
  so setting it doesn't restart the node.
- **Config keys**: we answer 22 of the firmware's 46 `get` keys and 17 of 38
  `set` keys. The rest are out of scope for a Linux-hosted relay — `bridge.*`
  (compile-gated upstream), `pwrmgt.*` (nRF52-only), chip-level radio tuning
  (`cad`, `agc.reset.interval`, `int.thresh`, `extra.sf`, `radio.rxgain`,
  `radio.fem.*`, `adc.multiplier`), `prv.key`, `bootloader.ver`.
  `allow.read.only` looks like a gap but only the room server acts on it.
  Still genuinely open: `dutycycle`/`af` (airtime factor).
- **TRACE, flood relay and delays are the library's.** meshcore-go (post-v1.2.0
  router) relays TRACE to the next hop with our SNR appended, re-floods only
  after local dispatch and only when no handler called `pkt.MarkDoNotRetransmit()`
  (the firmware's flag for "this packet was for me"), and consults `allowForward`
  last. **Every repeater handler that decrypts a packet for us must call
  `MarkDoNotRetransmit`** (login, REQ, CLI, PATH do) or the request gets
  re-flooded on our behalf. The relay timing hooks (`WithFloodRetransmitDelay`,
  `WithDirectRetransmitDelay`, `WithRxDelay`, `WithExtraAckTransmitCount`) are
  wired to `RepeaterConfig.TxDelayFactor` / `DirectTxDelayFactor` /
  `RxDelayBase` / `MultiAcks` in `delay.go`, porting `MyMesh::getRetransmitDelay`
  (rand[0, 5·airtime·factor]), `calcRxDelay` (`hardware.PacketScore` needs the
  SF, so rxdelay is off when the radio params are unknown) and `multi_acks`.
  Setting `WithRxDelay` moves dispatch onto the library's single inbound
  goroutine — handlers still never run concurrently.
- **Direct replies** to a client route along the path learned from its flood
  request **reversed** into send order (`learnFloodRoute` / `reverseHops`) —
  the PATH-return carries it in the sender's order. Flooded replies copy the
  request's hash width and scope (`chooseReplyScope`).
- **Status counters**: `n_packets_sent` is every TX (radio counter);
  `clear stats` resets only radio recv/sent, route and dup counters — not
  airtime or last SNR/RSSI. `n_sent_flood` / `n_sent_direct` count every
  transmission, as the firmware's Dispatcher does — relays via `allowForward`,
  everything we originate via `sendPkt` / `sendAdvert`. Use `sendPkt`, not
  `node.SendPacketDelayed`, for anything the repeater originates. The trace
  relay is the one exception: `allowForward` has already counted it.
  `n_recv_flood` / `n_recv_direct` and both dup counters come from the
  router's `node.RouteStats()` (`routeCounters`, rebased on `clear stats`). `err_events` and
  `n_recv_errors` have no host analogue and stay 0.
- **Airtime duty cycle is already enforced** by meshcore-go's mux at
  `DefaultAirtimeFactor = 1.0`, matching the firmware repeater's
  `_prefs.airtime_factor = 1.0`. The `dutycycle` / `af` CLI keys are missing,
  but the factor lives on the *shared* mux, so writing it belongs with
  `set radio` / `set tx` in the read-only-over-mesh category.
- **Direct MultiPart** ACK relays (`forwardMultipartDirect`) and the extra
  `multi.acks` copies are handled by the library.
- **Telemetry (0x03)** returns battery voltage and MCU temperature on the self
  channel, both read from the KISS modem board (`HW_CMD_GET_BATTERY` /
  `HW_CMD_GET_MCU_TEMP`, polled by `deviceStatsLoop` into `DeviceStats`). The
  temperature is omitted when the board can't measure one — the modem answers
  `HW_ERR_NO_CALLBACK` and `HaveMCUTemp` stays false — mirroring the firmware's
  `isnan` check. The operator's channel map adds local sensors, and the inverse
  mask and guest downgrade apply as the firmware's do (see "Serving telemetry"
  below).
- **Neighbours (0x06)** caps its reply at the firmware's 130-byte
  `results_buffer` (11 entries at the default 6-byte prefix) regardless of the
  requested `count`, and answers only `request_version == 0`. The ACL request
  (0x05) likewise stays silent unless both reserved bytes are zero.

### Protocol constants

```
PayloadTypes: Req(0), Response(1), TxtMsg(2), Ack(3), Advert(4), GrpTxt(5),
              GrpData(6), AnonReq(7), Path(8), Trace(9), MultiPart(A),
              RawCustom(B), Control(C)

reqTypeGetStatus     = 0x01
reqTypeGetNeighbors  = 0x06
txtTypeCliData       = 1

REQ types a repeater answers: 0x01 status, 0x03 telemetry, 0x05 ACL,
0x06 neighbours, 0x07 owner info. 0x02 KEEP_ALIVE is a dead #define in the
repeater firmware (only the room server implements it) — don't "add" it.
A companion answers 0x03 only, with no login: BaseChatMesh reaches
onContactRequest only for a contact, so the contact's own shared secret is the
credential and a stranger is never answered (though firmware auto-adds every
node it hears advertise, so in practice most nodes are contacts).
Every reply is [reflected client timestamp:4][body].
cliPrefixLen         = 3
CLI flags byte       = TXT_TYPE_CLI_DATA << 2  (= 4)
PathHashSize         = 1 byte
```

---

## Path routing — data model

**OutPath semantics on `node.Peer`:**
- `nil` = path unknown (will send as flood)
- `[]byte{}` (non-nil, zero-length) = direct neighbor (0 hops, no routing needed)
- `[]byte{...}` = multi-hop path (send as direct-routed)

**OutPathHashSize** must be stored per-peer (1–3 bytes per hop; firmware `path.hash.mode` 0–2). Don't hardcode `meshcore.PathHashSize` for outbound routing — use `peer.OutPathHashSize` (0 = default/1).

**PathReturn plaintext format** (decrypted from a `PayloadTypePath` packet):
```
[pathLenByte:1][path_data:N][extra_type:1][extra_data:M]
pathLenByte: upper 2 bits = (hashSize - 1), lower 6 bits = hopCount
pathDataLen = hopCount * hashSize
extra_type: PayloadTypeResponse (0x01), PayloadTypeAck (0x03), or 0xFF (dummy/padding)
```

**Firmware sends PathReturn for ALL flood requests** (login, status, CLI, neighbors) — not just login. Path re-learning is automatic whenever a flood request reaches the repeater.

**Persistence and direction:** the node's peer table is the one source for a send route, read by every send and by the path badges. A learned or operator-set route is also saved on the companion's contact row (`store.Contacts.UpdateOutPath` / `SetRoute`, send order: our neighbour first). `hydratePeerTables` loads it back into a companion's table as soon as it is created, before it starts, without overwriting a route RX learned first and adding a peer for a contact never heard advertising, so a route survives a restart and the badge shows the route a request takes. A new contact starts unknown, as the firmware's does (`BaseChatMesh.cpp:111`); a path for a node that is not a contact is refused (404), as it would have nowhere to be saved. `discovered_peers.out_path` is the **advert path** (peer's neighbour first, display only) and is never a send route. A manual `PUT .../contacts/{pubkey}/path` (or `.../repeaters/{pubkey}/path`) takes `{route: flood|direct|path, path, pathHashSize}`; a path must fit the wire's length byte (1 to 3 bytes per hop, whole hops, at most 64 bytes).

**Bytes per hop:** each contact has its own (`companion_contacts.path_hash_size`), set on add from the size the peer floods its adverts at, else the companion's own, and changed only by the operator. Everything sent to it carries it in the length byte (repeater-client requests, DMs, DM and room-push ACKs, telemetry replies), a flood and a 0-hop send included: a repeater with no route back floods its reply at the request's size (`sendFloodReply(..., packet->getPathHashSize())`, simple_repeater `MyMesh.cpp`), and our flood at that size makes the route we learn back come in at it too. A zero-hop advert always reads as 1 byte (`Mesh::sendZeroHop` sets `path_len = 0`), so it is stored as 0, unknown, and never overwrites a flood advert's size. The firmware companion floods at one size for every contact (`_prefs.path_hash_mode`); a size per contact is OwlShack's.

---

## DM plaintext format

```
[timestamp:4][flags_byte:1][text:N][padding...]
flags_byte = (type << 2) | (attempt & 3)
```
- Use `plaintext[4] >> 2` to get type (0=plain, 1=CLI). Don't compare raw byte.
- `CalcAckHash` takes unpadded plaintext (trim trailing `\x00` from text) + sender's pubkey.
- `attemptByte` for BuildAckPayload is `plaintext[5+textLen+1]` (padding byte after text).

---
## Repeater Settings — real CLI command names

Settings sections each have their own load+save and use the **real** firmware command names (not invented combined ones):

| Section | Get | Set |
|---|---|---|
| Identity (name) | n/a | `set name <new>` |
| Radio | `get radio` (returns `freq,bw,sf,cr` comma-separated) + `get tx` | `set radio freq,bw,sf,cr` + `set tx N` |
| Position | `get lat` + `get lon` | `set lat <x>` + `set lon <x>` (also map picker) |
| Advert intervals | `get advert.interval` (mins) + `get flood.advert.interval` (hours) | matching `set …` |
| Routing | `get repeat` / `get path.hash.mode` / `get loop.detect` / `get flood.max` | matching `set …` (separate calls — semicolon chaining is NOT supported) |
| Owner Info | `get owner.info` (returns `|`-separated; convert to `\n` for display) | `set owner.info <encoded>` (convert `\n` back to `|`) |
| Security | `get guest.password` | `password <admin>` + `set guest.password <guest>` |

The `>` prompt prefix is stripped from CLI responses by `stripPromptPrefix`. Firmware error replies are HTTP-200 payloads, so `SettingsTab`'s `cli` wrapper throws on `Error…` / `ERR` / `unknown config:` / `??:` / `not supported` and toasts them as failures. The zero-hop advert action is `advert.zerohop` (dotted) — `advert zerohop` matches the bare `advert` prefix and floods.

---

## QR Code format (official spec: docs.meshcore.io/qr_codes/)

- Channel: `meshcore://channel/add?name={name}&secret={32-hex-secret}`
- Contact: `meshcore://contact/add?name={name}&public_key={64-hex-pubkey}&type={1=companion|2=repeater|3=room|4=sensor}`

---
## Sensors (management — shares the repeater admin page)

Sensor nodes (peer type `SENSOR`, firmware `examples/simple_sensor`) speak the
same admin protocol as repeaters — ANON_REQ login, REQ, CLI over TXT_MSG — so
they are driven by the **same** client (`internal/client/repeater`), the same
type-agnostic `/api/companions/{name}/repeaters/{pubkey}/*` endpoints (the path
segment is historical; rooms share it too) and the same page:
`RepeaterDetailPage` mounted at `/companions/:name/sensors/:pubkey` with
`kind="sensor"`. `kind` gates only what the sensor firmware actually answers:

- **No guest password.** `SensorMesh::onAnonDataRecv` accepts the **admin**
  password (→ `PERM_ACL_ADMIN`) or a **blank** password from a key already in
  its ACL (permissions unchanged). Read-only access is therefore granted by an
  admin with `setperm <pubkey> 1`, after which that node logs in blank. The
  Security section hides the guest field for sensors.
- **REQ surface:** `GET_TELEMETRY_DATA` (any ACL client, perm-mask honoured),
  `GET_AVG_MIN_MAX` 0x04 (READ_ONLY+), `GET_ACCESS_LIST` (admin). **No**
  `GET_STATUS`, `GET_NEIGHBOURS` or owner-info — those tabs are hidden and the
  Status auto-fetch is not mounted (it would only time out).
- **Series history (0x04)** is `Client.SendSeriesReq` →
  `GET …/repeaters/{pubkey}/history?from=&to=` (seconds before now, `from` the
  older edge, default 24 h) → `SeriesPanel` under the Telemetry tab. Request
  payload `[0x04][start:u32][end:u32][res:2]`; reply after the tag is
  `[now:u32]` then per channel `[ch][lpp_type][min][max][avg]`, each value packed
  by the firmware's `putFloat` — **MSB-first**, `getDataSize(type)` bytes,
  scaled by `getMultiplier(type)`, two's-complement when `isSigned(type)`.
  `internal/telemetry/series.go` mirrors those three tables; keep them in step
  with `SensorMesh.cpp` if upstream adds types. A guest (`PERM_ACL_GUEST`)
  gets no reply — the firmware requires read-only or better — so the request
  times out rather than erroring.
- **Composite LPP types are never aggregated.** The firmware skips min/max/avg
  for GPS, polyline and accelerometer, but still emits their full payload so the
  stream stays aligned; the value is reported as a raw integer, not a scaled
  reading. `lppDataSize` returning 9/8/6 for those three is the only place this
  is visible in our code, so do not "fix" it into a decoded value.
- **Channel numbering moved in 1.16.** Firmware <=1.15 lumps external sensors
  onto the self channel (1) alongside the board's own readings; 1.16+ gives each
  sensor its own channel from 2 up. The board's voltage comes first and its MCU
  temperature last, after the sensors (`simple_repeater` `handleRequest`), so on
  <=1.15 an external temperature on channel 1 arrives before the board's.
  `internal/telemetry` takes the *first* reading of each self-channel type as the
  node's own: right for the voltage, but it takes that external temperature as
  the board's.
- **A board upgrading 1.15 -> 1.16 changes its channel keys**, so its stored
  series gets a one-off discontinuity: the old keys stop and new ones start.
  Accepted rather than migrated — the readings either side are the same sensor
  but there is nothing on the wire that proves it.
- **CLI:** CommonCLI plus `setperm` and `io` (GPIO, board-dependent — `getGpio()`
  defaults to 0). `sensor list` / `sensor get <k>` / `sensor set <k> <v>` are
  **CommonCLI**, so every role answers them (a GPS repeater exposes `gps`) —
  the catalogue leaves them untagged; `room.post` is room-only. `SensorVarsSection` replaces the
  repeater-only Routing section; it pages `sensor list` (`N vars\nkey=value…`,
  `... next:i` past 134 bytes).
- **Monitoring:** `monitorKind` maps SENSOR to the **companion** collector — the
  sessionless telemetry request is exactly what a sensor answers for any ACL
  client — so `MonitoringSettings` gets `kind="companion"` (no password
  fields). The companion must be in the sensor's ACL (has logged in once) —
  the contact page's Telemetry panel says so on failure, and shows the
  Monitoring panel for CHAT and SENSOR contacts alike.
- **Alerts.** A sensor's only outbound text is `sendAlert`: a plain TXT_MSG DM
  to each ACL client whose permission byte has the matching priority bit —
  `PERM_RECV_ALERTS_LO = 1<<6`, `PERM_RECV_ALERTS_HI = 1<<7` (`SensorMesh.h`;
  login grants both). High-priority alerts retry up to 4×, low-priority once.
  Our companion ACKs plain DMs, so the retry loop terminates. The alerts land
  in the sensor's `dm:` thread, which the Messages page **keeps listed** (unlike
  repeaters) but with the composer locked — typed text would be executed as a
  CLI command by an admin session or dropped otherwise.

- **Login reply byte 7 is the client's full permission byte** on both
  repeaters and sensors (`[ts:4][RESP_OK][0][isAdmin][permissions][rand:4][ver]`).
  `Session.Permissions` / `LoginResult.Permissions` carry it, so the header
  pill can say read-only vs read-write instead of "guest" for any non-admin,
  and a sensor session's own alert bits are visible.
- **`monitorKind` falls back to the contact's cached `type`** before the
  `isRepeater` metadata flag, so a manually added SENSOR (or one whose advert
  hasn't been heard since boot) still routes to the telemetry collector rather
  than to "no collector registered".
- **Discovery:** sensors answer `NODE_DISCOVER_REQ` when the filter includes
  `ADV_TYPE_SENSOR`, but the firmware repeater's `discover.neighbors` — and
  ours — ask for repeaters only, so sensors never appear in discovery results.
  That matches the firmware; don't widen the filter to "fix" it.
- **The permission byte is written whole.** `ClientACL::applyPermissions` does
  `c->permissions = perms`, so a `setperm` that sends only the role (1–3)
  clears bits 6–7 and silently unsubscribes that client from alerts. On
  sensors the Access tab therefore shows the two alert bits per client and
  composes `role | alertBits` on every change; `AddAccessDialog` defaults new
  sensor clients to both bits, matching login. Repeaters ignore those bits.
- Saving credentials on a sensor writes `isRepeater: false`; the contact's
  primary action is *Manage* → the sensors route.
- A sensor's inbound TXT_MSG is a CLI command (admin only) and its outbound text
  is `sendAlert` — there is no chat, which is why Messages never lists them.

## Serving telemetry (the companion and the repeater we run)

`companion_radio` keeps one mode per permission class (`NodePrefs.h`:
`telemetry_mode_base/loc/env`), each of deny / use-the-contact's-flags /
allow-all. We store the same three on `companions` as `deny`, `selected` and
`contacts`, and a contact's grant in its metadata as `telemPerms` (the
`TELEM_PERM_*` bits, unshifted — the firmware's `contact.flags >> 1` exists only
because bit 0 there is the favourite flag, which we have no use for).

The gate that surprises people: `MyMesh.cpp` replies **only** if
`TELEM_PERM_BASE` survives, so denying the base class silences position and
sensors too, however they are set. The requester narrows further with an inverse
mask in the first reserved byte. `/companions/:ref/telemetry` sets all of this;
`internal/node/companion/telemetry.go` applies it.

The repeater is different: it answers any logged-in client, sends base whatever
the mask says, and gives a guest (ACL role 0) base only
(`simple_repeater/MyMesh.cpp` `handleRequest`); a blank guest password admits
anyone. `internal/node/repeater/request.go` does the same.

A reply goes in the firmware's order: the battery, then the sensors, then the
MCU temperature last. There is never a position: the firmware sends GPS only
from a GPS module (`EnvironmentSensorManager::querySensors` wants `gps_active`,
and the boards with one built in send its fix), never the position typed in for
adverts, and no host here has one. The location class is stored for parity and
gates nothing. A flood a companion answers with (the path return, or the
datagram when it has no route back) takes the companion's own path hash size,
as `sendFloodScoped` uses `path_hash_mode + 1`; companions never scope a flood.

Channel 1 carries a voltage unless the operator mapped one there and its sensor
is failing. `getBattMilliVolts()` returns 0 on a board with no cell, so a
firmware node always sends one, and so do we when no row replaces it: a
mains-powered host publishes 0 V rather than nothing. A channel-1 row replaces
the built-in reading rather than joining it, which is how a PiSugar or an ADC
divider becomes the node's battery; if that sensor fails the row publishes
nothing, since falling back would report a different battery under the same key.

The reply budget is set by the requester's companion, not the packet: it
re-frames what it decrypts, block padding included, behind an 8-byte push header
(`companion_radio/MyMesh.cpp`) and `writeFrame` drops a frame over
`MAX_FRAME_SIZE` 176. So a body holds at most 156 bytes direct, 154 or 153 over a
zero- or one-byte flood path, and 170 less the path from two bytes up (the path
return's `[pathLen][path][type]` sits inside the ciphertext).
`sensor.MaxReplyBody` holds that arithmetic; a map may take 145 bytes, 153 less
the node's own voltage and temperature, and a longer path sends channel 1
alone. Channel 0 is refused: the firmware's LPP reader treats it as the
end of the data.

## Room servers (chat — "talking" is implemented; management UI is not)

A room server (peer type `ROOM`) hosts a server-stored chat: clients log in
(ANON_REQ), **post** via plain DMs, and the server **pushes** stored posts back
one-at-a-time, ACK-gated. Key design decision: **a room conversation IS the
`dm:<pubkey>` thread** — posting reuses `SendContactMessage` verbatim (the
room session secret equals the static-identity ECDH secret, because login
sends our static pubkey), so delivery status, retry, WS shapes and the chat UI
all come for free. Sender labels vary per message (group-chat-like) — the
existing `MessageGroup` sender rendering already handles that.

- **Login** (`repeater.Client.SendRoomLogin`): plaintext is
  `[timestamp:4][sync_since:4][password:N]` — the extra `sync_since` is the
  "push me posts newer than this" cursor. Response byte[6] role: `1`=admin,
  `2`=read-only (guest), `0`=read-write. Sessions share the repeater session
  map (`Session.IsRoom`/`Role`). Blank password = re-auth by pubkey if already
  in the server's ACL.
- **sync_since derivation**: `handleRoomLogin` uses the newest stored rx
  message in the `dm:<pubkey>` thread (`store.Messages.LatestRx`) when the
  body omits `syncSince`. No separate cursor column — the messages table is
  the cursor. First join (no history) sends 0 → server backfills its whole
  cyclic queue (max 32 posts).
- **Push RX** (`companion.handleRoomPush`): a TXT_MSG with
  `plaintext[4]>>2 == 2` (TXT_TYPE_SIGNED_PLAIN), layout
  `[post_timestamp:4][flags:1][author_pubkey_prefix:4][text:N]`. **The ACK
  CRC hashes OUR pubkey** (`sha256(plaintext_unpadded || recipient_key)`),
  unlike DM ACKs which hash the sender's, and it is a bare 4-byte CRC (no
  attempt byte — same for the legacy plain-text CLI ack), unlike the 6-byte DM
  ACK — `sendDMAck` takes the prebuilt ack payload. Not ACKing stalls the server's push stream for us. Dedup is
  by (post timestamp, text) against the last 50 stored rows — retries carry a
  fresh attempt byte so the packet differs, but the post is the same.
- **A room never pushes a post back to its author** (`!p->author.matches(client->id)`
  in both `getUnsyncedCount` and the push loop), and a post is only pushed once
  `POST_SYNC_DELAY_SECS` has elapsed. So a solo tester sees `n_posted` climb
  while `n_post_push` stays 0 — that is correct firmware behaviour, not a broken
  push stream. Verifying the push path needs a *second* client posting.
- **Receive survives restarts without re-login**: the server's ACL + secret
  persist on its filesystem and pushes decrypt with the contact ECDH secret,
  so posts keep flowing in and get ACKed with no local session. The session
  only gates the UI (join bar / read-only composer) and posting freshness.
- **Author names** resolve from the 4-byte pubkey prefix via
  `store.Peers.LookupByHash`; unknown authors render as the hex prefix.
- **Posts cap at 151 chars** (firmware `MAX_POST_TEXT_LEN = 160-9`); the
  composer charLimit drops to 151 for ROOM threads — longer text would be
  silently truncated server-side.
- **UI**: `CompanionDetailPage` shows a `RoomJoinBar` instead of the composer
  for an un-joined ROOM thread (password + save-to-contact switch → POST
  `/rooms/{pubkey}/login`); saved password lives in contact metadata
  `roomPassword`. The PATCH contact-metadata endpoint **replaces** the whole
  blob — always GET + spread + PATCH (RoomJoinBar does). Read-only role
  disables the composer. `/rooms/{pubkey}/session` returns the bare session
  or `{loggedIn:false}` (and `null` before any login — same presence-of-
  `pubkeyHex` derivation as repeaters).
- **Management** shares the admin page: `RepeaterDetailPage` at
  `/companions/:name/rooms/:pubkey` with `kind="room"` (the contact page shows
  *Message* and *Manage* for rooms). Room-specific plumbing lives under
  `/api/companions/{name}/rooms/{pubkey}/`:
  - `login` (carries `sync_since`; saved credentials use the `roomPassword`
    metadata key the chat's `RoomJoinBar` reads, so one save serves both).
  - `status` → `parseRoomStatus`: `ServerStats` is the repeater layout for 48
    bytes, then `n_posted` / `n_post_push` (u16 each) where a repeater has
    `rx_air_time_secs` — never route a room through `parseRepeaterStatus`.
    The Status tab hides RX air / chan util / recv errors and shows the post
    counters.
  - `keepalive` → `SendRoomKeepAlive`: REQ 0x02 `[tag][0x02][since:4]`, sent
    **direct only** (the firmware ignores flooded keep-alives and answers with
    a direct ACK carrying its unsynced count), after which posts resume through
    the normal DM path. Fire-and-forget; "Resync posts" on the Status tab.
  - Everything else (CLI, ACL, telemetry, path) is the shared type-agnostic
    surface. Firmware facts the page encodes: login outcomes are admin password
    → admin, `guest.password` (the *room* password) → read-write, otherwise
    read-only **only if `allow.read.only`** is on (else no reply); reply byte 6
    is 1 admin / 2 read-only / 0 read-write; the ACL list returns **admins
    only**; there is no owner-info or neighbours request and no `loop.detect`
    (rooms forward, honouring `repeat` and all three `flood.max*`); rooms answer
    telemetry, so `monitorKind` maps ROOM to the companion collector.
- **Still not done**: auto re-join on startup, and surfacing the keep-alive
  ACK's unsynced-post count (we don't listen for that ACK).

## Message delivery status

Messages have a `status` column: `NULL` (rx/legacy), `"sending"`, `"delivered"`, `"failed"`.
- WS action: `{ action: "status", companion, channel, id, status }`
- Retry endpoint: `POST /api/companions/{name}/messages/{messageId}/retry` — deletes failed msg, re-sends.

## CLI autocomplete catalogue

`src/helpers/CommonCLI.cpp` is the firmware CLI handler. Our catalogue is
[`web/frontend/src/data/cli-catalog.json`](../web/frontend/src/data/cli-catalog.json)
(`topLevelCommands` / `configKeys`), surfaced by
[`cliCatalog.ts`](../web/frontend/src/lib/cliCatalog.ts)
(`cliCommandsFor(role)` / `cliConfigKeysFor(role)`) and consumed by the terminal
in `RepeaterDetailPage.tsx`, which passes its `kind`. Regenerate from
CommonCLI.cpp + each role's MyMesh.cpp when upstream adds commands.

Entries carry `roles: ["repeater"|"sensor"|"room"]`; absent means every role.
The role facts behind the tags:

- Repeater-only: `neighbors`, `neighbor.remove`, `discover.neighbors`, `loop.detect` (sensor and room `formatNeighborsReply` return "not supported", and only the repeater implements `isLooped`), plus `bridge.*`.
- `io` sensor-only; `allow.read.only` room-only.
- `guest.password` repeater + room (sensors have no guest login); `region save`/`load` repeater + room (the sensor lacks the callbacks).
- Universal: `repeat` / `flood.max` — sensors and rooms **do** forward via `allowPacketForward` (the room also honours `flood.max.advert`/`.unscoped`).

## Radio presets

<https://api.meshcore.nz/api/v1/config> `suggested_radio_settings.entries` is
the source for
[`radio-presets.json`](../web/frontend/src/data/radio-presets.json) (consumed by
`RadioPresetSelect` on Settings and in the setup wizard). Regenerate it with
`go generate ./web` (`web/presetsgen`), don't hand-edit. The generator keeps the
feed's order, skips an entry Settings would refuse (it runs `config.Validate`
on each, and takes only the ten LoRa bandwidths) or one missing a field, and
refuses to write if the feed gives fewer than half the presets the file has.
`go run ./web/presetsgen -check <file>` exits 1 when the file differs from the
feed, and only warns when the feed is down or unusable, since regenerating
cannot fix that; the release workflow runs it on every tag. A preset's `network_settings.path_hash_size` becomes
`pathHashSize`, which the label shows but does **not** apply — set it on
Settings (see [mqtt-and-radio.md](./mqtt-and-radio.md)).
