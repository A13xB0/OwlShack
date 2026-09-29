# Changelog

Notable changes per release. Dates are the tag date; unreleased work sits at the
top until tagged.

## Unreleased

Schema `user_version` 20: adds the sensor tables, who may read each companion's telemetry, where a
CAP bot's alerts must be, which map tiles to use, and each contact's bytes per hop.

### Added

- **Bytes per hop for each contact.** A contact is added at the size it floods its adverts at, and
  everything sent to it by flood or direct goes out at that size: requests, DMs, ACKs and
  replies. A learned path keeps the size it was learned at. Before, requests to a repeater and DMs
  always flooded at 1 byte per hop. A saved path at another size is dropped on upgrade, so the next
  send floods and learns one at the right size.
- **One Path window for every contact.** Repeaters, rooms, sensors, chats and the contact page share
  it. It shows the route in use with each hop named, and sets Flood, Direct or a path at a chosen
  bytes per hop. Pick the hops from the repeater list, as on the Trace page, or type them as hex.
  A hop whose hash more than one repeater shares is marked, here and on the Trace page.
- **Sensors page.** Reads I2C sensors on the Pi every 5 seconds: SHTC3, LPS22HB, BME680, ENS210,
  ADS1115 or SGM58031 ADC inputs, and a PiSugar UPS. Each card shows whether it is healthy,
  calibrating, waiting, stale or failing.
- **Scanning for sensors.** A scan only reads, so it never disturbs the bus, and lists what could
  be at each address that answers. An SHTC3 sleeps until woken, so add it from the list.
- **Derived sensors.** Work a value out from other sensors, such as a dew point or an ADC divider
  in volts.
- **Web sensors.** Fetch a URL on a schedule and read numbers from the reply, such as a weather
  report's temperature and wind. Headers and logins are supported, and Test tries it before you save.
- **Air quality from the BME680.** An air-quality index and CO2 and VOC estimates after a 5-minute
  run-in. It calibrates to your air over days and remembers it across restarts.
- **The radio board as a sensor.** Add the battery voltage and MCU temperature of the KISS or
  openHop board your radio runs on, like any other sensor. It reads what OwlShack already asks the
  board for, so it adds nothing to the link.
- **Sensors over the mesh.** Choose which readings the repeater and each companion send when
  another node asks.
- **CAP alerts for one place.** A CAP bot can keep to alerts near a point, picked on a map or
  copied from the companion's position, or to alerts over regions you click on the map, such as
  the Auckland region. Regions cover every country. Test shows whether each alert is inside or
  outside. A feed whose alerts carry no map shape cannot be filtered this way, but a match pattern
  on the new `geocode` field can use the publisher's own area codes instead, such as
  `geocode:(?m)^UGC=TX` for US weather alerts in Texas.
- **Test an RSS or CAP bot before it fires.** Test in the bot editor fetches the live feed and lists
  its newest items. Pick one to see the exact message the bot would send, updated as you edit the
  template, with whether your match patterns would let it through and where a channel would cut it
  short. Nothing is sent.
- **A copy of the database before each upgrade.** When a new version changes the database, the
  old one is kept as `meshcore.db.pre-v<old>-to-v<new>`, so going back is stopping OwlShack and
  moving it back.
- **Who may read a companion's telemetry.** Battery, position and sensors each allow no one, chosen
  contacts or every contact. All three start at no one.
- **Satellite and topo views on the Map page.** Switch between the map, Esri satellite imagery and
  OpenTopoMap at the top right of the map. Your browser remembers the choice.

### Changed

- **Maps use OpenStreetMap by default.** Settings has a new Map tiles choice: OpenStreetMap, which
  needs no key, or CARTO. If you had saved a CARTO key you stay on CARTO. OpenStreetMap has no
  dark style, so in dark mode its colours are inverted, or with Dark mode map style set to
  Simplified, remapped to a plainer dark map with navy water.
- **Repeater telemetry follows the firmware.** Battery and temperature always go out, and a guest
  gets only those.
- **Fewer writes to the SD card.** The database syncs to disk in batches instead of after every
  packet. A power cut can lose the last few seconds of packets and readings, but a setting you
  saved is on disk before the page says it is saved.
- **A broker being down no longer marks the whole node degraded.** `/api/health`'s `status` now
  covers only what the node needs to work: the radio and a running node. MQTT has its own
  `mqtt.status`: `ok`, `degraded` when an enabled broker is not connected, or `off`. A monitor
  that alerted on MQTT through `status` should watch `mqtt.status` instead.

### Fixed

- **A repeater's path shows the route in use.** After a restart the repeater page and the chat's
  Path dialog read Flood while requests still went direct, until you reset the path. The saved
  route is now loaded at start, so what you see is what is sent.
- **A new contact starts with no route.** Adding a contact used the reversed path of the advert
  we heard as its route. It now floods until the contact answers with a path, as the firmware
  does.
- **A direct contact shows as Direct.** The contact page showed a direct neighbour as Flood.
- **Set path only takes a path that fits.** A path that was not whole hops, or used more than 3
  bytes per hop, was sent with a length that did not match. Those are now refused with a reason.
- **Resetting a path always sticks.** A reset or a set path could be lost when the database was
  busy, and the old route came back after a restart.
- **Meteoalarm CAP feeds work.** Every alert from a Meteoalarm feed failed to load, because the
  bot fetched the feed entry's web page rather than its alert document.
- **A CAP alert is sent once.** A feed that posts one alert under many entries, as Meteoalarm does
  once per area, sent it once per entry, up to five times a poll, and could push other alerts out.
- **A shared location on the map can be cleared.** Opening coordinates from a message on the map
  now shows a pin chip above the map, like the one for a packet path. Clicking it removes the pin
  and zooms back out to your peers.
- **A radio board that stops answering is caught.** Before, a board that hung while its USB link
  stayed up read as `ok` in `/api/health`. It now shows as `degraded` after two minutes without an
  answer, even when reconnecting did not fix it, and a board that was already hung when OwlShack
  started is caught the same way. An openHop board on a serial link, which nothing checked before,
  is now checked and reconnected the same way too. The automatic reconnect for a hung board also
  never started on a node with no MQTT and no repeater, and now it does.
- **A stuck transmitter is caught, and no longer fills the packet log.** When the board's
  transmit-done reply went missing, every later send was held and retried five times a second.
  Each retry was logged as a new sent packet (one was logged 196 times in 46 seconds) and counted
  as a send in `/api/health`. Each packet is now logged once. `/api/health` reports
  `txFailedInARow` and `txFailingSecs`, and shows `degraded` after two minutes of failed sends.
- **A companion that fails to start is no longer reported as a radio fault.** `/api/health` now
  says the companion or repeater failed to start, instead of `radio: modem not connected`.
- **Readings sent over the mesh are rounded, not cut short.** A battery at 4.1 V went out as
  4.09 V, and every reading sent over the mesh leaned low the same way.
- **A full disk shows up.** `/api/health` reports `diskFreeBytes` and shows `degraded` below
  32 MiB. Before, every write failed with nothing in health to say so.
- **Warnings and green status text were hard to read in light mode.** They now meet the 4.5:1
  contrast guideline.
- **Companion ACKs flooded with one-byte path hashes.** They now use the companion's own setting.
- **Saving one contact setting could clear another.** Each save now changes only what it names, and
  a bad value is refused with the reason.
- **The sidebar's system status was always "nominal".** It now reads `/api/health`: nominal,
  degraded with the reasons when you tap it, or offline when the page cannot reach OwlShack.
- **The database's log file never shrank.** After a busy spell, such as a restore, it stayed at its
  largest size. It now drops back to 4 MB, and `/api/health` reports its size as `walBytes`.

## v1.4.2 - 2026-09-21

Baseline `v1.4.1`, schema `user_version` 16, unchanged.

### Added

