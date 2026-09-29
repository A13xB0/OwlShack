import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Loader2, RefreshCw, Search, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import {
  type SensorCandidate,
  type SensorKind,
  type SensorScan,
} from "@/lib/sensorsApi";

// Entry is one row of the picker; a matched and a catalogue part differ only in filled options.
export interface Entry {
  kind: SensorKind;
  label: string;
  detail: string;
  // options is what the scan found, or empty for a part being added from the catalogue.
  options?: Record<string, string>;
}

// Section is one labelled run of rows: the scan's findings first, then the catalogue by category.
interface Section {
  label: string;
  rows: Entry[];
}

const listboxId = "sensor-catalogue-list";

const rowId = (i: number) => `sensor-catalogue-opt-${i}`;

// keyOf names a row by what it is, so the highlight stays on it when a scan inserts rows above.
const keyOf = (e: Entry) => `${e.kind.provider}/${e.kind.kind}/${e.detail}`;

// haystacks is what a row can be found by, built once per catalogue rather than per keystroke.
function haystacks(catalogue: SensorKind[]): Map<SensorKind, string> {
  return new Map(
    catalogue.map((k) => [
      k,
      [k.label, k.kind, k.description, k.category, ...(k.metrics ?? [])].join(" ").toLowerCase(),
    ]),
  );
}

function buildEntries(
  scan: SensorScan | null,
  catalogue: SensorKind[],
  found: Map<SensorKind, string>,
  query: string,
): { sections: Section[]; unknown: SensorCandidate[]; taken: SensorCandidate[]; matched: number } {
  const byKind = new Map(catalogue.map((k) => [`${k.provider}/${k.kind}`, k]));
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const matches = (k: SensorKind) => {
    if (terms.length === 0) return true;
    const h = found.get(k) ?? "";
    return terms.every((t) => h.includes(t));
  };

  const detected: Entry[] = [];
  const unknown: SensorCandidate[] = [];
  const taken: SensorCandidate[] = [];
  for (const c of scan?.candidates ?? []) {
    if (!c.addable) {
      unknown.push(c);
      continue;
    }
    if (c.usedBy) {
      taken.push(c);
      continue;
    }
    const k = byKind.get(`${c.provider}/${c.kind}`);
    if (!k || !matches(k)) continue;
    detected.push({
      kind: k,
      label: k.label,
      detail: c.detail || k.description || "",
      options: c.options,
    });
  }

  const detectedKeys = new Set(detected.map((e) => `${e.kind.provider}/${e.kind.kind}`));
  const grouped = new Map<string, Entry[]>();
  for (const k of catalogue) {
    if (!matches(k) || detectedKeys.has(`${k.provider}/${k.kind}`)) continue;
    const g = k.category || "Other";
    const row: Entry = {
      kind: k,
      label: k.label,
      detail: k.description || k.kind,
    };
    const rows = grouped.get(g);
    if (rows) rows.push(row);
    else grouped.set(g, [row]);
  }

  const sections: Section[] = detected.length
    ? [{ label: `Possible matches (${detected.length})`, rows: detected }]
    : [];
  for (const [label, rows] of [...grouped.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    sections.push({ label, rows });
  }
  return { sections, unknown, taken, matched: detected.length };
}

export function SensorCatalogue({
  scan,
  catalogue,
  catalogueError,
  onRetryCatalogue,
  query,
  onQuery,
  scanning,
  onRescan,
  onPick,
}: {
  scan: SensorScan | null;
  catalogue: SensorKind[] | null;
  // catalogueError is why the catalogue did not load, or null while it loads or once it has.
  catalogueError: string | null;
  onRetryCatalogue: () => void;
  query: string;
  onQuery: (q: string) => void;
  scanning: boolean;
  onRescan: () => void;
  onPick: (e: Entry) => void;
}) {
  const found = useMemo(() => haystacks(catalogue ?? []), [catalogue]);
  const { sections, unknown, taken, matched } = useMemo(
    () => buildEntries(scan, catalogue ?? [], found, query),
    [scan, catalogue, found, query],
  );

  // flat is the rows in shown order for the arrow keys; starts maps a section to its first index.
  const flat = useMemo(() => sections.flatMap((s) => s.rows), [sections]);
  const starts = useMemo(() => {
    const out: number[] = [];
    let n = 0;
    for (const s of sections) {
      out.push(n);
      n += s.rows.length;
    }
    return out;
  }, [sections]);

  const [activeKey, setActiveKey] = useState<string | null>(null);
  const hit = activeKey === null ? -1 : flat.findIndex((e) => keyOf(e) === activeKey);
  const idx = flat.length === 0 ? -1 : Math.max(0, hit);

  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    listRef.current
      ?.querySelector<HTMLElement>('[data-active="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [idx]);

  const search = useCallback(
    (v: string) => {
      onQuery(v);
      setActiveKey(null);
    },
    [onQuery],
  );

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLInputElement>) => {
      if (flat.length === 0) return;
      const move = (to: number) => {
        e.preventDefault();
        setActiveKey(keyOf(flat[Math.max(0, Math.min(to, flat.length - 1))]));
      };
      if (e.key === "ArrowDown") move(idx + 1);
      else if (e.key === "ArrowUp") move(idx - 1);
      else if (e.key === "Home") move(0);
      else if (e.key === "End") move(flat.length - 1);
      else if (e.key === "Enter" && idx >= 0) {
        e.preventDefault();
        onPick(flat[idx]);
      }
    },
    [flat, idx, onPick],
  );

  return (
    // min-w-0: a grid item in DialogContent, or the widest row sets the column width and the dialog scrolls sideways.
    <div className="space-y-2 min-w-0">
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground/70" />
        <Input
          value={query}
          onChange={(e) => search(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Search by part or what it measures"
          aria-label="Search parts"
          autoFocus
          role="combobox"
          aria-expanded={flat.length > 0}
          aria-controls={listboxId}
          aria-autocomplete="list"
          aria-activedescendant={idx >= 0 ? rowId(idx) : undefined}
          className="rounded-none border-border pl-8 font-mono text-base md:text-xs"
        />
      </div>

      {catalogueError ? (
        <div className="flex items-start justify-between gap-2 border border-warning/40 bg-warning/5 px-3 py-2">
          <p className="flex items-start gap-1.5 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning">
            <TriangleAlert className="size-3 shrink-0 mt-px" strokeWidth={1.8} />
            <span className="min-w-0 wrap-break-word">{catalogueError}</span>
          </p>
          <Button
            variant="ghost"
            size="xs"
            onClick={onRetryCatalogue}
            className="shrink-0 font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <RefreshCw className="size-3" />
            Retry
          </Button>
        </div>
      ) : catalogue === null ? (
        <p className="text-mono-xs text-muted-foreground/70">Loading the catalogue...</p>
      ) : flat.length === 0 ? (
        <p className="text-mono-xs text-muted-foreground/70">
          {query.trim() ? `Nothing matches "${query.trim()}".` : "No sensors can be added on this platform."}
        </p>
      ) : (
        <div
          ref={listRef}
          id={listboxId}
          role="listbox"
          aria-label="Parts"
          className="max-h-72 overflow-y-auto border border-border divide-y divide-border"
        >
          {sections.map((s, i) => (
            <Group
              key={s.label}
              label={s.label}
              rows={s.rows}
              start={starts[i]}
              active={idx}
              onPick={onPick}
              onHover={setActiveKey}
            />
          ))}
        </div>
      )}

      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          {matched > 0 ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
              A scan only reads, so these are matched by address; the part is checked when you add it.
            </p>
          ) : null}
          {taken.length > 0 ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
              Already added: {taken.map((c) => `${c.usedBy} (${c.detail})`).join(", ")}
            </p>
          ) : null}
          {unknown.length > 0 ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
              Also responding, with no driver here: {unknown.map((u) => u.label).join(", ")}
            </p>
          ) : null}
          {/* A provider that could not be scanned says so, or no possible matches reads as an empty bus. */}
          {(scan?.problems ?? []).map((p) => (
            <p
              key={p.provider}
              className="flex items-start gap-1.5 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning"
            >
              <TriangleAlert className="size-3 shrink-0 mt-px" strokeWidth={1.8} />
              <span className="min-w-0 wrap-break-word whitespace-pre-line">
                {p.label} not scanned: {p.reason}
              </span>
            </p>
          ))}
        </div>
        <Button
          variant="ghost"
          size="xs"
          disabled={scanning}
          onClick={onRescan}
          className="shrink-0 font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {scanning ? (
            <Loader2 className="size-3 animate-spin" />
          ) : (
            <RefreshCw className="size-3" />
          )}
          Scan
        </Button>
      </div>
    </div>
  );
}

