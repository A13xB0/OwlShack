# Frontend reference

The styling system, page patterns, mobile conventions and the PWA. Read `pages/DashboardPage.tsx` once before adding a page.

## UI / styling system

Aesthetic: **operator console / radio shack utility**. Phosphor-green primary on warm-charcoal dark, JetBrains Mono for everything diagnostic, sharp corners. **Always read [`pages/DashboardPage.tsx`](../web/frontend/src/pages/DashboardPage.tsx) once before adding a new page** — every other page mirrors its eyebrow → title → `space-y-8` → panel-with-section-titles → divided-row pattern.

### Theme tokens (full list in [`index.css`](../web/frontend/src/index.css))

- Standard shadcn token set + `*-foreground` variants.
- Status: `success`, `warning`, `info`.
- Signal: `signal`, `signal-strong`, `signal-weak`, `signal-dead` (used by `<SignalStrength>`).
- Charts: `chart-1`–`chart-5`.
- Sidebar tokens.

`--primary` is `oklch(0.78 0.18 155)` in dark mode. **Don't introduce a different accent.** Blue/violet are reserved for chart and type-pill tones.

### Typography

- Body: **Inter**. Mono: **JetBrains Mono**. **Self-hosted, never the Google Fonts CDN** — this is an offline-first, self-contained binary, so fonts must not be fetched over the network. The latin variable-weight woff2 files live in [`web/frontend/public/fonts/`](../web/frontend/public/fonts/) (embedded into the binary) and are declared via `@font-face` at the top of [`index.css`](../web/frontend/src/index.css). Don't reintroduce `<link>`s to `fonts.googleapis.com`/`fonts.gstatic.com`. To add a weight/family, vendor its woff2 here and add an `@font-face`.
- All labels, eyebrows, badges, buttons: mono uppercase, `tracking-[0.08em]`–`tracking-[0.14em]`.
- Numerics get `tabular-nums`.

### Layout primitives

- Panels: `bg-card border border-border` (or use the `panel` utility).
- Sharp corners: `rounded-none` by default, `rounded-sm` (2 px) for chips/tiles. Never `rounded-lg`/`xl`/`full`.
- Eyebrows: `<span className="label-overline">…</span>`.
- Stat grids: 1 px border separators via `gap-px bg-border border border-border` (see `DashboardPage.StatCell` and `RepeaterDetailPage.StatTile`).
- Page wrapper: `<div className="space-y-8">` then `<PageHeader>` then sections.
- Empty states: centered `<CircleDashed>` + small uppercase mono caption.
- Background grid texture is painted by `body::before` in `index.css` — keep it.

### Reusable components — use these, don't reinvent

- `<PageHeader />`, `<PageMeta />`
- `<ConnectionPill connected />`
- `<PeerTypePill type />`
- `<SignalStrength snr size="sm|md" showLabel />`
- `<PeerAvatar name size="xs|sm|md|lg" />`
- `<PathDialog companion pubkey name onChanged />`: the one route editor for every contact (repeater, room, sensor and chat pages, the contact page). Don't add another.
- `<HopPicker peers hashSize hops onHopsChange />`: pick hops from the repeater list; the Trace page and `PathDialog` share it.
- shadcn primitives at `@/components/ui/*`
- Toasts: `import { toast } from "sonner"`

### Mobile

The SPA is used on phones as an installed PWA, so every page is checked at a
412×915 viewport (Playwright, `page.setViewportSize`) — nothing may scroll
horizontally, and these conventions hold:

- **Bottom nav below `md`.** `BottomNav` in `AppShell.tsx` gives Overview /
  Messages / Peers / Map one tap and "More" opens the sidebar sheet. Its height
  is the CSS var `--bottom-nav` (`index.css`: `3.5rem + safe-area` under 768px,
  `0px` above). Anything sized to the viewport must subtract it — the chat page
  is `h-[calc(100dvh-3.5rem-env(safe-area-inset-top,0px)-var(--bottom-nav))]`, the map
  `calc(100dvh-260px-var(--bottom-nav))`, and `<main>` pads its bottom by it.
  Use `dvh`, never `vh`: the mobile URL bar collapsing makes `100vh` jump.
- **Safe areas.** `index.html` sets `viewport-fit=cover` and
  `black-translucent`, so content extends under the notch and home indicator;
  the header uses `.pt-safe`, the bottom nav `.pb-safe`. A new fixed-position
  element at a screen edge needs the matching utility.
- **Header label.** `routeLabel()` (desktop, full path) vs `routeLabelShort()`
  (below `sm`, current page only) — the truncation used to cut off the current
  page, the one part that mattered.
