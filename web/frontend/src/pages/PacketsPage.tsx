import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, CircleDashed, MapPin, RefreshCw, Search } from "lucide-react";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { useResume } from "@/lib/resume";
import { HopPath, type PathPeer } from "@/components/HopPath";
import { mapPathHref } from "@/lib/linkPath";
import { Button } from "@/components/ui/button";
import { HeaderButton } from "@/components/HeaderButton";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { PageHeader } from "@/components/PageHeader";
import { snrTextClass } from "@/components/SignalStrength";
import { formatDateTime, formatShortTime, truncate, truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";

interface Packet {
  id?: number;
  receivedAt: string;
  direction: "rx" | "tx" | string;
  raw: string;
  routeType?: number;
  payloadType?: number;
  route?: string;
  pathHashSize?: number;
  hops?: number;
  path?: string;
  packetHash?: string;
  summary?: string;
  snr?: number;
  rssi?: number;
}

interface PacketGroup {
  key: string;
  // origin is what the row renders: our own transmission when we sent the packet, otherwise the
  // first time we heard it. Rendering the newest observation instead flipped a packet we sent to RX
  // the moment a relay echoed it back — and took its route and hop count with it.
  origin: Packet;
  // Parsed once during grouping so the sort comparator allocates no Date objects.
  originTs: number;
  latestTs: number;
  observations: Packet[];
}

const PAYLOAD_TYPE_LABELS: Record<number, string> = {
  0: "Request",
  1: "Response",
  2: "Message",
  3: "ACK",
  4: "Advert",
  5: "Channel",
  6: "Ch Data",
  7: "Anon Req",
  8: "Path",
  9: "Trace",
  10: "Multi",
  11: "Control",
  12: "Raw",
};

function payloadLabel(pt: number | undefined): string {
  if (pt == null) return "—";
  return PAYLOAD_TYPE_LABELS[pt] ?? `T${pt}`;
}

// A hop count means opposite things by route type: a flood ACCUMULATES a hash at each relay, so the
// count is how far the packet has come; a direct route CONSUMES one, so the count is how far it has
// left to go. Live proof of the second: one packet logged as DIRECT hops=2 path=e640 on transmit
// came back as hops=1 path=40 after a single relay.
function hopSense(route: string | undefined): string | null {
  if (!route) return null;
  return route.includes("DIRECT") ? "remaining" : "travelled";
}

// "DIRECT" is the routing mode, not a hop count — a direct packet still crosses its stored path.
// Carrying the count in the same cell stops it reading as "went straight there" on the narrow
// layouts, where the Hops column is hidden entirely.
function routeLabel(p: Packet): string {
  if (!p.route) return "—";
  if (!p.hops) return p.route;
  return `${p.route} · ${p.hops}`;
}

function packetKey(p: Packet): string {
  if (p.packetHash && p.packetHash.length > 0) return p.packetHash;
  return `${p.direction}:${p.raw}`;
}

function dirGlyph(direction: string): string {
  if (direction === "tx") return "↑";
  if (direction === "rx") return "↓";
  return "·";
}

const MIN_SIDEBAR_WIDTH = 300;
const DEFAULT_SIDEBAR_WIDTH = 480;

// A broad type filter can match thousands of the ~10k stored rows; the UI says when this is hit.
const FILTER_LIMIT = 500;

const NO_PACKETS: Packet[] = [];

// beatsOrigin decides which observation represents the packet: the first one seen, TX or RX alike.
// A repeat never rewrites the row, it only joins the observation list. Row id beats timestamp where
// both are known, being insertion order rather than a clock — live socket packets carry no id yet,
// hence the fallback. Observations arrive newest-first from the API but oldest-first over the
// socket, so neither comparison can trust arrival order. receivedAt must therefore carry sub-second
// precision (both surfaces send RFC3339Nano): at second resolution a relay's echo and our own
// transmit tie, the strict comparison cannot break the tie, and whichever the loop happened to meet
// first became the row — the exact flapping this function exists to stop.
function beatsOrigin(p: Packet, ts: number, g: PacketGroup): boolean {
  if (p.id != null && g.origin.id != null) return p.id < g.origin.id;
  return ts < g.originTs;
}

// Collapses observations sharing a packet hash into one group, newest first.
function buildGroups(packets: Packet[]): PacketGroup[] {
  const map = new Map<string, PacketGroup>();
  for (const p of packets) {
    const k = packetKey(p);
    const ts = new Date(p.receivedAt).getTime();
    const existing = map.get(k);
    if (!existing) {
      map.set(k, { key: k, origin: p, originTs: ts, latestTs: ts, observations: [p] });
      continue;
    }
    existing.observations.push(p);
    if (ts > existing.latestTs) existing.latestTs = ts;
    if (beatsOrigin(p, ts, existing)) {
      existing.origin = p;
      existing.originTs = ts;
    }
  }
  // Ordered by origin, not by the last echo: sorting on latestTs would show one time and order by
  // another, and would jump an old packet back to the top every time a relay repeated it.
  return Array.from(map.values()).sort((a, b) => b.originTs - a.originTs);
}

export function PacketsPage() {
  // Hop hashes are resolved against the peer table; a failure here only leaves hops as hex.
  const { items: peers } = useApiList<PathPeer>(
    "/api/peers",
    "Failed to load peers",
  );
  const {
    items,
    setItems: setPackets,
    loading,
    error,
    reload,
    refresh,
  } = useApiList<Packet>("/api/packets?limit=100", "Failed to load packets");
  useResume(refresh);
  const livePackets = items ?? NO_PACKETS;
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [typeFilter, setTypeFilter] = useState<number | "ALL">("ALL");
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR_WIDTH);
  const draggingRef = useRef(false);
  // Read by onResizeStart: sidebarWidth as a dep would rebuild it on every drag pixel.
  const sidebarWidthRef = useRef(DEFAULT_SIDEBAR_WIDTH);
  sidebarWidthRef.current = sidebarWidth;

  // Debounce the search box so each keystroke doesn't fire a server query.
  const [debouncedSearch, setDebouncedSearch] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search.trim()), 300);
    return () => clearTimeout(t);
  }, [search]);

  const filtering = typeFilter !== "ALL" || debouncedSearch !== "";

  // Filtering queries all stored history; the live buffer only holds the last 200.
  const filterUrl = filtering
    ? `/api/packets?limit=${FILTER_LIMIT}` +
      (typeFilter !== "ALL" ? `&payloadType=${typeFilter}` : "") +
      (debouncedSearch ? `&q=${encodeURIComponent(debouncedSearch)}` : "")
    : null;
  const { items: filteredItems, loading: filterLoading } = useApiList<Packet>(
    filterUrl,
    "Failed to search packets",
  );

  const onWsMessage = useCallback(
    (topic: string, data: unknown) => {
      if (topic !== "packets" || !data) return;
      const pkt = data as Packet;
      setPackets((prev) => [pkt, ...(prev ?? [])].slice(0, 200));
    },
    [setPackets],
  );

  useWebSocket(["packets"], onWsMessage);

  // Pills come from the live buffer, so they stay stable while a search narrows results.
  const liveGroups = useMemo(() => buildGroups(livePackets), [livePackets]);
  const typeFilters = useMemo(() => {
    const counts = new Map<number, number>();
    for (const g of liveGroups) {
      const pt = g.origin.payloadType;
      if (pt == null) continue;
      counts.set(pt, (counts.get(pt) ?? 0) + 1);
    }
    // The active type must stay listed, or the menu opens with nothing checked.
    if (typeFilter !== "ALL" && !counts.has(typeFilter)) {
      counts.set(typeFilter, 0);
    }
    return Array.from(counts.entries()).sort((a, b) => a[0] - b[0]);
  }, [liveGroups, typeFilter]);

  const filteredGroups = useMemo(
    () => buildGroups(filteredItems ?? NO_PACKETS),
    [filteredItems],
  );
  const groups = filtering ? filteredGroups : liveGroups;
  const capped = filtering && (filteredItems?.length ?? 0) >= FILTER_LIMIT;

  const emptyMsg = filterLoading
    ? "Searching…"
    : filtering
      ? "No packets match the filter."
      : "Awaiting packets…";

  const selected = useMemo(() => {
    if (!selectedKey) return null;
    return groups.find((g) => g.key === selectedKey) || null;
  }, [groups, selectedKey]);

  const onResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    draggingRef.current = true;
    const startX = e.clientX;
    const startW = sidebarWidthRef.current;
    const maxW = Math.floor(window.innerWidth * 0.6);

    const onMove = (ev: MouseEvent) => {
      if (!draggingRef.current) return;
      const dx = startX - ev.clientX;
      const next = Math.max(MIN_SIDEBAR_WIDTH, Math.min(maxW, startW + dx));
      setSidebarWidth(next);
    };
    const onUp = () => {
      draggingRef.current = false;
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }, []);

  const totalPackets = livePackets.length;
  const liveGroupCount = liveGroups.length;

  return (
    <div className="space-y-4">
      <PageHeader
        title="Packets"
        meta={
          <span className="font-mono text-sm text-muted-foreground tabular-nums">
            {filtering
              ? `${groups.length}${capped ? "+" : ""} match${groups.length === 1 ? "" : "es"}${filterLoading ? " · …" : ""}`
              : `${liveGroupCount} unique · ${totalPackets} obs`}
          </span>
        }
        actions={
          <HeaderButton icon={RefreshCw} busy={loading} onClick={reload}>
            {loading ? "refreshing" : "refresh"}
          </HeaderButton>
        }
      />

      {loading && <PacketsSkeleton />}
      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      {!loading && !error && (
        <section className="panel overflow-hidden">
          <div className="flex flex-col gap-3 px-4 py-3 border-b border-border sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-0.5">
              <span className="label-overline block">
                {filtering ? "History · filtered" : "Live · last 200"}
              </span>
              <h2 className="font-mono text-sm uppercase tracking-widest">
                Packet stream
              </h2>
            </div>
            <div className="relative w-full sm:w-72">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground/60" />
              <Input
                type="search"
                placeholder="search hash or path…"
                value={search}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                  setSearch(e.target.value)
                }
                className="pl-9 font-mono text-base md:text-xs"
              />
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-1 px-4 py-3 border-b border-border">
            <span className="label-overline mr-2 sm:hidden">Filter</span>
            <PacketTypeMenu
              value={typeFilter}
              onChange={setTypeFilter}
              allCount={liveGroupCount}
              types={typeFilters}
            />
            <button
              type="button"
              onClick={() => setTypeFilter("ALL")}
              className={cn(
                "hidden sm:inline-flex items-center gap-1.5 px-2 py-1 border font-mono text-[10px] uppercase tracking-[0.12em] transition-colors",
                typeFilter === "ALL"
                  ? "border-primary/50 bg-primary/10 text-primary"
                  : "border-border bg-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              ALL
              <span className="tabular-nums text-muted-foreground/60">
                {liveGroupCount}
              </span>
            </button>
            {typeFilters.map(([pt, count]) => {
              const active = typeFilter === pt;
              return (
                <button
                  key={pt}
                  type="button"
                  onClick={() => setTypeFilter(pt)}
                  className={cn(
                    "hidden sm:inline-flex items-center gap-1.5 px-2 py-1 border font-mono text-[10px] uppercase tracking-[0.12em] transition-colors",
                    active
                      ? "border-primary/50 bg-primary/10 text-primary"
                      : "border-border bg-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  {payloadLabel(pt)}
                  <span className="tabular-nums text-muted-foreground/60">
                    {count}
                  </span>
                </button>
              );
            })}
          </div>
          {/* Mobile: compact list */}
          <div className="sm:hidden divide-y divide-border/60">
            {groups.length === 0 ? (
              <div className="px-6 py-12 text-center text-sm text-muted-foreground/60">
                <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                {emptyMsg}
              </div>
            ) : (
              groups.map((g) => {
                const p = g.origin;
                const isSelected = selectedKey === g.key;
                const heard = g.observations.length;
                return (
                  <div
                    key={g.key}
                    onClick={() => setSelectedKey(g.key)}
                    className={cn(
                      // content-visibility lets the browser skip layout/paint offscreen.
                      "px-4 py-2.5 cursor-pointer [content-visibility:auto] [contain-intrinsic-size:auto_58px]",
                      isSelected && "bg-primary/5",
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2 min-w-0">
                        <span
                          className={cn(
                            "font-mono text-sm",
                            p.direction === "tx" ? "text-primary" : "text-muted-foreground",
                          )}
                        >
                          {dirGlyph(p.direction)}
                        </span>
                        <span className="font-mono text-[11px] uppercase tracking-[0.08em] text-foreground/80">
                          {payloadLabel(p.payloadType)}
                        </span>
                        <span className="text-sm truncate text-muted-foreground">
                          {p.summary ? truncate(p.summary, 40) : truncateMid(p.raw, 6, 4)}
                        </span>
                      </div>
                      {heard > 1 && (
                        <span className="shrink-0 inline-flex items-center justify-center min-w-5 px-1 py-0.5 border border-primary/30 bg-primary/5 text-primary font-mono text-[10px] tabular-nums">
                          ×{heard}
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-3 mt-0.5 font-mono text-[10px] tabular-nums text-muted-foreground/60 pl-6">
                      <span>{formatShortTime(p.receivedAt)}</span>
                      {p.snr != null && (
                        <span className={snrTextClass(p.snr)}>{p.snr.toFixed(1)}dB</span>
                      )}
                      {p.route && <span className="uppercase">{routeLabel(p)}</span>}
                    </div>
                  </div>
                );
              })
            )}
          </div>

          {/* Desktop: full table */}
          <Table className="hidden sm:table">
            <TableHeader>
              <TableRow className="border-border hover:bg-transparent">
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-[110px]">
                  Time
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-[44px] text-center">
                  Dir
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-[90px]">
                  Type
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                  Summary
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                  Route
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] hidden md:table-cell text-right">
                  Path
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] hidden lg:table-cell text-right">
                  Hops
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] text-right w-[64px]">
                  Heard
                </TableHead>
                <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] hidden xl:table-cell text-right w-[120px]">
                  Signal
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.length === 0 ? (
                <TableRow>
                  <TableCell
                    colSpan={9}
                    className="text-center py-12 text-sm text-muted-foreground/60"
                  >
                    <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                    {emptyMsg}
                  </TableCell>
                </TableRow>
              ) : (
                groups.map((g) => {
                  const p = g.origin;
                  const isSelected = selectedKey === g.key;
                  const heard = g.observations.length;
                  return (
                    <TableRow
                      key={g.key}
                      onClick={() => setSelectedKey(g.key)}
                      className={cn(
                        "border-border/60 cursor-pointer group [content-visibility:auto] [contain-intrinsic-size:auto_44px]",
                        isSelected && "bg-primary/5",
                      )}
                    >
                      <TableCell className="font-mono text-xs tabular-nums text-muted-foreground">
                        {formatShortTime(p.receivedAt)}
                      </TableCell>
                      <TableCell className="text-center">
                        <span
                          className={cn(
                            "font-mono text-sm",
                            p.direction === "tx"
                              ? "text-primary"
                              : "text-muted-foreground",
                          )}
                          title={p.direction}
                        >
                          {dirGlyph(p.direction)}
                        </span>
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-[11px] uppercase tracking-[0.08em] text-foreground/80">
                          {payloadLabel(p.payloadType)}
                        </span>
                      </TableCell>
                      <TableCell className="max-w-0">
                        <div className="text-sm truncate">
                          {p.summary ? (
                            truncate(p.summary, 80)
                          ) : (
                            <span className="text-muted-foreground/60 italic">
                              {truncateMid(p.raw, 8, 6)}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-xs uppercase tracking-[0.08em] text-muted-foreground">
                          {routeLabel(p)}
                        </span>
                      </TableCell>
                      <TableCell className="hidden md:table-cell text-right font-mono text-xs tabular-nums text-muted-foreground">
                        {p.pathHashSize != null ? `${p.pathHashSize}B` : "—"}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell text-right font-mono text-xs tabular-nums text-muted-foreground">
                        {p.hops != null ? p.hops : "—"}
                      </TableCell>
                      <TableCell className="text-right">
                        {heard > 1 ? (
                          <span className="inline-flex items-center justify-center min-w-6 px-1.5 py-0.5 border border-primary/30 bg-primary/5 text-primary font-mono text-[10px] tabular-nums">
                            ×{heard}
                          </span>
                        ) : (
                          <span className="font-mono text-xs text-muted-foreground/40">
                            —
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="hidden xl:table-cell text-right">
                        {p.snr != null ? (
                          <span
                            className={cn(
                              "font-mono text-xs tabular-nums",
                              snrTextClass(p.snr),
                            )}
                          >
                            {p.snr.toFixed(1)}dB
                            {p.rssi != null && (
                              <span className="text-muted-foreground/50">
                                {" "}
                                / {p.rssi}
                              </span>
                            )}
                          </span>
                        ) : (
                          <span className="font-mono text-xs text-muted-foreground/40">
                            ——
                          </span>
                        )}
                      </TableCell>
                    </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>
        </section>
      )}

      <Sheet
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelectedKey(null);
        }}
      >
        <SheetContent
          side="right"
          showCloseButton={false}
          className={cn(
            "p-0 bg-card border-l border-border sm:max-w-none w-full max-w-[100vw]",
          )}
          style={{ width: sidebarWidth }}
        >
          <div
            onMouseDown={onResizeStart}
            className="absolute top-0 left-0 h-full w-1 cursor-col-resize hover:bg-primary/40 active:bg-primary/60 transition-colors hidden sm:block"
            aria-hidden
          />
          {selected && (
            <PacketDetail
              group={selected}
              peers={peers}
              onClose={() => setSelectedKey(null)}
            />
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function PacketDetail({
  group,
  peers,
  onClose,
}: {
  group: PacketGroup;
  peers: PathPeer[] | null;
  onClose: () => void;
}) {
  const p = group.origin;
  const heard = group.observations.length;
  const sortedObs = useMemo(
    () =>
      [...group.observations].sort(
        (a, b) =>
          new Date(b.receivedAt).getTime() -
          new Date(a.receivedAt).getTime(),
      ),
    [group.observations],
  );
  const firstSeen = sortedObs[sortedObs.length - 1]?.receivedAt;
  const lastSeen = sortedObs[0]?.receivedAt;

  return (
    <div className="flex flex-col h-full overflow-hidden">
      <div className="flex items-start justify-between px-5 py-4 border-b border-border shrink-0">
        <div className="space-y-1 min-w-0">
          <span className="label-overline block">Packet · detail</span>
          <SheetTitle className="font-mono text-sm uppercase tracking-widest">
            {payloadLabel(p.payloadType)}
          </SheetTitle>
          <SheetDescription className="font-mono text-xs text-muted-foreground break-all">
            {p.packetHash || "no hash"}
          </SheetDescription>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onClose}
          className="font-mono"
        >
          ×
        </Button>
      </div>

      <div className="flex-1 overflow-y-auto">
        <div className="px-5 py-4 border-b border-border space-y-2">
          <span className="label-overline block">Summary</span>
          <p className="text-sm leading-relaxed wrap-break-word">
            {p.summary || (
              <span className="text-muted-foreground/60 italic">
                no summary available
              </span>
            )}
          </p>
        </div>

        <div className="px-5 py-4 border-b border-border">
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 font-mono text-xs">
            <dt className="label-overline">Direction</dt>
            <dd className="tabular-nums">
              <span
                className={cn(
                  p.direction === "tx" ? "text-primary" : "text-foreground",
                )}
              >
                {dirGlyph(p.direction)} {p.direction}
              </span>
            </dd>

            <dt className="label-overline">Type</dt>
            <dd className="tabular-nums">
              {payloadLabel(p.payloadType)}
              {p.payloadType != null && (
                <span className="text-muted-foreground/60">
                  {" "}
                  · {p.payloadType}
                </span>
              )}
            </dd>

            <dt className="label-overline">Route</dt>
            <dd className="tabular-nums">
              {p.route || (
                <span className="text-muted-foreground/60">—</span>
              )}
            </dd>

            <dt className="label-overline">Path Size</dt>
            <dd className="tabular-nums">
              {p.pathHashSize != null ? `${p.pathHashSize}B` : "—"}
            </dd>

            <dt className="label-overline">Path</dt>
            <dd className="break-all">
              <HopPath
                path={p.path}
                hashSize={p.pathHashSize}
                direction={p.direction}
                route={p.route}
                peers={peers}
              />
              {p.path && (
                <Link
                  to={mapPathHref({
                    path: p.path,
                    hashSize: p.pathHashSize,
                    direction: p.direction,
                    route: p.route,
                  })}
                  className="mt-1 inline-flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary"
                >
                  <MapPin className="size-3" />
                  on map
                </Link>
              )}
            </dd>

            <dt className="label-overline">Hops</dt>
            <dd className="tabular-nums">
              {p.hops != null ? (
                <>
                  {p.hops}
                  <span className="text-muted-foreground/60"> {hopSense(p.route)}</span>
                </>
              ) : (
                "—"
              )}
            </dd>

            <dt className="label-overline">Signal</dt>
            <dd className="tabular-nums">
              {p.snr != null ? (
                <span className={snrTextClass(p.snr)}>
                  {p.snr.toFixed(1)}dB
                  {p.rssi != null && (
                    <span className="text-muted-foreground/60">
                      {" "}
                      · RSSI {p.rssi}
                    </span>
                  )}
                </span>
              ) : (
                <span className="text-muted-foreground/60">——</span>
              )}
            </dd>

            <dt className="label-overline">First Seen</dt>
            <dd className="tabular-nums text-muted-foreground">
              {firstSeen ? formatDateTime(firstSeen) : "—"}
            </dd>

            <dt className="label-overline">Last Seen</dt>
            <dd className="tabular-nums text-muted-foreground">
              {lastSeen ? formatDateTime(lastSeen) : "—"}
            </dd>

            <dt className="label-overline">Times Heard</dt>
            <dd className="tabular-nums">{heard}</dd>
          </dl>
        </div>

        {heard > 1 && (
          <div className="border-b border-border">
            <div className="flex items-center justify-between px-5 py-3 border-b border-border">
              <span className="label-overline">Observations</span>
              <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
                {heard}
              </span>
            </div>
            {/* Fixed layout, or a long hop chain widens the table instead of wrapping: a 31-hop
                path stretched this to 3032px inside a 479px sheet. */}
            <Table className="table-fixed w-full">
              <TableHeader>
                <TableRow className="border-border hover:bg-transparent">
                  <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] pl-5">
                    Time
                  </TableHead>
                  <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-9 text-center">
                    Dir
                  </TableHead>
                  <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-20">
                    Route
                  </TableHead>
                  <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] w-16 text-right pr-5">
                    SNR
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedObs.map((o, i) => (
                  <TableRow
                    key={`${o.receivedAt}-${i}`}
                    className={cn(
                      "border-border/60",
                      o === p && "bg-primary/5",
                    )}
                  >
                    {/* TableCell ships whitespace-nowrap, which outranks table-fixed and would keep
                        a long hop chain on one line. */}
                    <TableCell className="pl-5 align-top space-y-0.5 whitespace-normal">
                      <div className="font-mono text-xs tabular-nums text-muted-foreground">
                        {formatDateTime(o.receivedAt)}
                        {o === p && (
                          <span className="ml-2 text-[10px] uppercase tracking-[0.08em] text-primary">
                            first
                          </span>
                        )}
                      </div>
                      <div className="[overflow-wrap:anywhere]">
                        <HopPath
                          path={o.path}
                          hashSize={o.pathHashSize}
                          direction={o.direction}
                          route={o.route}
                          peers={peers}
                          compact
                        />
                      </div>
                    </TableCell>
                    <TableCell className="text-center align-top">
                      <span
                        className={cn(
                          "font-mono text-sm",
                          o.direction === "tx"
                            ? "text-primary"
                            : "text-muted-foreground",
                        )}
                      >
                        {dirGlyph(o.direction)}
                      </span>
                    </TableCell>
                    <TableCell className="align-top font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                      {routeLabel(o)}
                    </TableCell>
                    <TableCell className="text-right align-top font-mono text-xs tabular-nums pr-5">
                      {o.snr != null ? (
                        <span className={snrTextClass(o.snr)}>
                          {o.snr.toFixed(1)}dB
                        </span>
                      ) : (
                        <span className="text-muted-foreground/40">——</span>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        <div className="px-5 py-4 space-y-2">
          <div className="flex items-center justify-between">
            <span className="label-overline">Raw</span>
            <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
              {Math.floor(p.raw.length / 2)} bytes
            </span>
          </div>
          <pre className="font-mono text-xs leading-relaxed bg-muted/40 border border-border p-3 break-all whitespace-pre-wrap">
            {p.raw || ""}
          </pre>
        </div>
      </div>
    </div>
  );
}

function PacketsSkeleton() {
  return (
    <div className="panel">
      <div className="px-4 py-3 border-b border-border">
        <Skeleton className="h-3 w-24 mb-2" />
        <Skeleton className="h-4 w-40" />
      </div>
      <div className="space-y-px p-4">
        {[...Array(10)].map((_, i) => (
          <div
            key={i}
            className="flex items-center gap-4 py-2 border-b border-border/60"
          >
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-3 w-6" />
            <Skeleton className="h-3 w-20" />
            <Skeleton className="h-3 flex-1" />
            <Skeleton className="h-3 w-16" />
          </div>
        ))}
      </div>
    </div>
  );
}

// Phone-sized form of the type chips — single-select, so radio items.
function PacketTypeMenu({
  value,
  onChange,
  allCount,
  types,
}: {
  value: number | "ALL";
  onChange: (v: number | "ALL") => void;
  allCount: number;
  types: [number, number][];
}) {
  const label = value === "ALL" ? "all" : payloadLabel(value);
  const count =
    value === "ALL"
      ? allCount
      : (types.find(([pt]) => pt === value)?.[1] ?? 0);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="sm:hidden relative inline-flex items-center gap-1.5 border border-border bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground before:absolute before:inset-x-0 before:-inset-y-2 before:content-['']"
        >
          <span className="text-foreground">{label}</span>
          <span className="tabular-nums text-muted-foreground/60">{count}</span>
          <ChevronDown className="size-3" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="rounded-sm">
        <DropdownMenuRadioGroup
          value={value === "ALL" ? "ALL" : String(value)}
          onValueChange={(v) => onChange(v === "ALL" ? "ALL" : Number(v))}
        >
          <DropdownMenuRadioItem
            value="ALL"
            className="font-mono text-[11px] uppercase tracking-[0.08em]"
          >
            All
            <span className="ml-auto pl-3 tabular-nums text-muted-foreground/70">
              {allCount}
            </span>
          </DropdownMenuRadioItem>
          {types.map(([pt, c]) => (
            <DropdownMenuRadioItem
              key={pt}
              value={String(pt)}
              className="font-mono text-[11px] uppercase tracking-[0.08em]"
            >
              {payloadLabel(pt)}
              <span className="ml-auto pl-3 tabular-nums text-muted-foreground/70">
                {c}
              </span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