- **The RAK6421 WisBlock Pi HAT with a RAK13300 now works, in either IO slot.** Both are in the
  board list and both have been run on the hardware here: they receive from a live mesh, and a
  transmit from each was picked up and re-flooded by two neighbouring repeaters. Pick your slot in
  Settings and the SPI port is filled in for you, since slot 1 and slot 2 sit on different
  chip-selects. If the stored connection names a different one, the log now says so by name rather
  than leaving you to read "wrong wiring" and go looking at the hat.

### Changed

- **Every board in the list can actually be driven.** Boards that were listed but refused at
  startup are gone, and the BQ Voyage Station G3 has been fixed and now works. That is 15 boards,
  4 of them verified on hardware here. A preset that nobody can complete or test is worse than no
  preset, because a listed board reads as a supported one.

### Fixed

- **The chat composer on a phone.** The buttons and the text box were three different heights.
  They now match, the icons are larger, the send button is the icon alone, and the emoji button
  has moved inside the text box - about 20% more room to type. The character count no longer sits
  under the box taking up a line; it appears next to send once your message reaches three lines,
  or when you are close to the limit.
- **Reply and More could be tapped while invisible.** Tapping a message near those buttons opened
  the menu with nothing on screen to explain it.
- **The More menu was cut off at the bottom of the screen.** On a message low down, the last item
  sat behind the navigation bar. It now opens upward when there is no room below.
- **The threads page scrolled by a pixel.** Enough for a stray scrollbar over the whole page.

### Upgrading

Nothing to do. If your radio hat was one of the entries that has been removed, Settings will no
longer show it selected - those boards were refused at startup before, so nothing that was working
has stopped.

## v1.4.1 — 2026-09-18

Frontend only, the same day v1.4.0 shipped. The fix worth taking: if you run two companions
subscribed to the same channel, switching between them showed the other one's messages. The chat
page is a single route, so changing companion never remounted it, and the message cache was keyed
by channel name with no companion in the key — so opening a shared channel as the second companion
found the first one's rows under that key, rendered them, then backfilled from the first one's
highest id and merged the second's on top. You saw the same text twice, and the first companion's
sent copy rendered as Sent in the second's thread.

The rest is paths you can read. The peer sheet names the repeaters in an advert path instead of
printing five hex bytes, and any path — a peer's advert route, or a packet's — plots on the map as
a dashed line through each node.

Everything here was verified in a browser against the bench's 374 peers and 10,000 packets, on a
copy of the database driven by a loopback modem. Nothing in this release has been exercised on air,
and nothing in it touches the radio: no Go changed, and the schema does not move. There are still
no frontend tests in the repo, so none of this is guarded by CI.

Baseline `v1.4.0` · schema `user_version` 16, unchanged · frontend only

### Added

- **The peer sheet names the hops in an advert path.** It showed `F9 88 A3 8C E8`; it now reads
  `Teletronics Rangitumau → WR Aorangi → Remutaka → … → you`. This reuses the resolution the
  Packets page has had since v1.4.0-rc.2, so it keeps the same honesty: a hash matching no repeater
  stays hex, and one matching several is underlined with the count of other candidates and opens
  them on click. A hash is a pubkey prefix, not an identity — on a 338-peer mesh 92 of 211
  one-byte hashes match more than one node.
- **Plot a path on the map.** An **on map** link on the peer sheet, on the Peers list's hop count,
  and on a packet's path draws that path as a dashed line through each node, hides every peer not
  on it, and frames the map to it. A chip in the filter bar names what is shown and restores the
  normal map. Two rules keep the drawing as honest as the text it mirrors: a hop that cannot be
  placed — an unresolvable hash, or a named repeater with no position — **ends the line rather than
  bridging its neighbours**, because a leg drawn across an unknown hop asserts a link that was
  never reported and looks identical to one that was; and a `DIRECT` route carries only the road
  ahead, so neither end is us and no `you` is drawn. Nothing plots by selecting a marker: that
  opens the details panel and leaves the map alone.

### Fixed

- **Two companions on one channel showed duplicate and mis-sided messages.** Described above. The
  page is now keyed on the companion in the URL, which also stops the thread list, the composer,
  the room session and the read high-water mark carrying across a switch. Reproduced before and
  after on a copy of the database: on the old build the second companion's Public showed all 100 of
  the first's messages plus three duplicated texts, one of them on the wrong side.
- **Every page mount flashed the connection pill red.** `connected: false` meant both "the
  handshake is still in flight" and "the link dropped", so arriving on any page painted a red
  `offline` for the duration of the WebSocket handshake — one frame locally, the round trip on a
  real deployment, and the chat fix above made it fire on a routine interaction. The pill has a
  third state, and `pending` clears on the first open **or** the first close, so a server that is
  genuinely unreachable still reaches `offline` instead of sitting neutral for ever. Both failure
  states were provoked: killing the server goes red, and mounting a socket with no server reaches
  red within 400 ms. The Repeater page's pill is unchanged — there, "off" genuinely means stopped.
- **A short thread floated at the top of a tall message pane**, leaving a gap between it and the
  composer. You only ever saw it once the bug above stopped filling short threads with another
  companion's backlog.
- **The thread list showed the raw mention syntax**, `@[MWH1]`, where the message itself renders a
  styled `@MWH1`.
- **A hop chain's arrows and its trailing `you` were nearly invisible**, dimmed far below the names
  they separate.

### Internal

- Ten unused imports removed, and `noUnusedLocals` / `noUnusedParameters` turned on so the next one
  fails the build rather than accumulating. Both flags were `false`, which is how these built up.
  A branch cut before this that carries an unused import will now fail to build until it is dropped.

## v1.4.0 — 2026-09-18

Six candidates' worth of work since v1.3.1, which is the version most people are upgrading from.
The headline is transports and bots: a third radio backend — **openHop Modem firmware**, over the
network or USB — alongside MeshCore KISS and a bare SX126x on SPI; **RSS/Atom and CAP feed
triggers**; **DM triggers** with a policy deciding who may talk to a companion; **failover replies**
so a backup bot does not talk over the primary; **Debian packages** with a systemd unit for people
who do not want Docker; and **`GET /api/health`** for external monitoring. Underneath: repeater
admin that recovers from a stale route instead of going unreachable for good, ack waits computed
from airtime rather than one flat timeout, and two MQTT status fields corrected to carry what the
firmware says they carry.

The two gaps named in every candidate since rc.2 are now one. A feed trigger has transmitted from
real hardware: on an openHop modem over USB, with an SX1262 on SPI listening, the first poll primed
against an existing backlog without firing, a new item went out and was heard by the other node,
and a burst of eight was clamped to five with the other three never reaching the air — the two
behaviours that are invisible when they work. **The room keep-alive fix from rc.1 has still never
run against a live room server**, so that one behaviour ships unverified on air.

On packaging, rc.5's "no ARM build has executed anywhere" no longer holds: the arm64 binary has run
here, on a Pi 4, for the radio work above. **The armhf package is still the untested one** — it is
built for ARMv6 and the guard that proves it now reads the recorded `GOARM`, but no ARMv6 machine
has executed it. Treat the first Pi Zero install as the test.

Baseline `v1.3.1` · schema `user_version` 11 → 16 · `meshcore-go` v1.5.0 · Go 1.26+

### Upgrading

- **Coming from v1.3.0 or earlier? You are also taking v1.3.1, which is a security fix.** A
  repeater created through OwlShack had no admin password, and a blank one compared equal to the
  blank a login carries, so any node in radio range could log in as admin — change settings, lock
  the operator out, read the access list, run CLI commands. The migration sets a blank password to
  `password`, the firmware's own default, which closes the hole without inventing a secret you
  could not guess: **change it** on the Repeater page or with `password <new>`. Creating a repeater
  now requires an admin password, so `POST /api/config/repeater` rejects a blank one. Your schema
  range is `user_version` 10 → 16, not 11 → 16. Full detail under v1.3.1 below.
- **The database migrates itself** on first start, 11 through 16 from v1.3.1 (10 through 16 from
  v1.3.0). No manual SQL, and no step needs a downgrade path because none rewrites existing rows
  except the repeater-password one above.
