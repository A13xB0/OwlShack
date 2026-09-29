import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import L from "leaflet";
import { ChevronDown, Layers, MapPin, RefreshCw, Route, X } from "lucide-react";
import { toast } from "sonner";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { useResume } from "@/lib/resume";
import { useCompanions } from "@/hooks/useCompanions";
import { usePeerDetailSheet } from "@/hooks/usePeerDetailSheet";
import { isPeerDelete } from "@/lib/peerWs";
import { Button } from "@/components/ui/button";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { PageHeader } from "@/components/PageHeader";
import { ConnectionPill, PEER_TYPE_HEX } from "@/components/StatusIndicator";
import { InlineConfirm } from "@/components/InlineConfirm";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { PeerDetailSheet } from "@/components/PeerDetailSheet";
import { deletePeers, deletedPeersMessage } from "@/lib/peerApi";
import { peerLatLon, useThemeTiles, type BaseLayer } from "@/lib/leaflet";
import { pathEnds, resolveHops } from "@/lib/linkPath";
import { useOwnPosition } from "@/hooks/useOwnPosition";
import { drawLink, LINK_STAGGER } from "@/lib/mapLinks";
import { cn } from "@/lib/utils";

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lat: number;
  lon: number;
  lastSeen: string;
  snr: number | null;
  rssi: number | null;
  outPath?: string;
  outPathHashSize?: number;
}

interface NeighborLink {
  aPubkey: string;
  aName: string;
  aLat: number;
  aLon: number;
  bPubkey: string;
  bName: string;
  bLat: number;
  bLon: number;
  snrAtoB?: number;
  snrBtoA?: number;
  ts: number;
}

const TYPE_FILTERS = ["CHAT", "REPEATER", "ROOM", "SENSOR", "NONE"] as const;

const BASE_LAYERS: { value: BaseLayer; label: string }[] = [
  { value: "main", label: "Street" },
  { value: "satellite", label: "Satellite" },
  { value: "topo", label: "Topo" },
];
const BASE_KEY = "owlshack.map.base";

function storedBase(): BaseLayer {
  try {
    const v = window.localStorage.getItem(BASE_KEY);
    if (BASE_LAYERS.some((b) => b.value === v)) return v as BaseLayer;
  } catch {
    // storage blocked; the main map it is
  }
  return "main";
}

function isPeer(value: unknown): value is Peer {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return typeof v.pubkey === "string" && typeof v.name === "string";
}

function dotIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "meshcore-peer-dot",
    html: `<span style="display:grid; place-items:center; width:24px; height:24px;"><span style="display:block; width:10px; height:10px; border-radius:9999px; background:${color}; box-shadow: 0 0 0 2px rgba(0,0,0,0.55), 0 0 6px ${color};"></span></span>`,
    iconSize: [24, 24],
    iconAnchor: [12, 12],
    popupAnchor: [0, -8],
  });
}

// A distinct target marker for a deep-linked coordinate, so it stands out from the peer dots.
function focusIcon(): L.DivIcon {
  return L.divIcon({
    className: "meshcore-focus-pin",
    html: `<span style="display:flex; align-items:center; justify-content:center; width:20px; height:20px; border-radius:9999px; border:2px solid var(--primary); box-shadow: 0 0 0 2px rgba(0,0,0,0.55), 0 0 12px var(--primary);"><span style="display:block; width:6px; height:6px; border-radius:9999px; background:var(--primary);"></span></span>`,
    iconSize: [20, 20],
    iconAnchor: [10, 10],
    popupAnchor: [0, -12],
  });
}

// One DivIcon per peer type, built once — the markers-sync effect runs on every WS peer update.
const PEER_ICONS: Record<string, L.DivIcon> = Object.fromEntries(
  Object.entries(PEER_TYPE_HEX).map(([type, color]) => [type, dotIcon(color)]),
);
const FOCUS_ICON = focusIcon();

const NO_PEERS: Peer[] = [];

