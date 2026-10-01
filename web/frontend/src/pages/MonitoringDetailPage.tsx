import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ExternalLink, MoreHorizontal, RefreshCw, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";
import { useWebSocket } from "@/hooks/useWebSocket";
import { BackLink } from "@/components/BackLink";
import { PageHeader } from "@/components/PageHeader";
import { NodeStatGrid } from "@/components/NodeStatTiles";
import { MetricChart, type SeriesPoint } from "@/components/MetricChart";
import { TrackMap, type TrackPoint } from "@/components/TrackMap";
import { MonitoringSettings } from "@/components/MonitoringSettings";
import { LinkMonitorSettings } from "@/components/LinkMonitorSettings";
import { HeaderButton } from "@/components/HeaderButton";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { pollNode } from "@/lib/nodesApi";
import { formatSecsAgo } from "@/lib/format";
import { metricDef, metricOrderIndex } from "@/lib/metrics";
import { contactDetailPath } from "@/lib/routes";
import {
  filterMetricNames,
  filterMetrics,
  hopDirectionLabel,
  type PathPeer,
} from "@/lib/linkPath";
import type { LinkMonitorInfo } from "@/components/LinkMonitorSettings";

interface MonitoredNode {
  pubkey: string;
  companionId: string;
  kind: string;
  name: string;
  lastPollTs: number;
  lastOkTs: number;
  lastError?: string;
  metrics: Record<string, number>;
}

interface WsMetrics {
  pubkey: string;
  companionId: string;
  kind: string;
  name: string;
  ts: number;
  metrics: Record<string, number>;
}

interface RangePreset {
  key: string;
  label: string;
  spanSecs: number;
  bucketSecs: number;
}

// 24h buckets at 60s — the minimum link retry delay — so a poll and its retry aren't AVG'd together.
const RANGES: RangePreset[] = [
  { key: "24h", label: "24h", spanSecs: 86400, bucketSecs: 60 },
  { key: "7d", label: "7d", spanSecs: 7 * 86400, bucketSecs: 3600 },
  { key: "30d", label: "30d", spanSecs: 30 * 86400, bucketSecs: 21600 },
  { key: "1y", label: "1y", spanSecs: 365 * 86400, bucketSecs: 86400 },
];

const nowSecs = () => Math.floor(Date.now() / 1000);

const HERO_METRICS = ["battery_mv", "last_snr"];

// lat/lon feed the track map rather than a chart; altitude stays charted.
const TRACK_METRICS = new Set(["location_lat", "location_lon"]);

// A link's headline is delivery plus first-hop SNR, picked dynamically since path length varies.
function linkHeroMetrics(available: string[]): string[] {
  const hopKeys = available
    .filter((m) => /^snr_hop\d+$/.test(m))
    .sort(
      (a, b) =>
        parseInt(a.slice("snr_hop".length), 10) -
        parseInt(b.slice("snr_hop".length), 10),
    );
  return ["success", hopKeys[0]].filter(
    (m): m is string => !!m && available.includes(m),
  );
}

// Undefined leaves MetricChart's default title in place.
function hopMetricLabel(
  metric: string,
  hopLabel?: (hop: number) => string,
): string | undefined {
  if (!hopLabel) return undefined;
  const m = metric.match(/^snr_hop(\d+)$/);
  return m ? hopLabel(parseInt(m[1], 10)) : undefined;
}

function orderGridMetrics(available: string[], hero: string[]): string[] {
  const heroSet = new Set(hero);
  return available
    // noChart metrics (e.g. uptime) live only as stat tiles in NodeStatGrid.
    .filter((m) => !heroSet.has(m) && !TRACK_METRICS.has(m) && !metricDef(m).noChart)
    .sort((a, b) => {
      const ia = metricOrderIndex(a);
      const ib = metricOrderIndex(b);
      if (ia === ib) return a.localeCompare(b);
      return (ia < 0 ? Infinity : ia) - (ib < 0 ? Infinity : ib);
    });
}

