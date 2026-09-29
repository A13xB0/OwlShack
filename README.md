<p align="center">
  <img src="web/frontend/public/icons/icon-192.png" width="96" alt="OwlShack">
</p>

<h1 align="center">OwlShack</h1>

<p align="center">
  An operator console for <a href="https://github.com/meshcore-dev/MeshCore">MeshCore</a> mesh networks. One static Go binary, no CGO.
</p>

Plug a MeshCore radio into any Linux, macOS or Windows machine (a Raspberry Pi
is plenty), open `http://localhost:8080`, and you get a live console for the
mesh: chat, packet capture, mapping, remote repeater administration, and
telemetry, all persisted to SQLite. Built on the pure Go
[meshcore-go](https://github.com/meshcore-go/meshcore-go) library.

![Overview](docs/screenshots/overview.png)

## What it does

- **Runs nodes on the mesh.** One or more companion identities (channels, DMs,
  templated auto-responders) and, optionally, a repeater that relays traffic
  with its own identity, ACL and region scoping.
- **Observes everything.** Every packet, peer and advert is decoded, streamed
  live to the browser, mapped, and archived.
- **Administers remote nodes.** Log in to other operators' repeaters and drive
  them: status, CLI terminal, path, neighbours, settings, access control. Same
  for room servers and sensor nodes.
- **Watches the radio.** Modem fault counters, TX outcomes, RX losses, and a
  zero-hop discovery scan that answers "what can this radio actually hear?".
- **Talks to three radio families.** MeshCore firmware over serial or TCP
  (KISS), an openHop Modem over USB or the network, or a bare SX1262 driven
  straight off a Pi's SPI bus, no firmware node needed.
- **Bridges to MQTT.** Feeds aggregators like LetsMesh and CoreScope.

The web UI is the configuration surface and the database is the source of
truth; there is nothing to hand-edit.

## Screenshots

| | |
|---|---|
| ![Map](docs/screenshots/map.png) | ![Packets](docs/screenshots/packets.png) |
| Every geolocated peer, filterable by type, with link lines | Live packet stream, grouped by hash, with route and signal |
| ![Chat](docs/screenshots/chat.png) | ![Repeater](docs/screenshots/repeater.png) |
| Per-companion chat: channels, DMs, rooms, delivery status | The repeater you run, with live relay stats and ACL |
| ![Discover](docs/screenshots/discover.png) | ![Radio](docs/screenshots/radio.png) |
| Zero-hop scan: who is in direct range, and both SNR directions | Modem diagnostics, with unmeasurable counters shown as `—` |

Installable as a PWA, with a mobile layout and a light theme.

## The console

| Section | What it does |
|---------|--------------|
| Overview | Peer counts, type spectrum, recently seen, companion roster |
| Peers | Searchable, sortable table with type pills, signal bars, last-seen |
| Map | Leaflet map of geolocated peers, filterable, with link lines and any advert or packet path drawn hop by hop |
| Packets | Live packet stream with a detail panel (route, hops, signal, raw hex) |
| Trace | Interactive path builder and result timeline |
| Monitoring | Polled nodes with status and telemetry history charts |
| Radio | Modem diagnostics: TX outcomes, RX losses, board readings, faults |
| Discover | Zero-hop scan: which repeaters and sensors are in direct range |
| Sensors | I2C sensors and a PiSugar UPS on this host, the radio board's battery and MCU temperature, read every 5 s, values read from a web address such as a weather report, and values worked out from them |
| Companions | Per-companion chat, contacts, channels, remote repeaters, rooms, sensors, and the telemetry each one publishes |
| Bots | Create and edit triggers across every companion (group, dm and cron) |
| Repeater | The repeater this instance runs: relay stats, neighbours, access, the telemetry it publishes, settings |
| MQTT | Broker management and the companion that feeds the bridge |
| Settings | Connection, radio parameters, presets, listen address, log level |

## Supported hardware

### The machine running OwlShack

Any Linux, macOS or Windows host Go targets. Release binaries and Docker images
cover x86-64, 386, ARMv6/v7, ARM64, ppc64le, riscv64 and s390x. A Pi Zero 2 W
is enough to run a companion, a repeater and the console at once.

### Radio interfaces

Set on the Settings page. Pick a **Radio backend** — KISS modem, openHop Modem
or SPI radio hat — and the rest of the form follows: a transport and a device
for the first two, the hat and its SPI port for the third.

> [!CAUTION]
> **Which one you need.** SPI means this process is the radio driver: it
> clocks an SX126x over the host's own bus and toggles its reset, busy and
> RF-switch lines itself, so the board has to be one it holds a pin map for.
> Everything else (any other chip family, a gateway concentrator, a bridge
> that fakes a bus over USB) belongs behind firmware that already knows its
> own hardware — MeshCore over KISS, or an openHop Modem.

| Interface | Status |
|---|---|
| Native SX126x on a Raspberry Pi's SPI bus | Supported |
| MeshCore firmware over USB serial (KISS) | Supported |
| MeshCore firmware over TCP (KISS) | Supported |
| openHop Modem over the network | Supported |
| openHop Modem over USB serial | Supported |
| SX127x on SPI | Not supported |
| SX1302 / SX1303 concentrator boards | Not supported |
| USB-to-SPI bridges (CH341 and similar) | Not supported |

### SPI boards

Pin maps live in [`internal/modem/boards.json`](./internal/modem/boards.json)
and are chosen by board, never by pin. Every entry is an SX1262 on a
Raspberry Pi header, and every one of them can be driven:

| Board | Max TX | Bus | Status |
|---|---|---|---|
| Zindello UltraPeaterZero (E22, 1 W) | 22 dBm | SPI0.0 | **Verified on hardware** |
| Zindello UltraPeaterZero (E22P, 1 W) | 22 dBm | SPI0.0 | **Verified on hardware** |
| RAK6421 + RAK1330x, IO slot 1 | 22 dBm | SPI0.0 | **Verified on hardware** |
| RAK6421 + RAK1330x, IO slot 2 | 22 dBm | SPI0.1 | **Verified on hardware** |
| MeshAdv | 22 dBm | SPI0.0 | Preset available, untested |
| Waveshare LoRa HAT | 22 dBm | SPI0.0 | Preset available, untested |
| uConsole LoRa Module aio v2 | 22 dBm | SPI1.0 | Preset available, untested |
| PiMesh-1W (V1) | 18 dBm | SPI0.0 | Preset available, untested |
| PiMesh-1W (V2) | 18 dBm | SPI0.0 | Preset available, untested |
| NebraDuo-E22P-1W | 18 dBm | SPI0.0 | Preset available, untested |
| ZebraHat-1W | 18 dBm | SPI0.0 | Preset available, untested |
| ZebraHatDuo-R0-1W | 18 dBm | SPI0.0 | Preset available, untested |
| ZebraHatDuo-R1-1W | 18 dBm | SPI0.1 | Preset available, untested |
| BQ Voyage Station G3 | 19 dBm | SPI0.0 | Preset available, untested |
| NebraHat-2W | 8 dBm | SPI0.0 | Preset available, untested |

Max TX is the level the LoRa core is driven at, **not** what leaves the
antenna. Boards with an external PA reach far higher: NebraHat-2W is listed at
8 dBm because that 8 dBm drives its amplifier. Names are the registry's own.

An untested preset is a pin map someone contributed that nobody has since
confirmed against the board in hand. Get one wrong and the radio never
receives, or transmits into a dead antenna path, and both look exactly like a
quiet mesh, so watch the Radio page's counters before you trust a first
contact.

**SPI is Raspberry Pi only.** These are BCM header pins, so a hat for another
single-board computer will not work even when its radio is an SX1262 — the
LuckFox Pico boards, for one.

Hat not listed? Open a PR with its pin map, or lend or donate the board and it
gets added and tested here.

### KISS radios

Whatever board MeshCore firmware supports, OwlShack can drive: it speaks to
the firmware, not the chip. Verified here on a Seeed XIAO nRF52840 and a
RAK4631.

### Nodes OwlShack talks to

Any MeshCore repeater, room server or sensor node on the mesh, over the air,
with no wiring and nothing installed on them. Remote administration needs that
node's admin or guest password.

> [!WARNING]
> A first transmit is the moment a wrong pin map costs you hardware. Check the
> antenna is on before anything keys up, and that your frequency and power are
> legal where you are. The shipped defaults are New Zealand's narrow preset,
> 917.375 MHz at 22 dBm, which is not yours to assume. Pick your own region on
> the Settings page or in the wizard.

## Install

### Release binary

Pre-built binaries for Linux, macOS and Windows are on the
[Releases](https://github.com/meshcore-go/OwlShack/releases) page.

```bash
chmod +x OwlShack-linux-arm64
sudo mv OwlShack-linux-arm64 /usr/local/bin/OwlShack
```

### Debian / Ubuntu / Raspberry Pi OS

The installer picks the right package for the machine, installs it, and leaves
OwlShack running under systemd — nothing has to stay in a terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/meshcore-go/OwlShack/dev/packaging/install.sh | sudo sh
```

Or take the `.deb` for `amd64`, `arm64`, `armhf` or `i386` from the
[Releases](https://github.com/meshcore-go/OwlShack/releases) page yourself:

```bash
sudo apt install ./owlshack_1.4.0_arm64.deb
systemctl status owlshack
```

On a Raspberry Pi, `dpkg --print-architecture` is the answer that matters, not
the model: a 64-bit Pi OS reports `arm64`, a 32-bit one `armhf`. The `armhf`
package is built for ARMv6, so it runs on every Pi back to the Zero and the
original Model B.

The service runs as the `owlshack` user (added to `dialout`, plus `spi` and
`gpio` when the host has them), keeps its database in `/var/lib/owlshack`, and
serves `http://<host>:8860` — not 8080, which `http-alt` shares with half the
homelab. The package sets that as `LISTEN_DEFAULT` in `/etc/default/owlshack`,
which seeds the database on first run; after that the address belongs to the
Settings page like any other install. The same file carries `HOST`, `PORT`, `TZ`
and extra flags — `HOST` and `PORT` pin the address on every start, so use them
only when the UI should not be able to move it. To build one from
a checkout: `./build.sh && packaging/deb/build-deb.sh ./OwlShack amd64`.

`sudo apt remove owlshack` keeps `/var/lib/owlshack`, so reinstalling resumes
with the same identity and history. `sudo apt purge owlshack` deletes it, as a
purge should — and that directory is the only copy of the node's keys, contacts
and messages, so take a backup from the Settings page first.

### Docker

Published to `ghcr.io/meshcore-go/owlshack` for `linux/386`, `amd64`,
`arm/v6`, `arm/v7`, `arm64/v8`, `ppc64le`, `riscv64` and `s390x`.

```bash
docker run -d \
  --device /dev/ttyACM0 \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e TZ=Pacific/Auckland \
  ghcr.io/meshcore-go/owlshack
```

Drop `--device` for a TCP radio connection. Set `TZ` to your own zone: the
image carries the zone database but selects nothing, so without it the
container's idea of local time is UTC.

### From source

Requires Go 1.26+ and Node. The SPA is embedded into the binary, so it has to
be built first:

```bash
git clone https://github.com/meshcore-go/OwlShack.git
cd OwlShack
./build.sh          # builds the SPA, then a version-stamped binary
```

## Quick start

**1. Connect the radio.** USB usually appears as `/dev/ttyACM0` on Linux,
`/dev/cu.usbmodem*` on macOS. On Linux, join the serial group and log back in:

```bash
sudo usermod -a -G dialout $USER
```

**2. Run it.**

```bash
./OwlShack -vvv
```

On a first run OwlShack starts quietly: it observes the mesh and advertises
nothing until you create a companion.

**3. Open `http://localhost:8080`.** A first-run wizard walks through the radio
and your first companion.

| | |
|---|---|
| ![Wizard, welcome step](docs/screenshots/wizard-welcome.png) | ![Wizard, radio step](docs/screenshots/wizard-radio.png) |
| Two steps, and nothing is broadcast until you create a companion | Attached radios are detected by name; presets fill the RF fields |

The review step spells the whole configuration back in plain language before
anything goes on the air:

![Wizard, review step](docs/screenshots/wizard-review.png)

Moving an existing node to new hardware? Restore its backup from the welcome
step. That is the only point at which you can, so a running node is never
overwritten. Everything after setup is configured in the UI and written back to
the database.

## Configuration

Everything is configured in the web UI and stored relationally in
`meshcore.db`. There is no config file to write or maintain: the UI is the
configuration surface, and `SIGHUP` reloads from the database.

> **Migrating from an older release?** `--config <path>` imports a TOML, YAML or
> JSON config file once and overwrites the stored config, after which the file
> is no longer read. Legacy layouts (`nodeType`, `[[bot]]`, `[[observer]]`)
> fold onto the current one automatically. See
> [config-and-storage.md](./docs/config-and-storage.md) for that format.

The rest of this section is a reference for what the settings mean, wherever
you set them.

### Map tiles

The Map, and the position pickers, draw on CARTO basemaps, which now need a
free API key ([carto.com/basemaps/apikey](https://carto.com/basemaps/apikey/),
5M tiles a month). Without one the tiles render watermarked. Paste the key into
Settings; it is stored with your config and visible to anyone who can open the
UI.

### Connection and radio

| Field | Description | Default |
|-------|-------------|---------|
| `connection` | `serial:///dev/ttyACM0`, `tcp://host:port`, `openhop://host:port`, or `spi://` | `serial:///dev/ttyACM0` |
| `connectionType` | Derived from `connection`, not set by hand: `kiss`, `openhop` or `spi` | `kiss` |
| `spiBoard` | Board id from the registry, required for `spi://` | none |
| `modemToken` | openHop modem access token; write-only, reads report only whether one is stored | none |
| `baudRate` | Serial baud rate | `115200` |
| `freq` | Frequency in MHz | `917.375` |
| `bw` | Bandwidth in kHz | `62.50` |
| `sf` | Spreading factor | `7` |
| `cr` | Coding rate | `8` |
| `tx` | TX power | `22` |
| `logLevel` | `debug`, `info`, `warn`, `error`, `trace` (overridden by `-v`) | `info` |

**SPI radios.** With the connection type set to `spi`, OwlShack drives the
SX1262 itself and `spiBoard` picks the pin map; see
[Supported hardware](#supported-hardware) for which boards that build can
actually drive.

### Companions

A companion is a node identity OwlShack runs on the mesh. Add them on the
Companions page.

| Field | Description |
|-------|-------------|
| `name` | Display name on the mesh, and the storage key for this companion's history |
| `privateKey` | 64-hex ed25519 seed; leave it unset and one is generated and stored |
| `latitude` / `longitude` | Advertised position (decimal degrees) |
| `advertInterval` | Seconds between adverts; `0` = never |
| `channels` | Channels to join |
| `trigger` | Triggers attached to this companion |
| `dmPolicy` | Who may DM this companion: `contacts` (default), `allowlist` or `anyone` |
| `dmAllow` | Public keys the `allowlist` policy accepts, full or a leading prefix |

### Triggers

Auto-responders and scheduled messages, managed on the Bots page.

| Field | Description |
|-------|-------------|
| `type` | `group` (channel messages), `dm` (direct messages) or `cron` |
| `template` | Go `text/template` for the response |
| `channels` | Channels to listen on / send to |
| `match` | [Go regexps](https://pkg.go.dev/regexp/syntax) matched against incoming messages (group, dm) |
| `schedule` | Cron expression, e.g. `"*/5 * * * *"` (cron) |
| `contacts` | Who a `dm` trigger answers. The Bots page picks from known companions and stores public keys; a hand-written config may also use a peer name or a key prefix. Empty answers everyone the DM policy let through |
| `retryTimeout` / `maxRetries` | Repeater-echo timeout in seconds (`5`) and resend cap (`3`) |
| `charLimitBehaviour` | `truncate` or `split` past the character limit |
| `pathHashSize` | `0` = copy the sender's setting, `1`/`2`/`3` = bytes per hash |

After sending, a companion waits for a repeater to echo the message back; no
echo inside `retryTimeout` means a resend, up to `maxRetries`.

Channels are public or hashtag channels named directly (`Public`, `#general`),
or private channels carrying a shared key. `Public` is the well-known channel
every companion joins.

### Failover replies

Group bots can wait for another sender's response before replying. In the bot
editor, enable **Failover reply**, set the wait to **10 seconds**, and use:

```text
Match pattern:    (?i)^wlg$
Suppress pattern: ^@\[{{.Sender | reQuote}}\].+
```

The optional config fields are `failoverPattern` and `failoverTimeout` (1–3600
seconds). An empty pattern and zero timeout disable failover. The suppression
pattern is a Go template with the original request's `.Sender`; `reQuote`
escapes regex characters in that name. Brackets around the mention must be
escaped separately, as above. Invalid patterns are rejected when saving;
a pattern that becomes invalid for a particular sender is logged and that
request is answered immediately.

Only an accepted response heard on the same channel during the wait cancels
the reply; messages from this companion or the original requester do not.
The response need not match the request pattern. Expiry uses local elapsed
time and the reply retains the original request's template data. Retry settings
apply after sending. Duplicate requests keep the original deadline; pending
replies are cleared on bot edits, restart or shutdown. Each trigger holds at
most 256 pending requests; past that a request is logged and answered
immediately rather than dropped.

Any other sender matching the pattern can suppress a reply, and a broad pattern
can suppress multiple pending requests from the same name. Use staggered waits
for multiple backup bots; a response the backup cannot hear cannot suppress it.

### Template variables

Group triggers: `{{.Sender}}` `{{.Channel}}` `{{.Message}}` `{{.Match}}` (named
capture groups) `{{.Timestamp}}` `{{.SNR}}` `{{.RSSI}}` `{{.Hops}}`
`{{.PathHashes}}` `{{.PathHashSize}}`.

DM triggers: the same, minus `{{.Channel}}`, plus `{{.SenderPubKey}}`, the
sender's full public key, which is how the reply is addressed.

Cron triggers: `{{.Time}}` and `{{.Schedule}}`. `{{.BotName}}` is in every
template.

### Template functions

| Function | Does |
|---|---|
| `formatPathBytes` | Renders raw path hashes readably, joined by an optional separator (`Direct` when there is no path) |
| `now` | The current time, as a value you can format or take parts of |
| `date` | Formats a time in a layout, optionally in a named zone |

`formatPathBytes` takes the path hashes and an optional separator, defaulting
to `", "`:

```
{{formatPathBytes .PathHashes}}          A1, B2, C3
{{formatPathBytes .PathHashes " > "}}    A1 > B2 > C3
{{formatPathBytes .PathHashes ""}}       A1B2C3
```

A node heard direct renders as `Direct` whatever the separator, since there is
nothing to join.

`date` takes a time, a layout, and an optional [IANA zone](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones);
without a zone it uses the host's. Layouts are Go's, where the layout is itself
an example date: `2006-01-02 15:04:05`, `Mon`, `Jan`, `3:04PM`.

```
{{date now "15:04"}}                            18:30
{{date now "Mon 2 Jan, 3:04PM" "Pacific/Auckland"}}   Wed 9 Sep, 6:30PM
{{date now "15:04" "UTC"}}                      06:30
{{date .Timestamp "15:04" "UTC"}}               when the message was sent
{{now.Year}}                                    2026
```

A group trigger's `{{.Timestamp}}` arrives as raw unix seconds, so printing it
directly gives a bare number; pass it through `date` to render it. The zone
database is compiled into the binary, so a named zone resolves the same on
every platform.

Leaving the zone off uses whatever the process treats as local, which in a
container is UTC unless `TZ` is set. Name the zone in the template when it has
to be right regardless of where OwlShack runs.

## MQTT

OwlShack publishes observed traffic to MQTT brokers, used by
[LetsMesh](https://letsmesh.net) and
[CoreScope](https://github.com/Kpa-clawbot/CoreScope) to aggregate network
data. Brokers are managed on the MQTT page.

| Bridge field | Description |
|--------------|-------------|
| `node` | Companion that feeds the bridge, exactly one (empty = the first) |
| `enabled` | Whether the bridge runs |
| `iataCode` | Location identifier, e.g. an airport code |
| `statusInterval` | Seconds between status publishes (default `300`) |
| `owner` / `email` | Optional, included in MQTT token claims |

| Broker field | Description |
|--------------|-------------|
| `name` / `enabled` | Display name; whether this broker is used |
| `transport` | `websockets` or `tcp` |
| `host` / `port` / `path` | Endpoint; `path` is the WebSocket path (default `/`) |
| `packetTopic` / `statusTopic` | Templates; `{iata}` `{pubkey}` `{name}` (uppercase also resolves). Empty = `meshcore/{iata}/{pubkey}/<kind>` |
| `disallowedPacketTypes` | Types to exclude, e.g. `["ack", "advert"]` |
| `dedup` / `retainStatus` | Per-broker packet dedup; retain status messages |
| `tlsEnabled` / `tlsInsecure` | Enable TLS / skip certificate verification |
| `authType` | `token` (Ed25519 JWT from the node identity), `basic`, or `none` |
| `username` / `password` / `audience` | Basic credentials; token audience |

For LetsMesh, that is `mqtt-us-v1.letsmesh.net:443` over websockets with TLS
and `token` auth, the audience matching the host.

Only RX packets are published, never TX. See
[docs/mqtt-and-radio.md](./docs/mqtt-and-radio.md) for the wire schema.

## Running it

| Flag | Description |
|------|-------------|
| `-c, --config PATH` | One-time import of a legacy config file, then run with it |
| `-V, --version` | Print version and exit |
| `-v, --verbose` | Increase log verbosity (`-v` debug, `-vv` trace, `-vvv` trace+) |

| Variable | Effect |
|---|---|
| `HOST` | Bind host. Unset means all interfaces |
| `PORT` | Bind port. Unset means the stored value, then `8080` |
| `LISTEN_DEFAULT` | Seeds the stored listen address on first run only; Settings owns it after |
| `TZ` | The zone the process treats as local, e.g. `Pacific/Auckland` |

`HOST` and `PORT` override the stored listen address at startup (env > stored
config > `:8080`), which is handy for Docker and PaaS: `PORT=4432 ./OwlShack`.
An address that cannot be bound is fatal — the process exits rather than run on
with no web UI, so a service manager can restart it until the address exists.

`TZ` matters wherever a time is rendered without an explicit zone, which in
practice means bot templates and log lines. **A container has no local zone, so
it runs in UTC until you set `TZ`.** A mistyped zone is worth care: the runtime
falls back to UTC silently rather than refusing to start, so `TZ=Pacific/Aukland`
looks exactly like choosing UTC on purpose.

`SIGHUP` reloads config from the database without a restart. Reloads are
diff-based: only companions whose config actually changed are restarted, so
the rest keep their sessions. A radio change reconnects the modem; changing the
listen address needs a process restart.

```bash
kill -SIGHUP $(pgrep OwlShack)
```

## Security

There is **no authentication** on the REST API or the UI. Anyone who can reach
the port can reconfigure your radio, post as your companions, and drive any
repeater you have logged into. Bind it to localhost, or put it behind a reverse
proxy that authenticates.

## Docs

| Doc | Covers |
|---|---|
| [firmware-protocol.md](./docs/firmware-protocol.md) | The wire: CLI replies, request types, path routing, DM/room plaintexts, sensors |
| [config-and-storage.md](./docs/config-and-storage.md) | Config tables, `/api/config/*`, backup/restore, REST endpoints |
| [frontend.md](./docs/frontend.md) | Styling system, page patterns, mobile, PWA |
| [mqtt-and-radio.md](./docs/mqtt-and-radio.md) | MQTT wire schema, duty cycle, path hash size |
| [CHANGELOG.md](./CHANGELOG.md) | Release history |

## Licence

See [LICENSE](LICENSE).

Region boundaries for CAP bots are from [Natural Earth](https://www.naturalearthdata.com/), which is public domain.