- **`recv_errors` changes meaning on MQTT and `packet_parse_errors` is new.** A dashboard keyed on
  `recv_errors` will see OwlShack nodes drop, usually a long way, because a radio-driver failure is
  far rarer than a malformed packet. Nothing errors and no key disappears; read `client_version` to
  tell the versions apart. Full detail under rc.1.
- **Companion URLs are now `/companions/<id>-<slug>`.** Old bookmarks pointing at a bare name will
  not resolve. The change is what stops a URL breaking when a companion is renamed.
- **A web listen address that cannot be bound is now fatal.** A node whose port was already taken
  used to keep running with a live radio and no web UI, reporting `active` the whole time. It now
  exits, which is louder and is the point — but a host that got away with a clashing port will now
  fail to start.
- **The Debian package listens on 8860**, not 8080. The binary and the Docker image are unchanged;
  this applies only to installs from the `.deb`.

### Added

Rolled up; each candidate's section below carries the detail.

- **openHop Modem as a third radio backend** (rc.6, plus serial verified here) — `openhop://host:port`
  or `openhop:///dev/tty…`. The firmware owns the radio and does its own channel-activity detection.
  Its access token is a `modemToken` setting treated as a password, never returned by a config read.
- **Failover replies for group bots** (new since rc.6, contributed by @Darkfish in #52) — a bot waits
  a configurable 1–3600 seconds and stays quiet if another sender answers the request first.
- **RSS/Atom and CAP feed triggers** (rc.2) — poll a feed and broadcast new items to channels, to
  contacts, or both. First poll primes rather than replaying a backlog; one poll sends at most five.
- **DM triggers and `dmPolicy`** (rc.1) — bots that answer direct messages, and a per-companion
  policy for who is allowed to send them.
- **Debian packages and a one-line installer** (rc.5) — amd64, arm64, armhf and i386, with a systemd
  unit, a dedicated user and `/etc/default/owlshack`.
- **`GET /api/health`** (rc.2) — a monitoring endpoint for Uptime Kuma and similar.

### Fixed

- **The installer told you to read the journal without `sudo`.** `journalctl -u owlshack` prints
  only a permissions notice for a user outside `adm`/`systemd-journal`, and an unprivileged
  `systemctl status` drops the recent log lines from its output without saying so, which reads as a
  service that is running and quiet. The hint now says `sudo journalctl -u owlshack -f`.

Everything else fixed since v1.3.1 is itemised under the candidates below: MQTT counter semantics
and the KISS firmware counters that were never polled (rc.1), repeater admin against stale routes
and airtime-based ack waits (rc.1), a dozen console defects (rc.2–rc.4), and the three the openHop
work surfaced in code that was already wrong (rc.6).

## v1.4.0-rc.6 — 2026-09-17

rc.5 plus a third radio backend: **openHop Modem firmware**, over the network or USB, alongside
MeshCore KISS and a bare SX126x on SPI. It has run against real hardware here — handshake, receive,
transmit, reconnect and listen-before-talk — which is more than the armhf package can say.

Adding a transport that reconnects itself surfaced three things that were already wrong and are
fixed here: `/api/health` called the radio connected whenever a modem object existed, the stored
`connectionType` could contradict the connection string it is supposed to describe, and the
settings round-trip test only ever exercised an INSERT, so a column dropped from the upsert's
`ON CONFLICT` list was invisible — for any column, not just the new one.

Schema moves to `user_version` 15 for one new column. The two things keeping this off v1.4.0 are
unchanged: the room keep-alive has never run against a live room, and no feed trigger has yet
transmitted from real hardware.

Baseline `v1.3.1` · schema `user_version` 15

### Added

- **openHop Modem support.** Set the connection to `openhop://host:port` (or `openhop:///dev/tty…`)
  and pick "openHop Modem" as the radio backend. The firmware owns the radio and does its own
  channel-activity detection, so OwlShack only frames packets; the driver reconnects on its own and
  re-pushes the radio configuration afterwards, because the modem may have rebooted. The preamble is
  derived from the spreading factor rather than taken from openHop's own default, which no MeshCore
  node would hear.
  Verified against an openHop Modem on WiFi: handshake and every query, receive with correct SNR and
  RSSI, a self-advert on air, peers discovered, a reconfigure mid-run, and a dropped link recovering
  through the full backoff schedule with re-authentication. Listen-before-talk was exercised by
  lowering the modem's CAD threshold until a quiet channel reads busy — both the host retry loop and
  the modem's own `ERR_CHANNEL_BUSY` refusal behave. **openHop over serial is untested**: no such
  hardware here. **Correction (v1.4.0):** serial has since been verified on a Seeed XIAO Wio SX1262 —
  handshake, radio configuration, receive with SNR and RSSI, and transmit. Serial clients are not
  asked for a token, so `modemToken` stays a network-only concern.
- **`modemToken` setting.** The openHop access token, stored in its own column and treated as a
  password: reads return `modemTokenSet` and never the value, a write omits it to keep the stored
  one, and the UI field is masked and write-only. It is deliberately not part of the connection
  string, which config reads return in full.

### Fixed

- **`/api/health` reported a radio that was not there as connected.** `connected` meant "a modem
  object exists", which tracks the link only for transports OwlShack tears down and rebuilds. A
  self-reconnecting modem outlives its link, so a radio that had been unreachable for minutes still
  read `status: ok, problems: []`. Health now asks the modem when it can answer one.
- **The stored `connectionType` could disagree with the connection string.** Only the connection
  string decides which driver loads, so the label is now derived from it on every write instead of
  being taken from the caller — a config naming one backend and pointing at another no longer
  persists the contradiction.
- **The settings round-trip test could not see a dropped column.** It inserted once, so it only
  covered the INSERT arm of the upsert; removing a column from the `ON CONFLICT` list — the arm
  every save after the first one takes — left it green. It now writes twice.
- **The armhf guard in `build-deb.sh` was inert.** See the correction under rc.5: it disassembled a
  stripped binary, got nothing, and passed. It now reads the `GOARM` the toolchain records, which
  survives stripping, and fails closed when it cannot read one.

## v1.4.0-rc.5 — 2026-09-16

rc.4 plus native packaging: a `.deb` that installs OwlShack as a systemd service, for everyone who
does not want Docker. It brought two app changes with it — a listen address that cannot be bound
is now fatal instead of leaving a node that reports `active` with no web UI, and `LISTEN_DEFAULT`
seeds the stored address so a package can choose the port a fresh install starts on without taking
that field away from the Settings page. Same schema as rc.4, same binaries otherwise.

The packages install and purge cleanly on Debian 12 and 13 and on Ubuntu 22.04 and 24.04, but no
ARM build has executed anywhere: the armhf package is built for ARMv6 and checked by disassembly,
never run on a Pi. Treat the first Pi Zero install as the test. The two things keeping this off
v1.4.0 are unchanged: the room keep-alive has never run against a live room, and no feed trigger
has yet transmitted from real hardware.

Baseline `v1.3.1` · schema `user_version` 14

### Added

- **Debian packages and a one-line installer.** `.deb` for amd64, arm64, armhf and i386, built
  from the release binaries and attached to every tag. Installs `/usr/bin/owlshack`, a systemd
  unit running as the `owlshack` user (in `dialout`, plus `spi`/`gpio` where present) with its
  database in `/var/lib/owlshack`, and `/etc/default/owlshack` for `PORT`/`HOST`/`TZ`/flags. The
  packaged service listens on **8860**, not the crowded 8080; the binary and the Docker image are
  unchanged.
  `packaging/install.sh` picks the package for the host and installs it; `packaging/deb/build-deb.sh`
  builds one from a checkout. `apt remove` keeps the database, `apt purge` deletes it.
- **`LISTEN_DEFAULT` seeds the listen address on first run.** It writes the address into the
  database when none is configured and then leaves it alone, so the Settings page still owns it —
  which is how the Debian package can default to 8860 without the UI's port field going dead.
  `HOST` and `PORT` are unchanged and still pin the address on every start.
- **The armhf package is ARMv6, so a Pi Zero can run it.** Raspberry Pi OS reports `armhf` on an
  ARMv6 Pi as well as an ARMv7 one, and a GOARM=7 build installs there cleanly and then dies with
  SIGILL. `build-deb.sh` refuses an armhf binary that is not ARMv6. **Correction (rc.6):** the
  check shipped in rc.5 disassembled the binary, which cannot work on a release build — those are
  stripped, `go tool objdump` fails, and the instruction count came back zero, so the guard passed
  no matter what. The rc.5 armhf package is genuinely ARMv6, verified separately; the guard just
  was not the reason. It reads the recorded `GOARM` from rc.6 on.

### Fixed

- **A web listen address that cannot be bound is fatal.** The listener is bound before the
  server goroutine starts, and a later `Serve` error ends the run too. Until now a failed bind
  logged one line and the process carried on: the node kept running, `systemctl` reported
  `active`, and there was no web UI. Under systemd the unit now restarts every 5s until the
  address exists, which also covers an address that appears late in boot.

## v1.4.0-rc.4 — 2026-09-15

rc.3 plus one fix: the header read a companion's URL ref where its name belongs, a regression from
addressing companions by `<id>-<slug>`. Nothing else changed — same schema, same binaries
otherwise, and the two things still keeping this off v1.4.0 are unchanged: the room keep-alive has
never run against a live room, and no feed trigger has yet transmitted from real hardware.

Baseline `v1.3.1` · schema `user_version` 14

### Fixed

- **The header showed a companion's URL ref instead of its name.** Addressing a companion by
  `<id>-<slug>` changed what sits in the path, and the header label renders that segment directly —
  so it read `companions / 2-wes` where it used to read the companion's name. It now resolves the
  segment the way every other view does, and falls back to the raw segment until the roster loads.

## v1.4.0-rc.3 — 2026-09-15

A third candidate, and the first carrying schema 14. Since rc.2 the work has been in the console
rather than on the wire: a companion's URL survives a rename, a thread follows new messages only
when the reader is at the end of it, a tab that has been asleep catches up instead of needing a
reload, and a bot's path hash size offers the same 1-3 bytes as the rest of the app. A security
pass over the first of those found route confusion in the new path rewrite, fixed before release.

Unchanged from rc.2, and still the reason this is not v1.4.0: the room keep-alive has never run
against a live room, and no feed trigger has yet transmitted from real hardware.

Baseline `v1.3.1` · schema `user_version` 14

### Changed

- **Companion URLs no longer break when a companion is renamed.** A companion is now addressed by
  `/companions/<id>-<slug>` — the id is the authority and the slug is only there to keep the link
  readable, so a stale slug still resolves. Renaming previously left every open tab and bookmark on
  a dead URL, because the name was both the display label and the key in all 49 runtime API routes;
  `/companions/%F0%9F%90%B6Akl/contacts` returned `companion not found` the instant the rename
  landed, and reloading could not help because the stale name was in the address bar. Plain-name
  URLs still resolve, so existing bookmarks and installed PWAs keep working.
- `GET /api/companions` now includes `id`, and the `messages` WebSocket payload now carries
  `companionId` alongside `companion`. The live-message filter matches on the id, which is known
  from the URL on the first render and cannot change under a rename; it falls back to the name when
  a payload has no id rather than dropping the message.
- **A bot's path hash size no longer offers "mirror incoming" where nothing comes in.** Mirroring
  answers with the size the message arrived on, which only means something for a group or DM
  trigger; cron, RSS and CAP start the conversation themselves and silently fell back to the
  companion's size, leaving the config saying one thing and the radio doing another. The option is
  now offered only where it applies, a stored one is cleared when the type changes, and the config
  API rejects it outright so an imported file cannot set it either. The "default" option said
  "default (1)" whichever size the companion actually used; it now names the companion as the
  source and a hint says what that resolves to.
- **Path hash size is 1-3 bytes everywhere.** The bot form alone offered 4, which Settings, the
  companion editor and the repeater all rejected — so a bot could be set to a size the companion it
  transmits through could not be configured for. Bots now use the same list as every other field,
  and a stored 4 is clamped to 3 by migration: validation covers the whole assembled config on
  every save, so one left in place would have blocked unrelated config changes.

### Fixed

- **A companion path could be steered onto a route it never addressed.** The new ref rewrite split
  and rebuilt the *decoded* path, so an encoded slash inside the segment passed for a separator:
  `/api/companions/1-a%2Frepeaters%2FDEAD/cli` addresses `{name}/cli` and should 404, but reached
  the repeater CLI handler. The substituted name had the same flaw in reverse — nothing constrains
  a companion name, so one containing `/` spread across segments and both hijacked routes and made
  its own companion unreachable. Segments are now split and rebuilt on the escaped path. Found
  before release; no shipped version is affected.
- **A tab that has been asleep catches up instead of needing a reload.** Messages, the thread list,
  peers, packets and the map are fetched once and then updated only by the socket, so anything that
  happened while the tab was away was simply absent — and an empty stretch of chat is
  indistinguishable from a quiet mesh. Waking the tab, coming back online and re-opening a dropped
  socket now each re-fetch the view behind the user's back: no spinner, no blanked list, and what
  is on screen stays if the fetch fails. The chat page already backfilled its open thread when the
  socket reconnected, which left the two cases that actually bite: the thread list, and a socket
  that stayed open through the sleep while the hub dropped broadcasts it could not queue. The
  dashboard, traces, monitoring, discover and repeater pages are not yet covered.
- **A reconnect can no longer leave two live sockets.** Waking the tab replaces a socket that has
  already closed, but the old socket's close event still arrived afterwards and cleared the
  reference to its replacement, so the retry timer opened a second one. The close of a socket that
  has been replaced is now ignored.
- **A new message no longer jumps the view unless the reader is at the end of the thread.** Reading
  back through history, an arriving message leaves the scroll where it is and a jump-to-latest pill
  counts what has landed; clicking it, or scrolling back to the end, clears it. Posting still takes
  the author to their own message. This is what fixes the reported symptom — traffic anywhere, on
  any thread, can no longer move a reader who is not following along.
- **An update meant for one thread no longer touches another's messages.** `repeatCount` and
  delivery-status updates were applied with `setMessages(prev => prev.map(...))` without checking
  which thread they named, and `map` hands back a new array even when nothing matched — which the
  auto-scroll read as new traffic. Updates now apply only to the thread they name, and one that
  changes nothing keeps the existing array. This was the suspected trigger for the report above;
  it could not be confirmed on a local mesh, so the entry above is what the fix rests on.
- **The sidebar kept showing a companion's old name until the page was reloaded.** Config has no
  WebSocket topic, and the shell only refetched the roster when navigating to or from
  `/companions` — which a rename from the dialog on that page never does. Every companion mutation
  now notifies the cached rosters, so the sidebar, the shared companion list and the links built
  from them update in place.

## v1.4.0-rc.2 — 2026-09-14

A second candidate. Since rc.1: the three defects found by running rc.1 on a real server are
fixed, two new trigger types poll RSS/Atom and CAP feeds, and there is a health endpoint for
external monitoring. Still unverified on air, and the reason this is not yet v1.4.0: the room
keep-alive has never run against a live room, and no feed trigger has yet transmitted from real
hardware — its first-poll priming and five-per-poll clamp are the two behaviours that are invisible
when they work.

Baseline `v1.3.1` · schema `user_version` 13

### Fixed

- **A second companion on the same channel stole the sender's repeats.** A sent channel message
  showed "Heard 1x" while the packets page showed the same packet five times. The echo tracker
  keyed pending packets on the packet hash alone and was shared by every companion, so a second
  companion carrying the same channel decoded the sender's own packet as a received message and
  overwrote the sender's entry; the sender kept whichever echo landed before the overwrite, which
  was one. Pending entries are now scoped to the companion waiting on them. It reproduces only
  with more than one companion, which is why a single-companion bench could not find it, and is
  confirmed fixed on the server that reported it.
- **The peer sheet showed a contact as unattached until reload.** The membership fetch depended on
  the peer's key and the companion names, and adding a peer to a companion changed neither, so the
  slideout kept its first answer for as long as it stayed open. The add now bumps a version the
  fetch depends on.
- **Messages read while a thread was open came back unread.** The read-position report bailed when
  the unread count was zero and zeroed it locally the moment the thread opened, so it ran exactly
  once per visit: anything arriving while the thread was on screen was read by a person and never
  reported, and came back unread on the next visit. It now tracks the highest message id reported
  per conversation and posts whenever the thread holds a higher one. The entry case is verified
  live; the arriving-while-open case is not, because no message decoded to a held channel during
  the window.

### Added

- **`GET /api/health`, a monitoring endpoint for Uptime Kuma and similar.** Reports the radio, the
  database write queue and each MQTT broker as JSON, and **always answers 200
  while the process is alive** — a monitor that cannot reach OwlShack already fails the request, so
  the status code is not spent on a second opinion and the operator decides what is worth alerting
  on. `problems` is a possibly-empty array of binary faults and `status` is `ok` exactly when it is
  empty; a Json Query monitor on `$count(problems)` or `radio.connected` covers most cases.

  Radio health keeps three ages apart, because conflating them is how a quiet mesh gets mistaken
  for a dead board: `lastReplySecs` is the board answering a status query (the liveness probe's own
  signal), `lastRxSecs` is mesh traffic, `lastTxSecs` is our own sends. Each is `null` rather than
  `0` where there is nothing to measure from. Nothing here thresholds mesh silence — the right
  value differs by orders of magnitude between a bench node and a city repeater.

  `database.writesDroppedLastSecs` exposes the `WriteAsync` overflow for the first time. The
  counter was already incremented and logged, but nothing reported it, and a dropped write is
  invisible everywhere else: the row never appears and every surface downstream still looks
  healthy. The **age** is the field to alert on — `writesDropped` only ever rises, so it cannot
  distinguish failing now from a bad minute last week, and a past loss is deliberately kept out of
  `problems` so one transient overflow cannot pin the endpoint to `degraded` until restart.

  The response is written on the assumption it may be reachable from the internet. The running
  nodes are not listed at all: neither a name nor a peer count can report a fault — peers are
  hydrated from SQLite and only grow, so the count reads the same with the antenna unplugged, and
  a node that fails to start exits the process rather than quietly leaving a list — while both
  would tie a public hostname to a mesh identity that public maps resolve to coordinates. "No node
  is running at all" is a `problems` entry. There is no position either, and a broker reports
  `connectedSecs` / `lastErrorSecs` rather than the transport error, which would name a private
  broker's host and port. Both are ages rather than flags: `connected` is a sample, so a broker
  reconnecting every thirty seconds reads `true` on nearly every scrape and only a connection age
  resetting to near zero shows the flapping, and the observer never clears its last error on
  reconnect, so a boolean built from it would stay true until restart. This applies to
  `/api/health` only: the rest of the API has no authentication and should not be exposed
  alongside it.

  Board readings come from the new `StatsProvider.CachedStats`, which reads the last values the
  board volunteered instead of asking for fresh ones. `Stats` sends three hardware queries and then
  waits 500ms for the answers, so a monitor scraping on a schedule would have put that traffic on a
  half-duplex link every time it asked whether the radio was well. A stale reading still drops out
  on its own after `staleReadingAfter`, so nothing reports an old battery level as current.
  `GET /api/radio/status` still polls deliberately — it backs a diagnostics page someone is
  watching — which is where its ~500ms goes.

- **RSS/Atom and CAP triggers.** Two new bot types poll a feed on a schedule and broadcast each
  new item — to the companion's channels, as a DM to a list of contacts, or both. `rss` templates against the feed entry
  (`{{.Title}}`, `{{.Link}}`, `{{.Description}}`, `{{.Published}}`); `cap` fetches the alert
  document each entry links to and templates against the alert itself
  (`{{.Severity}}`, `{{.Urgency}}`, `{{.Event}}`, `{{.Headline}}`, `{{.Areas}}`, `{{.Expires}}`),
  with the whole decoded tree on `{{.Alert}}` and the parsed feed entry on `{{.Item}}`.
  Match patterns filter which items fire and are scoped to a named field (`severity:^Severe$`):
  patterns on one field are alternatives, different fields must all match.
  Feeds are parsed by `gofeed`, which handles RSS 2.0, Atom and JSON Feed. The poll interval is a
  number and a unit rather than a cron expression, floored at one minute so a bot cannot hammer a
  publisher.

  Two behaviours matter on a radio: the first poll after a start only *records* what is already
  published rather than firing on it, so a restart cannot replay a backlog onto the mesh; and one
  poll sends at most five items, so a publisher that reissues its whole feed cannot queue dozens
  of transmissions onto a duty-cycled radio.

  Schema `user_version` 13 adds `triggers.url`.

## v1.4.0-rc.1 — 2026-09-11

A release candidate, not a release: the mesh-facing work below was tested against real repeaters
and rooms on one bench, and wants a second node before it is called stable. Two of its defects
were found by ordinary use rather than by tests or review, and both are the kind only real traffic
reaches.

Corrects two fields on the MQTT status schema that carried a different measurement from the one
their name promises. The schema is shared: `meshcoretomqtt` forwards real firmware nodes to the
same brokers under the same topic, and it is the client LetsMesh recommends, so the names were
already defined by the firmware and OwlShack was the one publishing something else into them.

Baseline `v1.3.1` · schema `user_version` 12

### Fixed

- **Released binaries reported `Build: unknown`.** The release workflow stamped
  `buildinfo.Version` but not `buildinfo.Date`, which only `build.sh` set, so every published
  build answered the repeater's `ver` command without a date while a locally built one answered
  correctly. It now stamps the tagged commit's date, taken from the commit rather than the clock
  so all eleven matrix jobs agree and a rebuilt tag gives the same string.
- `--version` prints the build date alongside the version, in the same
  `<version> (Build: <date>)` shape the repeater's `ver` reply uses. Without it the missing stamp
  was only observable by querying the node over the air, which is why it went unnoticed.
- Build dates now use the firmware's own `%d-%b-%Y` (`10-Sep-2026`) rather than ISO, so a `ver`
  reply reads the same beside a real MeshCore node. `build.sh` previously stamped the UTC date,
  which in NZ meant a morning build claimed yesterday; it follows the firmware's local clock now.
- **The repeater access list showed a blank role for a guest.** The dropdown renders the matching
  option's label and there was no guest option, so the one role that cannot be granted was the one
  that displayed as nothing. The list now carries an unselectable item for whatever role the entry
  actually holds. That also keeps the popover usable: Radix positions it by aligning the selected
  item over the trigger, so a value with no item put the whole list off-screen at the viewport
  corner, where nothing could be clicked.
- **Granting access lists companions only.** A repeater, room server or sensor has no login
  client at all, since `ANON_REQ` is only ever sent from `BaseChatMesh` and none of them inherit
  it, so offering them was offering access that could never be used. On this bench that cut the
  list from 329 peers to 32. A peer whose type is unknown stays listed, and the manual pubkey
  field is unrestricted.
- Granting access now defaults to **Read only** rather than Read / Write, matching the official
  app. Note that on a repeater the two are the same access: the firmware tests only `isAdmin()`
  and a guest check, and nothing anywhere tests `PERM_ACL_READ_WRITE`. The distinction is real on
  a room server, where read/write is a member who may post.

### Added

- Chat bubbles show the path hash size next to the hop count, as `3 hops · 2B`. The field was
  already stored and served; only the render was missing. Shown only where there is a path, since
  a hash width describes nothing on a message heard direct.
- **DM triggers.** A trigger of type `dm` matches an incoming direct message and answers the
  sender. `TriggerConfig.Contacts` was persisted, served, backed up and documented since the
  first release with nothing reading it; it is now the sender filter, and an empty list answers
  everyone the DM policy allows. The Bots page picks senders from the known companions rather
  than taking typed text, so what is stored is a public key; a hand-written config may still use
  a peer name or a key prefix. Templates get the
  group variables minus `{{.Channel}}`, plus `{{.SenderPubKey}}`, since a DM reply is addressed
  by key rather than by name.
- **A companion accepts DMs from any peer it has heard, not only saved contacts.** Decryption
  tried the contact list alone, so a message from anyone else was unreadable and silently
  dropped. The firmware has no such limit: `BaseChatMesh` auto-adds every advert it hears into
  `contacts[]` and decrypts against that, which is the table OwlShack keeps as `discovered_peers`.
  A sender the policy accepts is filed as a contact on their first message, which is also what
  puts the thread in the conversation list.
- **`dmPolicy` on a companion decides who may DM it**: `contacts` (only saved contacts, which is
  what every install did before and stays the default), `allowlist` (only the public keys in
  `dmAllow`, full or a leading prefix), or `anyone`. A rejected DM is dropped before the ack, so
  the sender sees a failed send rather than silence.
- The peer picker behind the repeater ACL's Grant access is now shared with both DM surfaces
  (`PeerPicker`), so all three filter to companions, exclude what is already listed, and validate
  a manual key the same way.
- `formatPathBytes` takes an optional separator, so a template can render a path as `A1 > B2 > C3`
  rather than only the hardcoded `A1, B2, C3`. Omitted keeps the old output, and a second
  separator is an error rather than silently ignored, matching what `date` does with a zone.

### Upgrading

- **`recv_errors` changes meaning.** It now carries radio-driver receive failures, matching the
  firmware's own field (`driver.getPacketsRecvErrors()`, which `RadioLibWrapper::recvRaw`
  increments when `readData` fails after the interrupt). It was carrying packet parse failures.
  Both transports report it: the KISS firmware answers `HW_CMD_GET_STATS` with the same counter,
  which OwlShack now polls at status-publish time.