export function MonitoringDetailPage() {
  const { pubkey = "" } = useParams();
  const navigate = useNavigate();
  const [node, setNode] = useState<MonitoredNode | null>(null);
  const [available, setAvailable] = useState<string[]>([]);
  const [series, setSeries] = useState<Record<string, SeriesPoint[]>>({});
  const [range, setRange] = useState<RangePreset>(RANGES[0]);
  const [loading, setLoading] = useState(true);
  const [showSettings, setShowSettings] = useState(false);
  const [link, setLink] = useState<LinkMonitorInfo | null>(null);
  const [peers, setPeers] = useState<PathPeer[]>([]);

  const loadMeta = useCallback(() => {
    Promise.all([
      fetch("/api/nodes/monitored").then((r) => (r.ok ? r.json() : [])),
      fetch(`/api/nodes/${pubkey}/metrics`).then((r) => (r.ok ? r.json() : [])),
    ])
      .then(([nodes, names]: [MonitoredNode[], string[]]) => {
        setNode((nodes || []).find((n) => n.pubkey === pubkey) || null);
        setAvailable(names || []);
      })
      .catch(() => {});
  }, [pubkey]);

  useEffect(() => {
    loadMeta();
  }, [loadMeta]);

  // Link monitors only: resolves "snr_hopN" to real repeater names for titles.
  useEffect(() => {
    if (node?.kind !== "link") return;
    fetch("/api/links")
      .then((r) => (r.ok ? r.json() : []))
      .then((links: LinkMonitorInfo[]) =>
        setLink(
          (links || []).find(
            (l) => l.key.toLowerCase() === pubkey.toLowerCase(),
          ) || null,
        ),
      )
      .catch(() => {});
    fetch("/api/peers")
      .then((r) => (r.ok ? r.json() : []))
      .then((data: PathPeer[]) => setPeers(data || []))
      .catch(() => {});
  }, [node?.kind, pubkey]);

  const hopLabel = useMemo(
    () =>
      link ? hopDirectionLabel(link.path, link.pathHashSize, peers) : undefined,
    [link, peers],
  );

  // The link's settings hide first-hop/last-SNR readings everywhere they appear.
  const displayAvailable = useMemo(
    () =>
      filterMetricNames(
        available,
        !!link?.ignoreFirstHop,
        !!link?.hideLastSnr,
      ),
    [available, link?.ignoreFirstHop, link?.hideLastSnr],
  );

  const loadSeries = useCallback(() => {
    if (displayAvailable.length === 0) {
      setLoading(false);
      return;
    }
    setLoading(true);
    const to = nowSecs();
    const from = to - range.spanSecs;
    Promise.all(
      displayAvailable.map((metric) =>
        fetch(
          `/api/nodes/${pubkey}/history?metric=${encodeURIComponent(metric)}&from=${from}&to=${to}&bucket=${range.bucketSecs}`,
        )
          .then((r) => (r.ok ? r.json() : []))
          .then((pts: SeriesPoint[]) => [metric, pts || []] as const)
          .catch(() => [metric, []] as const),
      ),
    )
      .then((entries) => setSeries(Object.fromEntries(entries)))
      .finally(() => setLoading(false));
  }, [pubkey, displayAvailable, range]);

  useEffect(() => {
    loadSeries();
  }, [loadSeries]);

  const onWs = useCallback(
    (topic: string, payload: unknown) => {
      if (topic !== "metrics" || !payload) return;
      const m = payload as WsMetrics;
      if (m.pubkey !== pubkey) return;
      setNode((prev) => ({
        pubkey: m.pubkey,
        companionId: m.companionId || prev?.companionId || "",
        kind: m.kind,
        name: m.name || prev?.name || "",
        lastPollTs: m.ts,
        lastOkTs: m.ts,
        lastError: "",
        metrics: { ...(prev?.metrics || {}), ...m.metrics },
      }));
    },
    [pubkey],
  );

  useWebSocket(["metrics"], onWs);

  const { heroMetrics, gridMetrics } = useMemo(() => {
    const avail = new Set(displayAvailable);
    const hero =
      node?.kind === "link"
        ? linkHeroMetrics(displayAvailable)
        : HERO_METRICS.filter((m) => avail.has(m));
    return {
      heroMetrics: hero,
      gridMetrics: orderGridMetrics(displayAvailable, hero),
    };
  }, [displayAvailable, node?.kind]);
  const name = node?.name || pubkey.slice(0, 12);

  // Success pushes fresh metrics over WS, which `onWs` folds into the live stat tiles.
  const [polling, setPolling] = useState(false);
  const handlePoll = useCallback(() => {
    if (polling) return;
    setPolling(true);
    pollNode(pubkey)
      .then(() => toast.success(`Polled ${name}`))
      .catch((err) => toast.error(`${name}: ${err.message || "poll failed"}`))
      .finally(() => setPolling(false));
  }, [polling, pubkey, name]);

  // lat/lon (and alt) are recorded together each poll, so they share bucket timestamps.
  const trackPoints = useMemo<TrackPoint[]>(() => {
    const lat = series["location_lat"] || [];
    const lon = series["location_lon"] || [];
    if (!lat.length || !lon.length) return [];
    const lonByTs = new Map(lon.map((p) => [p.ts, p.value]));
    const altByTs = new Map((series["location_alt"] || []).map((p) => [p.ts, p.value]));
    const pts: TrackPoint[] = [];
    for (const p of lat) {
      const lo = lonByTs.get(p.ts);
      if (lo === undefined) continue;
      pts.push({ ts: p.ts, lat: p.value, lon: lo, alt: altByTs.get(p.ts) });
    }
    return pts;
  }, [series]);

  return (
    <div className="space-y-4">
      <BackLink to="/monitoring" label="monitoring" />

      <PageHeader
        title={name}
        meta={
          <span className="font-mono text-xs text-muted-foreground tabular-nums">
            {node?.kind || "node"} ·{" "}
            <LiveAgo ts={node?.lastOkTs ?? 0} prefix="polled" empty="never polled" />
          </span>
        }
        actions={
          <div className="flex items-center gap-2">
            <RangeSelector range={range} onChange={setRange} />
            <div className="hidden sm:flex items-center gap-2">
              <HeaderButton icon={RefreshCw} busy={polling} onClick={handlePoll} disabled={polling} title="Poll this node now">
                {polling ? "polling" : "poll"}
              </HeaderButton>
              {node?.companionId && node.kind !== "link" && (
                <HeaderButton icon={ExternalLink} to={contactDetailPath(node.companionId, pubkey, node.kind === "repeater")}>
                  {node.kind === "repeater" ? "repeater" : "contact"}
                </HeaderButton>
              )}
              {node?.companionId && (
                <HeaderButton icon={SlidersHorizontal} active={showSettings} onClick={() => setShowSettings((s) => !s)}>
                  configure
                </HeaderButton>
              )}
            </div>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <HeaderButton icon={MoreHorizontal} iconOnly className="sm:hidden">
                  Node actions
                </HeaderButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="rounded-none border-border w-48">
                <DropdownMenuItem onClick={handlePoll} disabled={polling} className="font-mono text-xs uppercase tracking-[0.08em]">
                  <RefreshCw className={cn("size-3.5", polling && "animate-spin")} /> {polling ? "polling" : "poll now"}
                </DropdownMenuItem>
                {node?.companionId && node.kind !== "link" && (
                  <DropdownMenuItem asChild className="font-mono text-xs uppercase tracking-[0.08em]">
                    <Link to={contactDetailPath(node.companionId, pubkey, node.kind === "repeater")}>
                      <ExternalLink className="size-3.5" /> {node.kind === "repeater" ? "repeater" : "contact"}
                    </Link>
                  </DropdownMenuItem>
                )}
                {node?.companionId && (
                  <DropdownMenuCheckboxItem
                    checked={showSettings}
                    onCheckedChange={(v) => setShowSettings(v === true)}
                    className="font-mono text-xs uppercase tracking-[0.08em]"
                  >
                    configure
                  </DropdownMenuCheckboxItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
      />

      {showSettings && node?.kind === "link" && (
        <LinkMonitorSettings
          linkKey={pubkey}
          onSaved={() => loadMeta()}
          onDeleted={() => navigate("/monitoring")}
        />
      )}
      {showSettings && node?.companionId && node.kind !== "link" && (
        <MonitoringSettings
          companionRef={node.companionId}
          pubkey={pubkey}
          kind={node.kind === "companion" ? "companion" : "repeater"}
          onSaved={() => loadMeta()}
        />
      )}

      {node && (
        <NodeStatGrid
          metrics={filterMetrics(
            node.metrics || {},
            !!link?.ignoreFirstHop,
            !!link?.hideLastSnr,
          )}
          className="grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 border border-border"
          hopLabel={hopLabel}
        />
      )}

      {trackPoints.length > 0 && (
        <section className="panel flex flex-col">
          <div className="flex items-baseline justify-between px-3 pt-2.5 pb-1.5">
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
              Track <span className="text-muted-foreground/40">· {trackPoints.length} pts</span>
            </span>
            <span className="font-mono text-sm tabular-nums" style={{ color: "var(--primary)" }}>
              {trackPoints[trackPoints.length - 1].lat.toFixed(5)},{" "}
              {trackPoints[trackPoints.length - 1].lon.toFixed(5)}
            </span>
          </div>
          <div className="px-1 pb-1">
            <TrackMap points={trackPoints} height={320} />
          </div>
        </section>
      )}

      {loading && Object.keys(series).length === 0 ? (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {[...Array(4)].map((_, i) => (
            <Skeleton key={i} className="h-56 w-full" />
          ))}
        </div>
      ) : available.length === 0 ? (
        <div className="panel py-16 text-center font-mono text-xs uppercase tracking-widest text-muted-foreground/60">
          No history recorded yet — first poll pending.
        </div>
      ) : (
        <>
          {heroMetrics.length > 0 && (
            <section
              className={cn(
                "grid gap-4",
                heroMetrics.length > 1 ? "lg:grid-cols-2" : "grid-cols-1",
              )}
            >
              {heroMetrics.map((metric) => (
                <MetricChart
                  key={metric}
                  metric={metric}
                  data={series[metric] || []}
                  spanSecs={range.spanSecs}
                  variant="hero"
                  height={260}
                  label={hopMetricLabel(metric, hopLabel)}
                />
              ))}
            </section>
          )}

          {gridMetrics.length > 0 && (
            <section className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
              {gridMetrics.map((metric) => (
                <MetricChart
                  key={metric}
                  metric={metric}
                  data={series[metric] || []}
                  spanSecs={range.spanSecs}
                  height={160}
                  label={hopMetricLabel(metric, hopLabel)}
                />
              ))}
            </section>
          )}
        </>
      )}
    </div>
  );
}

// Self-ticking so the relative time counts up without re-rendering the charts.
function LiveAgo({
  ts,
  prefix,
  empty,
}: {
  ts: number;
  prefix: string;
  empty: string;
}) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, []);
  if (!ts) return <>{empty}</>;
  const ageSecs = Math.max(0, nowSecs() - ts);
  return (
    <>
      {prefix} {formatSecsAgo(ageSecs)}
    </>
  );
}

function RangeSelector({
  range,
  onChange,
}: {
  range: RangePreset;
  onChange: (r: RangePreset) => void;
}) {
  return (
    <div className="flex items-center gap-px bg-border border border-border">
      {RANGES.map((r) => (
        <button
          key={r.key}
          type="button"
          onClick={() => onChange(r)}
          className={cn(
            "h-6.5 px-2.5 font-mono text-[11px] uppercase tracking-[0.12em] bg-card transition-colors",
            range.key === r.key
              ? "text-primary bg-primary/10"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {r.label}
        </button>
      ))}
    </div>
  );
}
