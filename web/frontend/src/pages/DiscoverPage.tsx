import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Radar, List, Map as MapIcon } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerTypePill } from "@/components/StatusIndicator";
import { SignalStrength } from "@/components/SignalStrength";
import { HeaderButton } from "@/components/HeaderButton";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { useOwnPosition } from "@/hooks/useOwnPosition";
import { useCompanions } from "@/hooks/useCompanions";
import { usePeerDetailSheet } from "@/hooks/usePeerDetailSheet";
import { PeerDetailSheet, type PeerLike } from "@/components/PeerDetailSheet";
import { DiscoverMap, type DiscoverMapNode } from "@/components/DiscoverMap";
import { peerLatLon } from "@/lib/leaflet";
import { formatClockTime, formatSecsAgo } from "@/lib/format";
import {
  discoverApi,
  nodeTypeLabel,
  NODE_TYPES,
  type DiscoveryInfo,
  type DiscoveryState,
} from "@/lib/discoverApi";

const DEFAULT_TYPES = [2, 4];
const NO_PEERS: PeerLike[] = [];

// The same direction glyphs the packets list uses: down is what we received, up what we sent.
function SnrRow({ arrow, title, snr }: { arrow: string; title: string; snr: number }) {
  return (
    <div className="flex items-center gap-1.5" title={title}>
      <span className="font-mono text-xs text-muted-foreground/70" aria-label={title}>
        {arrow}
      </span>
      <SignalStrength snr={snr} size="md" />
    </div>
  );
}

