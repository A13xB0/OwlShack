import { useEffect, useRef } from "react";
import L from "leaflet";
import { Label } from "@/components/ui/label";
import { themeTileLayer, useThemeTiles, wrapLon } from "@/lib/leaflet";
import { cn } from "@/lib/utils";

// lat/lon may be NaN (blank inputs) — the map then shows the world with no pin. radiusKm, when
// given, draws a circle round the pin and frames it as the radius changes.
export function PositionPicker({
  lat,
  lon,
  onPick,
  radiusKm,
  className,
}: {
  lat: number;
  lon: number;
  onPick: (lat: number, lon: number) => void;
  radiusKm?: number;
  className?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<L.Map | null>(null);
  const markerRef = useRef<L.Marker | null>(null);
  const circleRef = useRef<L.Circle | null>(null);
  const tileRef = useRef<L.TileLayer | null>(null);
  const onPickRef = useRef(onPick);
  onPickRef.current = onPick;

  const valid = Number.isFinite(lat) && Number.isFinite(lon);

  useEffect(() => {
    if (!containerRef.current || mapRef.current) return;
    const map = L.map(containerRef.current, {
      zoomControl: true,
      attributionControl: true,
    }).setView(valid ? [lat, lon] : [0, 0], valid ? 13 : 2);
    tileRef.current = themeTileLayer().addTo(map);
    map.on("click", (e) => onPickRef.current(e.latlng.lat, wrapLon(e.latlng.lng)));
    mapRef.current = map;
    return () => {
      map.remove();
      mapRef.current = null;
      tileRef.current = null;
      markerRef.current = null;
      circleRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useThemeTiles(mapRef, tileRef);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    if (!valid) {
      markerRef.current?.remove();
      markerRef.current = null;
      return;
    }
    // Drawn on the world copy nearest the view, so a pick past the antimeridian stays where it was tapped.
    const at: L.LatLngTuple = [lat, nearView(map, lon)];
    if (!markerRef.current) {
      markerRef.current = L.marker(at, { draggable: true })
        .addTo(map)
        .on("dragend", (e) => {
          const { lat: la, lng: ln } = (e.target as L.Marker).getLatLng();
          onPickRef.current(la, wrapLon(ln));
        });
      map.setView(at, Math.max(map.getZoom(), 12));
    } else {
      markerRef.current.setLatLng(at);
      if (!map.getBounds().contains(at)) map.panTo(at);
    }
  }, [lat, lon, valid]);

  const radiusM = valid && radiusKm && radiusKm > 0 ? radiusKm * 1000 : 0;
  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    if (!radiusM) {
      circleRef.current?.remove();
      circleRef.current = null;
      return;
    }
    const at: L.LatLngTuple = [lat, nearView(map, lon)];
    if (!circleRef.current) {
      circleRef.current = L.circle(at, {
        radius: radiusM,
        color: "var(--primary)",
        weight: 1.5,
        fillOpacity: 0.12,
        interactive: false,
      }).addTo(map);
    } else {
      circleRef.current.setLatLng(at).setRadius(radiusM);
    }
  }, [lat, lon, radiusM]);

  // Only a new radius reframes, so dragging the pin leaves the view where the operator put it.
  useEffect(() => {
    const circle = circleRef.current;
    if (circle) mapRef.current?.fitBounds(circle.getBounds(), { padding: [16, 16] });
  }, [radiusM]);

  return (
    <div className={cn("space-y-1", className)}>
      <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        Pick from map · tap or drag pin
      </Label>
      <div ref={containerRef} className="h-56 sm:h-64 border border-border bg-muted" />
    </div>
  );
}

function nearView(map: L.Map, lon: number): number {
  return lon + Math.round((map.getCenter().lng - lon) / 360) * 360;
}

// 6 decimals (~11 cm) is what the inputs display, so a pick and a typed value round-trip identically.
export function round6(v: number): string {
  return (Math.round(v * 1e6) / 1e6).toString();
}