- **`packet_parse_errors` is new** and carries what `recv_errors` used to: bytes that arrived
  intact and did not decode as a MeshCore packet. The radio did its job; something else sent
  malformed bytes.
- **A dashboard keyed on `recv_errors` will see OwlShack nodes change**, usually downwards, since
  a radio-driver failure is far rarer than a malformed packet. Nothing errors and no key
  disappears, so the change is invisible on the wire. Read `client_version` to tell the versions
  apart.
- The MQTT change needs no migration of its own. `user_version` does go to 12, for the DM policy below.

### Fixed

- **`recv_errors` published a parse failure under a name the firmware had already defined** as a
  radio-driver counter. A consumer aggregating across firmware nodes and OwlShack nodes was
  summing two unrelated measurements, with no way to tell them apart.
- **`hw_decode_errors` published an SPI data-packet readout failure** on the SPI path.
  `PacketsRecvErrors` was mapped into it, but that field is a malformed KISS **SETHARDWARE** frame,
  which is the battery, MCU-temperature and noise-floor channel rather than a mesh packet. It is
  now KISS-only, publishing 0 on SPI like the other four KISS counters, and `PacketsRecvErrors`
  goes to `recv_errors` where it belongs.
- **A driver error incremented two counters.** On the SPI path the modem error handler fed both
  `driver_errors` and the parse count, so `driver_errors` was a strict subset of `recv_errors` and
  the same event was counted twice. `recv_errors` also meant different things on the two
  transports: parse failures on KISS, parse plus driver failures on SPI.
