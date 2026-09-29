import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { Sensor } from "@/lib/sensorsApi";

// A group is one sensor and the readings of it that can be pointed at.
export interface SourceGroup {
  sensorId: number;
  name: string;
  // label is what the sensors page calls the reading, which is not always the metric itself.
  readings: { metric: string; label: string }[];
}

const sourceKey = (sensorId: number, metric: string) => `${sensorId}/${metric}`;

// sourceGroups lists what each sensor reports, so one can be chosen before it has ever been read.
export function sourceGroups(sensors: Sensor[], excludeId?: number): SourceGroup[] {
  const out: SourceGroup[] = [];
  for (const s of sensors) {
    if (s.id === excludeId) continue;
    const labels = new Map<string, string>(s.reports.map((m) => [m, m]));
    for (const r of s.readings) if (labels.has(r.metric)) labels.set(r.metric, r.label || r.metric);
    if (labels.size > 0) {
      const readings = [...labels].map(([metric, label]) => ({ metric, label }));
      out.push({ sensorId: s.id, name: s.name, readings });
    }
  }
  return out;
}

// SourceSelect groups readings by sensor; items are the metric alone, so the trigger labels itself.
export function SourceSelect({
  groups,
  sensorId,
  metric,
  onChange,
  label,
  className,
}: {
  groups: SourceGroup[];
  sensorId: number;
  metric: string;
  onChange: (sensorId: number, metric: string) => void;
  label: string;
  className?: string;
}) {
  const from = groups.find((g) => g.sensorId === sensorId);

  return (
    <Select
      value={sensorId ? sourceKey(sensorId, metric) : ""}
      onValueChange={(v) => {
        const cut = v.indexOf("/");
        onChange(Number(v.slice(0, cut)), v.slice(cut + 1));
      }}
    >
      <SelectTrigger
        aria-label={label}
        className={`rounded-none border-border font-mono text-xs w-full min-w-0 [&>span]:truncate ${className ?? ""}`}
      >
        {/* The sensor's name gives way before the reading, or two readings of one sensor look the same. */}
        <SelectValue placeholder="Choose a reading">
          {from ? (
            <>
              <span className="min-w-0 truncate">{from.name}</span>
              <span className="shrink-0 text-muted-foreground">
                {from.readings.find((r) => r.metric === metric)?.label ?? metric}
              </span>
            </>
          ) : null}
        </SelectValue>
      </SelectTrigger>
      <SelectContent className="rounded-none">
        {groups.map((g) => (
          <SelectGroup key={g.sensorId}>
            <SelectLabel className="font-mono text-[10px] uppercase tracking-[0.12em]">
              {g.name}
            </SelectLabel>
            {g.readings.map((r) => (
              <SelectItem
                key={r.metric}
                value={sourceKey(g.sensorId, r.metric)}
                className="font-mono text-xs rounded-none"
              >
                {r.label}
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
      </SelectContent>
    </Select>
  );
}
