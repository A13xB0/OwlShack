import { useEffect } from "react";
import L from "leaflet";
import markerIcon from "leaflet/dist/images/marker-icon.png";
import markerIcon2x from "leaflet/dist/images/marker-icon-2x.png";
import markerShadow from "leaflet/dist/images/marker-shadow.png";

// Leaflet's default marker icon URLs break under bundlers; rebind them once here.
type MarkerProto = L.Icon.Default & { _getIconUrl?: () => string };
delete (L.Icon.Default.prototype as MarkerProto)._getIconUrl;
L.Icon.Default.mergeOptions({
  iconUrl: markerIcon,
  iconRetinaUrl: markerIcon2x,
  shadowUrl: markerShadow,
});

export type MapProvider = "osm" | "carto";
export type MapDarkStyle = "original" | "simplified";

const OSM_ATTRIBUTION =
  '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors';

// The main map is the Settings provider; the Map page can also switch to satellite or topo.
export type BaseLayer = "main" | "satellite" | "topo";

type Provider = {
  url: (dark: boolean, key: string) => string;
  attribution: string;
  className?: string;
  maxNativeZoom?: number;
};

// OSM Standard has no dark style, so index.css recolours .tiles-darken in dark mode, as data-map-dark picks.
const PROVIDERS: Record<MapProvider | Exclude<BaseLayer, "main">, Provider> = {
  osm: {
    url: () => "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
    attribution: OSM_ATTRIBUTION,
    className: "tiles-darken",
  },
  // https://carto.com/basemaps/apikey/
  carto: {
    url: (dark, key) =>
      `https://basemaps.cartocdn.com/rastertiles/${dark ? "dark_all" : "light_all"}/{z}/{x}/{y}{r}.png${key ? `?key=${encodeURIComponent(key)}` : ""}`,
    attribution: `${OSM_ATTRIBUTION} &copy; <a href="https://carto.com/attributions">CARTO</a>`,
  },
  satellite: {
    url: () => "https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}",
    attribution:
      'Powered by <a href="https://www.esri.com">Esri</a> &copy; Esri, Vantor, Earthstar Geographics, and the GIS User Community',
  },
  topo: {
    url: () => "https://tile.opentopomap.org/{z}/{x}/{y}.png",
    attribution: `${OSM_ATTRIBUTION}, SRTM &copy; <a href="https://opentopomap.org">OpenTopoMap</a> (CC-BY-SA)`,
    maxNativeZoom: 17,
  },
};

// null until the settings arrive, so no provider is asked for tiles the operator did not choose.
let tiles: { provider: MapProvider; key: string } | null = null;
const tilesChanged = new EventTarget();

export function setTiles(provider: MapProvider, key: string | null, darkStyle: MapDarkStyle) {
  tiles = { provider: provider === "carto" ? "carto" : "osm", key: key ?? "" };
  document.documentElement.dataset.mapDark = darkStyle;
  tilesChanged.dispatchEvent(new Event("change"));
}

fetch("/api/config/settings")
  .then((r) => (r.ok ? r.json() : null))
  .catch(() => null)
  .then((s: { mapProvider?: MapProvider; mapTileKey?: string | null; mapDarkStyle?: MapDarkStyle } | null) => {
    // a save beat the fetch; don't let it win
    if (!tiles) setTiles(s?.mapProvider ?? "osm", s?.mapTileKey ?? null, s?.mapDarkStyle ?? "original");
  });

export function wrapLon(lon: number): number {
  return (((lon + 180) % 360) + 360) % 360 - 180;
}

// Peer coordinates are firmware fixed-point at 1e6 degrees. Passing the raw values to Leaflet puts
// the marker nowhere at all.
export function peerLatLon(lat: number, lon: number): [number, number] {
  return [lat / 1e6, wrapLon(lon / 1e6)];
}

function layerSpec(base: BaseLayer): { p: Provider; url: string } | null {
  const name = base === "main" ? tiles?.provider : base;
  if (!name) return null;
  const p = PROVIDERS[name];
  return { p, url: p.url(document.documentElement.classList.contains("dark"), tiles?.key ?? "") };
}

const layerUrls = new WeakMap<L.TileLayer, string>();

export function themeTileLayer(base: BaseLayer = "main"): L.TileLayer {
  const spec = layerSpec(base);
  const layer = spec
    ? L.tileLayer(spec.url, {
        attribution: spec.p.attribution,
        className: spec.p.className,
        maxNativeZoom: spec.p.maxNativeZoom,
        maxZoom: 19,
      })
    : L.tileLayer("", { maxZoom: 19 });
  layerUrls.set(layer, spec?.url ?? "");
  return layer;
}

// Keeps tileRef's layer matching base, the theme and the tile settings, replacing it only when its URL would change.
export function useThemeTiles(
  mapRef: { current: L.Map | null },
  tileRef: { current: L.TileLayer | null },
  base: BaseLayer = "main",
) {
  useEffect(() => {
    const swap = () => {
      const map = mapRef.current;
      if (!map) return;
      if (tileRef.current && layerUrls.get(tileRef.current) === (layerSpec(base)?.url ?? "")) return;
      tileRef.current?.remove();
      tileRef.current = themeTileLayer(base).addTo(map);
    };
    swap();
    const obs = new MutationObserver(swap);
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });
    tilesChanged.addEventListener("change", swap);
    return () => {
      obs.disconnect();
      tilesChanged.removeEventListener("change", swap);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base]);
}
