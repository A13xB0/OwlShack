import { useCallback, useEffect, useMemo, useState, type ChangeEvent } from "react";
import { RotateCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PATH_HASH_SIZE_OPTIONS } from "@/components/ConfigFields";
import { HopPath } from "@/components/HopPath";
import { HopPicker, ModeToggle, type HopPeer } from "@/components/HopPicker";
import { resolveHops } from "@/lib/linkPath";
import { cn } from "@/lib/utils";

// PathInfo is GET .../path: outPathHashSize is the route's own size, bytesPerHop the contact's setting.
export interface PathInfo {
  outPath: string;
  hops: number;
  hasPath: boolean;
  directNeighbor: boolean;
  outPathHashSize: number;
  bytesPerHop: number;
}

type RouteMode = "flood" | "direct" | "path";

const ROUTE_MODES: { value: RouteMode; label: string }[] = [
  { value: "flood", label: "Flood" },
  { value: "direct", label: "Direct" },
  { value: "path", label: "Via hops" },
];

// splitHops re-chunks hex hop hashes at hs bytes each, so a size change never merges hops silently.
function splitHops(hex: string, hs: number): string {
  const raw = hex.replace(/[\s,]/g, "").toUpperCase();
  return raw.match(new RegExp(`.{1,${hs * 2}}`, "g"))?.join(",") ?? "";
}

function describeRoute(info: PathInfo | null): string {
  if (!info) return "unknown";
  if (!info.hasPath) return "flood";
  if (info.directNeighbor) return "direct";
  return `${info.hops} hop${info.hops === 1 ? "" : "s"}`;
}

// A route is sent at its own size; bytes per hop is what a flood or 0-hop send carries, so a routed path says both when they differ.
function describeSize(info: PathInfo): string {
  const bytes = (n: number) => `${n} byte${n === 1 ? "" : "s"} per hop`;
  if (!info.hasPath || info.directNeighbor || info.outPathHashSize === info.bytesPerHop) return bytes(info.bytesPerHop);
  return `${bytes(info.outPathHashSize)} (floods at ${info.bytesPerHop})`;
}

function HexVerdict({ hex, valid, hashSize }: { hex: string; valid: boolean; hashSize: number }) {
  if (hex === "") return <span>enter at least one hop</span>;
  if (valid) return <span className="text-success">{hex.length / 2 / hashSize} hops</span>;
  if (!/^[0-9a-f]*$/.test(hex)) return <span className="text-destructive">hex digits only</span>;
  if ((hex.length / 2) % hashSize !== 0 || hex.length % 2 !== 0)
    return <span className="text-destructive">not whole {hashSize}-byte hops</span>;
  return <span className="text-destructive">longer than 63 hops or 64 bytes</span>;
}

