import { useEffect, useRef, useState } from "react";
import L from "leaflet";
import { Loader2, MapPin, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { configApi, type Region } from "@/lib/configApi";
import { themeTileLayer, useThemeTiles, wrapLon } from "@/lib/leaflet";
import { cn } from "@/lib/utils";

const OUTLINE: L.PathOptions = {
  color: "var(--primary)",
  weight: 1.5,
  fillOpacity: 0.15,
  interactive: false,
};

function meanLon(ring: [number, number][]): number {
  return ring.reduce((s, p) => s + p[0], 0) / ring.length;
}

// Each ring is moved by whole turns to sit nearest refLon, so a region beside the antimeridian draws beside its neighbours.
function toLatLngs(r: Region, refLon: number): L.LatLngTuple[][][] {
  return r.rings.map((ring) => {
    const d = Math.round((refLon - meanLon(ring)) / 360) * 360;
    return [ring.map(([lon, lat]) => [lat, lon + d] as L.LatLngTuple)];
  });
}

// Tap the map to pick the region under the tap, tap it again to drop it; ids are all the caller keeps.
export function RegionPicker({
  ids,
  onChange,
  suggest,
  className,
}: {
  ids: string[];
  onChange: (ids: string[]) => void;
  // a point whose region can be added without the map, such as the companion's position.
  suggest?: { label: string; lat: number; lon: number } | null;
  className?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<L.Map | null>(null);
  const tileRef = useRef<L.TileLayer | null>(null);
  const layerRef = useRef<L.LayerGroup | null>(null);
  const liveRef = useRef(true);
  const [regions, setRegions] = useState<Map<string, Region>>(new Map());
  const [failed, setFailed] = useState<Set<string>>(new Set());
  const [pending, setPending] = useState(0);
  // What the last tap did, or why it did nothing; read out, since the map itself says nothing.
  const [note, setNote] = useState<{ text: string; warn: boolean } | null>(null);
  // Updated as each answer lands, so two taps whose answers arrive before a render both count.
  const idsRef = useRef(ids);
  idsRef.current = ids;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  // A new bot has nothing to frame, so the view is the operator's from the start.
  const fittedRef = useRef(ids.length === 0);

  const pick = useRef(async (lat: number, lon: number, addOnly: boolean) => {
    setPending((n) => n + 1);
    setNote(null);
    try {
      const r = await configApi.regionAt(lat, wrapLon(lon));
      if (!liveRef.current) return;
      if (r === null) {
        setNote({ text: "No region there. Tap on land.", warn: true });
        return;
      }
      setRegions((prev) => new Map(prev).set(r.id, r));
      const cur = idsRef.current;
      const next = cur.includes(r.id)
        ? addOnly
          ? cur
          : cur.filter((x) => x !== r.id)
        : [...cur, r.id];
      idsRef.current = next;
      onChangeRef.current(next);
      if (next !== cur) {
        setNote({ text: `${next.includes(r.id) ? "Added" : "Removed"} ${r.name}, ${r.country}.`, warn: false });
      }
    } catch (e) {
      if (liveRef.current) {
        setNote({ text: e instanceof Error ? e.message : "Looking up the region failed", warn: true });
      }
    } finally {
      if (liveRef.current) setPending((n) => n - 1);
    }
  }).current;

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    liveRef.current = true;
    const map = L.map(containerRef.current, {
      zoomControl: true,
      attributionControl: true,
    }).setView([0, 0], 2);
    tileRef.current = themeTileLayer().addTo(map);
    layerRef.current = L.layerGroup().addTo(map);
    map.on("click", (e) => void pick(e.latlng.lat, e.latlng.lng, false));
    // Leaflet's keyboard pans and zooms; Enter picks what is under the centre, the keyboard's tap.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Enter") return;
      e.preventDefault();
      const c = map.getCenter();
      void pick(c.lat, c.lng, false);
    };
    const container = map.getContainer();
    container.addEventListener("keydown", onKey);
    mapRef.current = map;
    return () => {
      liveRef.current = false;
      container.removeEventListener("keydown", onKey);
      map.remove();
      mapRef.current = null;
      tileRef.current = null;
      layerRef.current = null;
    };
  }, [pick]);

  useThemeTiles(mapRef, tileRef);

  // A saved bot names its regions by id; fetch the outlines it has not got or failed to get.
  const missing = ids.filter((id) => !regions.has(id) && !failed.has(id)).join("\n");
  useEffect(() => {
    if (!missing) return;
    let live = true;
    const want = missing.split("\n");
    Promise.all(want.map((id) => configApi.region(id).catch(() => null))).then((got) => {
      if (!live) return;
      setRegions((prev) => {
        const next = new Map(prev);
        for (const r of got) if (r) next.set(r.id, r);
        return next;
      });
      const lost = want.filter((_, i) => got[i] === null);
      if (lost.length > 0) setFailed((prev) => new Set([...prev, ...lost]));
    });
    return () => {
      live = false;
    };
  }, [missing]);

  useEffect(() => {
    const map = mapRef.current;
    const layer = layerRef.current;
    if (!map || !layer) return;
    layer.clearLayers();
    const shown = ids.map((id) => regions.get(id)).filter((r): r is Region => !!r);
    const settled = ids.every((id) => regions.has(id) || failed.has(id));
    const refLon =
      fittedRef.current || shown.length === 0 ? map.getCenter().lng : meanLon(shown[0].rings[0]);
    for (const r of shown) L.polygon(toLatLngs(r, refLon), OUTLINE).addTo(layer);
    // Frame a saved selection once every outline has come back or failed; after that the view is the operator's.
    if (!fittedRef.current && settled) {
      fittedRef.current = true;
      const bounds = L.featureGroup(layer.getLayers()).getBounds();
      if (bounds.isValid()) map.fitBounds(bounds, { padding: [16, 16] });
    }
  }, [ids, regions, failed]);

  const lostCount = ids.filter((id) => failed.has(id)).length;

  return (
    <div className={cn("space-y-2", className)}>
      <Label className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        Tap a region to add or remove it
        {pending > 0 ? <Loader2 className="size-3 animate-spin" aria-label="Looking up" /> : null}
      </Label>
      <div
        ref={containerRef}
        aria-label="Region map. Arrow keys pan, plus and minus zoom, Enter adds or removes the region at the centre."
        className="h-64 sm:h-72 border border-border bg-muted"
      />
      <p
        role="status"
        className={cn(
          "font-mono text-[11px] empty:mb-0",
          note && !note.warn ? "text-muted-foreground" : "text-warning",
        )}
      >
        {note?.text ??
          (lostCount > 0 ? (
            <>
              {`Could not load ${lostCount} region outline${lostCount > 1 ? "s" : ""}. `}
              <button
                type="button"
                onClick={() => setFailed(new Set())}
                className="relative text-primary underline-offset-2 hover:underline before:absolute before:inset-x-0 before:-inset-y-3 before:content-[''] sm:before:hidden"
              >
                Try again
              </button>
            </>
          ) : null)}
      </p>
      {suggest ? (
        <Button
          variant="outline"
          size="sm"
          onClick={() => void pick(suggest.lat, suggest.lon, true)}
          className="h-auto min-h-8 whitespace-normal text-left rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          <MapPin className="size-3.5" />
          add {suggest.label}'s region
        </Button>
      ) : null}
      {ids.length > 0 ? (
        <ul className="flex flex-wrap gap-x-1.5 gap-y-4 sm:gap-y-1.5" aria-label="Chosen regions">
          {ids.map((id) => {
            const r = regions.get(id);
            return (
              <li key={id}>
                <button
                  type="button"
                  onClick={() => {
                    onChange(ids.filter((x) => x !== id));
                    setNote({ text: `Removed ${r?.name ?? id}.`, warn: false });
                  }}
                  className="relative inline-flex items-center gap-1.5 rounded-sm border border-primary/50 bg-card px-2 py-1 font-mono text-[11px] text-primary hover:border-primary before:absolute before:inset-x-0 before:-inset-y-2 before:content-[''] sm:before:hidden"
                  aria-label={`Remove ${r?.name ?? id}`}
                >
                  {r ? (
                    <>
                      {r.name}
                      <span className="text-muted-foreground">{r.country}</span>
                    </>
                  ) : (
                    id
                  )}
                  <X className="size-3" />
                </button>
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="font-mono text-[11px] text-muted-foreground">No regions chosen yet.</p>
      )}
    </div>
  );
}