- **The KISS firmware's own packet counters were never polled.** `HW_CMD_GET_STATS` has always
  answered with rx, tx and `getPacketsRecvErrors()`, and `meshcore-go` has wrapped it as
  `FirmwareCounters` since v1.4.0. Not asking meant publishing a 0 that read as a radio hearing
  everything cleanly. Polled now, and still absent rather than 0 when a modem does not answer.
- `GET /api/radio/status` omits `hwDecodeErrors` on SPI rather than reporting a 0 it never
  measured, and gains `recvErrors` on both transports. On KISS it now also reports `packetsRecv`
  and `packetsSent` from the firmware's own totals, where both were absent. This is the REST surface only: the MQTT
  payload keeps publishing both keys on both transports, because omitting a key there is a
  coordinated change and dropping to 0 is not.

### Fixed — direct messages

- **A retry past attempt 3 could never be acked.** The attempt number is hidden after the text's
  NUL terminator (`BaseChatMesh.cpp:434-436`), so trimming trailing NULs left the suffix in the
  body and hashed the ack over the wrong bytes. The text now ends at the first NUL, as the
  firmware's `strlen` does.
- **Retransmissions no longer show as duplicate messages.** Collapsed on sender, timestamp and
  text. The text has to be in the key: a plain DM's timestamp is the sending app's clock at second
  resolution (`MyMesh.cpp:1088`), not `getCurrentTimeUnique`, so two different messages can share a
  second and keying without it acked the second and dropped it.
