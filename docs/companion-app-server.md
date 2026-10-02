# Companion app server

How OwlShack serves the MeshCore companion protocol to apps (`internal/appserver`), and where it
departs from a firmware companion radio. `examples/companion_radio/MyMesh.cpp` in the firmware is
the reference for every command; the line numbers below are v1.17.1's.

## Shape

- **One TCP port per companion**, set as `companions[].app` (`companion_app` table, migration 023)
  and `PUT /api/config/companions/{id}/app`. Frames are the WiFi interface's: `<` + u16 length +
  payload from the app, `>` + u16 length + payload back (`SerialWifiInterface.cpp`). Codecs are
  meshcore-go's `companion.ParseCommand` and the response `ToBytes`.
- **`appserver` never imports the domain.** It owns listeners, sessions and each companion's
  offline queue; everything a command reads or changes goes through `appserver.Device`, which
  `internal/app/appdevice.go` implements over a running companion, the store and the same
  backend writes the UI uses (`SaveCompanion`, `SaveChannel`, `SetCompanionTelemetry`).
- **The server outlives every radio generation.** `Run` creates it once; every `installBackend`
  calls `applyApps`, which opens the ports the config asks for and points each at the companion as
  it is now running. A reload, rename or modem reconnect re-points a connected app instead of
  dropping it, and `blocksEqual` ignores `App`, so changing a port restarts no companion.
- **One session per port; a new connection replaces the current one**, as `checkRecvFrame` does.
  `PortStatus.Replaced` counts it, which is how two apps fighting over one port show up.
- **The offline queue belongs to the port, not the session.** Messages queue whether or not an
  app is connected (the firmware queues in `queueMessage` regardless) and sync oldest first, in
  V3 frames when the app's last `CMD_DEVICE_QUERY` asked for version 3 or later. 256 slots to the
  firmware's 16; a full queue drops its oldest channel message first (`addToOfflineQueue`). Like
  the firmware's, it is memory only: a process restart loses what was not synced.
- **Pushes reach only a connected app**, as the firmware's `_serial->isConnected()` checks.

## What the companion tells an app

`companion.AppSink` (`internal/node/companion/app.go`) is fed from the receive path: plain DMs and
CLI replies with the sender's own timestamp, room posts with the author's 4-byte prefix
(`TXT_TYPE_SIGNED_PLAIN`), channel text as `"sender: text"` with its slot, channel datagrams,
verified adverts, routes learned from path returns, raw receptions, traces, control and raw
custom packets. CLI replies inside a repeater session and routes the repeater client learns reach
it through `repeater.AppHooks`.

Adverts go through the app's auto-add rules (`shouldAutoAddContactType` plus max hops): manual add
off files every advertiser as a contact and pushes `PUSH_CODE_ADVERT`; on, only the ticked types
are filed, and the rest are pushed as `PUSH_CODE_NEW_ADVERT` and left out.

## Departures from the firmware

| Command | Firmware | Here, and why |
|---|---|---|
| `SET_RADIO_PARAMS`, `SET_RADIO_TX_POWER` | Changes the radio | Range-checked as the firmware does, then OK and nothing changes: the radio is shared with every other node. Apps send these in the same save as the name and position, and abandon the save at the first error |
| `SET_DEVICE_TIME` | Sets the RTC if not earlier | OK if not earlier than the host's clock, which NTP owns; nothing changes |
| `REBOOT` | Reboots | Closes the connection |
| `FACTORY_RESET` | Formats the filesystem | `ERR_CODE_FILE_IO_ERROR`: an app may not wipe a companion |
| `IMPORT_PRIVATE_KEY` | Behind `ENABLE_PRIVATE_KEY_IMPORT` | Always `RESP_CODE_DISABLED`; identities change in OwlShack |
| `EXPORT_PRIVATE_KEY` | Behind `ENABLE_PRIVATE_KEY_EXPORT` | `RESP_CODE_DISABLED` unless the operator ticked *Allow key export*; then the 64-byte `prv.key` form |
| `SET_CHANNEL` | Any slot below `MAX_GROUP_CHANNELS` | A slot is the channel's place in the companion's list: the next free slot adds, an occupied one renames or rekeys, an empty name removes (later slots move up). An unused slot reads as a blank channel and clearing one succeeds, as on firmware; setting a channel past the next free slot is `ERR_CODE_NOT_FOUND`. Slot 0 need not be `Public`: an imported app companion keeps the channels it lists, as `ApplyDefaults` only adds `Public` to companions without an app connection |
| `SET_DEFAULT_FLOOD_SCOPE` | Name and key stored | A hashtag region only: the key must be the one its name derives (`config.ScopeRegion`), since OwlShack keeps no private region keys |
| `GET_BATT_AND_STORAGE` | Board battery and flash | The radio board's battery when it reports one; storage reads 0 |
| `GET_STATS` | Board counters | What the node and modem expose; counters with no source here read 0 |
| `GET_CUSTOM_VARS`, `SET_CUSTOM_VAR` | Sensor settings | None to read; setting one is `ERR_CODE_ILLEGAL_ARG` |
| `CMD_SET_DEVICE_PIN` | BLE PIN | Stored and reported in `DEVICE_INFO`; there is no BLE |

Everything else is answered as `handleCmdFrame` answers it, error codes included: a frame the
firmware's length guards refuse comes back as `ERR_CODE_UNSUPPORTED_CMD`, an unknown key as
`ERR_CODE_NOT_FOUND`, and a send that cannot be queued as `ERR_CODE_TABLE_FULL`.

## Sends and requests

- **A DM is sent once.** `SendAppText` → `node.SendTextOnce` with the app's timestamp and attempt;
  the app runs its own retries, as it does against firmware. `RESP_CODE_SENT` carries the ACK CRC
  and the airtime timeout; the ACK pushes `PUSH_CODE_SEND_CONFIRMED`. CLI data goes out with the
  node's `UniqueTimestamp`, as the firmware sends it, so a repeater never sees a replay.
- **Floods follow the session's scope** set by `SET_FLOOD_SCOPE_KEY` (per connection, as
  `send_scope` is per radio), else the companion's flood scope.
- **Logins and requests** start through the repeater client (`StartLogin`, `StartRequest`), answer
  `RESP_CODE_SENT` with the tag and timeout at once, and push the reply when it comes:
  `LOGIN_SUCCESS`, `STATUS_RESPONSE`, `TELEMETRY_RESPONSE`, `BINARY_RESPONSE` (binary and anon),
  `PATH_DISCOVERY_RESPONSE`. A timeout pushes nothing, as at the firmware. A room login resumes
  from the newest post stored for that room.
- **What an app sends shows in the chat**: plain DMs and channel posts are saved like UI sends.

## Security

The protocol has no authentication, the same as a WiFi companion radio: anyone who can reach the
port can send as the companion. Bind to `127.0.0.1` or a trusted network's address where that
matters; the UI says so next to the switch.