// PathDialog is the one route editor for every contact: repeaters, rooms, sensors and chat.
export function PathDialog({
  open,
  onOpenChange,
  companion,
  pubkey,
  name,
  onChanged,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  companion: string;
  pubkey: string;
  name: string;
  onChanged?: () => void;
}) {
  const base = `/api/companions/${encodeURIComponent(companion)}/contacts/${encodeURIComponent(pubkey)}/path`;
  const [info, setInfo] = useState<PathInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [peers, setPeers] = useState<HopPeer[]>([]);
  const [origin, setOrigin] = useState<{ lat: number; lon: number } | null>(null);
  const [mode, setMode] = useState<RouteMode>("flood");
  const [builder, setBuilder] = useState<"select" | "manual">("select");
  const [hops, setHops] = useState<HopPeer[]>([]);
  const [manualHex, setManualHex] = useState("");
  const [hashSize, setHashSize] = useState(1);
  const [saving, setSaving] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  // What the editor opened on, so Save stays off until something differs from it.
  const [seeded, setSeeded] = useState({ mode: "flood" as RouteMode, hashSize: 1, hex: "" });

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [p, ps, cs] = await Promise.all([
        fetch(base).then(async (r) => {
          if (r.ok) return r.json() as Promise<PathInfo>;
          const body = await r.json().catch(() => null);
          throw new Error(body?.error ?? `the server answered ${r.status}`);
        }),
        fetch("/api/peers").then((r) => (r.ok ? (r.json() as Promise<HopPeer[] | null>) : null)),
        fetch("/api/companions").then((r) => (r.ok ? r.json() : null)),
      ]);
      const repeaters = (ps ?? []).filter((x) => x.type === "REPEATER");
      setInfo(p);
      setLoadError(null);
      setPeers(repeaters);
      const c = (cs ?? []).find((x: { name: string }) => x.name === companion);
      setOrigin(c && c.lat != null && c.lon != null ? { lat: c.lat, lon: c.lon } : null);
      return { p, repeaters };
    } finally {
      setLoading(false);
    }
  }, [base, companion]);

  // The editor opens on the route in use; a route's hops are fixed at its own size, so it opens at that size.
  const seed = useCallback((p: PathInfo | null, repeaters: HopPeer[]) => {
    const m: RouteMode = !p?.hasPath ? "flood" : p.directNeighbor ? "direct" : "path";
    const hs = (m === "path" ? p?.outPathHashSize : p?.bytesPerHop) || 1;
    setMode(m);
    setHashSize(hs);
    setSeeded({ mode: m, hashSize: hs, hex: m === "path" ? (p?.outPath ?? "").toLowerCase() : "" });
    setManualHex(splitHops(p?.outPath ?? "", hs));
    const resolved = m === "path" && p ? resolveHops(p.outPath, hs, repeaters) : [];
    // Only a route whose every hop names one repeater can be edited as a list; any other stays as hex.
    const named = resolved.every((h) => h.peer && h.alternatives.length === 0);
    setBuilder(named ? "select" : "manual");
    setHops(
      named
        ? resolved.map((h) => repeaters.find((r) => r.pubkey === h.peer!.pubkey)!).filter(Boolean)
        : [],
    );
  }, []);

  useEffect(() => {
    if (!open) return;
    // Start blank, so a reopen never shows the last contact's route or an unsaved edit while this one loads.
    setInfo(null);
    setLoadError(null);
    seed(null, []);
    load()
      .then(({ p, repeaters }) => seed(p, repeaters))
      .catch((e: Error) => setLoadError(e.message));
  }, [open, load, seed]);

  const pathHex = useMemo(() => {
    if (builder === "manual") return manualHex.replace(/[\s,]/g, "").toLowerCase();
    return hops.map((h) => h.pubkey.slice(0, hashSize * 2)).join("").toLowerCase();
  }, [builder, manualHex, hops, hashSize]);
  const pathBytes = pathHex.length / 2;
  const changed =
    mode !== seeded.mode || hashSize !== seeded.hashSize || (mode === "path" && pathHex !== seeded.hex);
  const pathValid =
    /^([0-9a-f]{2})+$/.test(pathHex) && pathBytes % hashSize === 0 && pathBytes / hashSize <= 63 && pathBytes <= 64;

  const save = async () => {
    setSaving(true);
    try {
      const r = await fetch(base, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ route: mode, path: mode === "path" ? pathHex : "", pathHashSize: hashSize }),
      });
      if (!r.ok) {
        const body = await r.json().catch(() => null);
        toast.error(body?.error ?? "Saving the path failed");
        return;
      }
      toast.success("Path saved");
      onChanged?.();
      onOpenChange(false);
    } catch {
      toast.error("Saving the path failed");
    } finally {
      setSaving(false);
    }
  };

  const reset = async () => {
    setResetting(true);
    try {
      const r = await fetch(base, { method: "DELETE" });
      if (!r.ok) throw new Error("reset");
      toast.success("Path reset to flood");
      onChanged?.();
      load()
        .then(({ p, repeaters }) => seed(p, repeaters))
        .catch((e: Error) => setLoadError(e.message));
    } catch {
      toast.error("Resetting the path failed");
    } finally {
      setResetting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="rounded-none border-border bg-card sm:max-w-2xl overflow-x-hidden"
        // Focus the dialog itself: landing on the first route button reads as a second selection.
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).focus();
        }}
      >
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">Path</DialogTitle>
          <DialogDescription className="font-mono text-xs">
            How messages reach {name}. Bytes per hop applies to everything sent to it, including flood and direct.
          </DialogDescription>
        </DialogHeader>

        <section className="space-y-2 border border-border bg-background/40 p-3">
          <div className="flex items-center justify-between gap-2">
            <span className="label-overline">In use</span>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => load().catch((e: Error) => setLoadError(e.message))}
              disabled={loading}
              className="font-mono uppercase tracking-[0.08em]"
            >
              <RotateCw className={cn("size-3", loading && "animate-spin")} /> refresh
            </Button>
          </div>
          {info === null && !loading ? (
            <p className="font-mono text-xs text-muted-foreground/70">
              No route information{loadError ? `: ${loadError}` : ""}.
            </p>
          ) : (
            <div className="space-y-1.5">
              <p className="font-mono text-xs uppercase tracking-[0.08em]">
                <span className="text-foreground">{describeRoute(info)}</span>
                {info && <span className="text-muted-foreground"> · {describeSize(info)}</span>}
              </p>
              {info?.hasPath && !info.directNeighbor && (
                <HopPath path={info.outPath} hashSize={info.outPathHashSize} direction="tx" peers={peers} />
              )}
            </div>
          )}
        </section>

        <section className="space-y-3">
          <div role="group" aria-label="Route" className="flex items-center gap-1 border border-border bg-muted/30 p-0.5 w-fit">
            {ROUTE_MODES.map((m) => (
              <ModeToggle key={m.value} active={mode === m.value} onClick={() => setMode(m.value)}>
                {m.label}
              </ModeToggle>
            ))}
          </div>

          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            <div className="flex items-center gap-2">
              <span className="label-overline">Bytes per hop</span>
              <Select
                value={String(hashSize)}
                onValueChange={(v) => {
                  const hs = Number(v);
                  setManualHex((p) => splitHops(p, hs));
                  setHashSize(hs);
                }}
              >
                <SelectTrigger
                  aria-label="Bytes per hop"
                  className="rounded-none font-mono text-[10px] uppercase tracking-widest h-7 w-28 border-border bg-background"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent className="rounded-sm">
                  {PATH_HASH_SIZE_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {mode === "path" && (
              <div className="flex items-center gap-1 border border-border bg-muted/30 p-0.5 w-fit">
                <ModeToggle
                  active={builder === "select"}
                  onClick={() => {
                    // Typed hex becomes the list when every hop names one repeater; otherwise the list stays as it was.
                    const resolved = builder === "manual" ? resolveHops(pathHex, hashSize, peers) : [];
                    if (resolved.length > 0 && resolved.every((h) => h.peer && h.alternatives.length === 0)) {
                      setHops(resolved.map((h) => peers.find((r) => r.pubkey === h.peer!.pubkey)!).filter(Boolean));
                    }
                    setBuilder("select");
                  }}
                >
                  pick
                </ModeToggle>
                <ModeToggle
                  active={builder === "manual"}
                  onClick={() => {
                    // Carry the picked hops over, or manual hex would still show the stored route.
                    if (builder === "select" && hops.length > 0) setManualHex(splitHops(pathHex, hashSize));
                    setBuilder("manual");
                  }}
                >
                  manual hex
                </ModeToggle>
              </div>
            )}
          </div>

          {/* Kept mounted across pick and manual, so the list keeps its sort and filter. */}
          <div hidden={mode !== "path" || builder !== "select"}>
            <HopPicker
              peers={peers}
              hashSize={hashSize}
              hops={hops}
              onHopsChange={setHops}
              origin={origin}
              summary={`${hops.length} hop${hops.length === 1 ? "" : "s"} · ${pathBytes}B`}
              listClassName="max-h-56"
            />
          </div>
          {mode === "path" && builder === "manual" && (
              <div className="space-y-1.5">
                <Input
                  value={manualHex}
                  onChange={(e: ChangeEvent<HTMLInputElement>) => setManualHex(e.target.value)}
                  aria-label="Hops, our neighbour first"
                  placeholder={["a4, 1b, e2", "a4c1, 1b09", "a4c1d2, 1b0977"][hashSize - 1]}
                  className="rounded-none font-mono text-base md:text-xs border-border"
                />
                <p className="font-mono text-[10px] tabular-nums text-muted-foreground">
                  Our neighbour first · {pathBytes}B · <HexVerdict hex={pathHex} valid={pathValid} hashSize={hashSize} />
                </p>
              </div>
          )}
        </section>

        <DialogFooter className="gap-2 sm:justify-between">
          <Button
            variant="ghost"
            size="sm"
            onClick={reset}
            disabled={!info?.hasPath || resetting || saving}
            className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em] text-destructive hover:text-destructive"
          >
            Reset to flood
          </Button>
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => onOpenChange(false)}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={save}
              disabled={saving || resetting || loading || info === null || !changed || (mode === "path" && !pathValid)}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              Save path
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