export function MapPage() {
  const {
    items,
    setItems: setPeers,
    loading,
    error,
    reload,
    refresh,
  } = useApiList<Peer>("/api/peers", "Failed to load peers");
  useResume(refresh);
  const peers = items ?? NO_PEERS;
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const companions = useCompanions();
  const { selectPeer, sheetProps } = usePeerDetailSheet(peers);

  const [showLinks, setShowLinks] = useState(false);
  const { items: links } = useApiList<NeighborLink>(
    showLinks ? "/api/nodes/neighbor-links" : null,
    "Failed to load neighbor links",
  );

  const [searchParams, setSearchParams] = useSearchParams();
  const focus = useMemo(() => {
    const la = parseFloat(searchParams.get("lat") ?? "");
    const lo = parseFloat(searchParams.get("lon") ?? "");
    if (!Number.isFinite(la) || !Number.isFinite(lo)) return null;
    if (la < -90 || la > 90 || lo < -180 || lo > 180) return null;
    return { lat: la, lon: lo };
  }, [searchParams]);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<L.Map | null>(null);
  const tileLayerRef = useRef<L.TileLayer | null>(null);
  const markersRef = useRef<Map<string, L.Marker>>(new Map());
  const focusMarkerRef = useRef<L.Marker | null>(null);
  const linksLayerRef = useRef<L.LayerGroup | null>(null);
  const pathLayerRef = useRef<L.LayerGroup | null>(null);
  const fittedRef = useRef(false);
  // Bumped on zoom so the links redraw: whether a label fits depends on the current scale.
  const [zoomTick, setZoomTick] = useState(0);

  const handleMessage = useCallback(
    (topic: string, data: unknown) => {
      if (topic !== "peers") return;
      if (isPeerDelete(data)) {
        const gone = new Set(data.pubkeys.map((k) => k.toLowerCase()));
        setPeers((prev) =>
          (prev ?? []).filter((p) => !gone.has(p.pubkey.toLowerCase())),
        );
        return;
      }
      if (!isPeer(data)) return;
      setPeers((prev) => {
        const arr = prev ?? [];
        const idx = arr.findIndex((p) => p.pubkey === data.pubkey);
        if (idx === -1) return [data, ...arr];
        const next = arr.slice();
        next[idx] = { ...next[idx], ...data };
        return next;
      });
    },
    [setPeers],
  );

  const { connected, pending } = useWebSocket(["peers"], handleMessage);

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    const map = L.map(containerRef.current, {
      center: [0, 0],
      zoom: 2,
      worldCopyJump: true,
      zoomControl: true,
      attributionControl: true,
    });
    linksLayerRef.current = L.layerGroup().addTo(map);
    pathLayerRef.current = L.layerGroup().addTo(map);
    map.on("zoomend", () => setZoomTick((t) => t + 1));
    mapRef.current = map;

    return () => {
      map.remove();
      mapRef.current = null;
      tileLayerRef.current = null;
      linksLayerRef.current = null;
      pathLayerRef.current = null;
      markersRef.current.clear();
      focusMarkerRef.current = null;
      fittedRef.current = false;
    };
  }, []);

  const [base, setBase] = useState<BaseLayer>(storedBase);
  useThemeTiles(mapRef, tileLayerRef, base);
  const chooseBase = (b: BaseLayer) => {
    setBase(b);
    try {
      window.localStorage.setItem(BASE_KEY, b);
    } catch {
      // the choice just lasts until the page closes
    }
  };

  // A ?lat=&lon= deep link marks the view fitted so the peer auto-fit can't yank it away.
  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    if (!focus) {
      if (focusMarkerRef.current) {
        map.removeLayer(focusMarkerRef.current);
        focusMarkerRef.current = null;
      }
      return;
    }
    map.setView([focus.lat, focus.lon], 15);
    fittedRef.current = true;
    if (focusMarkerRef.current) {
      focusMarkerRef.current.setLatLng([focus.lat, focus.lon]);
    } else {
      focusMarkerRef.current = L.marker([focus.lat, focus.lon], {
        icon: FOCUS_ICON,
        zIndexOffset: 1000,
      })
        .addTo(map)
        .bindPopup(
          `<div style="font-family: var(--font-mono); font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em;">shared location</div><div style="font-family: var(--font-mono); font-size: 11px; color: var(--muted-foreground);">${focus.lat}, ${focus.lon}</div>`,
        );
    }
    focusMarkerRef.current.openPopup();
  }, [focus]);

  const located = useMemo(
    () => peers.filter((p) => p.lat !== 0 || p.lon !== 0),
    [peers],
  );

  const ownPos = useOwnPosition();

  // Only an explicit "on map" link puts a path up. Selecting a marker opens its details and leaves
  // the map exactly as it was.
  const activePath = useMemo(() => {
    const hex = searchParams.get("path");
    if (!hex) return null;
    return {
      hex,
      hashSize: Number(searchParams.get("hs")) || 1,
      origin: searchParams.get("origin") ?? undefined,
      direction: searchParams.get("dir") ?? undefined,
      route: searchParams.get("route") ?? undefined,
    };
  }, [searchParams]);

  // Resolves the path to map nodes once: the dashed runs, which peers to keep plotted, and the
  // chip's label. A hop we cannot place ends the run instead of bridging its neighbours — a leg
  // drawn across an unplaceable hop asserts a link that was never reported.
  const pathView = useMemo(() => {
    if (!activePath) return null;
    const byPubkey = new Map(peers.map((p) => [p.pubkey, p]));
    const origin = activePath.origin ? byPubkey.get(activePath.origin) : undefined;
    const hops = resolveHops(activePath.hex, activePath.hashSize, peers).map((h) =>
      h.peer ? byPubkey.get(h.peer.pubkey) : undefined,
    );
    const at = (p?: Peer): [number, number] | null =>
      p && (p.lat !== 0 || p.lon !== 0) ? peerLatLon(p.lat, p.lon) : null;

    const { weLead, weTrail } = pathEnds(activePath.direction, activePath.route);
    const points: ([number, number] | null)[] = [
      ...(weLead ? [ownPos] : [at(origin)]),
      ...hops.map(at),
      ...(weTrail ? [ownPos] : []),
    ];

    const runs: [number, number][][] = [];
    let run: [number, number][] = [];
    for (const pt of points) {
      if (pt) {
        run.push(pt);
      } else {
        if (run.length > 1) runs.push(run);
        run = [];
      }
    }
    if (run.length > 1) runs.push(run);

    const nodes = new Set(
      [origin, ...hops].filter((p): p is Peer => !!p).map((p) => p.pubkey),
    );
    return { runs, nodes, label: origin?.name ?? "packet path" };
  }, [activePath, peers, ownPos]);

  useEffect(() => {
    const layer = pathLayerRef.current;
    if (!layer) return;
    layer.clearLayers();
    for (const run of pathView?.runs ?? []) {
      L.polyline(run, {
        color: "var(--primary)",
        weight: 2,
        opacity: 0.9,
        dashArray: "6 6",
        // Every vertex is a node: Leaflet's default simplification drops one whose neighbour is
        // within a pixel, which silently erases a hop from the drawing.
        smoothFactor: 0,
        interactive: false,
      }).addTo(layer);
    }
  }, [pathView]);

  // Frame the path itself, or the plotted peers come back at whatever zoom the last one left.
  useEffect(() => {
    const map = mapRef.current;
    if (!map || !pathView) return;
    const bounds = L.latLngBounds(pathView.runs.flat());
    if (bounds.isValid()) map.fitBounds(bounds, { padding: [60, 60], maxZoom: 12 });
  }, [pathView]);

  const clearPath = useCallback(() => {
    const next = new URLSearchParams(searchParams);
    for (const k of ["path", "hs", "origin", "dir", "route"]) next.delete(k);
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  const clearPin = useCallback(() => {
    const next = new URLSearchParams(searchParams);
    next.delete("lat");
    next.delete("lon");
    setSearchParams(next, { replace: true });
    const bounds = L.latLngBounds([...markersRef.current.values()].map((m) => m.getLatLng()));
    if (bounds.isValid()) mapRef.current?.fitBounds(bounds, { padding: [40, 40], maxZoom: 12 });
  }, [searchParams, setSearchParams]);

  // Showing a path drops every peer that is not on it — the point of plotting one is to read it,
  // and 300 unrelated dots is what made that hard.
  const plotted = useMemo(() => {
    const byType = located.filter((p) => !hidden.has(p.type));
    return pathView ? byType.filter((p) => pathView.nodes.has(p.pubkey)) : byType;
  }, [located, hidden, pathView]);

  const [confirmClear, setConfirmClear] = useState(false);
  const [clearing, setClearing] = useState(false);

  // Deletes exactly what's plotted, so the type pills double as a cleanup scope.
  const deleteShown = useCallback(async () => {
    setClearing(true);
    try {
      const result = await deletePeers(plotted.map((p) => p.pubkey));
      toast.success(deletedPeersMessage(result));
      setConfirmClear(false);
    } catch (e) {
      toast.error(`Delete failed: ${e instanceof Error ? e.message : "error"}`);
    } finally {
      setClearing(false);
    }
  }, [plotted]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;

    const next = new Set(plotted.map((p) => p.pubkey));
    const markers = markersRef.current;

    for (const [key, marker] of markers.entries()) {
      if (!next.has(key)) {
        map.removeLayer(marker);
        markers.delete(key);
      }
    }

    for (const p of plotted) {
      const [lat, lon] = peerLatLon(p.lat, p.lon);
      const icon = PEER_ICONS[p.type] || PEER_ICONS.NONE;
      const existing = markers.get(p.pubkey);
      if (existing) {
        existing.setLatLng([lat, lon]);
        // setIcon rebuilds the marker's DOM element — skip when unchanged.
        if (existing.getIcon() !== icon) existing.setIcon(icon);
      } else {
        const marker = L.marker([lat, lon], { icon }).addTo(map);
        // pubkey is stable for a marker, so this closure never goes stale.
        marker.on("click", () => selectPeer(p.pubkey));
        markers.set(p.pubkey, marker);
      }
    }

    if (!fittedRef.current && plotted.length > 0) {
      const bounds = L.latLngBounds(
        plotted.map((p) => peerLatLon(p.lat, p.lon)),
      );
      if (bounds.isValid()) {
        map.fitBounds(bounds, { padding: [40, 40], maxZoom: 12 });
        fittedRef.current = true;
      }
    }
  }, [plotted]);

  // Full clear-and-redraw: links are cheap and have no per-link identity to diff against.
  useEffect(() => {
    const map = mapRef.current;
    const layer = linksLayerRef.current;
    if (!map || !layer) return;
    layer.clearLayers();
    if (!showLinks || !links) return;

    links.forEach((l, i) => {
      drawLink(
        map,
        linksLayerRef.current!,
        peerLatLon(l.aLat, l.aLon),
        peerLatLon(l.bLat, l.bLon),
        l.snrAtoB ?? null,
        l.snrBtoA ?? null,
        LINK_STAGGER[i % LINK_STAGGER.length],
      );
    });
  }, [links, showLinks, zoomTick]);


  const toggleType = useCallback((type: string) => {
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(type)) next.delete(type);
      else next.add(type);
      return next;
    });
  }, []);

  const typeCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const p of located) counts[p.type] = (counts[p.type] || 0) + 1;
    return counts;
  }, [located]);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Map"
        meta={
          <span className="font-mono text-sm text-muted-foreground tabular-nums">
            {plotted.length}/{peers.length} with location
          </span>
        }
        actions={
          <>
            <Button
              variant="ghost"
              size="sm"
              onClick={reload}
              className="h-7 gap-1.5 px-2 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary"
            >
              <RefreshCw className={cn("size-3", loading && "animate-spin")} />
              refresh
            </Button>
            <ConnectionPill connected={connected} pending={pending} />
          </>
        }
      />

      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      <section className="panel overflow-hidden">
        <div className="flex flex-wrap items-center gap-2 px-4 py-3 border-b border-border">
          <span className="label-overline mr-2">Filter</span>
          <FilterMenu
            hidden={hidden}
            counts={typeCounts}
            onToggleType={toggleType}
            showLinks={showLinks}
            onToggleLinks={() => setShowLinks((v) => !v)}
          />
          {TYPE_FILTERS.map((t) => {
            const isHidden = hidden.has(t);
            const color = PEER_TYPE_HEX[t] || PEER_TYPE_HEX.NONE;
            const count = typeCounts[t] || 0;
            return (
              <button
                key={t}
                type="button"
                onClick={() => toggleType(t)}
                className={cn(
                  "hidden sm:inline-flex items-center gap-1.5 px-2 py-1 border border-border bg-card font-mono text-[10px] uppercase tracking-[0.12em] transition-all hover:border-foreground/40",
                  isHidden && "opacity-40 line-through",
                )}
              >
                <span
                  className="size-2 rounded-full"
                  style={{ background: color }}
                  aria-hidden
                />
                {t}
                <span className="tabular-nums text-muted-foreground/70">
                  {count}
                </span>
              </button>
            );
          })}
          <button
            type="button"
            onClick={() => setShowLinks((v) => !v)}
            className={cn(
              "ml-1 hidden sm:inline-flex items-center gap-1.5 border border-border bg-card px-2 py-1 pl-3 font-mono text-[10px] uppercase tracking-[0.12em] transition-all hover:border-foreground/40 border-l-2",
              showLinks ? "border-primary/60 text-primary" : "text-muted-foreground",
            )}
          >
            links
          </button>
          {pathView && (
            <button
              type="button"
              onClick={clearPath}
              className="inline-flex items-center gap-1.5 border border-primary/60 bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-primary transition-all hover:border-primary"
            >
              <Route className="size-3" />
              path: {pathView.label}
              <X className="size-3" />
            </button>
          )}
          {focus && (
            <button
              type="button"
              onClick={clearPin}
              className="inline-flex items-center gap-1.5 border border-primary/60 bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-primary transition-all hover:border-primary"
            >
              <MapPin className="size-3" />
              pin: {focus.lat}, {focus.lon}
              <X className="size-3" />
            </button>
          )}
          <div className="ml-auto flex items-center gap-3">
            {plotted.length > 0 &&
              (clearing ? (
                <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                  deleting…
                </span>
              ) : (
                <InlineConfirm
                  confirming={confirmClear}
                  onAskRemove={() => setConfirmClear(true)}
                  onCancel={() => setConfirmClear(false)}
                  onConfirm={deleteShown}
                  triggerLabel={`delete ${plotted.length} shown`}
                />
              ))}
            <span className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
              <MapPin className="size-3" />
              <span className="tabular-nums">
                {plotted.length}/{located.length}
              </span>
              <span className="text-muted-foreground/60">located</span>
            </span>
          </div>
        </div>

        <div className="relative">
          <div
            ref={containerRef}
            className="h-[calc(100dvh-260px-var(--bottom-nav))] min-h-105 w-full"
          />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label="Map layer"
                title="Map layer"
                className="absolute top-2.5 right-2.5 z-1000 grid size-10 md:size-8 place-items-center border border-border bg-card text-muted-foreground shadow-md hover:text-foreground data-[state=open]:text-primary"
              >
                <Layers className="size-4" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="rounded-sm">
              <DropdownMenuLabel className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                Map layer
              </DropdownMenuLabel>
              <DropdownMenuRadioGroup value={base} onValueChange={(v) => chooseBase(v as BaseLayer)}>
                {BASE_LAYERS.map((b) => (
                  <DropdownMenuRadioItem
                    key={b.value}
                    value={b.value}
                    className="font-mono text-[11px] uppercase tracking-[0.08em]"
                  >
                    {b.label}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </section>

      <PeerDetailSheet {...sheetProps} companions={companions} />
    </div>
  );
}

// Phone-sized form of the type pills, below sm only — the pills stay on wider screens.
function FilterMenu({
  hidden,
  counts,
  onToggleType,
  showLinks,
  onToggleLinks,
}: {
  hidden: Set<string>;
  counts: Record<string, number>;
  onToggleType: (type: string) => void;
  showLinks: boolean;
  onToggleLinks: () => void;
}) {
  const shown = TYPE_FILTERS.filter((t) => !hidden.has(t)).length;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="sm:hidden relative inline-flex items-center gap-1.5 border border-border bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground before:absolute before:inset-x-0 before:-inset-y-2 before:content-['']"
        >
          types
          <span className="tabular-nums text-foreground">
            {shown}/{TYPE_FILTERS.length}
          </span>
          {showLinks && <span className="text-primary">· links</span>}
          <ChevronDown className="size-3" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="rounded-sm">
        {TYPE_FILTERS.map((t) => (
          <DropdownMenuCheckboxItem
            key={t}
            checked={!hidden.has(t)}
            onCheckedChange={() => onToggleType(t)}
            onSelect={(e) => e.preventDefault()}
            className="font-mono text-[11px] uppercase tracking-[0.08em]"
          >
            <span
              className="size-2 rounded-full"
              style={{ background: PEER_TYPE_HEX[t] || PEER_TYPE_HEX.NONE }}
              aria-hidden
            />
            {t}
            <span className="ml-auto pl-3 tabular-nums text-muted-foreground/70">
              {counts[t] || 0}
            </span>
          </DropdownMenuCheckboxItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuCheckboxItem
          checked={showLinks}
          onCheckedChange={onToggleLinks}
          onSelect={(e) => e.preventDefault()}
          className="font-mono text-[11px] uppercase tracking-[0.08em]"
        >
          Neighbour links
        </DropdownMenuCheckboxItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
