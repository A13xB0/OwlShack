import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleDashed,
  GitCompare,
  Link2,
  Pencil,
  Send,
  Square,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { Button } from "@/components/ui/button";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineConfirm } from "@/components/InlineConfirm";
import { HopPicker, ModeToggle, type HopPeer } from "@/components/HopPicker";
import { SignalTestStats } from "@/components/SignalTestStats";
import { PageHeader } from "@/components/PageHeader";
import { SignalStrength } from "@/components/SignalStrength";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  signalTestApi,
  computeStats,
  type SignalTestDetail,
  type SignalTestSummary,
  type SignalTestWsMessage,
} from "@/lib/signalTestApi";
import {
  hexToHopHashes,
  buildPeerCandidatesByHash,
  resolveHopPeer,
} from "@/lib/linkPath";

interface Companion {
  name: string;
  pubkey: string;
  peerCount: number;
  lat?: number | null;
  lon?: number | null;
}

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lastSeen: string;
  snr: number | null;
  rssi: number | null;
  lat: number;
  lon: number;
}

interface TraceWsMessage {
  companion: string;
  tag: number;
  hops: number;
  path: string[];
  hopSNRs: number[];
  snr?: number;
}

interface TraceResult {
  companion: string;
  tag: number;
  path: string[];
  hopSNRs: number[];
  snr?: number;
  elapsedMs: number;
}

type HashSize = 1 | 2 | 4;
type BuilderMode = "select" | "manual";

// Silence window: every progressive echo resets it; elapsing with no progress is a timeout.
const TRACE_TIMEOUT_MS = 5000;

const HEX_RE = /^[0-9a-f]+$/i;

// A resolved hop reads as a plain name until it is a guess, when it carries the count of other
// repeaters the hash could equally have named.
function hopName(hit?: { name: string; alternatives: number }): string | undefined {
  if (!hit) return undefined;
  return hit.alternatives > 0 ? `${hit.name} +${hit.alternatives}` : hit.name;
}

// Resolves a 1-indexed hop number to "N. <repeater name or hash>". A hash that several repeaters
// share gets "+N" appended, and one that names no repeater at all stays hex — see resolveHopPeer.
function hopLabelFor(pathHex: string, hashSize: number, peers: Peer[]) {
  const hashes = hexToHopHashes(pathHex, hashSize);
  const byHash = buildPeerCandidatesByHash(peers, hashSize);
  return (hop: number) => {
    const hash = hashes[hop - 1];
    if (!hash) return `Hop ${hop}`;
    const { peer, alternatives } = resolveHopPeer(hash, byHash.get(hash));
    if (!peer) return `${hop}. ${hash}`;
    return `${hop}. ${peer.name}${alternatives.length > 0 ? ` +${alternatives.length}` : ""}`;
  };
}

