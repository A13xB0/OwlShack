import { useCallback, useEffect, useState } from "react";
import { Antenna, Loader2, RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { SectionTitle } from "@/components/SectionTitle";
import { StatTile } from "@/components/NodeStatTiles";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { batteryBand, type Band } from "@/lib/metrics";
import { fetchRadioStatus, resetModem, type RadioStatus } from "@/lib/radioApi";

const POLL_MS = 5000;

// An absent counter is not a zero: this transport cannot measure it at all.
const ABSENT = "—";

function fmtUptime(secs: number): string {
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${secs}s`;
}

// Any non-zero fault count is worth an operator's eye; the tile colour is the only thing that scans.
function faultBand(n: number): Band {
  return n > 0 ? "warn" : "none";
}

function num(n: number): string {
  return n.toLocaleString();
}

function opt(n: number | undefined): string {
  return n === undefined ? ABSENT : num(n);
}

// A radio that has attempted transmits and failed every one is broken, which is exactly the case a
// counter alone reads past — it was in the payload the whole time and nobody was looking at it.
function txVerdict(s: RadioStatus): { band: Band; text: string } {
  const attempts = s.txSent + s.txFailed;
  if (attempts === 0) return { band: "none", text: "nothing sent yet" };
  if (s.txFailed === 0) return { band: "good", text: "all transmits confirmed" };
  if (s.txSent === 0) {
    return { band: "bad", text: `every transmit failed (${num(s.txFailed)}) — suspect the radio, not the link` };
  }
  const pct = Math.round((s.txFailed / attempts) * 100);
  return { band: "warn", text: `${num(s.txFailed)} of ${num(attempts)} transmits failed (${pct}%)` };
}

export function RadioHealthPage() {
  const [status, setStatus] = useState<RadioStatus | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [resetting, setResetting] = useState(false);

  useEffect(() => {
    let live = true;
    let inFlight = false;
    const poll = async () => {
      if (inFlight) return; // the endpoint polls the board, so a slow read must not stack up
      inFlight = true;
      try {
        const s = await fetchRadioStatus();
        if (live) setStatus(s);
      } catch {
        if (live) setStatus(null);
      } finally {
        inFlight = false;
        if (live) setLoaded(true);
      }
    };
    poll();
    const id = setInterval(poll, POLL_MS);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, []);

  const doReset = useCallback(async () => {
    setConfirming(false);
    setResetting(true);
    try {
      await resetModem();
      toast.success("Modem reset requested", {
        description: "The radio reconnects in the background; counters restart from zero.",
      });
    } catch (e) {
      toast.error("Reset failed", { description: (e as Error).message });
    } finally {
      setResetting(false);
    }
  }, []);

  if (!loaded) {
    return (
      <div className="flex flex-col gap-4">
        <PageHeader eyebrow="Diagnostics" title="Radio" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!status) {
    return (
      <div className="flex flex-col gap-4">
        <PageHeader eyebrow="Diagnostics" title="Radio" />
        <div className="bg-card border border-border rounded-md px-4 py-6 text-sm text-muted-foreground">
          The modem is not connected, so it has no counters to report. This is what a failed
          reconnect looks like — check the log for <span className="font-mono">modem reconnect attempt failed</span>.
        </div>
      </div>
    );
  }

  const spi = status.transport === "spi";
  const tx = txVerdict(status);
  const radioLine = `${(status.freqHz / 1_000_000).toFixed(3)} MHz · ${(status.bwHz / 1000).toFixed(1)} kHz · SF${status.sf} · CR${status.cr} · ${status.txPower} dBm`;

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        eyebrow="Diagnostics"
        title="Radio"
        meta={
          <span className="font-mono text-xs text-muted-foreground">
            {status.transport.toUpperCase()} · {radioLine}
          </span>
        }
        actions={
          resetting ? (
            <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" /> resetting
            </span>
          ) : (
            <span className="inline-flex items-center gap-2">
              {confirming ? (
                <>
                  <span className="text-xs text-muted-foreground">Drop the radio and reconnect?</span>
                  <Button size="sm" variant="destructive" onClick={doReset}>
                    Reset
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>
                    Cancel
                  </Button>
                </>
              ) : (
                <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>
                  <RotateCcw className="size-3.5" />
                  Reset modem
                </Button>
              )}
            </span>
          )
        }
      />

      <div className="bg-card border border-border rounded-md px-4 py-3 flex items-center gap-3">
        <Antenna
          className={
            tx.band === "bad"
              ? "size-4 text-signal-weak"
              : tx.band === "warn"
                ? "size-4 text-warning"
                : "size-4 text-muted-foreground"
          }
        />
        <span className="text-sm">{tx.text}</span>
      </div>

      <section className="flex flex-col gap-2">
        <SectionTitle eyebrow="Board" title="Readings" />
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-px bg-border rounded-md overflow-hidden">
          <StatTile label="Uptime" value={fmtUptime(status.uptimeSecs)} />
          <StatTile
            label="Battery"
            value={status.batteryMv === undefined ? ABSENT : (status.batteryMv / 1000).toFixed(2)}
            unit={status.batteryMv === undefined ? undefined : "V"}
            band={status.batteryMv === undefined ? "none" : batteryBand(status.batteryMv)}
          />
          <StatTile
            label="MCU temp"
            value={status.mcuTempC === undefined ? ABSENT : status.mcuTempC.toFixed(1)}
            unit={status.mcuTempC === undefined ? undefined : "°C"}
          />
          <StatTile
            label="Noise floor"
            value={status.noiseFloor === undefined ? ABSENT : String(status.noiseFloor)}
            unit={status.noiseFloor === undefined ? undefined : "dBm"}
          />
        </div>
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle eyebrow="Transmit" title="Outcomes" />
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-px bg-border rounded-md overflow-hidden">
          <StatTile label="Sent" value={num(status.txSent)} band={status.txSent > 0 ? "good" : "none"} />
          <StatTile label="Failed" value={num(status.txFailed)} band={faultBand(status.txFailed)} />
          <StatTile label="Requeued" value={num(status.txRequeued)} />
          <StatTile label="Dropped busy" value={num(status.txDroppedBusy)} band={faultBand(status.txDroppedBusy)} />
          <StatTile label="Dropped queue" value={num(status.txDroppedQueue)} band={faultBand(status.txDroppedQueue)} />
          <StatTile label="Queue" value={num(status.txQueueLen)} band={status.txQueueLen > 0 ? "warn" : "none"} />
        </div>
      </section>

      <section className="flex flex-col gap-2">
        <SectionTitle eyebrow="Receive" title="Losses and stalls" />
        <div className="grid grid-cols-2 sm:grid-cols-5 gap-px bg-border rounded-md overflow-hidden">
          <StatTile label="Queue full (new)" value={num(status.inboundDroppedNew)} band={faultBand(status.inboundDroppedNew)} />
          <StatTile label="Queue full (oldest)" value={opt(status.inboundDroppedOldest)} band={faultBand(status.inboundDroppedOldest ?? 0)} />
          <StatTile label="Board frame decode" value={opt(status.hwDecodeErrors)} band={faultBand(status.hwDecodeErrors ?? 0)} />
          <StatTile label="Radio recv errors" value={opt(status.recvErrors)} band={faultBand(status.recvErrors ?? 0)} />
          <StatTile label="Handler slow" value={num(status.handlerSlow)} band={faultBand(status.handlerSlow)} />
          <StatTile label="Hardware errors" value={opt(status.hwErrors)} band={faultBand(status.hwErrors ?? 0)} />
          <StatTile label="TX outcome lost" value={opt(status.txOutcomeLost)} band={faultBand(status.txOutcomeLost ?? 0)} />
          <StatTile label="Signal meta timeouts" value={opt(status.rxMetaTimeouts)} band={faultBand(status.rxMetaTimeouts ?? 0)} />
          <StatTile
            label="Signal meta unmatched"
            value={opt(status.rxMetaMisattributed)}
            band={faultBand(status.rxMetaMisattributed ?? 0)}
          />
        </div>
      </section>

      {spi && (
        <section className="flex flex-col gap-2">
          <SectionTitle eyebrow="SPI radio" title="Chip counters" />
          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-px bg-border rounded-md overflow-hidden">
            <StatTile label="Packets recv" value={opt(status.packetsRecv)} />
            <StatTile label="Packets sent" value={opt(status.packetsSent)} />
            {/* No band: CRC errors are ordinary background on a shared band, and a tile that is
                always amber teaches people to ignore the ones that are not. */}
            <StatTile label="CRC errors" value={opt(status.crcErrors)} />
            <StatTile label="Driver errors" value={opt(status.driverErrors)} band={faultBand(status.driverErrors ?? 0)} />
            <StatTile
              label="Receiver recoveries"
              value={opt(status.recvRecoveries)}
              band={faultBand(status.recvRecoveries ?? 0)}
            />
          </div>
        </section>
      )}

      <p className="text-xs text-muted-foreground">
        {ABSENT} means this transport cannot measure that counter, which is not the same as zero.
        Counters restart from zero whenever the modem reconnects.
      </p>
    </div>
  );
}
