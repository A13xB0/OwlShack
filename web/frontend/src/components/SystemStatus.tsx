import { useCallback, useEffect, useRef, useState } from "react";
import { Activity } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useResume } from "@/lib/resume";
import { cn } from "@/lib/utils";

interface Health {
  status: "ok" | "degraded";
  problems: string[];
  version: string;
  uptimeSecs: number;
}

// Health problems are thresholds of about two minutes, so 30 s adds little; the socket catches a lost server at once.
const POLL_MS = 30_000;

function uptime(secs: number): string {
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`;
}

// The sidebar's system line, read from /api/health, the same answer an external monitor gets.
export function SystemStatus() {
  const { connected, pending } = useWebSocket([], undefined, false);
  const [health, setHealth] = useState<Health | null>(null);
  const [failed, setFailed] = useState(false);
  const seq = useRef(0);

  const check = useCallback(async () => {
    if (document.visibilityState === "hidden") return;
    const n = ++seq.current;
    try {
      const r = await fetch("/api/health", { signal: AbortSignal.timeout(10_000) });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const h = (await r.json()) as Health;
      if (n !== seq.current) return;
      setHealth(h);
      setFailed(false);
    } catch {
      if (n === seq.current) setFailed(true);
    }
  }, []);

  // The first check is the socket's connect below, so a page load asks once.
  useEffect(() => {
    const t = window.setInterval(() => void check(), POLL_MS);
    return () => window.clearInterval(t);
  }, [check]);
  useResume(check);
  useEffect(() => {
    if (connected) void check();
  }, [connected, check]);

  const offline = failed || (!pending && !connected);
  const count = health?.problems.length ?? 0;
  const state = offline ? "offline" : !health ? "checking" : health.status === "ok" ? "nominal" : "degraded";
  // Offline matches the LIVE pill's destructive, so the sidebar and the page header agree about the socket.
  const tone = {
    nominal: "text-success",
    checking: "text-muted-foreground",
    degraded: "text-warning",
    offline: "text-destructive",
  }[state];

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`System ${state}${state === "degraded" ? `, ${count} problem${count === 1 ? "" : "s"}` : ""}`}
          className="group relative flex w-full items-center justify-between px-2 py-2.5 md:py-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 before:absolute before:inset-x-0 before:-top-1.5 before:-bottom-0.5 before:content-[''] focus-visible:outline focus-visible:outline-1 focus-visible:outline-primary"
        >
          <span>system</span>
          <span className={cn("inline-flex items-center gap-1.5", tone)}>
            <Activity className="size-2.5" />
            <span className="underline decoration-dotted decoration-current/40 underline-offset-2 group-hover:decoration-current">
              {state}
            </span>
            {state === "degraded" ? <span className="tabular-nums">{count}</span> : null}
          </span>
        </button>
      </PopoverTrigger>
      <PopoverContent side="top" align="end" className="w-72 p-0">
        <div className="border-b border-border px-3 py-2">
          <span className="label-overline">System {state}</span>
        </div>
        {offline ? (
          <p className="px-3 py-2 text-xs text-muted-foreground">
            Can't reach OwlShack. Check it's running and this device is on its network. This updates when
            it's back.
          </p>
        ) : health && health.problems.length > 0 ? (
          <ul className="max-h-56 overflow-y-auto py-1">
            {health.problems.map((p) => (
              <li key={p} className="px-3 py-1.5 text-xs break-words">
                {p}
              </li>
            ))}
          </ul>
        ) : (
          <p className="px-3 py-2 text-xs text-muted-foreground">
            {health ? "The radio is answering and your nodes are running." : "Checking…"}
          </p>
        )}
        {health && !offline ? (
          <p className="border-t border-border px-3 py-2 font-mono text-[10px] text-muted-foreground tabular-nums">
            {health.version} · up {uptime(health.uptimeSecs)}
          </p>
        ) : null}
      </PopoverContent>
    </Popover>
  );
}
