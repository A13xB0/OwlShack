import { useMemo, useState } from "react";
import { Check, Loader2, Plus, Search, TriangleAlert, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import type { SensorField, SensorTest } from "@/lib/sensorsApi";

// A list field's value is a JSON array of rows keyed by column; anything else reads as no rows.
export type Row = Record<string, string>;

export function listRows(value: string | undefined): Row[] {
  if (!value?.trim()) return [];
  try {
    const rows = JSON.parse(value);
    return Array.isArray(rows) ? (rows as Row[]) : [];
  } catch {
    return [];
  }
}

export const writeRows = (rows: Row[]) => JSON.stringify(rows);

// Go's duration syntax, as the server stores it: 10m, 1h30m, 168h.
export function parseSpan(v: string | undefined): number | null {
  if (!v?.trim()) return null;
  const parts = [...v.trim().matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)];
  if (!parts.length || parts.map((p) => p[0]).join("") !== v.trim())
    return null;
  return parts.reduce(
    (secs, [, n, u]) =>
      secs + Number(n) * (u === "h" ? 3600 : u === "m" ? 60 : 1),
    0,
  );
}

function writeSpan(secs: number): string {
  if (secs % 3600 === 0) return `${secs / 3600}h`;
  if (secs % 60 === 0) return `${secs / 60}m`;
  return `${secs}s`;
}

const UNITS = [
  { name: "days", secs: 86400 },
  { name: "hours", secs: 3600 },
  { name: "minutes", secs: 60 },
];

// The menu reads smallest first, as a person picks a unit.
const MENU = [...UNITS].reverse();

// spanWords says a length the way the field's limits are read out: 1 minute, 7 days.
export function spanWords(secs: number): string {
  const u =
    UNITS.find((x) => secs >= x.secs && secs % x.secs === 0) ?? UNITS[2];
  const n = secs / u.secs;
  return `${n} ${n === 1 ? u.name.slice(0, -1) : u.name}`;
}

// fieldProblem mirrors the server's checks on a typed field, so the form says what it would refuse.
export function fieldProblem(
  f: SensorField,
  value: string,
  options: Record<string, string>,
  label: (key: string) => string,
): string | null {
  if (f.type === "duration" && value) {
    const secs = parseSpan(value);
    if (secs === null) return `${f.label} needs a number.`;
    if ((f.minSecs && secs < f.minSecs) || (f.maxSecs && secs > f.maxSecs)) {
      return `${f.label} has to be from ${spanWords(f.minSecs ?? 0)} to ${spanWords(f.maxSecs ?? 0)}.`;
    }
    const least = f.atLeast ? parseSpan(options[f.atLeast]) : null;
    if (least !== null && secs < least)
      return `${f.label} has to be at least as long as ${label(f.atLeast!).toLowerCase()}.`;
  }
  if (f.type === "list") {
    const rows = listRows(value);
    if (f.required && rows.length === 0)
      return `Add at least one row to ${f.label}.`;
    for (const [n, row] of rows.entries()) {
      const missing = f.columns?.find((c) => c.required && !row[c.key]?.trim());
      if (missing)
        return `${f.label} row ${n + 1} needs a ${missing.label.toLowerCase()}.`;
    }
  }
  return null;
}