- **Touch targets ≥ 40px** without changing the visual. `Button` (base, `xs`,
  `icon-*`) carries a phone-only `before:` hit area (`md:before:hidden`),
  `Switch` the same (`before:-inset-3`), `Input` and `SelectTrigger` carry
  `min-h-10 md:min-h-0` — **`min-h`, not `h`**, so a page passing an explicit
  `h-7`/`h-8` still gets 40px on a phone and its exact height back on desktop
  (changing the base `h-9` would have let the `md:` override beat those; note
  this does NOT hold for `SelectTrigger`, whose
  `data-[size=default]:h-9` outranks a page's `h-7` — such a trigger renders
  36px tall on both), dropdown items are `min-h-10 md:min-h-0`, filter chips use
  `before:-inset-y-2 sm:before:hidden`, `RepeaterTab` is `min-h-10 sm:min-h-0`,
  and `DialogContent` is capped at `max-h-[calc(100dvh-2rem)]` and scrolls
  (per-dialog caps are redundant). The peers checkbox keeps
  its 16px box and grows a `before:absolute before:-inset-3` hit area. Don't
  pad a bordered element to enlarge it — the border grows with it.
- **A hit area may only grow vertically.** The `::before` has no
  `pointer-events-none` (it exists to catch the tap) and the later sibling
  paints on top, so horizontal growth steals taps from the adjacent button —
  `icon-xs`'s old `before:-inset-2` plus `InlineConfirm`'s `size-10 -m-2` put
  Remove's hit box 12px inside the neighbouring 24px Edit pencil, so tapping
  Edit's right half opened "Remove?". The `icon-*` sizes are therefore
  `before:-inset-y-*` only, and `InlineConfirm` adds no size override. Two 24px
  icons `gap-1` apart cannot both be 40px **wide** (80px of target in 52px of
  room), so those rows are 24×40: to widen them the visual icons have to grow.
- **Row pattern** (Contacts / Repeaters / Channels): `gap-2 sm:gap-4 px-3
  sm:px-4`, name in a `min-w-0` wrapper with `truncate`, pill `shrink-0`,
  secondary line as one `truncate` `<code>`, remove control `iconOnly` with an
  `ariaLabel`. Hover-only affordances (`group-hover` icons) are `hidden
  sm:block`; the chat's message actions are reachable by long-press.
- **Labels** — `.label-overline` and `.text-mono-xs` are 11px below `sm`, 10px
  above. Tab strips (`RepeaterTab`) drop their icons and tighten padding below
  `sm`, and their `TabsList` is `flex-wrap`, so 4 tabs fit one line and 8 wrap
  rather than clipping.
- **No pull-to-refresh.** `body { overscroll-behavior-y: contain }` — a reload
  drops the socket and the composer draft; the SW update toast covers the
  legitimate case.
- Inputs are `text-base md:text-sm` (16px on phones) so iOS doesn't zoom on
  focus. Keep that — a bare `text-xs`/`text-sm` on an `<Input>` overrides it
  and inverts the sizes (12px on phones); write `text-base md:text-xs`.

### Cursors

`index.css` has a global rule giving `cursor: pointer` to non-disabled `<button>`, `[role="button"|"menuitem"|"option"|"tab"]`, `<summary>`, and `<label[for]>`. Don't add per-component cursor classes. The shadcn `dropdown-menu.tsx` was patched to use `cursor-pointer` instead of its default `cursor-default` to match this convention.

### Maps

`settings.map_provider` picks the tiles (`osm` default, `carto`), set on the Settings page;
`PROVIDERS` in `lib/leaflet.ts` holds each one's URL and attribution, and adding one means a
value there, in `config.MapProviders`, and in the Settings select. CARTO's key
(`settings.map_tile_key`) goes on as `?key=`; blank still serves keyless tiles. OSM Standard has
no dark style, so its layer carries `tiles-darken`, recoloured in dark mode as
`settings.map_dark_style` says (`setTiles` copies it to `data-map-dark` on the root, so a change
needs no tile reload): `original` inverts with the hues kept; `simplified` is the `#osm-dark` SVG
colour matrix in `index.html`, fitted to OSM's palette so water goes navy below dark-grey land,
which no invert can do. Every
map calls `useThemeTiles`, which swaps the layer when its URL would change (theme, tile setting, or
base layer), and keeps Leaflet's attribution control: every provider's terms require it. Only the
Map page passes a base layer (`main`, Esri `satellite`, OpenTopoMap `topo`), remembered per browser;
every other map is the main map.


