import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  Check,
  ChevronDown,
  CircleDashed,
  RefreshCw,
  Search,
  Users,
} from "lucide-react";
import { toast } from "sonner";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { useResume } from "@/lib/resume";
import { useCompanions } from "@/hooks/useCompanions";
import { usePeerDetailSheet } from "@/hooks/usePeerDetailSheet";
import { isPeerDelete } from "@/lib/peerWs";
import { deletePeers, deletedPeersMessage } from "@/lib/peerApi";
import { InlineConfirm } from "@/components/InlineConfirm";
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { PageHeader } from "@/components/PageHeader";
import { PeerTypePill } from "@/components/StatusIndicator";
import { SignalStrength } from "@/components/SignalStrength";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerDetailSheet, advertPathInfo } from "@/components/PeerDetailSheet";
import { mapPathHref } from "@/lib/linkPath";
import { timeAgo, truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lat: number;
  lon: number;
  lastSeen: string;
  lastAdvertTs?: number;
  snr: number | null;
  rssi: number | null;
  outPath?: string;
  outPathHashSize?: number;
}

// Stops propagation so ticking a row doesn't also open its detail sheet.
function SelectBox({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: () => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onChange();
      }}
      className={cn(
        "relative flex size-4 shrink-0 items-center justify-center border transition-colors before:absolute before:-inset-3 before:content-[''] sm:before:hidden",
        checked
          ? "border-primary bg-primary text-primary-foreground"
          : "border-border hover:border-primary/60",
      )}
    >
      {checked && <Check className="size-3" />}
    </button>
  );
}

function HopsBadge({ peer }: { peer: Peer }) {
  const { hops } = advertPathInfo(peer.outPath, peer.outPathHashSize);
  if (hops === 0) {
    return (
      <span className="font-mono text-[10px] uppercase tracking-[0.08em] text-success/80">
        direct
      </span>
    );
  }
  return (
    <Link
      to={mapPathHref({
        path: peer.outPath ?? "",
        hashSize: peer.outPathHashSize,
        origin: peer.pubkey,
      })}
      onClick={(e) => e.stopPropagation()}
      className="font-mono text-xs tabular-nums text-muted-foreground underline decoration-dotted decoration-muted-foreground/40 underline-offset-2 hover:text-primary hover:decoration-primary"
    >
      {hops} hop{hops > 1 ? "s" : ""}
    </Link>
  );
}

const TYPE_FILTERS = ["ALL", "CHAT", "REPEATER", "ROOM", "SENSOR", "NONE"] as const;
type TypeFilter = (typeof TYPE_FILTERS)[number];

const NO_PEERS: Peer[] = [];

function isPeer(value: unknown): value is Peer {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return typeof v.pubkey === "string" && typeof v.name === "string";
}