export function DiscoverPage() {
  const [state, setState] = useState<DiscoveryState | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [types, setTypes] = useState<number[]>(DEFAULT_TYPES);
  const [starting, setStarting] = useState(false);
  const [view, setView] = useState("list");
  // What the last scan actually asked for: the chips can be changed afterwards, and a "nothing
  // answered" line must describe the scan that ran, not the one the user is about to run.
  const [scanned, setScanned] = useState<number[]>([]);
  const secsLeft = useRef(0);
  const [now, setNow] = useState(() => Date.now());

  // A discovery response carries only a key and two SNRs, so position and everything the detail
  // sheet shows comes from the peers we have already heard advert from.
  const { items } = useApiList<PeerLike>("/api/peers", "Failed to load peers");
  const peers = items ?? NO_PEERS;
  const companions = useCompanions();
  const origin = useOwnPosition();
  const { selectPeer, sheetProps } = usePeerDetailSheet(peers);

  const load = useCallback(async () => {
    try {
      const s = await discoverApi.state();
      setState(s);
      secsLeft.current = s.secsLeft;
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  // Tick the countdown locally rather than polling: the answers arrive over the socket, and the only
  // thing that changes every second is how long is left to wait.
  useEffect(() => {
    if (!state?.running) return;
    const id = setInterval(() => {
      setState((prev) => {
        if (!prev?.running) return prev;
        const left = prev.secsLeft - 1;
        return left <= 0 ? { ...prev, running: false, secsLeft: 0 } : { ...prev, secsLeft: left };
      });
    }, 1000);
    return () => clearInterval(id);
  }, [state?.running]);

  const onWs = useCallback((topic: string, data: unknown) => {
    if (topic !== "discovered" || !data) return;
    const found = data as DiscoveryInfo;
    setState((prev) =>
      prev
        ? { ...prev, results: [found, ...prev.results.filter((r) => r.pubkey !== found.pubkey)] }
        : prev,
    );
  }, []);
  useWebSocket(["discovered"], onWs);

  const scan = async () => {
    setStarting(true);
    try {
      const s = await discoverApi.start(types);
      setState(s);
      setScanned(types);
      setError("");
    } catch (e) {
      toast.error("Scan failed", { description: (e as Error).message });
    } finally {
      setStarting(false);
    }
  };

  const toggle = (t: number) =>
    setTypes((prev) => (prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t]));

  const running = state?.running ?? false;
  const results = useMemo(() => state?.results ?? [], [state?.results]);
  const scannedAt = state?.scanStartedAt ? formatClockTime(state.scanStartedAt) : "";
  // Empty after a reload: the server keeps the results but not what was asked for, so the wording
  // falls back to a claim that holds either way.
  const scannedLabel = NODE_TYPES.filter((t) => scanned.includes(t.value))
    .map((t) => t.label.toLowerCase())
    .join(" or ");
  const nothingFound = scannedLabel
    ? `Nothing answered the scan at ${scannedAt}. No ${scannedLabel} in direct radio range of this radio.`
    : `Nothing answered the scan at ${scannedAt}.`;

  // A node we have never heard advert from has no peer row, so there is nothing for the sheet to
  // show and no position to map. Those rows stay unclickable rather than opening an empty sheet.
  const known = useMemo(() => {
    const byKey = new Map(peers.map((p) => [p.pubkey.toLowerCase(), p]));
    return new Map(
      results.flatMap((r) => {
        const p = byKey.get(r.pubkey.toLowerCase());
        return p ? [[r.pubkey, p] as const] : [];
      }),
    );
  }, [peers, results]);

  const mapNodes: DiscoverMapNode[] = useMemo(
    () =>
      results.flatMap((r) => {
        const p = known.get(r.pubkey);
        // 0,0 is how an unset position arrives, not a node in the Atlantic.
        if (!p || !Number.isFinite(p.lat) || !Number.isFinite(p.lon) || (p.lat === 0 && p.lon === 0))
          return [];
        const [lat, lon] = peerLatLon(p.lat, p.lon);
        return [
          {
            pubkey: r.pubkey,
            name: r.name || p.name,
            snr: r.snr,
            reportedSnr: r.reportedSnr,
            lat,
            lon,
          },
        ];
      }),
    [results, known],
  );
  const unplaced = results.length - mapNodes.length;

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        eyebrow="Diagnostics"
        title="Discover"
        meta={
          <span className="font-mono text-xs text-muted-foreground">
            direct radio range only
            {scannedAt && ` · scanned ${scannedAt}`}
          </span>
        }
        actions={
          <HeaderButton
            tone="primary"
            icon={Radar}
            busy={starting || running}
            onClick={scan}
            disabled={starting || running || types.length === 0}
          >
            {starting || running ? `listening ${state?.secsLeft ?? 0}s` : "scan"}
          </HeaderButton>
        }
      />

      <div className="bg-card border border-border rounded-md px-4 py-3 flex flex-col gap-2">
        <span className="label-overline">Discover Nodes</span>
        <div className="flex flex-wrap gap-2">
          {NODE_TYPES.map((t) => (
            <button
              key={t.value}
              onClick={() => toggle(t.value)}
              disabled={running}
              className={
                types.includes(t.value)
                  ? "border border-primary/50 bg-primary/10 text-primary px-2.5 py-1 text-xs font-mono rounded-sm disabled:opacity-50"
                  : "border border-border text-muted-foreground px-2.5 py-1 text-xs font-mono rounded-sm disabled:opacity-50"
              }
            >
              {t.label}
            </button>
          ))}
        </div>
      </div>

      {!loaded ? (
        <Skeleton className="h-40 w-full" />
      ) : error ? (
        <div className="bg-card border border-border rounded-md px-4 py-6 text-sm text-muted-foreground">
          {error}
        </div>
      ) : results.length === 0 ? (
        <div className="bg-card border border-border rounded-md px-4 py-6 text-sm text-muted-foreground">
          {running
            ? "Listening. Responders wait a random moment before answering so they do not talk over each other, so late arrivals are normal."
            : scannedAt
              ? nothingFound
              : "Run a scan to see what this radio can hear."}
        </div>
      ) : (
        <Tabs value={view} onValueChange={setView} className="space-y-4">
          <TabsList>
            <TabsTrigger value="list">
              <List className="size-3.5" />
              List
            </TabsTrigger>
            <TabsTrigger value="map">
              <MapIcon className="size-3.5" />
              Map
            </TabsTrigger>
          </TabsList>

          <TabsContent value="list" className="mt-0">
            <div className="panel divide-y divide-border">
              {results.map((r) => {
                const peer = known.get(r.pubkey);
                return (
                  <button
                    type="button"
                    key={r.pubkey}
                    onClick={() => peer && selectPeer(peer.pubkey)}
                    disabled={!peer}
                    className="w-full text-left px-4 py-2.5 flex items-center gap-3 hover:bg-muted/30 disabled:hover:bg-transparent"
                  >
                    <PeerAvatar name={r.name || r.pubkey.slice(0, 12)} size="sm" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="font-mono text-sm truncate">
                          {r.name || <span className="text-muted-foreground italic">unknown</span>}
                        </span>
                        <PeerTypePill type={nodeTypeLabel(r.type).toUpperCase()} />
                      </div>
                      <code className="font-mono text-[10px] text-muted-foreground/70">
                        {r.pubkey.slice(0, 16)}…
                      </code>
                    </div>
                    <div className="flex flex-col items-end gap-0.5">
                      <SnrRow arrow="↓" title="we hear them" snr={r.snr} />
                      <SnrRow arrow="↑" title="they hear us" snr={r.reportedSnr} />
                      <div className="font-mono text-[10px] text-muted-foreground/70 tabular-nums">
                        {formatSecsAgo(
                          Math.max(0, Math.floor((now - new Date(r.heard).getTime()) / 1000)),
                        )}
                      </div>
                    </div>
                  </button>
                );
              })}
            </div>
          </TabsContent>

          <TabsContent value="map" className="mt-0 space-y-2">
            {mapNodes.length > 0 ? (
              <DiscoverMap nodes={mapNodes} origin={origin} onSelect={selectPeer} />
            ) : (
              <div className="bg-card border border-border rounded-md px-4 py-6 text-sm text-muted-foreground">
                Nothing that answered has a known position, so there is nothing to plot.
              </div>
            )}
            {mapNodes.length > 0 && !origin && (
              <p className="text-xs text-warning">
                No link lines: this node has no position configured, so there is nothing to draw
                them from. Set one on the companion or repeater.
              </p>
            )}
            {unplaced > 0 && (
              <p className="text-xs text-warning">
                {unplaced} of {results.length} answered without a position. A discovery response
                carries no coordinates, and there is no advert on record for{" "}
                {unplaced === 1 ? "that node" : "those nodes"} to take one from.
              </p>
            )}
          </TabsContent>
        </Tabs>
      )}

      <PeerDetailSheet {...sheetProps} companions={companions} />
    </div>
  );
}