Leaflet popup / zoom-control / attribution / container backgrounds are themed in `index.css`. Default marker icons are bundler-incompatible; the rebind lives **once** in [`lib/leaflet.ts`](../web/frontend/src/lib/leaflet.ts), so any file that imports from there gets working pins — don't add another `L.Icon.Default.mergeOptions` block.

**Every lat/lon entry uses [`components/PositionPicker.tsx`](../web/frontend/src/components/PositionPicker.tsx)** (`<PositionPicker lat lon onPick>` under the two inputs; `round6` trims a pick to the 6 decimals the inputs show). Sites: the companion edit dialog, the setup wizard, the repeater node Settings, the remote repeater's Position section, and the contact location editor. Adding a coordinate field anywhere means adding the picker too.

---

## Patterns established in the recent rewrite

**Tab persistence + fetch-once.** For tabs that hit the mesh (Status, Neighbors), use Radix `forceMount` on the `<TabsContent>` plus `data-[state=inactive]:hidden`, and pass an `active={tab === "x"}` prop into the tab component. Inside, a `fetchedRef` guards the auto-fetch so it only fires the first time the tab becomes active. Manual refresh button always works. This deliberately avoids spamming the radio just because a user toggles tabs. Implementation in [`RepeaterDetailPage.tsx`](../web/frontend/src/pages/RepeaterDetailPage.tsx).

**Per-action busy state.** Any pair of buttons that share a busy flag (load/save, refresh/discover) should use a discriminated union (`"load" | "save" | null`). Only the spinner on the actually running button spins; both stay disabled. See `SectionFooter` and the Neighbors tab.

**Dropdowns over free text** for any setting with a finite valid set on the firmware side. The `SelectField` component in `RepeaterDetailPage.tsx` gracefully handles unknown device values by appending them as `"<value> (custom)"`. Currently used for:
- Bandwidth: 10 LoRa widths (7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500 kHz).
- Spreading factor: SF5–SF12.
- Coding rate: 4/5–4/8.
- Repeat: on/off.
- Path hash mode (remote repeater CLI only): `0=1B / 1=2B / 2=3B`. The firmware validator is `mode < 3` and the width is `mode + 1`. Our own config stores BYTES — see **Path hash size**.
- Loop detect: off / minimal / moderate / strict.

**Chat ordering is by row id, never by timestamp.** A message's `timestamp` is
the *sender's* clock — a room stamps each post with its own RTC, and a node
whose clock is wrong (one on the bench read 2024 while we read 2026) would drop
its posts into the middle of the thread, or sink a just-active thread to the
bottom of the list. So `mergeMessages` sorts by `id` (the order we learned of a
message; no id yet ⇒ newest ⇒ last) and the threads list sorts on
`lastMessageId` from `/conversations` (`convRecency`), falling back to
`lastActive` only when a thread has no messages. The stored timestamp must stay
the sender's: `handleRoomLogin` derives `sync_since` from `Messages.LatestRx`,
which has to be in the *room's* clock domain or resync breaks. Fix a wrong
remote clock with `clock sync` (firmware sets its RTC from our command's
timestamp; it refuses to go backwards).

**Message cache + delta backfill** (in [`CompanionDetailPage.tsx`](../web/frontend/src/pages/CompanionDetailPage.tsx)):
- `messageCacheRef: Map<channel, Message[]>`
- First open: full fetch via `?limit=100`. Cache populated.
- Re-open: render cache instantly, then fire `?afterId=lastId` and merge.
- WS messages always update both cache and active state via `mergeMessages` (dedup by `id`, fallback `timestamp+sender+text`).
- WS reconnect (`connected` false → true) triggers the same `afterId` backfill on the active channel.

