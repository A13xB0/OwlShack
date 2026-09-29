import { useCallback, useMemo, useRef } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SourceSelect, sourceGroups } from "@/components/SourceSelect";
import type { Sensor, SensorBinding } from "@/lib/sensorsApi";

// freeName fills a blank row's name once its source is chosen: the metric, numbered if already taken.
function freeName(metric: string, taken: SensorBinding[]): string {
  const base = metric.replace(/[^A-Za-z0-9_]/g, "") || "value";
  const stem = /^[0-9]/.test(base) ? `v${base}` : base;
  if (!taken.some((b) => b.name === stem)) return stem;
  for (let n = 2; ; n++) {
    if (!taken.some((b) => b.name === `${stem}${n}`)) return `${stem}${n}`;
  }
}

export function BindingEditor({
  bindings,
  onChange,
  sensors,
  excludeId,
}: {
  bindings: SensorBinding[];
  onChange: (b: SensorBinding[]) => void;
  sensors: Sensor[];
  // excludeId keeps a sensor out of its own source list; a loop is refused on save either way.
  excludeId?: number;
}) {
  const groups = useMemo(() => sourceGroups(sensors, excludeId), [sensors, excludeId]);

  // Client ids: keyed on the index, removing an earlier row destroys the one being typed in.
  const keys = useRef<number[]>([]);
  const seq = useRef(0);
  while (keys.current.length < bindings.length) keys.current.push(++seq.current);

  const add = useCallback(
    () => onChange([...bindings, { name: "", sensorId: 0, metric: "" }]),
    [bindings, onChange],
  );

  const update = useCallback(
    (i: number, next: Partial<SensorBinding>) =>
      onChange(bindings.map((b, j) => (j === i ? { ...b, ...next } : b))),
    [bindings, onChange],
  );

  const remove = useCallback(
    (i: number) => {
      keys.current.splice(i, 1);
      onChange(bindings.filter((_, j) => j !== i));
    },
    [bindings, onChange],
  );

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <span className="label-overline block">Reads</span>
        <Button
          variant="ghost"
          size="xs"
          onClick={add}
          disabled={groups.length === 0}
          className="font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          <Plus className="size-3" />
          Add reading
        </Button>
      </div>

      {groups.length === 0 ? (
        <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
          Nothing to read yet. Add a sensor first, then come back.
        </p>
      ) : bindings.length === 0 ? (
        <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
          Add at least one reading for the expression to use.
        </p>
      ) : (
        // Wider apart than a binding's own two rows on a phone, or every row reads as one list.
        <div className="space-y-3 sm:space-y-1.5">
          {bindings.map((b, i) => (
            // A phone has no room for three on one line, so the source takes its own row.
            <div
              key={keys.current[i]}
              className="grid grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[7rem_minmax(0,1fr)_auto] items-center gap-1.5"
            >
              <Input
                value={b.name}
                onChange={(e) => update(i, { name: e.target.value })}
                aria-label={`Name for reading ${i + 1}`}
                placeholder="name"
                className="col-start-1 row-start-1 rounded-none border-border font-mono text-base md:text-xs min-w-0"
              />
              <SourceSelect
                groups={groups}
                sensorId={b.sensorId}
                metric={b.metric}
                label={`Source for reading ${i + 1}`}
                onChange={(sensorId, metric) =>
                  update(i, { sensorId, metric, name: b.name.trim() || freeName(metric, bindings) })
                }
                className="col-span-2 row-start-2 sm:col-span-1 sm:col-start-2 sm:row-start-1"
              />
              <Button
                variant="ghost"
                size="xs"
                onClick={() => remove(i)}
                aria-label={`Remove reading ${i + 1}`}
                className="col-start-2 row-start-1 sm:col-start-3 shrink-0 text-muted-foreground hover:text-destructive"
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {bindings.length > 0 ? (
        <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
          The expression can use these names and nothing else.
        </p>
      ) : null}
    </section>
  );
}