function Group({
  label,
  rows,
  start,
  active,
  onPick,
  onHover,
}: {
  label: string;
  rows: Entry[];
  start: number;
  active: number;
  onPick: (e: Entry) => void;
  onHover: (key: string) => void;
}) {
  return (
    <div role="group" aria-label={label}>
      {/* Opaque, or the rows scrolling under it show through. */}
      <div aria-hidden className="sticky top-0 bg-muted px-3 py-1">
        <span className="label-overline">{label}</span>
      </div>
      <div role="presentation" className="divide-y divide-border">
        {rows.map((e, i) => {
          const at = start + i;
          const on = at === active;
          return (
            <div
              key={keyOf(e)}
              id={rowId(at)}
              role="option"
              aria-selected={on}
              data-active={on}
              tabIndex={-1}
              onClick={() => onPick(e)}
              onMouseMove={() => onHover(keyOf(e))}
              className={cn(
                // The transparent border keeps the text from shifting; scroll-mt clears the sticky header.
                "w-full bg-card px-3 py-2 text-left min-h-10 scroll-mt-8 border-l-2 border-transparent flex items-baseline gap-2",
                on && "bg-primary/10 border-primary text-primary",
              )}
            >
              <span className="font-mono text-xs shrink-0">{e.label}</span>
              <code
                className={cn(
                  "font-mono text-[11px] sm:text-[10px] leading-none truncate",
                  on ? "text-primary/70" : "text-muted-foreground/70",
                )}
              >
                {e.detail}
              </code>
              <span
                className={cn(
                  "ml-auto shrink-0 label-overline",
                  on ? "text-primary/60" : "text-muted-foreground/50",
                )}
              >
                {e.kind.provider}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