export function PeersPage() {
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
  const companions = useCompanions();
  const { selectPeer, sheetProps } = usePeerDetailSheet(peers);
  const [search, setSearch] = useState("");
  const [typeFilter, setTypeFilter] = useState<TypeFilter>("ALL");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirmBulk, setConfirmBulk] = useState(false);
  const [bulkDeleting, setBulkDeleting] = useState(false);

  const toggleSelect = useCallback((pubkey: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(pubkey)) next.delete(pubkey);
      else next.add(pubkey);
      return next;
    });
  }, []);

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

  useWebSocket(["peers"], handleMessage);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return peers
      .filter((p) => {
        if (typeFilter !== "ALL" && p.type !== typeFilter) return false;
        if (!q) return true;
        return (
          (p.name || "").toLowerCase().includes(q) ||
          (p.pubkey || "").toLowerCase().includes(q)
        );
      })
      .sort(
        (a, b) =>
          new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime(),
      );
  }, [peers, search, typeFilter]);

  const typeCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const p of peers) counts[p.type] = (counts[p.type] || 0) + 1;
    return counts;
  }, [peers]);

  const withLocation = useMemo(
    () => peers.filter((p) => p.lat !== 0 || p.lon !== 0).length,
    [peers],
  );

  const allFilteredSelected =
    filtered.length > 0 && filtered.every((p) => selected.has(p.pubkey));

  // Selections outside the current filter are preserved, so filtering builds one up.
  const toggleSelectAll = useCallback(() => {
    setSelected((prev) => {
      const next = new Set(prev);
      const allOn = filtered.every((p) => next.has(p.pubkey));
      for (const p of filtered) {
        if (allOn) next.delete(p.pubkey);
        else next.add(p.pubkey);
      }
      return next;
    });
  }, [filtered]);

  const bulkDelete = useCallback(async () => {
    setBulkDeleting(true);
    try {
      toast.success(deletedPeersMessage(await deletePeers([...selected])));
      setSelected(new Set()); // the WS "delete" broadcast prunes the list
      setConfirmBulk(false);
    } catch (e) {
      toast.error(
        `Bulk delete failed: ${e instanceof Error ? e.message : "error"}`,
      );
    } finally {
      setBulkDeleting(false);
    }
  }, [selected]);

  return (
    <div className="space-y-4">
      <PageHeader
        title="Peers"
        meta={
          <span className="font-mono text-sm text-muted-foreground tabular-nums">
            {filtered.length}/{peers.length} · {withLocation} located
          </span>
        }
        actions={
          <HeaderButton icon={RefreshCw} busy={loading} onClick={reload}>
            {loading ? "refreshing" : "refresh"}
          </HeaderButton>
        }
      />

      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      <section className="panel">
        <div className="flex flex-col gap-3 px-4 py-3 border-b border-border sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-0.5">
            <span className="label-overline block">Roster</span>
            <h2 className="font-mono text-sm uppercase tracking-widest">
              Discovered peers
            </h2>
          </div>
          <div className="relative w-full sm:w-72">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground/60" />
            <Input
              type="search"
              placeholder="search name or pubkey…"
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
          <PeerTypeMenu
            value={typeFilter}
            onChange={setTypeFilter}
            countFor={(t) => (t === "ALL" ? peers.length : typeCounts[t] || 0)}
          />
          {TYPE_FILTERS.map((t) => {
            const active = typeFilter === t;
            const count = t === "ALL" ? peers.length : typeCounts[t] || 0;
            return (
              <button
                key={t}
                type="button"
                onClick={() => setTypeFilter(t)}
                className={cn(
                  "hidden sm:inline-flex items-center gap-1.5 px-2 py-1 border font-mono text-[10px] uppercase tracking-[0.12em] transition-colors",
                  active
                    ? "border-primary/50 bg-primary/10 text-primary"
                    : "border-border bg-transparent text-muted-foreground hover:text-foreground hover:border-border",
                )}
              >
                {t}
                <span className="tabular-nums text-muted-foreground/60">
                  {count}
                </span>
              </button>
            );
          })}
        </div>

        {/* Bulk selection bar */}
        {selected.size > 0 && (
          <div className="flex items-center justify-between gap-3 border-b border-border bg-primary/5 px-4 py-2.5">
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground tabular-nums">
              {selected.size} selected
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setSelected(new Set())}
                className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
              >
                clear
              </Button>
              {bulkDeleting ? (
                <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                  deleting…
                </span>
              ) : (
                <InlineConfirm
                  confirming={confirmBulk}
                  onAskRemove={() => setConfirmBulk(true)}
                  onCancel={() => setConfirmBulk(false)}
                  onConfirm={bulkDelete}
                  triggerLabel={`delete ${selected.size}`}
                />
              )}
            </div>
          </div>
        )}

        {/* Mobile: compact list */}
        <div className="sm:hidden divide-y divide-border/60">
          {loading ? (
            [...Array(6)].map((_, i) => (
              <div key={i} className="px-4 py-3 space-y-1.5">
                <div className="flex items-center gap-2">
                  <Skeleton className="size-7 rounded-sm" />
                  <Skeleton className="h-3 w-24" />
                </div>
                <Skeleton className="h-2.5 w-32 ml-9" />
              </div>
            ))
          ) : filtered.length === 0 ? (
            <div className="px-6 py-16 text-center text-sm text-muted-foreground/60">
              {peers.length === 0 ? (
                <>
                  <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                  Awaiting first contact…
                </>
              ) : (
                <>
                  <Users className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                  No peers match the current filter.
                </>
              )}
            </div>
          ) : (
            filtered.map((p) => (
              <div
                key={p.pubkey}
                className="flex items-center gap-3 px-4 py-2.5 hover:bg-muted/40 transition-colors"
              >
                <SelectBox
                  checked={selected.has(p.pubkey)}
                  onChange={() => toggleSelect(p.pubkey)}
                  label={`Select ${p.name || "peer"}`}
                />
                <button
                  type="button"
                  onClick={() => selectPeer(p.pubkey)}
                  className="flex flex-1 min-w-0 items-center gap-3 text-left"
                >
                  <PeerAvatar name={p.name || "?"} size="sm" />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-sm truncate">
                        {p.name || (
                          <span className="text-muted-foreground italic">
                            unknown
                          </span>
                        )}
                      </span>
                      <PeerTypePill type={p.type} />
                    </div>
                    <div className="flex items-center gap-3 mt-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
                      <SignalStrength snr={p.snr} size="sm" />
                      <HopsBadge peer={p} />
                      <span>{timeAgo(p.lastSeen)}</span>
                    </div>
                  </div>
                </button>
              </div>
            ))
          )}
        </div>

        {/* Desktop: full table */}
        <Table className="hidden sm:table">
          <TableHeader>
            <TableRow className="border-border hover:bg-transparent">
              <TableHead className="w-8">
                <SelectBox
                  checked={allFilteredSelected}
                  onChange={toggleSelectAll}
                  label="Select all peers"
                />
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                Name
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                Type
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                Pubkey
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                Signal
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em]">
                Hops
              </TableHead>
              <TableHead className="font-mono text-[10px] uppercase tracking-[0.12em] text-right">
                Last seen
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              [...Array(6)].map((_, i) => (
                <TableRow key={i} className="border-border/60">
                  <TableCell>
                    <Skeleton className="size-4 rounded-none" />
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Skeleton className="size-7 rounded-sm" />
                      <Skeleton className="h-3 w-24" />
                    </div>
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-4 w-14" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-3 w-28" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-3 w-16" />
                  </TableCell>
                  <TableCell>
                    <Skeleton className="h-3 w-12" />
                  </TableCell>
                  <TableCell className="text-right">
                    <Skeleton className="h-3 w-10 ml-auto" />
                  </TableCell>
                </TableRow>
              ))
            ) : filtered.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={7}
                  className="text-center py-16 text-sm text-muted-foreground/60"
                >
                  {peers.length === 0 ? (
                    <>
                      <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                      Awaiting first contact…
                    </>
                  ) : (
                    <>
                      <Users className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                      No peers match the current filter.
                    </>
                  )}
                </TableCell>
              </TableRow>
            ) : (
              filtered.map((p) => (
                <TableRow
                  key={p.pubkey}
                  onClick={() => selectPeer(p.pubkey)}
                  className="border-border/60 group cursor-pointer"
                >
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <SelectBox
                      checked={selected.has(p.pubkey)}
                      onChange={() => toggleSelect(p.pubkey)}
                      label={`Select ${p.name || "peer"}`}
                    />
                  </TableCell>
                  <TableCell className="font-medium">
                    <div className="flex items-center gap-2.5">
                      <PeerAvatar name={p.name || "?"} size="sm" />
                      <span className="truncate">
                        {p.name || (
                          <span className="text-muted-foreground italic">
                            unknown
                          </span>
                        )}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <PeerTypePill type={p.type} />
                  </TableCell>
                  <TableCell>
                    <code className="font-mono text-xs text-muted-foreground">
                      {truncateMid(p.pubkey, 6, 4)}
                    </code>
                  </TableCell>
                  <TableCell>
                    <SignalStrength snr={p.snr} />
                  </TableCell>
                  <TableCell>
                    <HopsBadge peer={p} />
                  </TableCell>
                  <TableCell className="text-right font-mono text-xs tabular-nums text-muted-foreground">
                    {timeAgo(p.lastSeen)}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </section>

      <PeerDetailSheet {...sheetProps} companions={companions} />
    </div>
  );
}

// Phone-sized form of the type chips — single-select, so radio items.
function PeerTypeMenu({
  value,
  onChange,
  countFor,
}: {
  value: TypeFilter;
  onChange: (v: TypeFilter) => void;
  countFor: (t: TypeFilter) => number;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="sm:hidden relative inline-flex items-center gap-1.5 border border-border bg-card px-2 py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground before:absolute before:inset-x-0 before:-inset-y-2 before:content-['']"
        >
          <span className="text-foreground">{value}</span>
          <span className="tabular-nums text-muted-foreground/60">
            {countFor(value)}
          </span>
          <ChevronDown className="size-3" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="rounded-sm">
        <DropdownMenuRadioGroup
          value={value}
          onValueChange={(v) => onChange(v as TypeFilter)}
        >
          {TYPE_FILTERS.map((t) => (
            <DropdownMenuRadioItem
              key={t}
              value={t}
              className="font-mono text-[11px] uppercase tracking-[0.08em]"
            >
              {t}
              <span className="ml-auto pl-3 tabular-nums text-muted-foreground/70">
                {countFor(t)}
              </span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