export function TracesPage() {
  const [companions, setCompanions] = useState<Companion[]>([]);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [companionName, setCompanionName] = useState<string>("");
  const [hashSize, setHashSize] = useState<HashSize>(1);
  const [mode, setMode] = useState<BuilderMode>("select");
  const [selectedPath, setSelectedPath] = useState<HopPeer[]>([]);
  const [mirrorReturn, setMirrorReturn] = useState(false);
  const [manualHex, setManualHex] = useState<string>("");

  const [activeTag, setActiveTag] = useState<number | null>(null);
  const [waitStartedAt, setWaitStartedAt] = useState<number | null>(null);
  const [deadlineAt, setDeadlineAt] = useState<number | null>(null);
  const [liveSNRs, setLiveSNRs] = useState<number[]>([]);
  const [result, setResult] = useState<TraceResult | null>(null);
  const [waitError, setWaitError] = useState<string | null>(null);
  const [tickNow, setTickNow] = useState(Date.now());
  const timeoutRef = useRef<number | null>(null);

  // Signal test mode — single | test toggle, run params, live progress.
  const [runMode, setRunMode] = useState<"single" | "test">("single");
  const [testCount, setTestCount] = useState(20);
  const [testIntervalSecs, setTestIntervalSecs] = useState(30);
  const [testLabel, setTestLabel] = useState("");
  const [starting, setStarting] = useState(false);
  const [addingLink, setAddingLink] = useState(false);
  const [activeTest, setActiveTest] = useState<SignalTestDetail | null>(null);
  const activeTestIdRef = useRef<number | null>(null);
  activeTestIdRef.current = activeTest?.id ?? null;

  const testsUrl = companionName
    ? `/api/signal-tests?companion=${encodeURIComponent(companionName)}`
    : null;
  const {
    items: savedTests,
    setItems: setSavedTests,
    loading: testsLoading,
    reload: reloadTests,
  } = useApiList<SignalTestSummary>(testsUrl, "Failed to load signal tests");
  const reloadTestsRef = useRef(reloadTests);
  reloadTestsRef.current = reloadTests;

  const [compareIds, setCompareIds] = useState<Set<number>>(new Set());
  const [compareOpen, setCompareOpen] = useState(false);
  // Render-mirrored so the WS callback can tell a partial echo from the final packet without re-creating.
  const plannedHopCountRef = useRef(0);
  // Same mirror for the in-flight state, so the WS callback doesn't resubscribe on every tag/timer change.
  const activeTagRef = useRef<number | null>(null);
  activeTagRef.current = activeTag;
  const waitStartedAtRef = useRef<number | null>(null);
  waitStartedAtRef.current = waitStartedAt;

  // (Re)arm the silence timer. Called on send and on every partial echo.
  const armTimeout = useCallback(() => {
    if (timeoutRef.current) window.clearTimeout(timeoutRef.current);
    setDeadlineAt(Date.now() + TRACE_TIMEOUT_MS);
    timeoutRef.current = window.setTimeout(() => {
      setActiveTag(null);
      setWaitStartedAt(null);
      setDeadlineAt(null);
      setWaitError("No response from final repeater · timed out");
      timeoutRef.current = null;
    }, TRACE_TIMEOUT_MS);
  }, []);

  const onWsMessage = useCallback(
    (topic: string, data: unknown) => {
      if (topic === "peers" && data) {
        const p = data as Peer;
        if (p.type === "REPEATER") {
          setPeers((prev) => {
            const idx = prev.findIndex((x) => x.pubkey === p.pubkey);
            if (idx === -1) return [...prev, p];
            const next = prev.slice();
            next[idx] = { ...next[idx], ...p };
            return next;
          });
        }
        return;
      }
      if (topic === "signaltest" && data) {
        const msg = data as SignalTestWsMessage;
        if (msg.testId !== activeTestIdRef.current) return;
        if (msg.action === "run") {
          setActiveTest((prev) =>
            prev && prev.id === msg.testId
              ? {
                  ...prev,
                  runsDone: prev.runsDone + 1,
                  okCount: prev.okCount + (msg.ok ? 1 : 0),
                  runs: [
                    ...prev.runs,
                    {
                      seq: msg.seq,
                      sentAt: msg.sentAt,
                      ok: msg.ok,
                      hopSNRs: msg.hopSNRs || [],
                      snr: msg.snr,
                      elapsedMs: msg.elapsedMs,
                    },
                  ],
                }
              : prev,
          );
        } else if (msg.action === "status") {
          setActiveTest((prev) =>
            prev && prev.id === msg.testId
              ? { ...prev, status: msg.status, finishedAt: msg.finishedAt }
              : prev,
          );
          reloadTestsRef.current();
        }
        return;
      }
      if (topic !== "traces" || !data) return;
      const msg = data as TraceWsMessage;
      const activeTag = activeTagRef.current;
      const waitStartedAt = waitStartedAtRef.current;
      if (activeTag == null || msg.tag !== activeTag || waitStartedAt == null) {
        return;
      }

      // Each echo carries the SNRs so far; msg.hops is the full path length from the firmware.
      const snrs = Array.isArray(msg.hopSNRs) ? msg.hopSNRs : [];
      const totalHops =
        msg.hops && msg.hops > 0 ? msg.hops : plannedHopCountRef.current;
      const isFinal = totalHops > 0 && snrs.length >= totalHops;

      if (isFinal) {
        const elapsed = Date.now() - waitStartedAt;
        setResult({
          companion: msg.companion,
          tag: msg.tag,
          path: msg.path || [],
          hopSNRs: snrs,
          snr: msg.snr,
          elapsedMs: elapsed,
        });
        setLiveSNRs(snrs);
        setActiveTag(null);
        setWaitStartedAt(null);
        setDeadlineAt(null);
        setWaitError(null);
        if (timeoutRef.current) {
          window.clearTimeout(timeoutRef.current);
          timeoutRef.current = null;
        }
      } else {
        // Partial echo: a repeater relayed but the final hop hasn't replied yet.
        setLiveSNRs((prev) => (snrs.length > prev.length ? snrs : prev));
        armTimeout();
      }
    },
    [armTimeout],
  );

  useWebSocket(
    ["traces", "peers", "signaltest"],
    onWsMessage,
  );

  // A running test is server-side, so switching companions clears the view and re-picks it up from the list.
  useEffect(() => {
    setActiveTest(null);
  }, [companionName]);

  useEffect(() => {
    if (!savedTests || activeTest) return;
    const running = savedTests.find((t) => t.status === "running");
    if (!running) return;
    signalTestApi.get(running.id).then(setActiveTest).catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedTests]);

  useEffect(() => {
    if (activeTag == null) return;
    const id = window.setInterval(() => setTickNow(Date.now()), 200);
    return () => window.clearInterval(id);
  }, [activeTag]);

  useEffect(() => {
    return () => {
      if (timeoutRef.current) window.clearTimeout(timeoutRef.current);
    };
  }, []);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([
      fetch("/api/companions").then((r) => {
        if (!r.ok) throw new Error("companions");
        return r.json();
      }),
      fetch("/api/peers").then((r) => {
        if (!r.ok) throw new Error("peers");
        return r.json();
      }),
    ])
      .then(([cs, ps]: [Companion[] | null, Peer[] | null]) => {
        const list = cs || [];
        setCompanions(list);
        setPeers((ps || []).filter((p) => p.type === "REPEATER"));
        if (list.length > 0 && !companionName) setCompanionName(list[0].name);
      })
      .catch(() => setError("Failed to load companions or peers"))
      .finally(() => setLoading(false));
  }, [companionName]);

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const companionCoords = useMemo(() => {
    const c = companions.find((x) => x.name === companionName);
    return c && c.lat != null && c.lon != null
      ? { lat: c.lat, lon: c.lon }
      : null;
  }, [companions, companionName]);

  // Hop names for the timeline. Keyed by hash to the resolved repeater, or absent when no repeater
  // matches — the timeline then shows "unknown" over the raw hash, which is the honest reading.
  const peerByHash = useMemo(() => {
    const candidates = buildPeerCandidatesByHash(peers, hashSize);
    const map = new Map<string, { name: string; alternatives: number }>();
    for (const [hash, list] of candidates) {
      const { peer, alternatives } = resolveHopPeer(hash, list);
      if (peer) map.set(hash, { name: peer.name, alternatives: alternatives.length });
    }
    return map;
  }, [peers, hashSize]);

  const computedPathHex = useMemo(() => {
    const base =
      mode === "manual"
        ? manualHex.trim().toLowerCase()
        : selectedPath
            .map((p) => p.pubkey.slice(0, hashSize * 2))
            .join("")
            .toLowerCase();
    if (!mirrorReturn) return base;
    const hops = hexToHopHashes(base, hashSize);
    return hops.length <= 1 ? base : base + hops.slice(0, -1).reverse().join("");
  }, [mode, manualHex, selectedPath, hashSize, mirrorReturn]);

  // Read-only preview of the auto-added return leg, minus the turnaround hop.
  const mirrorPeers = useMemo(() => {
    if (!mirrorReturn || selectedPath.length <= 1) return [];
    return selectedPath.slice(0, -1).reverse();
  }, [mirrorReturn, selectedPath]);

  const pathHexValid = useMemo(() => {
    if (!computedPathHex) return mode === "select";
    if (!HEX_RE.test(computedPathHex)) return false;
    if (computedPathHex.length % (hashSize * 2) !== 0) return false;
    return true;
  }, [computedPathHex, hashSize, mode]);

  const pathByteLen = computedPathHex.length / 2;
  const pathHopCount = hashSize > 0 ? pathByteLen / hashSize : 0;

  // Per-hop hashes of the path in flight; tells the WS handler how many hops to expect.
  const routeHashes = useMemo(() => {
    const step = hashSize * 2;
    if (step <= 0) return [];
    const out: string[] = [];
    for (let i = 0; i + step <= computedPathHex.length; i += step) {
      out.push(computedPathHex.slice(i, i + step));
    }
    return out;
  }, [computedPathHex, hashSize]);
  plannedHopCountRef.current = routeHashes.length;

  const onChangeHashSize = (v: string) => {
    setHashSize(Number(v) as HashSize);
  };

  const sendTrace = async () => {
    if (!companionName) {
      toast.error("Select a companion first");
      return;
    }
    if (mode === "manual" && !computedPathHex) {
      toast.error("Path is empty");
      return;
    }
    if (!pathHexValid) {
      toast.error("Path is not valid hex for the selected hash size");
      return;
    }

    setResult(null);
    setWaitError(null);
    setLiveSNRs([]);

    try {
      const res = await fetch(
        `/api/companions/${encodeURIComponent(companionName)}/trace`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            path: computedPathHex,
            pathHashSize: hashSize,
          }),
        },
      );
      if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try {
          const data = await res.json();
          if (data && typeof data.error === "string") msg = data.error;
        } catch {
          // ignore
        }
        toast.error(`Trace failed: ${msg}`);
        return;
      }
      const data = (await res.json()) as { tag: number };
      setActiveTag(data.tag);
      setWaitStartedAt(Date.now());
      armTimeout();
      toast.success(`Trace sent · tag ${data.tag.toString(16).padStart(8, "0")}`);
    } catch (err) {
      toast.error(`Trace failed: ${(err as Error).message}`);
    }
  };

  const startTest = async () => {
    if (!companionName) {
      toast.error("Select a companion first");
      return;
    }
    if (mode === "manual" && !computedPathHex) {
      toast.error("Path is empty");
      return;
    }
    if (!pathHexValid) {
      toast.error("Path is not valid hex for the selected hash size");
      return;
    }
    if (testCount < 1 || testCount > 1000) {
      toast.error("Count must be between 1 and 1000");
      return;
    }
    if (testIntervalSecs < 5 || testIntervalSecs > 3600) {
      toast.error("Interval must be between 5 and 3600 seconds");
      return;
    }

    setStarting(true);
    try {
      const { id } = await signalTestApi.start(companionName, {
        path: computedPathHex,
        pathHashSize: hashSize,
        count: testCount,
        intervalSecs: testIntervalSecs,
        label: testLabel || undefined,
      });
      const detail = await signalTestApi.get(id);
      setActiveTest(detail);
      reloadTests();
      toast.success(`Signal test started · ${testCount} runs`);
    } catch (err) {
      toast.error(`Failed to start test: ${(err as Error).message}`);
    } finally {
      setStarting(false);
    }
  };

  const addLinkMonitor = async () => {
    if (!companionName) {
      toast.error("Select a companion first");
      return;
    }
    if (mode === "manual" && !computedPathHex) {
      toast.error("Path is empty");
      return;
    }
    if (!pathHexValid) {
      toast.error("Path is not valid hex for the selected hash size");
      return;
    }
    setAddingLink(true);
    try {
      const res = await fetch(
        `/api/companions/${encodeURIComponent(companionName)}/links`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            path: computedPathHex,
            pathHashSize: hashSize,
            label: testLabel || undefined,
          }),
        },
      );
      if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try {
          const data = await res.json();
          if (data && typeof data.error === "string") msg = data.error;
        } catch {
          // ignore
        }
        toast.error(`Failed to add link monitor: ${msg}`);
        return;
      }
      toast.success("Path added to Monitoring — polled on a schedule");
    } catch (err) {
      toast.error(`Failed to add link monitor: ${(err as Error).message}`);
    } finally {
      setAddingLink(false);
    }
  };

  const cancelActiveTest = async () => {
    if (!activeTest) return;
    try {
      await signalTestApi.cancel(activeTest.id);
      toast.success("Test cancelled");
    } catch (err) {
      toast.error(`Cancel failed: ${(err as Error).message}`);
    }
  };

  const toggleCompare = (id: number) => {
    setCompareIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const isWaiting = activeTag != null && waitStartedAt != null;
  const remainingMs =
    isWaiting && deadlineAt != null ? Math.max(0, deadlineAt - tickNow) : 0;

  return (
    <div className="space-y-4">
      <PageHeader
        title="Traces"
        meta={
          <span className="font-mono text-sm text-muted-foreground tabular-nums">
            {peers.length} repeaters · {companions.length} companions
          </span>
        }
      />

      {loading && <TracesSkeleton />}
      {error && <LoadErrorAlert message={error} onRetry={load} />}

      {!loading && !error && (
        <section className="grid grid-cols-1 lg:grid-cols-5 gap-6">
          <div className="lg:col-span-3 space-y-6">
            <div className="panel">
              <SectionTitle eyebrow="Path · builder" title="Compose route" />
              <div className="px-4 py-4 space-y-4">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  <div className="space-y-1.5">
                    <label className="label-overline block">Companion</label>
                    <Select
                      value={companionName}
                      onValueChange={setCompanionName}
                    >
                      <SelectTrigger className="w-full font-mono text-xs uppercase tracking-[0.06em] rounded-none">
                        <SelectValue placeholder="Select companion" />
                      </SelectTrigger>
                      <SelectContent className="rounded-none font-mono text-xs">
                        {companions.length === 0 ? (
                          <div className="px-3 py-2 text-muted-foreground/60">
                            none configured
                          </div>
                        ) : (
                          companions.map((c) => (
                            <SelectItem
                              key={c.name}
                              value={c.name}
                              className="font-mono text-xs uppercase tracking-[0.06em]"
                            >
                              {c.name}
                            </SelectItem>
                          ))
                        )}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="space-y-1.5">
                    <label className="label-overline block">
                      Path Hash Size
                    </label>
                    <Select
                      value={String(hashSize)}
                      onValueChange={onChangeHashSize}
                    >
                      <SelectTrigger className="w-full font-mono text-xs uppercase tracking-[0.06em] rounded-none">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent className="rounded-none font-mono text-xs">
                        <SelectItem value="1">1 byte</SelectItem>
                        <SelectItem value="2">2 bytes</SelectItem>
                        <SelectItem value="4">4 bytes</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                  <div className="flex items-center gap-1 border border-border bg-muted/30 p-0.5 w-fit">
                    <ModeToggle
                      active={runMode === "single"}
                      onClick={() => setRunMode("single")}
                    >
                      single
                    </ModeToggle>
                    <ModeToggle
                      active={runMode === "test"}
                      onClick={() => setRunMode("test")}
                    >
                      signal test
                    </ModeToggle>
                  </div>
                  <div className="flex items-center gap-1 border border-border bg-muted/30 p-0.5 w-fit">
                    <ModeToggle
                      active={mode === "select"}
                      onClick={() => setMode("select")}
                    >
                      pick
                    </ModeToggle>
                    <ModeToggle
                      active={mode === "manual"}
                      onClick={() => setMode("manual")}
                    >
                      manual hex
                    </ModeToggle>
                  </div>
                  <label className="flex items-center gap-2 min-h-10 sm:min-h-0 font-mono text-xs uppercase tracking-[0.06em] cursor-pointer select-none">
                    <Switch
                      size="sm"
                      checked={mirrorReturn}
                      onCheckedChange={setMirrorReturn}
                    />
                    Mirror return
                  </label>
                </div>

                {/* Kept mounted in manual mode, so the list keeps its sort and filter across a switch. */}
                <div hidden={mode !== "select"}>
                  <HopPicker
                    peers={peers}
                    hashSize={hashSize}
                    hops={selectedPath}
                    onHopsChange={setSelectedPath}
                    origin={companionCoords}
                    summary={`${pathHopCount} hops · ${pathByteLen}B`}
                    trailing={mirrorPeers.map((peer, i) => (
                      <li
                        key={`mirror-${peer.pubkey}-${i}`}
                        title="Auto-added return hop (mirror return)"
                        className="inline-flex items-center gap-1.5 border border-dashed border-border/50 bg-muted/10 px-2 py-1 font-mono text-xs text-muted-foreground/60"
                      >
                        <span className="tabular-nums">{selectedPath.length + i + 1}</span>
                        <span>{peer.name || <span className="italic">unknown</span>}</span>
                        <span className="text-muted-foreground/40">{peer.pubkey.slice(0, hashSize * 2)}</span>
                      </li>
                    ))}
                  />
                </div>
                {mode === "manual" && (
                  <div className="space-y-2">
                    <label className="label-overline block">
                      Manual path · hex
                    </label>
                    <Textarea
                      value={manualHex}
                      onChange={(
                        e: React.ChangeEvent<HTMLTextAreaElement>,
                      ) => setManualHex(e.target.value)}
                      placeholder={`example: ${"ab".repeat(hashSize)} ${"cd".repeat(hashSize)}`}
                      className="font-mono text-base md:text-xs rounded-none min-h-24 break-all"
                    />
                    <div className="flex items-center justify-between font-mono text-[10px] tabular-nums text-muted-foreground">
                      <span>
                        {pathByteLen}B · {pathHopCount} hops
                      </span>
                      <span>
                        {pathHexValid ? (
                          <span className="text-success">valid</span>
                        ) : (
                          <span className="text-destructive">
                            invalid · must be hex divisible by {hashSize * 2}
                          </span>
                        )}
                      </span>
                    </div>
                  </div>
                )}

                {runMode === "test" && (
                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 pt-2 border-t border-border">
                    <div className="space-y-1.5">
                      <label className="label-overline block">Count</label>
                      <Input
                        type="number"
                        min={1}
                        max={1000}
                        value={testCount}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                          setTestCount(Number(e.target.value) || 1)
                        }
                        className="h-8 rounded-none font-mono text-xs tabular-nums"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <label className="label-overline block">Interval</label>
                      <Select
                        value={String(testIntervalSecs)}
                        onValueChange={(v) => setTestIntervalSecs(Number(v))}
                      >
                        <SelectTrigger className="w-full h-8 font-mono text-xs uppercase tracking-[0.06em] rounded-none">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="rounded-none font-mono text-xs">
                          {[10, 15, 30, 60, 120].map((s) => (
                            <SelectItem key={s} value={String(s)}>
                              {s}s
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-1.5">
                      <label className="label-overline block">
                        Label (optional)
                      </label>
                      <Input
                        value={testLabel}
                        onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                          setTestLabel(e.target.value)
                        }
                        placeholder="e.g. new antenna"
                        className="h-8 rounded-none font-mono text-base md:text-xs"
                      />
                    </div>
                    <p className="sm:col-span-3 font-mono text-[10px] text-muted-foreground/60">
                      {testCount} traces every {testIntervalSecs}s · runs
                      ~{Math.round((testCount * testIntervalSecs) / 60)} min ·
                      be gentle with airtime on a shared channel
                    </p>
                  </div>
                )}

                <div className="pt-2 border-t border-border space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <div className="space-y-0.5 min-w-0 flex-1">
                      <span className="label-overline block">Path · hex</span>
                      <code className="font-mono text-xs text-muted-foreground break-all block">
                        {computedPathHex || (
                          <span className="text-muted-foreground/40">
                            (empty)
                          </span>
                        )}
                      </code>
                    </div>
                    {runMode === "single" ? (
                      <Button
                        onClick={sendTrace}
                        disabled={
                          isWaiting ||
                          !companionName ||
                          !pathHexValid ||
                          (mode === "manual" && !computedPathHex)
                        }
                        className="rounded-none font-mono uppercase tracking-widest gap-2"
                      >
                        <Send className="size-3.5" />
                        {isWaiting ? "waiting…" : "send trace"}
                      </Button>
                    ) : (
                      <div className="flex items-center gap-2 shrink-0">
                        <Button
                          variant="outline"
                          onClick={addLinkMonitor}
                          disabled={
                            addingLink ||
                            !companionName ||
                            !pathHexValid ||
                            (mode === "manual" && !computedPathHex)
                          }
                          title="Poll this path continuously on a schedule"
                          className="rounded-none font-mono uppercase tracking-widest gap-2"
                        >
                          <Link2 className="size-3.5" />
                          {addingLink ? "adding…" : "monitor path"}
                        </Button>
                        <Button
                          onClick={startTest}
                          disabled={
                            starting ||
                            activeTest?.status === "running" ||
                            !companionName ||
                            !pathHexValid ||
                            (mode === "manual" && !computedPathHex)
                          }
                          className="rounded-none font-mono uppercase tracking-widest gap-2"
                        >
                          <Send className="size-3.5" />
                          {activeTest?.status === "running"
                            ? "test running…"
                            : starting
                              ? "starting…"
                              : "start test"}
                        </Button>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div className="lg:col-span-2 space-y-6">
            {runMode === "single" ? (
              <div className="panel">
                <SectionTitle
                  eyebrow={
                    isWaiting
                      ? "Awaiting · live"
                      : result
                        ? "Result"
                        : "Result · idle"
                  }
                  title="Trace timeline"
                  trailing={
                    isWaiting && (
                      <span className="font-mono text-[10px] tabular-nums text-primary">
                        {(remainingMs / 1000).toFixed(1)}s
                      </span>
                    )
                  }
                />
                <div className="px-4 py-4">
                  {!result && !isWaiting && !waitError && (
                    <div className="text-center py-10">
                      <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                      <p className="text-sm text-muted-foreground/60">
                        No trace sent yet
                      </p>
                    </div>
                  )}

                  {(isWaiting || result || waitError) && (
                    <TraceTimeline
                      isWaiting={isWaiting}
                      waitError={waitError}
                      result={result}
                      companionName={companionName}
                      routeHashes={routeHashes}
                      liveSNRs={liveSNRs}
                      peerByHash={peerByHash}
                    />
                  )}
                </div>
              </div>
            ) : (
              <div className="panel">
                <SectionTitle
                  eyebrow={
                    activeTest
                      ? `Test #${activeTest.id} · ${activeTest.status}`
                      : "Signal test · idle"
                  }
                  title="Live progress"
                  trailing={
                    activeTest?.status === "running" && (
                      <Button
                        variant="outline"
                        size="xs"
                        onClick={cancelActiveTest}
                        className="rounded-none font-mono uppercase tracking-widest gap-1"
                      >
                        <Square className="size-3" /> cancel
                      </Button>
                    )
                  }
                />
                <div className="px-4 py-4">
                  {!activeTest ? (
                    <div className="text-center py-10">
                      <CircleDashed className="size-6 mx-auto mb-2 text-muted-foreground/40" />
                      <p className="text-sm text-muted-foreground/60">
                        No test running
                      </p>
                    </div>
                  ) : (
                    <div className="space-y-3">
                      <div className="flex items-center justify-between font-mono text-[11px] tabular-nums text-muted-foreground">
                        <span>
                          {activeTest.runs.length}/{activeTest.count} runs
                        </span>
                        <span>{activeTest.okCount} ok</span>
                      </div>
                      <SignalTestStats
                        stats={computeStats(activeTest.runs)}
                        hopLabel={hopLabelFor(
                          activeTest.path,
                          activeTest.pathHashSize,
                          peers,
                        )}
                      />
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
        </section>
      )}

      {!loading && !error && (
        <SavedTestsPanel
          tests={savedTests}
          loading={testsLoading}
          peers={peers}
          compareIds={compareIds}
          onToggleCompare={toggleCompare}
          onCompare={() => setCompareOpen(true)}
          onRenamed={(id, label) =>
            setSavedTests(
              (prev) =>
                prev?.map((t) => (t.id === id ? { ...t, label } : t)) ?? prev,
            )
          }
          onDeleted={(id) => {
            setSavedTests(
              (prev) => prev?.filter((t) => t.id !== id) ?? prev,
            );
            setCompareIds((prev) => {
              if (!prev.has(id)) return prev;
              const next = new Set(prev);
              next.delete(id);
              return next;
            });
          }}
        />
      )}

      <CompareDialog
        open={compareOpen}
        onOpenChange={setCompareOpen}
        testIds={Array.from(compareIds)}
        tests={savedTests ?? []}
      />
    </div>
  );
}

interface TimelineStep {
  badge: React.ReactNode;
  label: string;
  name?: string;
  hashHex?: string;
  snr?: number;
  showSignal: boolean;
  meta?: string;
  metaError?: boolean;
  state: "active" | "pending" | "done" | "error";
}

function TraceTimeline({
  isWaiting,
  waitError,
  result,
  companionName,
  routeHashes,
  liveSNRs,
  peerByHash,
}: {
  isWaiting: boolean;
  waitError: string | null;
  result: TraceResult | null;
  companionName: string;
  routeHashes: string[];
  liveSNRs: number[];
  peerByHash: Map<string, { name: string; alternatives: number }>;
}) {
  const steps = useMemo<TimelineStep[]>(() => {
    const out: TimelineStep[] = [];

    // Prefer the hashes the firmware echoed back over the path we sent.
    const hashes =
      result && result.path.length
        ? result.path.map((h) => h.toLowerCase())
        : routeHashes;
    const snrs = result ? result.hopSNRs : liveSNRs;

    // 1. Us — this ↓SNR is how strongly we received the final packet back.
    out.push({
      badge: "TX",
      label: "Started the trace",
      name: companionName || "—",
      snr: result?.snr ?? undefined,
      showSignal: result != null && result.snr != null,
      state: "done",
    });

    // 2. One node per hop; the next-expected hop pulses while we wait.
    hashes.forEach((hash, i) => {
      const snr = snrs[i];
      const hasSnr = typeof snr === "number";
      let state: TimelineStep["state"];
      if (hasSnr) state = "done";
      else if (isWaiting && i === snrs.length) state = "active";
      else state = "pending";
      out.push({
        badge: hash.slice(0, 2).toUpperCase(),
        label: `Hop ${i + 1} · Repeated the packet`,
        name: hopName(peerByHash.get(hash)),
        hashHex: hash,
        snr: hasSnr ? snr : undefined,
        showSignal: hasSnr,
        state,
      });
    });

    // 3. Completion marker — final packet received, timed out, or still waiting.
    if (result) {
      out.push({
        badge: <Check className="size-4" />,
        label: "Received trace response",
        meta: `Duration: ${result.elapsedMs}ms`,
        showSignal: false,
        state: "done",
      });
    } else if (waitError) {
      out.push({
        badge: <X className="size-4" />,
        label: "No trace response",
        meta: waitError,
        metaError: true,
        showSignal: false,
        state: "error",
      });
    } else {
      out.push({
        badge: "RX",
        label: "Awaiting trace response",
        name: "listening…",
        showSignal: false,
        state: "active",
      });
    }

    return out;
  }, [companionName, result, routeHashes, liveSNRs, isWaiting, waitError, peerByHash]);

  return (
    <ol className="relative">
      {steps.map((step, idx) => (
        <li key={idx} className="flex gap-3 relative pb-3 last:pb-0">
          <div className="relative flex flex-col items-center">
            <div
              className={cn(
                "size-9 border flex items-center justify-center font-mono text-[10px] uppercase tabular-nums shrink-0",
                step.state === "done" &&
                  "border-primary/40 bg-primary/10 text-primary",
                step.state === "active" &&
                  "border-primary/40 bg-primary/10 text-primary scan-pulse",
                step.state === "pending" &&
                  "border-border bg-muted/40 text-muted-foreground/60",
                step.state === "error" &&
                  "border-destructive/40 bg-destructive/10 text-destructive",
              )}
            >
              {step.badge}
            </div>
            {idx < steps.length - 1 && (
              <div
                className={cn(
                  "w-px flex-1 mt-1",
                  step.state === "done" ? "bg-primary/30" : "bg-border",
                )}
              />
            )}
          </div>
          <div className="flex-1 min-w-0 pb-2 pt-0.5 space-y-0.5">
            <div className="flex items-center justify-between gap-2">
              <span className="label-overline">{step.label}</span>
              {step.showSignal && step.snr != null && (
                <SignalStrength snr={step.snr} size="sm" />
              )}
            </div>
            {(step.name || step.hashHex) && (
              <div className="font-mono text-xs truncate">
                {step.name || (
                  <span className="text-muted-foreground/60 italic">
                    unknown
                  </span>
                )}
              </div>
            )}
            {step.hashHex && (
              <code className="font-mono text-[10px] tabular-nums text-muted-foreground">
                {step.hashHex}
              </code>
            )}
            {step.meta && (
              <div
                className={cn(
                  "font-mono text-[11px] tabular-nums",
                  step.metaError ? "text-destructive" : "text-success",
                )}
              >
                {step.meta}
              </div>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}

function SavedTestsPanel({
  tests,
  loading,
  peers,
  compareIds,
  onToggleCompare,
  onCompare,
  onRenamed,
  onDeleted,
}: {
  tests: SignalTestSummary[] | null;
  loading: boolean;
  peers: Peer[];
  compareIds: Set<number>;
  onToggleCompare: (id: number) => void;
  onCompare: () => void;
  onRenamed: (id: number, label: string) => void;
  onDeleted: (id: number) => void;
}) {
  return (
    <section className="panel">
      <SectionTitle
        eyebrow="Signal tests · saved"
        title="Test history"
        trailing={
          compareIds.size > 1 && (
            <Button
              variant="outline"
              size="xs"
              onClick={onCompare}
              className="rounded-none font-mono uppercase tracking-widest gap-1"
            >
              <GitCompare className="size-3" /> compare ({compareIds.size})
            </Button>
          )
        }
      />
      {loading && !tests ? (
        <div className="px-4 py-6 space-y-2">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : !tests || tests.length === 0 ? (
        <div className="px-4 py-8 text-center text-sm text-muted-foreground/60">
          No saved tests yet · run a signal test above to see results here
        </div>
      ) : (
        <div className="divide-y divide-border/60">
          {tests.map((t) => (
            <SavedTestRow
              key={t.id}
              test={t}
              peers={peers}
              selected={compareIds.has(t.id)}
              onToggleSelect={() => onToggleCompare(t.id)}
              onRenamed={(label) => onRenamed(t.id, label)}
              onDeleted={() => onDeleted(t.id)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function StatusPill({ status }: { status: SignalTestSummary["status"] }) {
  const styles: Record<string, string> = {
    running: "border-primary/40 bg-primary/10 text-primary",
    done: "border-success/40 bg-success/10 text-success",
    cancelled: "border-border bg-muted/40 text-muted-foreground",
    interrupted: "border-destructive/40 bg-destructive/10 text-destructive",
  };
  return (
    <span
      className={cn(
        "px-1.5 py-0.5 border font-mono text-[9px] uppercase tracking-[0.1em] w-24 text-center shrink-0",
        styles[status] || styles.cancelled,
      )}
    >
      {status}
    </span>
  );
}

function SavedTestRow({
  test,
  peers,
  selected,
  onToggleSelect,
  onRenamed,
  onDeleted,
}: {
  test: SignalTestSummary;
  peers: Peer[];
  selected: boolean;
  onToggleSelect: () => void;
  onRenamed: (label: string) => void;
  onDeleted: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [detail, setDetail] = useState<SignalTestDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [labelDraft, setLabelDraft] = useState(test.label);

  const toggleExpanded = () => {
    const next = !expanded;
    setExpanded(next);
    if (next && !detail) {
      setDetailLoading(true);
      signalTestApi
        .get(test.id)
        .then(setDetail)
        .catch(() => toast.error("Failed to load test detail"))
        .finally(() => setDetailLoading(false));
    }
  };

  const saveLabel = async () => {
    try {
      await signalTestApi.update(test.id, { label: labelDraft });
      onRenamed(labelDraft);
      setRenaming(false);
    } catch (err) {
      toast.error(`Rename failed: ${(err as Error).message}`);
    }
  };

  const doDelete = async () => {
    try {
      await signalTestApi.remove(test.id);
      onDeleted();
    } catch (err) {
      toast.error(`Delete failed: ${(err as Error).message}`);
    }
  };

  const hopCount =
    test.pathHashSize > 0
      ? test.path.length / (test.pathHashSize * 2)
      : 0;
  const successPct =
    test.runsDone > 0 ? Math.round((test.okCount / test.runsDone) * 100) : 0;

  return (
    <div>
      <div className="flex items-center gap-3 px-4 py-2.5">
        <label className="flex shrink-0 p-3 -m-3 sm:p-0 sm:m-0">
          <input
            type="checkbox"
            checked={selected}
            onChange={onToggleSelect}
            className="size-3.5 accent-primary"
            aria-label="Select for compare"
          />
        </label>
        <button
          type="button"
          onClick={toggleExpanded}
          className="text-muted-foreground/60 hover:text-foreground shrink-0"
          aria-label={expanded ? "collapse" : "expand"}
        >
          {expanded ? (
            <ChevronDown className="size-3.5" />
          ) : (
            <ChevronRight className="size-3.5" />
          )}
        </button>
        <div className="min-w-0 flex-1">
          {renaming ? (
            <div className="flex items-center gap-2">
              <Input
                value={labelDraft}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                  setLabelDraft(e.target.value)
                }
                className="h-7 rounded-none font-mono text-base md:text-xs"
                autoFocus
              />
              <Button
                size="xs"
                onClick={saveLabel}
                className="rounded-none font-mono uppercase tracking-widest"
              >
                <Check className="size-3" />
              </Button>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => {
                  setLabelDraft(test.label);
                  setRenaming(false);
                }}
                className="rounded-none font-mono uppercase tracking-widest"
              >
                <X className="size-3" />
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium truncate">
                {test.label || `Test #${test.id}`}
              </span>
              <button
                type="button"
                onClick={() => setRenaming(true)}
                className="text-muted-foreground/40 hover:text-primary shrink-0"
                aria-label="rename"
              >
                <Pencil className="size-3" />
              </button>
            </div>
          )}
          <div className="font-mono text-[10px] text-muted-foreground tabular-nums">
            {formatDateTime(test.startedAt * 1000)} · {hopCount} hops ·{" "}
            {test.intervalSecs}s interval
          </div>
        </div>
        <StatusPill status={test.status} />
        <span className="font-mono text-xs tabular-nums text-muted-foreground w-16 text-right shrink-0">
          {test.runsDone}/{test.count}
        </span>
        <span className="font-mono text-xs tabular-nums text-muted-foreground w-12 text-right shrink-0">
          {successPct}%
        </span>
        <InlineConfirm
          confirming={confirmingDelete}
          onAskRemove={() => setConfirmingDelete(true)}
          onCancel={() => setConfirmingDelete(false)}
          onConfirm={doDelete}
          iconOnly
          ariaLabel="Delete test"
        />
      </div>
      {expanded && (
        <div className="px-4 pb-4 pl-11">
          {detailLoading || !detail ? (
            <Skeleton className="h-24" />
          ) : (
            <SignalTestStats
              stats={detail.stats}
              hopLabel={hopLabelFor(detail.path, detail.pathHashSize, peers)}
            />
          )}
        </div>
      )}
    </div>
  );
}

function CompareDialog({
  open,
  onOpenChange,
  testIds,
  tests,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  testIds: number[];
  tests: SignalTestSummary[];
}) {
  const [details, setDetails] = useState<Record<number, SignalTestDetail>>(
    {},
  );
  const [loading, setLoading] = useState(false);
  const idsKey = testIds.join(",");

  useEffect(() => {
    if (!open || testIds.length === 0) return;
    setLoading(true);
    Promise.all(testIds.map((id) => signalTestApi.get(id)))
      .then((list) => {
        const map: Record<number, SignalTestDetail> = {};
        list.forEach((d) => {
          map[d.id] = d;
        });
        setDetails(map);
      })
      .catch(() => toast.error("Failed to load test comparison"))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, idsKey]);

  const maxHop = Math.max(
    0,
    ...testIds.map((id) => details[id]?.stats.perHop.length ?? 0),
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="rounded-none border-border bg-card max-w-3xl">
        <DialogHeader>
          <DialogTitle className="font-mono uppercase tracking-[0.08em] text-sm">
            Compare tests
          </DialogTitle>
        </DialogHeader>
        {loading ? (
          <Skeleton className="h-40" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full font-mono text-[11px] border border-border">
              <thead>
                <tr className="bg-muted/40 text-muted-foreground/70 uppercase tracking-[0.08em]">
                  <th className="text-left px-2 py-1 font-normal">Metric</th>
                  {testIds.map((id) => (
                    <th key={id} className="text-right px-2 py-1 font-normal">
                      {tests.find((t) => t.id === id)?.label || `#${id}`}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                <tr>
                  <td className="px-2 py-1 text-muted-foreground/70">
                    Success rate
                  </td>
                  {testIds.map((id) => (
                    <td
                      key={id}
                      className="px-2 py-1 text-right tabular-nums"
                    >
                      {details[id]
                        ? `${(details[id].stats.successRate * 100).toFixed(0)}%`
                        : "—"}
                    </td>
                  ))}
                </tr>
                <tr>
                  <td className="px-2 py-1 text-muted-foreground/70">
                    Timeouts
                  </td>
                  {testIds.map((id) => (
                    <td
                      key={id}
                      className="px-2 py-1 text-right tabular-nums"
                    >
                      {details[id]?.stats.timeouts ?? "—"}
                    </td>
                  ))}
                </tr>
                {Array.from({ length: maxHop }, (_, i) => i + 1).map(
                  (hop) => (
                    <tr key={hop}>
                      <td className="px-2 py-1 text-muted-foreground/70">
                        Hop {hop}
                      </td>
                      {testIds.map((id) => {
                        const h = details[id]?.stats.perHop.find(
                          (x) => x.hop === hop,
                        );
                        return (
                          <td
                            key={id}
                            className="px-2 py-1 text-right tabular-nums"
                          >
                            {h
                              ? `${h.mean.toFixed(1)}±${h.stddev.toFixed(1)} (${h.min.toFixed(1)}…${h.max.toFixed(1)})`
                              : "—"}
                          </td>
                        );
                      })}
                    </tr>
                  ),
                )}
              </tbody>
            </table>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function TracesSkeleton() {
  return (
    <div className="grid grid-cols-1 lg:grid-cols-5 gap-6">
      <div className="lg:col-span-3 panel p-4 space-y-4">
        <Skeleton className="h-4 w-32" />
        <div className="grid grid-cols-2 gap-4">
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
        </div>
        <Skeleton className="h-32" />
        <Skeleton className="h-9 w-32" />
      </div>
      <div className="lg:col-span-2 panel p-4 space-y-3">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-9" />
        <Skeleton className="h-9" />
        <Skeleton className="h-9" />
      </div>
    </div>
  );
}