**Contact detail primary action is per type** (`ContactDetailPage`): REPEATER → *Manage* (the repeater management route); CHAT and ROOM → *Message* (a room's chat *is* its `dm:` thread); SENSOR → no button. Sensor firmware (`SensorMesh::onPeerDataRecv`) treats an inbound TXT_MSG only as a CLI command and only from an admin, and its own outbound text is `sendAlert` notifications — there is no chat to open; its telemetry panel is on the page. When a sensor management page exists it slots in as *Manage* the way repeaters do.

**Threads list filters out repeaters.** `isRepeater` conversations (set by `routes_conversations.go` from contact metadata or peer type) are managed on the dedicated `RepeatersListPage` instead.

**Public is always pinned to the top** of the threads list regardless of sort mode (`recent` / `name` / `unread`).

**Initial scroll instant, subsequent smooth.** Conversations open with `behavior: "instant"` for the first scroll-to-bottom; later message arrivals use `smooth`.

**Reply asymmetry.**
- Replying to a **received** message: composer pre-fills with `@[sender] ` only.
- Replying to your **own** message: composer pre-fills with the message quoted using `> ` line prefixes.

**Message context menu** uses a `data-context-menu` attribute + `mousedown`-outside listener (NOT `click`/`scroll`) so the menu isn't killed by auto-scroll.

**Hover icons on messages.** Each bubble row reveals a Reply icon and a 3-dot context menu icon on hover, opposite the bubble. The 3-dot opens the same menu as right-click / long-press.

**Save-credentials toggle on login.** `RepeaterDetailPage` LoginCard has a Switch that, when checked, persists the password to contact metadata via `PATCH /api/companions/{name}/contacts/{pubkey}`. Toggling off clears any saved password. Pre-checked when a saved password is present. Blank passwords are allowed (some repeaters have no admin password set).

**`loggedIn` derivation.** `/session` returns either `{loggedIn:false}` or the bare session struct (no `loggedIn` field). The page treats presence of `pubkeyHex` as logged in. **Don't change this without changing `api/repeater.go` simultaneously** — the asymmetry is in the firmware response.

**Position picker.** Shared `PositionPicker` (see Maps) — an interactive Leaflet map below the lat/lon inputs:
- Click anywhere → fills lat/lon (rounded to 6 decimals).
- Pin is draggable; release updates inputs.
- Manual entry into the inputs re-centers the map and moves the pin.

**CLI terminal autocomplete.** `<Tab>` opens / applies highlighted suggestion. `↑/↓` cycles suggestions when open (otherwise still cycles command history). `Esc` closes. `Enter` always submits. Suggestions only render when input is non-empty (otherwise they'd reappear after every submit). Catalogue is curated and split into top-level commands and config keys; both are ranked prefix-first then substring-match. No result cap. When the input starts with `get ` or `set `, the menu switches to the config keys list.

## PWA (installability)

The SPA is an installable PWA (Phase 1: installability only — no push yet). Hand-rolled, **no Workbox/`vite-plugin-pwa`** — this is a live WebSocket console where offline shows nothing useful, so the dependency wasn't worth it.

- Static assets live in [`web/frontend/public/`](../web/frontend/public/) (`manifest.webmanifest`, `sw.js`, `apple-touch-icon.png`, `favicon-32.png`, `icons/`). Vite copies `public/` verbatim into `dist/`, so they're embedded and served from root by the SPA handler — **no Go route needed**.
- The SW ([`public/sw.js`](../web/frontend/public/sw.js)) is deliberately conservative so it never serves a stale build or stale data: **network-first for navigations** (cached shell is only an offline fallback), **cache-first for `/assets/*`** (Vite content-hashes them → immutable), and **pure passthrough for `/api/*` + the WS** (never cached).
- **`VERSION` is auto-stamped, never bumped by hand.** It's the `__BUILD_VERSION__` token that `build.sh` `sed`s to the build version in `dist/sw.js`, so `sw.js` changes every release — required because the browser skips re-installing a byte-identical SW (no update = no toast). It also names the cache (old caches dropped on `activate`).
- Registered once at startup via [`src/lib/registerSW.ts`](../web/frontend/src/lib/registerSW.ts) from `main.tsx`. It shows a "new version — reload" toast when an updated SW is waiting: the SW no longer auto-`skipWaiting`s, so Reload posts `SKIP_WAITING` and the ensuing `controllerchange` reloads the page.
- **Static assets carry explicit `Cache-Control`** (`spaHandler`, [`internal/api/server.go`](../internal/api/server.go)): `/assets/*` `immutable`, everything else `no-cache`. embed.FS files have no `Last-Modified`/`ETag`, so without `no-cache` the browser's manifest/SW update check can keep reading a stale manifest from its heuristic cache.
- Icons are regenerated by [`web/frontend/scripts/gen-icons.py`](../web/frontend/scripts/gen-icons.py) (needs Pillow; not part of `build.sh`) — the lucide `Radio` mark in `--primary` on `--background`, colors computed from the oklch theme tokens. Re-run it if the brand mark or those tokens change.
- `internal/api/server.go` has an `init()` registering the `.webmanifest` MIME type as `application/manifest+json` — Go's mime table lacks it, so the file server would otherwise sniff the manifest as `text/plain`.
- Manifest `theme_color`/`background_color` are the dark `--background` (`#090b0d`); `display: standalone`.

---
