import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowUpRight,
  CircleDashed,
  Compass,
  Crosshair,
  Radio,
  Signal,
  Users,
} from "lucide-react";
import { useWebSocket } from "@/hooks/useWebSocket";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { PageHeader } from "@/components/PageHeader";
import { PeerDetailSheet } from "@/components/PeerDetailSheet";
import { usePeerDetailSheet } from "@/hooks/usePeerDetailSheet";
import { isPeerDelete } from "@/lib/peerWs";
import {
  PEER_TYPE_HEX,
  PeerTypePill,
} from "@/components/StatusIndicator";
import { timeAgo, truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";
import { companionPath } from "@/lib/companionRef";

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lat: number;
  lon: number;
  lastSeen: string;
  snr: number | null;
  rssi: number | null;
}

interface Companion {
  id: number;
  name: string;
  pubkey: string;
  peerCount: number;
  channels?: { name: string }[];
}

interface DashboardData {
  peers: Peer[];
  companions: Companion[];
}

const NO_PEERS: Peer[] = [];

export function DashboardPage() {
  const [data, setData] = useState<DashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { selectPeer, sheetProps } = usePeerDetailSheet(data?.peers ?? NO_PEERS);

  const onWsMessage = useCallback((topic: string, payload: unknown) => {
    if (topic !== "peers" || !payload) return;
    if (isPeerDelete(payload)) {
      const gone = new Set(payload.pubkeys.map((k) => k.toLowerCase()));
      setData((prev) =>
        prev
          ? {
              ...prev,
              peers: prev.peers.filter((p) => !gone.has(p.pubkey.toLowerCase())),
            }
          : prev,
      );
      return;
    }
    const p = payload as Peer;
    setData((prev) => {
      if (!prev) return prev;
      const idx = prev.peers.findIndex((x) => x.pubkey === p.pubkey);
      const updated = [...prev.peers];
      if (idx >= 0) {
        updated[idx] = { ...updated[idx], ...p };
      } else {
        updated.push(p);
      }
      return { ...prev, peers: updated };
    });
  }, []);

  const { connected } = useWebSocket(["peers"], onWsMessage);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([
      fetch("/api/peers").then((r) => {
        if (!r.ok) throw new Error("peers");
        return r.json();
      }),
      fetch("/api/companions").then((r) => {
        if (!r.ok) throw new Error("companions");
        return r.json();
      }),
    ])
      .then(([peers, companions]) => {
        setData({ peers: peers || [], companions: companions || [] });
      })
      .catch(() => setError("Failed to load dashboard data"))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const stats = useMemo(() => {
    if (!data) return null;
    const withLocation = data.peers.filter(
      (p) => p.lat !== 0 || p.lon !== 0,
    );
    const recent = data.peers.filter(
      (p) => Date.now() - new Date(p.lastSeen).getTime() < 3600000,
    );
    const totalChannels = data.companions.reduce(
      (sum, c) => sum + (c.channels?.length || 0),
      0,
    );
    const typeCounts = data.peers.reduce<Record<string, number>>((acc, p) => {
      acc[p.type] = (acc[p.type] || 0) + 1;
      return acc;
    }, {});
    const recentSorted = [...data.peers]
      .sort(
        (a, b) =>
          new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime(),
      )
      .slice(0, 8);
    return {
      withLocation,
      recent,
      totalChannels,
      typeCounts,
      recentSorted,
    };
  }, [data]);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Overview"
        meta={
          stats && (
            <span className="font-mono text-sm text-muted-foreground tabular-nums">
              {stats.recent.length}/{data?.peers.length ?? 0} active · {timeAgo(new Date().toISOString())}
            </span>
          )
        }
      />

      {loading && <DashboardSkeleton />}
      {error && <LoadErrorAlert message={error} onRetry={load} />}

      {!loading && !error && data && stats && (
        <>
          <section className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-px bg-border border border-border">
            <StatCell
              label="Peers"
              value={data.peers.length.toString()}
              hint={`${stats.recent.length} active · 60m`}
              icon={<Users className="size-4" strokeWidth={1.5} />}
            />
            <StatCell
              label="Companions"
              value={data.companions.length.toString()}
              hint={`${stats.totalChannels} channels`}
              icon={<Radio className="size-4" strokeWidth={1.5} />}
            />
            <StatCell
              label="Geolocated"
              value={stats.withLocation.length.toString()}
              hint={`of ${data.peers.length} known peers`}
              icon={<Compass className="size-4" strokeWidth={1.5} />}
            />
            <StatCell
              label="Channel Activity"
              value={
                stats.totalChannels === 0 ? "—" : stats.totalChannels.toString()
              }
              hint={connected ? "stream open" : "stream closed"}
              icon={<Signal className="size-4" strokeWidth={1.5} />}
            />
          </section>

          <section className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <div className="panel lg:col-span-2 overflow-hidden">
              <SectionTitle
                title="Recently seen"
                eyebrow="Live · last 8"
                trailing={
                  <Link
                    to="/peers"
                    className="text-mono-xs text-muted-foreground hover:text-primary inline-flex items-center gap-1"
                  >
                    open all <ArrowUpRight className="size-3" />
                  </Link>
                }
              />
              <Table>
                <TableHeader>
                  <TableRow className="border-border hover:bg-transparent">
                    <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                      Name
                    </TableHead>
                    <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                      Type
                    </TableHead>
                    <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] hidden sm:table-cell">
                      Pubkey
                    </TableHead>
                    <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] text-right">
                      Last
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {stats.recentSorted.length === 0 ? (
                    <TableRow>
                      <TableCell
                        colSpan={4}
                        className="text-center py-12 text-sm text-muted-foreground/60"
                      >
                        <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                        Awaiting first contact…
                      </TableCell>
                    </TableRow>
                  ) : (
                    stats.recentSorted.map((p) => (
                      <TableRow
                        key={p.pubkey}
                        onClick={() => selectPeer(p.pubkey)}
                        className="border-border/60 group cursor-pointer"
                      >
                        <TableCell className="font-medium">
                          {p.name || (
                            <span className="text-muted-foreground italic">
                              unknown
                            </span>
                          )}
                        </TableCell>
                        <TableCell>
                          <PeerTypePill type={p.type} />
                        </TableCell>
                        <TableCell className="hidden sm:table-cell">
                          <code className="font-mono text-xs text-muted-foreground">
                            {truncateMid(p.pubkey, 6, 4)}
                          </code>
                        </TableCell>
                        <TableCell className="text-right font-mono text-xs tabular-nums text-muted-foreground">
                          {timeAgo(p.lastSeen)}
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </div>

            <div className="space-y-6">
              <div className="panel">
                <SectionTitle eyebrow="Spectrum" title="Peer types" />
                <div className="px-4 pb-4 space-y-2.5">
                  {Object.keys(stats.typeCounts).length === 0 ? (
                    <p className="text-sm text-muted-foreground/60">
                      No peers discovered yet.
                    </p>
                  ) : (
                    Object.entries(stats.typeCounts)
                      .sort((a, b) => b[1] - a[1])
                      .map(([type, count]) => {
                        const pct = (count / data.peers.length) * 100;
                        return (
                          <div key={type} className="space-y-1">
                            <div className="flex items-baseline justify-between font-mono text-xs">
                              <span className="uppercase tracking-[0.08em]">
                                {type}
                              </span>
                              <span className="tabular-nums text-muted-foreground">
                                {count}
                                <span className="text-muted-foreground/40">
                                  {" "}
                                  · {pct.toFixed(0)}%
                                </span>
                              </span>
                            </div>
                            <div className="h-1 bg-muted relative overflow-hidden">
                              <div
                                className="absolute inset-y-0 left-0"
                                style={{
                                  width: `${pct}%`,
                                  background:
                                    PEER_TYPE_HEX[type] || PEER_TYPE_HEX.NONE,
                                }}
                              />
                            </div>
                          </div>
                        );
                      })
                  )}
                </div>
              </div>

              <div className="panel">
                <SectionTitle eyebrow="Comms" title="Companions" />
                <div className="divide-y divide-border">
                  {data.companions.length === 0 ? (
                    <p className="text-sm text-muted-foreground/60 px-4 py-6 text-center">
                      None configured.
                    </p>
                  ) : (
                    data.companions.map((c) => (
                      <Link
                        key={c.name}
                        to={companionPath(c)}
                        className="flex items-center justify-between px-4 py-3 hover:bg-muted/40 transition-colors group"
                      >
                        <div className="space-y-0.5">
                          <div className="text-sm font-medium">{c.name}</div>
                          <div className="text-mono-xs text-muted-foreground">
                            {c.peerCount} peers · {c.channels?.length || 0} ch
                          </div>
                        </div>
                        <Crosshair className="size-3.5 text-muted-foreground/40 group-hover:text-primary transition-colors" />
                      </Link>
                    ))
                  )}
                </div>
              </div>
            </div>
          </section>
        </>
      )}

      <PeerDetailSheet {...sheetProps} companions={data?.companions ?? []} />
    </div>
  );
}

function StatCell({
  label,
  value,
  hint,
  icon,
  accent,
}: {
  label: string;
  value: string;
  hint?: string;
  icon?: React.ReactNode;
  accent?: boolean;
}) {
  return (
    <div
      className={cn(
        "bg-card relative px-5 py-5 flex flex-col gap-2 group",
        accent && "bg-linear-to-br from-primary/5 via-card to-card",
      )}
    >
      <div className="flex items-center justify-between">
        <span className="label-overline">{label}</span>
        <span className="text-muted-foreground/60 group-hover:text-primary transition-colors">
          {icon}
        </span>
      </div>
      <div className="flex items-baseline gap-2">
        <span
          className={cn(
            "font-mono text-3xl font-semibold tabular-nums leading-none",
            accent && "text-primary",
          )}
        >
          {value}
        </span>
      </div>
      {hint && (
        <span className="text-mono-xs text-muted-foreground">{hint}</span>
      )}
    </div>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-px bg-border">
        {[...Array(4)].map((_, i) => (
          <div key={i} className="bg-card p-5 space-y-3">
            <Skeleton className="h-3 w-20" />
            <Skeleton className="h-8 w-16" />
            <Skeleton className="h-2 w-24" />
          </div>
        ))}
      </div>
      <Skeleton className="h-64 w-full" />
    </div>
  );
}