- **A DM's path and hop count are recorded**, as group messages already were, so View Paths no
  longer says Direct for a message that crossed repeaters. A direct-routed DM records its hops as
  absent rather than 0: direct routing consumes the path hop by hop (`Mesh.cpp:334-342`), so the
  count is genuinely unrecoverable at the destination.
- **Ack waits are computed from airtime** instead of a flat 5s, following the firmware
  (`MyMesh.cpp:851-858`). At the bench preset the firmware waits 7.9s flood and 9.5s for a 2-hop
  direct, so a 5s cap was reporting delivered messages as failed.
- A peer that teaches us a route is taught ours back, so it stops flooding its replies at us; a
  route is dropped after total delivery failure; and text is capped at the 158 bytes `MAX_TEXT_LEN`
  allows.

### Fixed — repeater and room admin

- **Commands to a distant node no longer time out while the reply is still in flight.** Every
  request waited a flat 10s regardless of distance or payload; the firmware sizes its own waits
  from airtime, and a full-size reply over 3 hops needs about 17s. Telemetry, the largest reply of
  any command, was the one that never arrived.
- **Admin traffic no longer floods after every restart.** The route was being read from the
  in-memory peer table, which starts empty, while the learned route sat unread in the database.
  Since firmware answers a flood request with a flood reply unconditionally, one unread route cost
  both directions.
- **A stale route no longer makes a node unreachable for good.** With routes now surviving a
  restart, a route gone stale failed every login identically and nothing ever cleared it. A login
  that times out on a learned route drops it so the next attempt rediscovers by flooding. Login
  only: a mid-session timeout is more likely ordinary loss, and dropping a good route over that
  would put a lossy link into a flood loop.
- **A room keep-alive was malformed on air.** It took its path-length byte from one route and its
  path bytes from another, so after a restart it declared a 3-hop path and sent none — and the
  receiver read three bytes of payload as path. The room ignored it, the push stream stalled, and
  the log recorded a well-formed send. All four send paths now build through one constructor that
  cannot mix the two.
- **"Remove all Neighbours" from the phone app returned `Unknown command`.** The firmware matches
  `neighbor.remove ` with its trailing space and treats an empty pubkey as a prefix matching every
  entry; the dispatcher trimmed the space away, and underneath that an empty pubkey was rejected
  outright. Both fixed — the second was invisible until the first was.
- A login rejected for a bad password or a replay-guard trip now says which. Both were bare
  returns, so an operator saw a login simply not happen.

### Added — packets and paths

- **Hop hashes resolve to node names** on the Packets, Trace and Monitoring pages. A hash is a
  pubkey prefix, not an identity: on a 338-peer mesh 92 of 211 one-byte hashes match more than one
  peer. The hashes stay on screen as the fact, a name that could be several peers carries the
  count and opens the candidate list, and a hash matching no repeater stays hex — only a repeater
  forwards, so a hash matching only a phone has not been identified at all.
- **A packet's row no longer changes when a relay echoes it.** It was rendered from whichever
  observation arrived last, so a packet you sent flipped to RX, taking its route and hop count with
  it. Rows render from the first observation seen and repeats join the observation list, which
  carries each one's own route, hops and path.