export function DurationInput({
  id,
  field,
  value,
  onChange,
}: {
  id: string;
  field: SensorField;
  value: string;
  onChange: (v: string) => void;
}) {
  const secs = parseSpan(value || field.default);
  const shown =
    secs === null
      ? UNITS[2]
      : (UNITS.find((u) => secs >= u.secs && secs % u.secs === 0) ?? UNITS[2]);
  // The unit is kept while the number is cleared, so typing a new number stays in the unit chosen.
  const [unit, setUnit] = useState(shown.secs);
  const u = value ? shown.secs : unit;
  const n = secs === null ? "" : String(secs / u);
  return (
    <div className="flex gap-2">
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={1}
        step={1}
        value={value ? n : ""}
        placeholder={
          field.default
            ? String((parseSpan(field.default) ?? 0) / u)
            : undefined
        }
        onChange={(e) => {
          const x = Math.floor(Number(e.target.value));
          onChange(e.target.value === "" || !(x > 0) ? "" : writeSpan(x * u));
        }}
        className="w-24 rounded-none border-border font-mono text-base tabular-nums md:text-xs"
      />
      <Select
        value={String(u)}
        onValueChange={(v) => {
          const next = Number(v);
          setUnit(next);
          if (secs !== null && value) onChange(writeSpan((secs / u) * next));
        }}
      >
        <SelectTrigger
          aria-label={`${field.label} unit`}
          className="w-32 rounded-none border-border font-mono text-xs"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent className="rounded-none">
          {MENU.map((x) => (
            <SelectItem
              key={x.secs}
              value={String(x.secs)}
              className="rounded-none font-mono text-xs"
            >
              {x.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

// A test's reading of one value, keyed by its name, shown under its row.
export type RowResults = Record<
  string,
  { value: number | null; unit: string; error: string }
>;

export function ListInput({
  id,
  field,
  value,
  onChange,
  results,
}: {
  id: string;
  field: SensorField;
  value: string;
  onChange: (v: string) => void;
  results?: RowResults;
}) {
  const rows = listRows(value);
  const columns = field.columns ?? [];
  const noun = field.label.replace(/s$/, "").toLowerCase();
  const set = (next: Row[]) => onChange(writeRows(next));
  // The last column is the long one: a path, a pattern or a header's value; on a phone a third column takes its own line.
  const wide = columns.length === 3;
  const grid = wide
    ? "grid-cols-[minmax(0,1fr)_4.5rem_auto] sm:grid-cols-[minmax(0,1fr)_4.5rem_minmax(0,1.6fr)_auto]"
    : "grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_auto]";
  return (
    <div id={id} className="space-y-1.5">
      {rows.length > 0 ? (
        <div className="space-y-2 sm:space-y-1.5">
          <div className={cn("items-center gap-x-1.5", grid, wide ? "hidden sm:grid" : "grid")}>
            {columns.map((c) => (
              <span
                key={c.key}
                className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70"
              >
                {c.label}
              </span>
            ))}
            <span />
          </div>
          {rows.map((row, n) => {
            const r = results?.[row.name?.trim() ?? ""];
            return (
              <div key={n} className="space-y-1">
                <div className={cn("grid items-center gap-1.5", grid)}>
                  {columns.map((c, ci) => (
                    <Input
                      key={c.key}
                      aria-label={`${c.label} of ${noun} ${n + 1}`}
                      value={row[c.key] ?? ""}
                      placeholder={c.placeholder}
                      spellCheck={false}
                      onChange={(e) =>
                        set(
                          rows.map((x, i) =>
                            i === n ? { ...x, [c.key]: e.target.value } : x,
                          ),
                        )
                      }
                      className={cn(
                        "h-8 min-w-0 rounded-none border-border font-mono text-base md:text-xs",
                        wide && ci === 2 && "order-last col-span-2 sm:order-none sm:col-span-1",
                      )}
                    />
                  ))}
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`Remove ${noun} ${n + 1}`}
                    onClick={() => set(rows.filter((_, i) => i !== n))}
                    className="text-muted-foreground hover:text-destructive"
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
                {r ? (
                  <p
                    className={cn(
                      "flex items-start gap-1.5 font-mono text-[11px] leading-snug",
                      r.error ? "text-destructive" : "text-primary",
                    )}
                  >
                    {r.error ? (
                      <TriangleAlert className="mt-px size-3 shrink-0" />
                    ) : (
                      <Check className="mt-px size-3 shrink-0" />
                    )}
                    <span className="min-w-0 wrap-anywhere">
                      {r.error || `${r.value}${r.unit ? ` ${r.unit}` : ""}`}
                    </span>
                  </p>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : (
        <p className="font-mono text-[11px] text-muted-foreground/70">
          No {field.label.toLowerCase()} yet.
        </p>
      )}
      <Button
        variant="outline"
        size="xs"
        onClick={() => set([...rows, {}])}
        disabled={rows.length >= 32}
        className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
      >
        <Plus className="size-3" />
        Add {noun}
      </Button>
    </div>
  );
}

interface Leaf {
  path: string;
  value: string;
  unit: string;
}

const isNumberish = (v: unknown) =>
  typeof v === "number" ||
  typeof v === "boolean" ||
  (typeof v === "string" && v.trim() !== "" && !Number.isNaN(Number(v)));

// leaves lists every number in a reply with its path; a unit comes from a <parent>_units sibling, as Open-Meteo sends current_units beside current.
function leaves(doc: unknown, limit = 400): Leaf[] {
  const out: Leaf[] = [];
  // chain holds the objects above node, so a leaf can look beside its parent.
  const walk = (node: unknown, path: string[], chain: unknown[]) => {
    if (out.length >= limit) return;
    if (isNumberish(node)) {
      out.push({
        path: path.join("."),
        value: String(node),
        unit: unitFor(path, chain),
      });
    } else if (Array.isArray(node)) {
      node.forEach((x, i) => walk(x, [...path, String(i)], [...chain, node]));
    } else if (node && typeof node === "object") {
      for (const [k, v] of Object.entries(node))
        walk(v, [...path, k], [...chain, node]);
    }
  };
  walk(doc, [], []);
  return out;
}

function unitFor(path: string[], chain: unknown[]): string {
  if (path.length < 2) return "";
  const grand = chain[chain.length - 2] as Record<string, unknown> | undefined;
  const units =
    grand && typeof grand === "object"
      ? grand[`${path[path.length - 2]}_units`]
      : undefined;
  const unit =
    units && typeof units === "object"
      ? (units as Record<string, unknown>)[path[path.length - 1]]
      : undefined;
  return typeof unit === "string" ? unit : "";
}

// A value's default name: the path's last word, made an identifier.
export function nameFor(path: string, taken: string[]): string {
  const word =
    path
      .split(".")
      .reverse()
      .find((s) => !/^\d+$/.test(s)) ?? "value";
  let base = word.replace(/[^A-Za-z0-9_]/g, "_");
  if (!/^[A-Za-z_]/.test(base)) base = `v_${base}`;
  let name = base;
  for (let i = 2; taken.includes(name); i++) name = `${base}_${i}`;
  return name;
}

export function TestPanel({
  testing,
  result,
  onTest,
  disabledReason,
  json,
  pickedPaths,
  onPick,
}: {
  testing: boolean;
  result: SensorTest | null;
  onTest: () => void;
  // disabledReason says what to fill in before a test could work; null when it can run.
  disabledReason: string | null;
  json: boolean;
  pickedPaths: string[];
  onPick: (leaf: Leaf) => void;
}) {
  const [filter, setFilter] = useState("");
  const doc = useMemo(() => {
    if (!result || !json || result.truncated) return undefined;
    try {
      return JSON.parse(result.body) as unknown;
    } catch {
      return undefined;
    }
  }, [result, json]);
  const all = useMemo(() => (doc === undefined ? [] : leaves(doc)), [doc]);
  const q = filter.trim().toLowerCase();
  const shown = q ? all.filter((l) => l.path.toLowerCase().includes(q)) : all;
  const ok = result?.status.startsWith("2");

  return (
    <section className="space-y-2.5 border border-border bg-muted/40 p-3">
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <span className="label-overline block">Test</span>
          <p className="font-mono text-[11px] leading-relaxed text-muted-foreground/80 sm:text-[10px]">
            {disabledReason ??
              "Fetch the address once with these settings, to check them and pick values from the reply."}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={onTest}
          disabled={testing || disabledReason !== null}
          className="shrink-0 rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {testing ? <Loader2 className="size-3.5 animate-spin" /> : null}
          {result ? "Test again" : "Test address"}
        </Button>
      </div>

      {result ? (
        <div className="space-y-2">
          {result.status ? (
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px]">
              <span
                className={cn(
                  "border px-1.5 py-0.5 uppercase tracking-[0.1em]",
                  ok
                    ? "border-primary/45 text-primary"
                    : "border-destructive/50 text-destructive",
                )}
              >
                {result.status}
              </span>
              {result.contentType ? (
                <span className="text-muted-foreground">
                  {result.contentType.split(";")[0]}
                </span>
              ) : null}
            </p>
          ) : null}
          {result.error ? (
            <p
              role="alert"
              className="flex items-start gap-1.5 font-mono text-[11px] leading-snug text-destructive"
            >
              <TriangleAlert className="mt-px size-3 shrink-0" />
              <span className="min-w-0 wrap-anywhere">{result.error}</span>
            </p>
          ) : null}

          {json && all.length > 0 ? (
            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <span className="label-overline">Numbers in the reply</span>
                <span className="font-mono text-[10px] tabular-nums text-muted-foreground/70">
                  {all.length}
                </span>
              </div>
              {all.length > 8 ? (
                <div className="relative">
                  <Search className="pointer-events-none absolute top-1/2 left-2 size-3 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    aria-label="Filter the numbers by path"
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                    placeholder="Filter by path"
                    className="h-7 rounded-none border-border pl-7 font-mono text-base md:text-xs"
                  />
                </div>
              ) : null}
              <ul className="max-h-56 divide-y divide-border overflow-y-auto border border-border bg-card">
                {shown.map((l) => {
                  const picked = pickedPaths.includes(l.path);
                  return (
                    <li
                      key={l.path}
                      className="flex items-center gap-2 px-2 py-1"
                    >
                      <code
                        className="min-w-0 flex-1 truncate font-mono text-[11px]"
                        title={l.path}
                      >
                        {l.path}
                      </code>
                      <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
                        {l.value}
                        {l.unit ? ` ${l.unit}` : ""}
                      </span>
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={picked}
                        onClick={() => onPick(l)}
                        aria-label={
                          picked
                            ? `${l.path} is a value already`
                            : `Add ${l.path} as a value`
                        }
                        className="shrink-0 rounded-none font-mono text-[10px] uppercase tracking-[0.12em] text-primary"
                      >
                        {picked ? (
                          <Check className="size-3" />
                        ) : (
                          <Plus className="size-3" />
                        )}
                        {picked ? "Added" : "Add"}
                      </Button>
                    </li>
                  );
                })}
                {shown.length === 0 ? (
                  <li className="px-2 py-2 font-mono text-[11px] text-muted-foreground">
                    No path matches.
                  </li>
                ) : null}
              </ul>
            </div>
          ) : result.body ? (
            <div className="space-y-1.5">
              <span className="label-overline block">
                Reply{result.truncated ? ", first 256 KiB" : ""}
              </span>
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap wrap-anywhere border border-border bg-card p-2 font-mono text-[11px] leading-snug">
                {result.body.slice(0, 8000)}
              </pre>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
