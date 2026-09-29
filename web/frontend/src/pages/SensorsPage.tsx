import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CircleDashed, Clock, Plus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { PageHeader } from "@/components/PageHeader";
import { SensorDialog } from "@/components/SensorDialog";
import { Led, SensorModule, isWide } from "@/components/SensorModule";
import { useApiList } from "@/hooks/useApiList";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useResume } from "@/lib/resume";
import { cn } from "@/lib/utils";
import { deleteSensor, type Sensor, type SensorKind } from "@/lib/sensorsApi";
import { SENSOR_STATES, STALE_SECS, ageText, stateOf, type SensorState } from "@/lib/sensorView";

// Catalogue groups in the order an operator reads a rig; any other category follows, alphabetically.
const GROUPS = ["Environment", "Power", "Analogue", "Online", "Derived"];

export function SensorsPage() {
  const {
    items: sensors,
    loading,
    error,
    reload,
    refresh,
    replace,
  } = useApiList<Sensor>("/api/sensors", "Failed to load sensors");
  // The catalogue is fixed for the process, so it is loaded once here and the dialog reads it.
  const {
    items: kinds,
    error: kindsError,
    reload: reloadKinds,
  } = useApiList<SensorKind>("/api/sensors/kinds", "Failed to load the parts catalogue");
  const [confirmRemove, setConfirmRemove] = useState<number | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  // null while adding; the sensor being changed while editing.
  const [editing, setEditing] = useState<Sensor | null>(null);
  const [filter, setFilter] = useState<SensorState | null>(null);
  const [linked, setLinked] = useState<number[] | null>(null);

  // Stamped as each set lands, so an age counts on from the host's own and never compares two clocks.
  const [received, setReceived] = useState(() => ({ list: sensors, at: Date.now() }));
  if (received.list !== sensors) setReceived({ list: sensors, at: Date.now() });
  const now = useNow(1000);
  const sinceSecs = Math.max(0, (now - received.at) / 1000);

  // A socket that slept missed the pushes, so the page asks again rather than show what it last heard.
  useResume(refresh);

  // Every poll pass pushes the whole set, so the page replaces rather than merges.
  useWebSocket(
    ["sensors"],
    useCallback(
      (topic: string, data: unknown) => {
        if (topic === "sensors") replace((data as Sensor[]) || []);
      },
      [replace],
    ),
  );

  // A double click on "yes" would otherwise send the delete twice.
  const removing = useRef(false);
  const remove = useCallback(
    async (s: Sensor) => {
      if (removing.current) return;
      removing.current = true;
      try {
        await deleteSensor(s.id);
        toast.success(`Removed ${s.name}`);
        setConfirmRemove(null);
        refresh();
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed to remove sensor");
      } finally {
        removing.current = false;
      }
    },
    [refresh],
  );

  const openAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const view = useMemo(() => {
    const list = sensors ?? [];
    const counts = Object.fromEntries(SENSOR_STATES.map((k) => [k, 0])) as Record<SensorState, number>;
    const feeds = new Map<number, Sensor[]>();
    for (const s of list) {
      counts[stateOf(s)]++;
      for (const b of s.bindings) {
        const l = feeds.get(b.sensorId) ?? [];
        if (!l.includes(s)) l.push(s);
        feeds.set(b.sensorId, l);
      }
    }
    const byGroup = new Map<string, Sensor[]>();
    for (const s of list) {
      const g = s.category || "Other";
      byGroup.set(g, [...(byGroup.get(g) ?? []), s]);
    }
    const rank = (g: string) => (GROUPS.includes(g) ? GROUPS.indexOf(g) : GROUPS.length);
    const groups = [...byGroup.entries()]
      .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
      // A wide card leads its group, so it never leaves a hole in the row above it.
      .map(([name, members]) => ({ name, members: members.sort((a, b) => Number(isWide(b)) - Number(isWide(a))) }));
    return { counts, feeds, groups };
  }, [sensors]);

  const shown = view.groups
    .map((g) => ({ ...g, members: filter ? g.members.filter((s) => stateOf(s) === filter) : g.members }))
    .filter((g) => g.members.length > 0);
  // Nothing arriving at all is the socket or the poll, not seven sensors, so it is said once.
  const silent = sensors !== null && sensors.length > 0 && sinceSecs > STALE_SECS;

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="Attached to this host"
        title="Sensors"
        meta={
          sensors ? (
            <span className="font-mono text-sm text-muted-foreground tabular-nums">
              {sensors.length} configured
            </span>
          ) : null
        }
        actions={
          <Button size="sm" onClick={openAdd} className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]">
            <Plus className="size-3.5" />
            Add sensor
          </Button>
        }
      />

      {loading ? <SensorsSkeleton /> : null}
      {error ? <LoadErrorAlert message={error} onRetry={reload} /> : null}

      {!loading && !error && sensors ? (
        sensors.length === 0 ? (
          <section className="panel px-6 py-14 text-center">
            <CircleDashed className="mx-auto mb-3 size-8 text-muted-foreground/40" />
            <p className="font-mono text-sm uppercase tracking-widest text-muted-foreground">No sensors yet</p>
            <p className="mx-auto mt-2 mb-4 max-w-[46ch] text-[13px] text-muted-foreground">
              Add a part wired to this Pi to read temperature, pressure, air quality or a voltage. Adding one scans
              the I2C buses first. A scan only reads, and nothing is written to a part until you choose it.
            </p>
            <Button size="sm" onClick={openAdd} className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]">
              <Plus className="size-3.5" />
              Add sensor
            </Button>
          </section>
        ) : (
          <div>
            <div className="mb-6 flex flex-wrap items-stretch gap-1.5">
              {SENSOR_STATES.map((k) => {
                const n = view.counts[k];
                return (
                  <button
                    key={k}
                    type="button"
                    aria-pressed={filter === k}
                    disabled={n === 0 && filter !== k}
                    onClick={() => setFilter(filter === k ? null : k)}
                    className={cn(
                      "inline-flex min-h-8 flex-auto items-center gap-2 border border-border bg-card px-2.5 font-mono text-[11px] uppercase tracking-widest text-muted-foreground sm:flex-none",
                      n === 0 && "opacity-55",
                      n > 0 && k === "calibrating" && "border-primary/45",
                      n > 0 && k === "stale" && "border-warning/50 bg-warning/7 text-warning",
                      n > 0 && k === "failing" && "border-destructive/50 bg-destructive/7 text-destructive",
                      filter === k && "outline -outline-offset-1 outline-foreground",
                    )}
                  >
                    <Led state={k} />
                    <b
                      className={cn(
                        "min-w-[1.2ch] text-right font-mono text-sm font-semibold tracking-normal tabular-nums text-foreground",
                        n === 0 && "text-muted-foreground",
                        n > 0 && k === "calibrating" && "text-primary",
                        n > 0 && k === "stale" && "text-warning",
                        n > 0 && k === "failing" && "text-destructive",
                      )}
                    >
                      {n}
                    </b>
                    {k}
                  </button>
                );
              })}
              <span
                className={cn(
                  "w-full self-center font-mono text-[11px] tabular-nums text-muted-foreground sm:ml-auto sm:w-auto",
                  silent && "text-warning",
                )}
              >
                updated {ageText(sinceSecs)} ago
              </span>
            </div>

            {silent ? (
              <div className="mb-4 flex items-start gap-2.5 border border-warning/45 bg-warning/7 px-3 py-2.5 font-mono text-[11.5px] leading-normal text-warning">
                <Clock className="mt-0.5 size-3.5 shrink-0" strokeWidth={1.8} />
                <div>
                  <p className="font-sans text-[13px] text-foreground">
                    No update from OwlShack for {ageText(sinceSecs)}, so every value on this page is at least that old.
                  </p>
                  The connection or the sensor poll has stopped. The counts are right again once updates arrive.
                </div>
              </div>
            ) : null}

            {shown.length === 0 ? (
              <p className="font-mono text-[11px] text-muted-foreground">Nothing in this state.</p>
            ) : null}

            <div className="space-y-7">
              {shown.map((g) => (
                <section key={g.name}>
                  <div className="mb-2.5 flex items-center gap-2.5">
                    <h2 className="font-mono text-xs font-semibold uppercase tracking-[0.14em]">{g.name}</h2>
                    <span className="h-px flex-1 bg-border" />
                    <span className="font-mono text-[11px] tabular-nums text-muted-foreground">{g.members.length}</span>
                  </div>
                  <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,17rem),1fr))] items-start gap-3">
                    {g.members.map((s) => (
                      <SensorModule
                        key={s.id}
                        sensor={s}
                        all={sensors}
                        kind={kinds?.find((k) => k.provider === s.provider && k.kind === s.kind)}
                        feeds={view.feeds.get(s.id) ?? []}
                        age={s.ageSecs === null ? null : s.ageSecs + sinceSecs}
                        retryIn={s.retryInSecs === null ? null : Math.max(0, s.retryInSecs - sinceSecs)}
                        linked={linked?.includes(s.id) ?? false}
                        confirming={confirmRemove === s.id}
                        onHover={setLinked}
                        onEdit={() => {
                          setEditing(s);
                          setDialogOpen(true);
                        }}
                        onAskRemove={() => setConfirmRemove(s.id)}
                        onCancel={() => setConfirmRemove(null)}
                        onConfirm={() => remove(s)}
                      />
                    ))}
                  </div>
                </section>
              ))}
            </div>
          </div>
        )
      ) : null}

      <SensorDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        editing={editing}
        sensors={sensors ?? []}
        kinds={kinds}
        kindsError={kindsError}
        onRetryKinds={reloadKinds}
        onSaved={() => {
          setDialogOpen(false);
          refresh();
        }}
      />
    </div>
  );
}

// One clock for the page, so every age on it moves together.
function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(id);
  }, [everyMs]);
  return now;
}

function SensorsSkeleton() {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,17rem),1fr))] gap-3">
      {[...Array(3)].map((_, i) => (
        <div key={i} className="panel">
          <div className="space-y-2 px-3.5 py-3">
            <Skeleton className="h-3.5 w-32" />
            <Skeleton className="h-2.5 w-24" />
          </div>
          <div className="grid grid-cols-2 gap-px border-t border-border bg-border">
            {[...Array(2)].map((_, j) => (
              <div key={j} className="space-y-2 bg-card px-3.5 py-3.5">
                <Skeleton className="h-2.5 w-16" />
                <Skeleton className="h-7 w-20" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