- The route label carries its hop count, since `DIRECT` is a routing mode rather than a claim of
  zero hops. The count means opposite things by route and now says which: a flood accumulates a
  hash at each relay, so it is distance travelled; a direct route consumes one, so it is distance
  remaining.

### Known limitations

- **The airtime-based reply timeout is unproven.** No reply has yet been observed arriving in the
  window it opens; every one that arrived came back in under 4s. It is justified on firmware
  fidelity — a 5-hop link would legitimately need ~27s and a 10s cap would cut it off — not on a
  measured win. Its visible cost is that a failure now takes longer to report.
- **Packet loss on long paths is not addressed.** On a 3-hop link here, requests are lost outright
  rather than arriving late, and no timeout recovers that. Retrying lost requests is a design
  change, not a tweak.
- **The frontend has no automated tests**, this repository has no runner for them, and a large part
  of this release is frontend. Those paths are verified by inspection and by driving the running
  app, not by anything that would fail in CI.
- The rename is invisible to a consumer that does not parse `client_version`: the key stays
  present, nothing errors, and a delta computed over `recv_errors` steps once per node at upgrade.
  Nothing on the wire distinguishes the two meanings, and no transport field is published either,
  so an SPI node's `hw_decode_errors: 0` reads the same as a KISS node measuring zero.
- **`packets_recv` and `packets_sent` on MQTT are still the observer's own tallies**, not the
  radio's, so they undercount by exactly the parse failures `packet_parse_errors` now counts. The
  firmware's counters are polled and reach `GET /api/radio/status`, but feeding them onto the wire
  changes two more keys on the shared schema, and `packets_recv` is what
  `flood_rx + direct_rx + dups` reconciles against. Its own change.
- `packet_parse_errors` has not been observed non-zero on the bench. Read a 0 there as
  "nothing malformed arrived, or the path is not exercised", not as proof the counter works.

## v1.3.1

**Security fix.** A repeater created through OwlShack had no admin password, and a blank one
compared equal to the blank a login request sends, so any node in radio range could log in as
admin: change settings, set a password and lock the operator out, read the access list, run CLI
commands. Upgrade if you run a repeater.

Baseline `v1.3.0` · schema `user_version` 10 → 11

### Upgrading

- **The database migrates itself** on first start. A blank repeater admin password becomes
  `password`, the same default the firmware ships (`ADMIN_PASSWORD` in `simple_repeater`), rather
  than an invented value an operator could not guess.
- **Change it.** `password` is the well-known firmware default, so it closes the "no password at
  all" hole without being a secret. Set your own on the Repeater page, or over CLI with
  `password <new>`.
- **A repeater with a password already set is untouched** by the migration.
- **Creating a repeater now requires an admin password.** `POST /api/config/repeater` rejects a
  missing or blank one, and the setup form will not submit without it.

### Fixed

- **A blank repeater admin password granted admin to any node in range.** `CreateRepeater` never
  set one and the column defaults to `''`, so `authLogin` matched the blank password a login
  carries against the blank stored one and returned `permAdmin`. The auth logic itself mirrors the
  firmware faithfully, including the fall-through when a blank-password sender is not in the ACL;
  the firmware is not exposed because it never stores a blank password. Three paths could leave one
  blank and all three now refuse: create (required field), `PUT /api/config/repeater/admin`
  (sending `""` cleared it), and the `password` CLI command. The guest password is deliberately
  still clearable — a blank guest password grants `PERM_ACL_GUEST`, which is 0, and is what the
  firmware does.
- **Migration slot 10 was not covered by the frozen-slots test.** `TestMigrations_ShippedSlotsFrozen`
  pinned v1.1.0 and v1.2.0 only, so the slot v1.3.0 shipped could have been edited or renumbered
  without failing anything. Pinned now, along with slot 11.

### Known limitations

- Setting an admin password longer than 15 characters is accepted but cannot be typed by a firmware
  client, which truncates to that length. Validate or truncate at the boundary in a later release.
- No authentication on the REST API or web UI, so anyone who can reach the port can still read and
  change the repeater's password directly.

## v1.3.0

Drive a bare SX12xx LoRa chip directly on the host SPI bus: no companion MCU, no
KISS firmware in between. Plus zero-hop node discovery that no longer needs a
repeater personality, a radio-health endpoint, a dead-receiver watchdog, and an
MQTT layer that no longer blocks startup on brokers that are not there.

Baseline `v1.2.0` · schema `user_version` 9 → 10 · `meshcore-go` v1.4.0 · Go 1.26+

### Upgrading

- **The database migrates itself** on first start, adding `settings.spi_board`.
  Existing installs get `NULL`, which means "KISS modem" — the previous
  behaviour. No manual SQL.
- **Absent is not zero.** Counters a transport cannot measure are now *omitted*
  from `GET /api/radio/status` instead of published as `0`. A KISS modem has no
  chip-level CRC count; an SPI radio has no KISS framing errors. Read a missing
  key as "not measurable" and `0` as "measured none" — a consumer that treats
  absent as zero will report a healthy radio for one that cannot answer. On the
  SPI path `inboundDroppedOldest`, `rxMetaTimeouts`, `rxMetaMisattributed`,
  `hwErrors` and `txOutcomeLost` are the newly omitted keys, alongside
  `batteryMv` and `mcuTempC`.
- **The MQTT status schema is unchanged.** Those same counters still publish `0`
  there, because that payload is shared with meshcore-bot and CoreScope; making
  it omit them is a coordinated change, not a local one.
- **The first MQTT `online` status arrives slightly later**, once the broker
  connection completes, rather than during startup. Same message and topic; no
  longer ordered before the node starts serving.

### Added

- **SPI radio support.** A `spi://` connection scheme drives the LoRa chip itself
  — SX1262 and SX127x — over the host's SPI bus and GPIO lines.
- **Board registry** of 21 definitions, as JSON rather than buried in Go,
  selectable in Settings and the setup wizard and served at
  `GET /api/spi/boards`. Two are verified against hardware; the other 19 are
  transcribed from vendor documentation and marked as such, because a wrong
  `RESET` pin looks exactly like a dead radio.
- **`GET /api/radio/status`** reports the modem's link counters: frames dropped,
  decode and hardware errors, slow-handler stalls, and on the SPI path the
  chip's own packet totals, CRC errors, driver faults and receiver recoveries.
  On a hat with no battery and no MCU temperature sensor these are the only
  health signal the radio has.
- **Dead-receiver watchdog.** An SX126x can be left deaf by a failed
  `ResumeReceive` while every other indicator still reads healthy. The driver
  now reports a stuck receiver, OwlShack reconnects the modem, and each recovery
  is counted so a flapping radio is visible rather than merely quiet.
- **`meshcore-go` v1.4.0**, with `hardware/transport` and the new
  `hardware/sx12xx` at the same tag. Activity-LED blinking moved into the
  library's chip drivers, where every consumer gets it.
- **Date and time in bot templates.** `now` gives the current time and `date`
  formats one in any Go layout, with an optional IANA zone:
  `{{date now "Mon 3:04PM" "Pacific/Auckland"}}`. `date` also reads the raw
  unix seconds a group trigger's `{{.Timestamp}}` arrives as, which previously
  rendered as a bare number and had no way to be formatted. The zone database
  is compiled in, so a named zone resolves identically on every release target
  rather than only where the host ships zoneinfo. Omitting the zone uses the
  process's local time, which in a container is UTC until `TZ` is set: the
  README now documents that, and a mistyped `TZ` falls back to UTC silently.
- **A new owl mark** replaces the radio glyph in the sidebar, the favicon and
  every PWA icon. An installed app picks up the new icon on its next launch.
  The maskable and iOS icons are opaque on purpose: Android crops the maskable
  to a circle, and iOS renders transparency in an `apple-touch-icon` as black.
