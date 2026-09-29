import { Clock, Pencil, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineConfirm } from "@/components/InlineConfirm";
import { cn } from "@/lib/utils";
import type { Sensor, SensorKind, SensorReading } from "@/lib/sensorsApi";
import {
  ACCURACY,
  ageText,
  displayReading,
  iaqBand,
  readingLabel,
  splitReadings,
  stateOf,
  uncalibrated,
  type SensorState,
} from "@/lib/sensorView";

const WORD: Record<SensorState, string> = {
  healthy: "",
  calibrating: "Calibrating",
  waiting: "Waiting",
  stale: "Stale",
  failing: "Failing",
};

// Shape as well as colour, so a state survives colour blindness and a greyscale screen.
export function Led({ state, className }: { state: SensorState; className?: string }) {
  return (
    <span
      role="img"
      aria-label={state}
      className={cn(
        "inline-block shrink-0",
        state === "failing"
          ? "h-2.5 w-2.75 bg-destructive [clip-path:polygon(50%_0,100%_100%,0_100%)]"
          : "size-2.25",
        state === "healthy" && "bg-primary",
        state === "calibrating" &&
          "border-[1.5px] border-primary bg-[linear-gradient(90deg,var(--primary)_50%,transparent_50%)]",
        state === "waiting" && "border-[1.5px] border-dashed border-muted-foreground",
        state === "stale" && "border-[1.5px] border-warning",
        className,
      )}
    />
  );
}

// wide is a card that spans its row: several headline values, details or a calibration block.
export function isWide(s: Sensor): boolean {
  const r = splitReadings(s.readings);
  return r.headline.length >= 3 || r.detail.length > 0 || r.calibration.length > 0;
}

// The part as printed on it, then the options its kind says identify it, such as the address.
function partOf(s: Sensor, kind: SensorKind | undefined): string {
  if (!kind) return `${s.provider} / ${s.kind}`;
  if (s.bindings.length) return kind.label;
  const values = kind.fields.filter((f) => f.identifies).map((f) => hostOf(s.options[f.key]));
  return [kind.label, ...values.filter(Boolean)].join(" ");
}

