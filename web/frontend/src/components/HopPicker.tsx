import { useMemo, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { ArrowDown, ArrowUp, CircleDashed, GripVertical, Plus, Radio, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { truncateMid } from "@/lib/format";
import { buildPeerCandidatesByHash } from "@/lib/linkPath";
import { cn } from "@/lib/utils";

export interface HopPeer {
  pubkey: string;
  name: string;
  type: string;
  lastSeen: string;
  snr: number | null;
  lat: number;
  lon: number;
}

type PeerSort = "name" | "recent" | "signal" | "distance";

// "Alphabetical" is wider than the phone-sized trigger and clips, hence a short form.
const SORT_LABEL: Record<PeerSort, string> = {
  name: "Alphabetical",
  recent: "Last seen",
  signal: "Signal",
  distance: "Distance",
};
const SORT_LABEL_SHORT: Record<PeerSort, string> = {
  name: "Name",
  recent: "Recent",
  signal: "Signal",
  distance: "Dist",
};

// Great-circle distance in km between two lat/lon pairs (both in degrees).
function haversineKm(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const R = 6371;
  const dLat = ((lat2 - lat1) * Math.PI) / 180;
  const dLon = ((lon2 - lon1) * Math.PI) / 180;
  const a =
    Math.sin(dLat / 2) ** 2 +
    Math.cos((lat1 * Math.PI) / 180) * Math.cos((lat2 * Math.PI) / 180) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(a));
}

export function ModeToggle({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "px-3 py-1 font-mono text-[10px] uppercase tracking-[0.12em] transition-colors relative before:absolute before:inset-x-0 before:-inset-y-2 before:content-[''] sm:before:hidden",
        active
          ? "bg-primary/10 text-primary border border-primary/30"
          : "border border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

// HopPicker builds a route from the repeater list: our neighbour first, a repeater may appear twice.
export function HopPicker({
  peers,
  hashSize,
  hops,
  onHopsChange,
  origin,
  summary,
  trailing,
  listClassName,
}: {
  peers: HopPeer[];
  hashSize: number;
  hops: HopPeer[];
  onHopsChange: Dispatch<SetStateAction<HopPeer[]>>;
  // origin enables the distance sort.
  origin?: { lat: number; lon: number } | null;
  summary?: string;
  // trailing renders after the hops, for chips the caller adds (the Trace page's mirrored return).
  trailing?: ReactNode;
  listClassName?: string;
}) {
  const [filter, setFilter] = useState("");
  const [chosenSort, setSort] = useState<PeerSort>("name");
  // With no location there is no distance, so the list and its label both fall back to names.
  const sort: PeerSort = chosenSort === "distance" && !origin ? "name" : chosenSort;
  const [draggingIndex, setDraggingIndexState] = useState<number | null>(null);
  // Mirrored in a ref: two dragover events can land before a re-render, and the second must see the first's move.
  const dragRef = useRef<number | null>(null);
  const setDraggingIndex = (i: number | null) => {
    dragRef.current = i;
    setDraggingIndexState(i);
  };

  const filteredPeers = useMemo(() => {
    const f = filter.trim().toLowerCase();
    // Selected repeaters stay listed so one can be added to the path twice (b8, e6, b8).
    const matched = f
      ? peers.filter((p) => p.name.toLowerCase().includes(f) || p.pubkey.toLowerCase().includes(f))
      : peers;
    return matched.slice().sort((a, b) => {
      if (sort === "distance" && origin) {
        // Nearest first; peers with no advertised location sink to the bottom.
        const distFor = (p: HopPeer) =>
          p.lat === 0 && p.lon === 0 ? Infinity : haversineKm(origin.lat, origin.lon, p.lat / 1e6, p.lon / 1e6);
        return distFor(a) - distFor(b);
      }
      if (sort === "signal") return (b.snr ?? -Infinity) - (a.snr ?? -Infinity);
      if (sort === "recent") return (b.lastSeen || "").localeCompare(a.lastSeen || "");
      // Alphabetical; unnamed repeaters sink to the bottom, pubkey as a tiebreaker.
      const an = a.name.trim().toLowerCase();
      const bn = b.name.trim().toLowerCase();
      if (!an !== !bn) return an ? -1 : 1;
      return an.localeCompare(bn) || a.pubkey.localeCompare(b.pubkey);
    });
  }, [peers, filter, sort, origin]);

  // A hop hash is a pubkey prefix, so at 1 byte several repeaters can answer to it; the chip says how many others.
  const byHash = useMemo(() => buildPeerCandidatesByHash(peers, hashSize), [peers, hashSize]);

  const moveAt = (idx: number, dir: -1 | 1) =>
    onHopsChange((prev) => {
      const j = idx + dir;
      if (j < 0 || j >= prev.length) return prev;
      const next = [...prev];
      [next[idx], next[j]] = [next[j], next[idx]];
      return next;
    });

  const dragOverToIndex = (toIdx: number) => {
    const from = dragRef.current;
    if (from === null || from === toIdx) return;
    onHopsChange((prev) => {
      const next = [...prev];
      const [moved] = next.splice(from, 1);
      next.splice(toIdx, 0, moved);
      return next;
    });
    setDraggingIndex(toIdx);
  };

  // A chip is keyed by its repeater and which occurrence it is, so a reorder moves it rather than remounting the one being dragged.
  const keys = useMemo(() => {
    const seen = new Map<string, number>();
    return hops.map((h) => {
      const n = seen.get(h.pubkey) ?? 0;
      seen.set(h.pubkey, n + 1);
      return `${h.pubkey}-${n}`;
    });
  }, [hops]);

  const clearDrag = (e: React.DragEvent) => {
    e.preventDefault();
    setDraggingIndex(null);
  };

  // Drop zones exist only mid-drag: at rest a w-0 item still takes a gap and pushes the first chip in.
  const dropZone = (toIdx: number) =>
    draggingIndex !== null && (
    <li
      aria-hidden
      onDragOver={(e) => {
        e.preventDefault();
        dragOverToIndex(toIdx);
      }}
      onDrop={clearDrag}
      className="w-10"
    />
  );

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="label-overline">Selected route</span>
        <div className="flex items-center gap-2">
          {summary && <span className="font-mono text-[10px] tabular-nums text-muted-foreground">{summary}</span>}
          {hops.length > 0 && (
            <Button
              variant="ghost"
              size="xs"
              onClick={() => onHopsChange([])}
              className="font-mono uppercase tracking-[0.08em]"
            >
              <Trash2 className="size-3" /> clear
            </Button>
          )}
        </div>
      </div>
      {hops.length === 0 ? (
        <div className="border border-dashed border-border/60 bg-muted/20 px-4 py-6 text-center">
          <CircleDashed className="size-5 mx-auto mb-2 text-muted-foreground/40" />
          <p className="text-sm text-muted-foreground/60">No hops selected · pick repeaters below</p>
        </div>
      ) : (
        <ol className="flex flex-wrap items-stretch gap-2">
          {dropZone(0)}
          {hops.map((peer, idx) => {
            const hash = peer.pubkey.slice(0, hashSize * 2).toLowerCase();
            const others = (byHash.get(hash)?.length ?? 1) - 1;
            return (
              <li
                key={keys[idx]}
                draggable
                onDragStart={() => setDraggingIndex(idx)}
                onDragEnd={() => setDraggingIndex(null)}
                onDragOver={(e) => {
                  e.preventDefault();
                  dragOverToIndex(idx);
                }}
                onDrop={clearDrag}
                className={cn(
                  "inline-flex items-center gap-1.5 border border-border bg-muted/40 px-2 py-1 font-mono text-xs hover:border-primary/40 transition-colors cursor-grab active:cursor-grabbing",
                  others > 0 && "border-warning/50",
                  draggingIndex === idx && "opacity-40",
                )}
              >
                <GripVertical className="size-3 text-muted-foreground/40" />
                <span className="text-muted-foreground/60 tabular-nums">{idx + 1}</span>
                <span className="text-foreground">
                  {peer.name || <span className="italic text-muted-foreground">unknown</span>}
                </span>
                <span className="text-muted-foreground/60">{hash}</span>
                {others > 0 && (
                  <span
                    className="text-warning"
                    title={`${others} other repeater${others === 1 ? "" : "s"} share${others === 1 ? "s" : ""} ${hash} at ${hashSize} byte${hashSize === 1 ? "" : "s"} per hop`}
                  >
                    <span aria-hidden>+{others}</span>
                    <span className="sr-only">
                      , {others} other repeater{others === 1 ? "" : "s"} share{others === 1 ? "s" : ""} this hash
                    </span>
                  </span>
                )}
                <span className="ml-1 inline-flex items-center gap-0.5">
                  <button
                    type="button"
                    onClick={() => moveAt(idx, -1)}
                    disabled={idx === 0}
                    className="relative inline-flex items-center justify-center size-5 hover:text-primary disabled:opacity-30 disabled:cursor-not-allowed before:absolute before:-inset-y-2.5 before:content-[''] sm:before:hidden"
                    aria-label={`move hop ${idx + 1} earlier`}
                  >
                    <ArrowUp className="size-3" />
                  </button>
                  <button
                    type="button"
                    onClick={() => moveAt(idx, 1)}
                    disabled={idx === hops.length - 1}
                    className="relative inline-flex items-center justify-center size-5 hover:text-primary disabled:opacity-30 disabled:cursor-not-allowed before:absolute before:-inset-y-2.5 before:content-[''] sm:before:hidden"
                    aria-label={`move hop ${idx + 1} later`}
                  >
                    <ArrowDown className="size-3" />
                  </button>
                  <button
                    type="button"
                    onClick={() => onHopsChange((prev) => prev.filter((_, i) => i !== idx))}
                    className="relative inline-flex items-center justify-center size-5 hover:text-destructive before:absolute before:-inset-y-2.5 before:content-[''] sm:before:hidden"
                    aria-label={`remove hop ${idx + 1}`}
                  >
                    <X className="size-3" />
                  </button>
                </span>
              </li>
            );
          })}
          {dropZone(hops.length - 1)}
          {trailing}
        </ol>
      )}

      <div className="space-y-2 pt-2 border-t border-border">
        <div className="flex items-center justify-between gap-2">
          <span className="label-overline shrink-0">Repeaters</span>
          <div className="flex min-w-0 flex-1 sm:flex-none items-center gap-2">
            <Select value={sort} onValueChange={(v) => setSort(v as PeerSort)}>
              <SelectTrigger aria-label="Sort repeaters" className="h-7 w-24 sm:w-36 shrink-0 rounded-none font-mono text-[11px] uppercase tracking-[0.06em]">
                {/* Item-aligned SelectContent measures this node, so the labels must ride inside SelectValue. */}
                <SelectValue>
                  <span className="sm:hidden">{SORT_LABEL_SHORT[sort]}</span>
                  <span className="hidden sm:inline">{SORT_LABEL[sort]}</span>
                </SelectValue>
              </SelectTrigger>
              <SelectContent className="rounded-none font-mono text-xs">
                {(["name", "recent", "signal"] as const).map((k) => (
                  <SelectItem key={k} value={k} className="font-mono text-xs uppercase tracking-[0.06em]">
                    {SORT_LABEL[k]}
                  </SelectItem>
                ))}
                <SelectItem
                  value="distance"
                  disabled={!origin}
                  className="font-mono text-xs uppercase tracking-[0.06em]"
                >
                  Distance{!origin ? " (no location)" : ""}
                </SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={filter}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setFilter(e.target.value)}
              placeholder="filter…"
              aria-label="Filter repeaters"
              className="h-7 w-32 min-w-0 flex-1 sm:flex-none rounded-none font-mono text-base md:text-xs"
            />
          </div>
        </div>
        <div className={cn("border border-border max-h-72 overflow-y-auto divide-y divide-border/60", listClassName)}>
          {filteredPeers.length === 0 ? (
            <div className="px-3 py-6 text-center text-sm text-muted-foreground/60">
              <Radio className="size-5 mx-auto mb-2 text-muted-foreground/30" />
              No matching repeaters
            </div>
          ) : (
            filteredPeers.map((peer) => (
              <button
                key={peer.pubkey}
                type="button"
                onClick={() => onHopsChange((prev) => [...prev, peer])}
                className="w-full flex items-center justify-between gap-3 px-3 py-2 sm:py-1.5 text-left hover:bg-muted/40 transition-colors group"
              >
                <div className="min-w-0 flex-1 flex flex-col sm:flex-row sm:items-baseline sm:gap-2">
                  <div className="text-sm font-medium truncate">
                    {peer.name || <span className="italic text-muted-foreground">unknown</span>}
                  </div>
                  <code className="font-mono text-[10px] text-muted-foreground tabular-nums shrink-0">
                    {truncateMid(peer.pubkey, 8, 4)}
                  </code>
                </div>
                <Plus className="size-3.5 text-muted-foreground/40 group-hover:text-primary transition-colors" />
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