- **Zero-hop node discovery**, on a Discover page and at `POST /api/discover` /
  `GET /api/discover`, with each answer also broadcast on the new `discovered`
  websocket topic. It asks every repeater and sensor in direct radio range to
  answer and waits 30 s, because responders stagger their replies by a widened
  random delay — an empty list before the window closes means "not yet", not
  "nothing there". Results show as a list or on a map with a link line per
  responder, and each known node opens the standard peer detail panel.
  Two properties are worth stating plainly. It works from *whichever* node is
  running: the firmware's request carries no sender identity, so finding what a
  radio can hear no longer requires running a repeater. And every link is
  reported in both directions — the SNR we measured on the reply beside the SNR
  the responder reported for our request — because a link limited by our receive
  looks identical to a healthy one if you only print a single number.
  Only repeaters and sensors are offered. `simple_room_server` has no
  `onControlDataRecv` override and `companion_radio` forwards the frame to its
  phone app without ever replying, so listing those types would return an empty
  result that reads as "none in range".

### Fixed

- **A radio that will not start no longer takes the web UI down with it.** `modem.Setup` ran before
  the HTTP server and its failure was fatal, so a node whose modem had moved, been unplugged or been
  swapped for a different board exited before serving the page that would have fixed it — and the
  log said "complete setup in the web UI" while guaranteeing there was none. Recovery meant a shell,
  `sqlite3`, or deleting the database. The server now starts first; a radio that cannot open is
  reported, retried with capped backoff, and corrected from Settings, which reconnects it in place.
  `GET /api/radio/status` answers `503` while there is no modem rather than a page of zeroes. This
  also covers a first run on an SPI host, where the bootstrapped default is a KISS modem on
  `/dev/ttyACM0` that does not exist — previously the setup wizard could not be reached at all.
- **MQTT startup no longer waits for absent brokers.** Connections were dialled
  in line, serially, with a 10 s timeout each — on boot and again on every
  `SIGHUP` reload. Three unreachable brokers cost 30 s before the mesh node
  started. The dial now runs in the background; the first connect failure is
  still logged at error level so an unreachable broker stays visible.
- **Token refresh no longer drops publishes.** Refreshing a broker token
  disconnected the old client before dialling the new one, leaving nothing to
  publish through for the whole dial — up to 10 s, every 8 minutes, per token
  broker. The new client is established first and the old one dropped
  immediately after the swap.
- **A failed token refresh now retries.** When the reconnect failed, the retry
  loop found the stale client still reporting connected and returned without
  dialling; the broker stayed dead until the next refresh tick, by which point
  its token had expired. The stale client is dropped on the failure path, which
  is what lets the retry work.
- **A KISS modem that goes quiet without erroring is now detected.** A serial
  read timeout returns `(0, nil)` rather than an error, so a device that stays
  enumerated but stops talking — a USB autosuspend that never resumes, wedged
  firmware, a stalled hypervisor passthrough — left the read loop spinning every
  100 ms with nothing to report. No error, no reconnect, no log line: the node
  looked healthy and simply never heard another packet. A liveness probe now
  asks the modem for its status every 30 s and reconnects after three
  consecutive unanswered probes. Inbound silence is deliberately *not* the
  trigger — a quiet mesh is normal — and the probe stays disarmed until the
  modem has answered at least once, so firmware that does not implement the
  status queries is never reconnected in a loop.
- **Board readings are no longer published after the modem stops answering.**
  The reply flags were sticky, so once the serial port went away the status
  payload kept carrying the last battery voltage and MCU temperature — a user
  log showed `battery_mv=4148` and `mcu_temp_c=20.5` being published while every
  query was returning `write frame: input/output error` and the port was closed.
  A reading now drops out of the payload if the modem has not answered for 45 s.
- **Five counters no longer publish `0` on an SPI node.** Queue-full-oldest,
  the two signal-metadata pairing counts, hardware errors and lost TX outcomes
  are KISS framing concepts with no analogue on the SPI path, so they read as a
  radio measuring them and finding nothing wrong. They are now absent there and
  render as `—`. Decode errors, queue-full-new and handler-slow *are* measured
  on both paths and keep their numbers.
- **The SPI radio's slow-handler count** was read from the wrong source and
  always reported `0`, which looks like a modem keeping up comfortably.
- **Battery and MCU temperature no longer publish `0`** on hats that have
  neither, which reported a flat cell and a freezing board.
- **Signal-strength bars now line up down a column.** The dB label had no fixed
  width, so the bar icon beside it shifted with the digit count — `0.0dB` is a
  character shorter than `-6.3dB`. The rows knocked out of alignment were the
  ones reading near zero, which is where the eye goes anyway.
- **The repeater's Discover button now waits 30 s between requests.** Firing
  again while the first request is still being answered only adds contention,
  and the firmware allows each repeater four discovery responses every two
  minutes (`discover_limiter(4, 120)`); past that it stays silent, which looks
  exactly like it dropping out of radio range.

### Verified on hardware

| Board | Host | Build | Radio |
|---|---|---|---|
| Zindello UltraPeaterZero (E22P, 1 W) | Raspberry Pi 4 | `linux/arm64` | SF7 · 62.5 kHz · 22 dBm |
| Zindello UltraPeaterZero (E22, 1 W) | Raspberry Pi Zero W | `linux/arm` v6 | SF7 · 62.5 kHz · 22 dBm |

Both nodes transmit and receive each other's traffic, which is the only real
proof the external PA radiates — `TX_DONE` fires whether or not anything leaves
the antenna. Reset recovery was exercised by pulling the chip's `NRST` line and
watching the receiver re-arm.

Discovery was exercised on air against four repeaters. The scan was run with the
repeater personality deliberately out of the path, so the answers prove the
feature does what it claims: three to four repeaters replied to a companion-only
scan, within about three seconds each on SF7. One link came back asymmetric by
roughly 15 dB, which is the case the two SNR columns exist for. A sensors-only
scan returned nothing while those same repeaters were still answering repeater
scans, which is the type filter proven over RF rather than only in a test.

### Internal

- Comments cut to one line or none across 174 files, a net reduction of ~2,400
  lines. A comment that restates its code is a second thing to keep true.
- New tests for the non-blocking startup, the refresh failure path, the
  dead-radio watcher and one-instant status sampling, each mutation-checked by
  reintroducing the bug and confirming the test fails.
- The suite runs clean under `-race -shuffle=on`, which is what exposed retry
  goroutines outliving their tests and racing across test boundaries.
- README rewritten around what the app now does, with screenshots and a
  supported-hardware section whose figures come from `GET /api/spi/boards`
  rather than a hand count.
- Documentation corrections: the MQTT bridge exists as two forks with different
  field sets, so references now name which one; and one reference doc claimed a
  shipped feature was still missing.

### Known limitations

- **No authentication on the REST API or web UI** — the largest real gap for a
  tool that can reconfigure a repeater. Do not expose it to an untrusted
  network.
- Repeater and room sessions live in memory, so a restart drops every login.
- Of 21 SPI board definitions, two are verified on hardware and ten are
  untested presets. The other nine cannot be selected at all: seven need a
  non-default `gpiochip` the GPIO library cannot address, and two have no
  confirmed RF-switch control.
- No autostart unit ships with the binary; a host that reboots does not bring
  its node back.
- A companion or repeater that fails to *restart* on a config reload is still
  fatal; only the radio's own failure is now survivable.
- **Sensor discovery is unproven on air.** The type filter is verified in both
  directions, but no sensor was within radio range of the test bench, so a
  sensor actually answering has only been read in the firmware source.
- A discovery response carries no position, so the Discover map can only place
  nodes whose advert is already on record. The page reports how many it could
  not place rather than quietly showing fewer nodes than the list.
- A node that answers discovery but has never been heard advertising has no
  peer record, so it cannot be opened or mapped. Those rows read `never` and are
  not clickable. This case has not been seen on the bench.

## v1.2.0

Repeater, sensor and room parity with firmware 1.17.1; backup and restore; MQTT
observability. CI extended to run checks on pull requests into `dev` as well as
`main`, and `CLAUDE.md` split into a lean map plus `docs/` reference.

Schema `user_version` 9.