// A web address names its sensor by host; the rest of it, and any key in its query, is not for a card.
function hostOf(v: string | undefined): string | undefined {
  if (!v || !/^https?:\/\//.test(v)) return v;
  try {
    return new URL(v).host;
  } catch {
    return v;
  }
}

export function SensorModule({
  sensor: s,
  all,
  kind,
  feeds,
  age,
  retryIn,
  linked,
  confirming,
  onHover,
  onEdit,
  onAskRemove,
  onCancel,
  onConfirm,
}: {
  sensor: Sensor;
  // all is every configured sensor, so a derived one can show what it reads and each source's value.
  all: Sensor[];
  kind: SensorKind | undefined;
  // feeds are the derived sensors reading this one.
  feeds: Sensor[];
  // age is the host's age at sending plus the time since, so it counts up between pushes.
  age: number | null;
  // retryIn counts down to the next try at opening a sensor that would not open.
  retryIn: number | null;
  linked: boolean;
  confirming: boolean;
  onHover: (ids: number[] | null) => void;
  onEdit: () => void;
  onAskRemove: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const state = stateOf(s);
  const r = splitReadings(s.readings);
  const dim = state === "failing" || state === "stale";
  const calibrating = r.calibration.some(uncalibrated);
  const warming = r.calibration.some((x) => x.format === "flag" && !x.value);
  // The part counted it down at its sample; the time since has passed too.
  const left = r.calibration.find((x) => x.metric === "air_quality_run_in_left");
  const leftSecs = left ? Math.max(0, left.value - (age ?? 0)) : null;
  const indexHeld = calibrating && s.reports.includes("iaq") && !s.readings.some((x) => x.metric === "iaq");
  const links = [...new Set([...s.bindings.map((b) => b.sensorId), ...feeds.map((f) => f.id)])];
  const tiles = r.headline.length + (indexHeld ? 1 : 0);

  return (
    <article
      id={`sensor-${s.id}`}
      onMouseEnter={links.length ? () => onHover(links) : undefined}
      onMouseLeave={links.length ? () => onHover(null) : undefined}
      className={cn(
        "@container panel flex min-w-0 flex-col scroll-mt-4 target:border-primary target:ring-1 target:ring-primary",
        isWide(s) && "col-span-full",
        linked && "border-primary ring-1 ring-primary",
      )}
    >
      <header className="flex min-h-13 items-center gap-2.5 py-2.5 pl-3.5 pr-2">
        <Led state={state} />
        <div className="min-w-0 flex-1">
          <h3 title={s.name} className="truncate font-mono text-[13px] font-medium tracking-[0.04em]">
            {s.name}
          </h3>
          <code className="block truncate font-mono text-[11px] text-muted-foreground sm:text-[10px]">
            {partOf(s, kind)}
          </code>
        </div>
        {/* Hidden while confirming, or the name column is crushed to nothing on a phone. */}
        {confirming ? null : (
          <>
            <span
              className={cn(
                "flex shrink-0 flex-col items-end gap-0.5 font-mono text-[10px] uppercase tracking-[0.12em] tabular-nums text-muted-foreground",
                state === "stale" && "text-warning",
                state === "failing" && "text-destructive",
              )}
            >
              {WORD[state] ? (
                <span className={cn("font-semibold", state === "calibrating" && "text-primary")}>
                  {warming ? "Warming up" : WORD[state]}
                </span>
              ) : null}
              <span>{ageText(age)}</span>
            </span>
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={onEdit}
              aria-label={`Edit ${s.name}`}
              className="shrink-0 text-muted-foreground hover:text-foreground"
            >
              <Pencil className="size-3.5" />
            </Button>
          </>
        )}
        <div className="shrink-0">
          <InlineConfirm
            confirming={confirming}
            onAskRemove={onAskRemove}
            onCancel={onCancel}
            onConfirm={onConfirm}
            iconOnly
            ariaLabel={`Remove ${s.name}`}
          />
        </div>
      </header>

      {confirming ? (
        <p className="border-t border-border px-3.5 py-2 text-mono-xs text-muted-foreground">
          Removing it also takes it off every node&apos;s published channels.
        </p>
      ) : null}

      {state === "failing" ? (
        <Band tone="failing" icon={<TriangleAlert className="size-3.5" strokeWidth={1.8} />}>
          {s.error}
          <span className="block text-muted-foreground">
            {s.ageSecs === null ? "Never read yet." : `Last good read ${ageText(age)} ago, shown below.`}
          </span>
          {retryIn === null ? null : (
            <span className="block text-muted-foreground tabular-nums">
              {retryIn > 0 ? `Trying again in ${countdown(retryIn)}.` : "Trying again now."}
            </span>
          )}
        </Band>
      ) : null}
      {state === "stale" ? (
        <Band tone="stale" icon={<Clock className="size-3.5" strokeWidth={1.8} />}>
          No new reading for {ageText(age)}.
          <span className="block text-muted-foreground">The last one is shown below. No error was reported.</span>
        </Band>
      ) : null}

      {s.readings.length === 0 && state !== "failing" ? (
        <p className="border-t border-border p-3.5 text-mono-xs text-muted-foreground">Waiting for the first read</p>
      ) : null}

      {tiles > 0 ? (
        <div
          className={cn(
            "grid gap-px border-t border-border bg-border",
            tiles === 1 && "grid-cols-1",
            tiles === 2 && "grid-cols-2",
            tiles === 3 && "grid-cols-1 @sm:grid-cols-3",
            tiles >= 4 && "grid-cols-2 @xl:grid-cols-4",
          )}
        >
          {indexHeld ? (
            <div className="hatch flex min-w-0 flex-col gap-2 bg-card px-3.5 pb-3 pt-3.5">
              <span className="label-overline">air quality index</span>
              <span className="font-mono text-[13px] text-muted-foreground">held back</span>
              <span className="font-mono text-[11px] text-primary">
                {leftSecs === null ? "until run-in finishes" : `ready in ${countdown(leftSecs)}`}
              </span>
            </div>
          ) : null}
          {r.headline.map((x) => (
            <Tile key={`${x.metric}-${x.label ?? ""}`} reading={x} sensor={s} dim={dim} />
          ))}
        </div>
      ) : null}

      {r.detail.length > 0 || r.calibration.length > 0 ? (
        <div className="grid divide-y divide-border border-t border-border @3xl:auto-cols-fr @3xl:grid-flow-col @3xl:divide-x @3xl:divide-y-0">
          {r.detail.length > 0 ? (
            <div className="px-3.5 py-3">
              <dl className="grid grid-cols-[minmax(0,max-content)_max-content] items-baseline justify-start gap-x-5 gap-y-1.5 @xl:grid-cols-[minmax(0,max-content)_max-content_minmax(0,max-content)_max-content] @3xl:grid-cols-[minmax(0,max-content)_max-content]">
                {r.detail.map((x) => {
                  const v = displayReading(x);
                  return (
                    <div key={`${x.metric}-${x.label ?? ""}`} className="contents">
                      <dt className="min-w-0 font-mono text-[11px] uppercase tracking-[0.08em] text-muted-foreground">
                        {readingLabel(x)}
                      </dt>
                      <dd className={cn("whitespace-nowrap text-right font-mono text-[13px] font-medium tabular-nums", dim && "text-muted-foreground")}>
                        {v.value}
                        {v.unit ? <small className="ml-1 text-[11px] font-normal text-muted-foreground">{v.unit}</small> : null}
                      </dd>
                    </div>
                  );
                })}
              </dl>
            </div>
          ) : null}
          {r.calibration.length > 0 ? <Calibration readings={r.calibration} leftSecs={leftSecs} /> : null}
        </div>
      ) : null}

      {s.bindings.length > 0 ? (
        <div className="flex flex-col gap-1.5 border-t border-border px-3.5 py-3">
          <span className="label-overline">Reads</span>
          {s.bindings.map((b) => {
            const src = all.find((x) => x.id === b.sensorId);
            const reading = src?.readings.find((x) => x.metric === b.metric);
            const v = reading ? displayReading(reading) : null;
            return (
              <a
                key={b.name}
                href={`#sensor-${b.sensorId}`}
                className="group grid grid-cols-[auto_auto_minmax(0,1fr)_auto] items-baseline gap-2 py-0.5 font-mono text-xs"
              >
                <Led state={src ? stateOf(src) : "failing"} />
                <span className="min-w-[1ch] font-semibold text-primary">{b.name}</span>
                <span className="min-w-0 wrap-break-word group-hover:underline group-hover:underline-offset-2">
                  {src ? src.name : `sensor ${b.sensorId}`}
                  <small className="ml-1 text-[11px] text-muted-foreground">{b.metric}</small>
                </span>
                <span className="whitespace-nowrap tabular-nums text-muted-foreground">
                  {v ? `${v.value}${v.unit ? ` ${v.unit}` : ""}` : ""}
                </span>
              </a>
            );
          })}
        </div>
      ) : null}

      {r.flags.length > 0 ? (
        <div className="flex flex-wrap gap-1.5 border-t border-border px-3.5 py-2.5">
          {r.flags.map((x) => (
            <span
              key={x.metric}
              className={cn(
                "rounded-sm border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em]",
                x.value ? "border-primary/45 text-primary" : "border-border text-muted-foreground",
              )}
            >
              {x.value ? "" : "not "}
              {readingLabel(x)}
            </span>
          ))}
        </div>
      ) : null}

      {feeds.length > 0 ? (
        <div className="mt-auto flex flex-wrap items-baseline gap-x-2.5 gap-y-1 border-t border-border px-3.5 py-2 font-mono text-[11px] text-muted-foreground">
          Feeds
          {feeds.map((f) => (
            <a
              key={f.id}
              href={`#sensor-${f.id}`}
              className="border-b border-dotted border-muted-foreground text-foreground hover:border-primary hover:text-primary"
            >
              {f.name}
            </a>
          ))}
        </div>
      ) : null}
    </article>
  );
}

function Band({ tone, icon, children }: { tone: "failing" | "stale"; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div
      role="status"
      className={cn(
        "flex items-start gap-2 border-t border-border px-3.5 py-2 font-mono text-[11px] leading-normal wrap-anywhere",
        tone === "failing" ? "bg-destructive/7 text-destructive" : "bg-muted text-warning",
      )}
    >
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function Tile({ reading, sensor, dim }: { reading: SensorReading; sensor: Sensor; dim: boolean }) {
  const v = displayReading(reading);
  // The index is only an estimate until it has seen both clean and polluted air.
  const accuracy = reading.metric === "iaq" ? sensor.readings.find((x) => x.metric === "iaq_accuracy")?.value : undefined;
  const provisional = accuracy !== undefined && accuracy < 2;
  return (
    <div className={cn("@container flex min-w-0 flex-col gap-2 bg-card px-3.5 pb-3 pt-3.5", provisional && "hatch")}>
      <span className="label-overline">{readingLabel(reading)}</span>
      <div className="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5">
        <span
          className={cn(
            "min-w-0 wrap-anywhere font-mono text-2xl font-semibold @min-[11rem]:text-3xl leading-none tabular-nums",
            provisional && "text-foreground/70",
            dim && "text-muted-foreground",
          )}
        >
          {v.value}
        </span>
        {v.unit ? <span className="font-mono text-sm text-muted-foreground">{v.unit}</span> : null}
      </div>
      {accuracy !== undefined ? (
        <span className={cn("font-mono text-[11px]", provisional ? "text-primary" : "text-foreground")}>
          {provisional ? `provisional, accuracy ${ACCURACY[accuracy] ?? accuracy}` : iaqBand(reading.value)}
        </span>
      ) : null}
    </div>
  );
}

// m:ss, so a few minutes of run-in reads as a clock rather than a count of seconds.
function countdown(secs: number): string {
  const s = Math.ceil(secs);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function Calibration({ readings, leftSecs }: { readings: SensorReading[]; leftSecs: number | null }) {
  const warming = readings.some((x) => x.format === "flag" && !x.value);
  const settling = readings.some((x) => x.format === "count" && x.value < 2);
  return (
    <div className="px-3.5 py-3">
      <span className="label-overline">Calibration</span>
      <div className="my-2.5 grid grid-cols-[auto_auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
        {readings.map((x) =>
          x.format === "number" ? null : x.format === "flag" ? (
            <div key={x.metric} className="contents">
              <span className="font-mono text-[11px] uppercase tracking-[0.08em] text-muted-foreground">
                {readingLabel(x)}
              </span>
              <span className={cn("col-span-2 font-mono text-xs", x.value ? "text-primary" : "")}>
                {x.value ? "yes" : leftSecs === null ? "not yet" : `warming up, ${countdown(leftSecs)} left`}
              </span>
            </div>
          ) : (
            <div key={x.metric} className="contents">
              <span className="font-mono text-[11px] uppercase tracking-[0.08em] text-muted-foreground">
                {readingLabel(x)}
              </span>
              <span className="inline-flex gap-0.5" aria-hidden="true">
                {[1, 2, 3].map((i) => (
                  <i key={i} className={cn("h-1.75 w-4 border", i <= x.value ? "border-primary bg-primary" : "border-primary/55")} />
                ))}
              </span>
              <span className="font-mono text-xs">
                {Math.round(x.value)} of 3, {ACCURACY[Math.round(x.value)] ?? ""}
              </span>
            </div>
          ),
        )}
      </div>
      <p className="text-[12.5px] leading-normal text-muted-foreground">
        {warming
          ? `The gas plate is warming up. The air quality index, CO2 and breath VOC equivalents and gas percentage appear ${leftSecs === null ? "once run-in finishes" : `in about ${countdown(leftSecs)}`}.`
          : settling
            ? "It has not yet seen both clean and stale air, so treat the index as a guide. Air the room, then breathe near the sensor, or leave it running for a few hours."
            : "Calibrated for this room. It keeps learning in the background."}
      </p>
    </div>
  );
}
