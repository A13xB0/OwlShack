import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Loader2, Plus, Trash2, TriangleAlert, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SourceSelect, sourceGroups, type SourceGroup } from "@/components/SourceSelect";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiList } from "@/hooks/useApiList";
import { cn } from "@/lib/utils";
import {
  fetchTelemetryMap,
  sameNode,
  saveTelemetryMap,
  type LPPType,
  type Sensor,
  type TelemetryMap,
  type TelemetryMapEntry,
  type TelemetryNode,
} from "@/lib/sensorsApi";

// A reading on the channel it sits under; the key is a client id so React's identity survives edits.
interface Row {
  key: number;
  type: number;
  sensorId: number;
  metric: string;
}

// A channel carries readings, and is its own thing on screen because it is one on the wire.
interface Channel {
  key: number;
  channel: number;
  rows: Row[];
}

function toChannels(entries: TelemetryMapEntry[], nextKey: () => number): Channel[] {
  const out: Channel[] = [];
  for (const e of entries) {
    let ch = out.find((c) => c.channel === e.channel);
    if (!ch) {
      ch = { key: nextKey(), channel: e.channel, rows: [] };
      out.push(ch);
    }
    ch.rows.push({ key: nextKey(), type: e.type, sensorId: e.sensorId, metric: e.metric });
  }
  return out;
}

function toEntries(channels: Channel[], node: TelemetryNode): TelemetryMapEntry[] {
  return channels.flatMap((c) =>
    c.rows.map((r) => ({
      node,
      channel: c.channel,
      type: r.type,
      sensorId: r.sensorId,
      metric: r.metric,
    })),
  );
}

// an picks the article for a type name, so the editor never says "a Analog input".
const an = (name = "") => (/^[aeiou]/i.test(name) ? `an ${name}` : `a ${name}`);

// TelemetryMapEditor edits one node's map, compared by value so a parent's live refresh leaves it be; reloadToken re-reads whether the node serves.
export const TelemetryMapEditor = memo(
  function TelemetryMapEditor({
    node: given,
    reloadToken = 0,
  }: {
    node: TelemetryNode;
    reloadToken?: number;
  }) {
    // Keyed on primitives: a parent rebuilding the prop inline would refetch on every render.
    const node = useMemo(() => ({ kind: given.kind, id: given.id }), [given.kind, given.id]);

    const [map, setMap] = useState<TelemetryMap | null>(null);
    const [channels, setChannels] = useState<Channel[]>([]);
    const [saving, setSaving] = useState(false);
    const [failed, setFailed] = useState("");

    const keySeq = useRef(0);
    const nextKey = useCallback(() => ++keySeq.current, []);

    // Fetched here, so this drops onto any node's page without that page knowing about sensors.
    const { items: sensors, loading: sensorsLoading, error: sensorsError } = useApiList<Sensor>(
      "/api/sensors",
      "Failed to load sensors",
    );

    const load = useCallback(() => {
      setFailed("");
      fetchTelemetryMap()
        .then((m) => {
          setMap(m);
          setChannels(toChannels(m.entries.filter((e) => sameNode(e.node, node)), nextKey));
        })
        .catch((e) => setFailed(e instanceof Error ? e.message : "Failed to load what this node publishes"));
    }, [node, nextKey]);

    useEffect(load, [load]);

    // Rows being edited are left alone: this is only here to re-read whether the node still serves.
    useEffect(() => {
      if (reloadToken === 0) return;
      fetchTelemetryMap()
        .then((m) => setMap((prev) => (prev ? { ...m, entries: prev.entries } : m)))
        .catch(() => {});
    }, [reloadToken]);

    const sources = useMemo(() => sourceGroups(sensors ?? []), [sensors]);
    const byCode = useMemo(() => new Map((map?.types ?? []).map((t) => [t.code, t])), [map?.types]);
    const selfChannel = map?.selfChannel ?? 0;
    const selfTypesKey = (map?.selfTypes ?? []).join(",");
    const selfTypes = useMemo(
      () => (selfTypesKey ? selfTypesKey.split(",").map(Number) : []),
      [selfTypesKey],
    );

    const bytes = useMemo(
      () =>
        channels.reduce(
          (n, c) => n + c.rows.reduce((m, r) => m + (byCode.get(r.type)?.bytes ?? 0), 0),
          0,
        ),
      [channels, byCode],
    );

    // Per channel and per row, so each problem is reported where it is rather than at the bottom.
    const trouble = useMemo(() => {
      const channelIssue = new Map<number, string>();
      const rowIssue = new Map<number, string>();
      const seen = new Set<number>();
      for (const c of channels) {
        // Emptiness first, or a channel both duplicated and empty reports only one; an empty channel 1 is a real choice, the node's own readings again.
        if (c.rows.length === 0 && c.channel !== selfChannel) {
          channelIssue.set(c.key, "This channel has no readings.");
        }
        if (seen.has(c.channel)) channelIssue.set(c.key, `Channel ${c.channel} is listed twice.`);
        seen.add(c.channel);

        const seenType = new Set<number>();
        for (const r of c.rows) {
          const from = sources.find((g) => g.sensorId === r.sensorId);
          if (!r.sensorId || !r.metric) {
            rowIssue.set(r.key, "Nothing chosen yet, so this channel cannot be saved.");
          } else if (!(sensors ?? []).some((s) => s.id === r.sensorId)) {
            // Otherwise this renders as the placeholder, which is what an untouched row looks like.
            rowIssue.set(r.key, "That sensor has been removed. Choose another reading.");
          } else if (!from?.readings.some((x) => x.metric === r.metric)) {
            rowIssue.set(r.key, `That sensor no longer reports ${r.metric}. Choose another reading.`);
          } else if (c.channel === selfChannel && !selfTypes.includes(r.type)) {
            rowIssue.set(r.key, `Channel ${selfChannel} is this node's own: it carries a battery or a board temperature.`);
          } else if (seenType.has(r.type)) {
            rowIssue.set(r.key, `Channel ${c.channel} already sends ${an(byCode.get(r.type)?.name)}.`);
          }
          seenType.add(r.type);
        }
      }
      return { channelIssue, rowIssue };
    }, [channels, byCode, sensors, sources, selfChannel, selfTypes]);

    const overBudget = map ? bytes > map.maxBytes : false;
    const blocked = trouble.channelIssue.size > 0 || trouble.rowIssue.size > 0 || overBudget;

    // Keyed on the numbers, or every edit hands ChannelCard a new Set and the memoisation buys nothing.
    const usedKey = channels.map((c) => c.channel).join(",");
    const used = useMemo(
      () => new Set(usedKey.split(",").filter(Boolean).map(Number)),
      [usedKey],
    );

    // The first channel above the node's own that is free, or none; channel 1 is never added this way.
    const nextFree = useMemo(() => {
      if (!map) return null;
      let n = map.selfChannel + 1;
      while (used.has(n) && n <= map.maxChannel) n++;
      return n <= map.maxChannel ? n : null;
    }, [map, used]);

    const addChannel = useCallback(() => {
      if (nextFree === null) return;
      setChannels((prev) => [...prev, { key: nextKey(), channel: nextFree, rows: [] }]);
    }, [nextFree, nextKey]);

    const setChannelNumber = useCallback(
      (key: number, channel: number) =>
        setChannels((prev) =>
          prev.map((c) => {
            if (c.key !== key) return c;
            if (!map || channel !== map.selfChannel) return { ...c, channel };
            // Channel 1 offers only the node's own types, so a row moved there takes its metric's where that is one.
            const rows = c.rows.map((r) => {
              const t = map.defaults[r.metric];
              return !map.selfTypes.includes(r.type) && t !== undefined && map.selfTypes.includes(t)
                ? { ...r, type: t }
                : r;
            });
            return { ...c, channel, rows };
          }),
        ),
      [map],
    );

    const removeChannel = useCallback(
      (key: number) => setChannels((prev) => prev.filter((c) => c.key !== key)),
      [],
    );

    const addRow = useCallback(
      (key: number) =>
        setChannels((prev) =>
          prev.map((c) => {
            if (c.key !== key || !map) return c;
            // Start on a type this channel is not already carrying, so adding never lands on a clash.
            const taken = new Set(c.rows.map((r) => r.type));
            const offered =
              c.channel === map.selfChannel
                ? map.types.filter((t) => map.selfTypes.includes(t.code))
                : map.types;
            const type = offered.find((t) => !taken.has(t.code))?.code ?? offered[0]?.code ?? 0;
            return { ...c, rows: [...c.rows, { key: nextKey(), type, sensorId: 0, metric: "" }] };
          }),
        ),
      [map, nextKey],
    );

    const updateRow = useCallback(
      (channelKey: number, rowKey: number, next: Partial<Row>) =>
        setChannels((prev) =>
          prev.map((c) =>
            c.key === channelKey
              ? { ...c, rows: c.rows.map((r) => (r.key === rowKey ? { ...r, ...next } : r)) }
              : c,
          ),
        ),
      [],
    );

    const removeRow = useCallback(
      (channelKey: number, rowKey: number) =>
        setChannels((prev) =>
          prev.map((c) =>
            c.key === channelKey ? { ...c, rows: c.rows.filter((r) => r.key !== rowKey) } : c,
          ),
        ),
      [],
    );

    const entries = useMemo(() => toEntries(channels, node), [channels, node]);
    const dirty = useMemo(
      () =>
        map != null &&
        JSON.stringify(entries) !== JSON.stringify(map.entries.filter((e) => sameNode(e.node, node))),
      [entries, map, node],
    );

    const save = useCallback(async () => {
      setSaving(true);
      try {
        const saved = await saveTelemetryMap(node, entries);
        setMap(saved);
        setChannels(toChannels(saved.entries.filter((e) => sameNode(e.node, node)), nextKey));
        toast.success("Published readings saved");
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed to save the map");
      } finally {
        setSaving(false);
      }
    }, [entries, node, nextKey]);

    if (failed) return <LoadErrorAlert message={failed} onRetry={load} />;
    if (!map) return <Skeleton className="h-40 w-full" />;

    const info = map.nodes.find((n) => sameNode(n.node, node));

    return (
      <section className="panel">
        <header className="flex items-start gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0 flex-1">
            <h2 className="font-mono text-sm uppercase tracking-widest">Published readings</h2>
            <p className="font-mono text-[11px] sm:text-[10px] text-muted-foreground/70">
              What this node answers with when another asks it for telemetry
            </p>
          </div>
          <span
            className={cn(
              "mt-0.5 shrink-0 font-mono text-[10px] uppercase tracking-[0.12em] tabular-nums",
              overBudget ? "text-warning" : "text-muted-foreground/70",
            )}
          >
            {dirty ? "unsaved · " : ""}
            {bytes}/{map.maxBytes}b
          </span>
        </header>

        <div className="space-y-2.5 px-4 py-3">
          {/* A node that stores a map but cannot answer a request would otherwise look configured. */}
          {info && !info.serves ? (
            <p className="flex items-start gap-1.5 border border-warning/40 bg-warning/5 px-2.5 py-2 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning">
              <TriangleAlert className="size-3 shrink-0 mt-px" strokeWidth={1.8} />
              <span className="min-w-0 wrap-break-word">{info.reason}</span>
            </p>
          ) : null}

          {sensorsLoading ? (
            <Skeleton className="h-8 w-full" />
          ) : sensorsError ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning">
              {sensorsError}
            </p>
          ) : sources.length === 0 ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
              No sensors are configured on this host yet, so there is nothing to publish.
            </p>
          ) : channels.length === 0 ? (
            <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
              Nothing is published yet. Add a channel, then choose what goes on it.
            </p>
          ) : (
            channels.map((c) => (
              <ChannelCard
                key={c.key}
                channel={c}
                types={map.types}
                selfTypes={selfTypes}
                defaults={map.defaults}
                sources={sources}
                selfChannel={map.selfChannel}
                maxChannel={map.maxChannel}
                used={used}
                issue={trouble.channelIssue.get(c.key)}
                rowIssue={trouble.rowIssue}
                rowIssueKey={c.rows.map((r) => trouble.rowIssue.get(r.key) ?? "").join("|")}
                onChannel={setChannelNumber}
                onAddRow={addRow}
                onRemove={removeChannel}
                onUpdateRow={updateRow}
                onRemoveRow={removeRow}
              />
            ))
          )}

          <div className="flex flex-wrap items-center gap-2">
            <p
              className={cn(
                "min-w-0 font-mono text-[11px] sm:text-[10px] leading-relaxed",
                overBudget ? "text-warning" : "text-muted-foreground/70",
              )}
            >
              {overBudget
                ? `Too big for one reply by ${bytes - map.maxBytes} bytes.`
                : `Channel ${map.selfChannel} is this node's own battery and temperature. Put a sensor there to publish that instead; without one the battery is the board's, or 0 V if it has none.`}
            </p>
            {/* ml-auto keeps the buttons right whether or not the text beside them wraps. */}
            <div className="ml-auto flex shrink-0 gap-2">
              <Button
                variant="ghost"
                size="xs"
                onClick={addChannel}
                disabled={sources.length === 0 || nextFree === null}
                className="font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                <Plus className="size-3" />
                Add channel
              </Button>
              <Button
                size="xs"
                onClick={save}
                disabled={!dirty || blocked || saving}
                className="font-mono text-[11px] uppercase tracking-[0.12em]"
              >
                {saving ? <Loader2 className="size-3 animate-spin" /> : null}
                Save
              </Button>
            </div>
          </div>
        </div>
      </section>
    );
  },
  (a, b) => sameNode(a.node, b.node) && a.reloadToken === b.reloadToken,
);

// Memoised with stable callbacks, so editing one channel does not re-render every other one.
interface ChannelCardProps {
  channel: Channel;
  types: LPPType[];
  selfTypes: number[];
  defaults: Record<string, number>;
  sources: SourceGroup[];
  selfChannel: number;
  maxChannel: number;
  used: Set<number>;
  issue?: string;
  rowIssue: Map<number, string>;
  // rowIssueKey lets sameCard compare this card's issues without the shared Map's identity.
  rowIssueKey: string;
  onChannel: (key: number, channel: number) => void;
  onAddRow: (key: number) => void;
  onRemove: (key: number) => void;
  onUpdateRow: (channelKey: number, rowKey: number, next: Partial<Row>) => void;
  onRemoveRow: (channelKey: number, rowKey: number) => void;
}

// sameCard compares this card's own issues by value, since rowIssue is a fresh Map on every edit.
function sameCard(a: ChannelCardProps, b: ChannelCardProps): boolean {
  return (
    a.channel === b.channel &&
    a.issue === b.issue &&
    a.rowIssueKey === b.rowIssueKey &&
    a.used === b.used &&
    a.types === b.types &&
    a.selfTypes === b.selfTypes &&
    a.defaults === b.defaults &&
    a.sources === b.sources &&
    a.selfChannel === b.selfChannel &&
    a.maxChannel === b.maxChannel
  );
}

const ChannelCard = memo(function ChannelCard({
  channel,
  types,
  selfTypes,
  defaults,
  sources,
  selfChannel,
  maxChannel,
  used,
  issue,
  rowIssue,
  onChannel,
  onAddRow,
  onRemove,
  onUpdateRow,
  onRemoveRow,
}: ChannelCardProps) {
  // A channel is a choice from a short list, not a number to type.
  const options = useMemo(() => {
    const out: number[] = [];
    for (let n = selfChannel; n <= maxChannel; n++) out.push(n);
    return out;
  }, [selfChannel, maxChannel]);

  return (
    <article className={cn("border border-border", issue && "border-warning/50")}>
      <header className="flex items-center gap-2 border-b border-border bg-muted/30 py-1.5 pl-2.5 pr-1">
        <span className="label-overline shrink-0">Channel</span>
        <Select
          value={String(channel.channel)}
          onValueChange={(v) => onChannel(channel.key, Number(v))}
        >
          <SelectTrigger
            aria-label={`Channel number, currently ${channel.channel}`}
            className={cn(
              "w-18 rounded-none border-border px-2 font-mono text-xs tabular-nums",
              issue && "border-warning text-warning",
            )}
          >
            {/* The number alone: the qualifier sits beside the control, where it has room. */}
            <SelectValue>{channel.channel}</SelectValue>
          </SelectTrigger>
          <SelectContent className="rounded-none">
            {options.map((n) => (
              <SelectItem
                key={n}
                value={String(n)}
                // Taken channels stay listed so the numbering reads whole, but cannot be picked.
                disabled={n !== channel.channel && used.has(n)}
                className="font-mono text-xs rounded-none tabular-nums"
              >
                {n}
                {n === selfChannel ? " · this node" : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {channel.channel === selfChannel ? (
          <span className="shrink-0 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
            this node
          </span>
        ) : null}
        <span className="flex-1" />
        <Button
          variant="ghost"
          size="xs"
          onClick={() => onRemove(channel.key)}
          aria-label={`Remove channel ${channel.channel}`}
          className="shrink-0 text-muted-foreground hover:text-destructive"
        >
          <Trash2 className="size-3.5" />
        </Button>
      </header>

      <div className="divide-y divide-border/60">
        {channel.rows.map((r, i) => (
          <ReadingRow
            key={r.key}
            row={r}
            index={i}
            channelKey={channel.key}
            channelNumber={channel.channel}
            types={channel.channel === selfChannel ? types.filter((t) => selfTypes.includes(t.code)) : types}
            typeName={types.find((t) => t.code === r.type)?.name ?? `type ${r.type}`}
            defaults={defaults}
            sources={sources}
            issue={rowIssue.get(r.key)}
            onUpdate={onUpdateRow}
            onRemove={onRemoveRow}
          />
        ))}

        <div className="flex items-center justify-between gap-2 px-2.5 py-1.5">
          {issue ? (
            <p className="min-w-0 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning">
              {issue}
            </p>
          ) : (
            <span />
          )}
          <Button
            variant="ghost"
            size="xs"
            onClick={() => onAddRow(channel.key)}
            className="shrink-0 font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <Plus className="size-3" />
            Add reading
          </Button>
        </div>
      </div>
    </article>
  );
}, sameCard);

// ReadingRow leads with the reading; the type follows from the metric, so it stays a quiet line.
const ReadingRow = memo(function ReadingRow({
  row,
  index,
  channelKey,
  channelNumber,
  types,
  typeName,
  defaults,
  sources,
  issue,
  onUpdate,
  onRemove,
}: {
  row: Row;
  index: number;
  channelKey: number;
  channelNumber: number;
  types: LPPType[];
  // typeName names the row's type even when this channel does not offer it, rather than a blank control.
  typeName: string;
  defaults: Record<string, number>;
  sources: SourceGroup[];
  issue?: string;
  onUpdate: (channelKey: number, rowKey: number, next: Partial<Row>) => void;
  onRemove: (channelKey: number, rowKey: number) => void;
}) {
  const where = `reading ${index + 1} on channel ${channelNumber}`;
  return (
    // A phone puts the type under the reading; from sm up it sits alongside, in a fixed column so the pickers line up.
    <div className="grid grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[minmax(0,1fr)_10.5rem_auto] items-center gap-x-1.5 gap-y-1 px-2.5 py-2">
      <div className="col-start-1 row-start-1 min-w-0">
        <SourceSelect
          groups={sources}
          sensorId={row.sensorId}
          metric={row.metric}
          label={`Sensor for ${where}`}
          onChange={(sensorId, metric) =>
            onUpdate(channelKey, row.key, {
              sensorId,
              metric,
              // The type follows the metric until the operator has set a source themselves.
              type: row.sensorId ? row.type : (defaults[metric] ?? row.type),
            })
          }
          className={cn(issue && "border-warning")}
        />
      </div>
      <Button
        variant="ghost"
        size="xs"
        onClick={() => onRemove(channelKey, row.key)}
        aria-label={`Remove ${where}`}
        className="col-start-2 row-start-1 sm:col-start-3 shrink-0 text-muted-foreground hover:text-destructive"
      >
        <X className="size-3.5" />
      </Button>

      <div className="col-span-2 row-start-2 sm:col-span-1 sm:col-start-2 sm:row-start-1 flex items-center gap-1.5">
        <span className="shrink-0 font-mono text-[11px] sm:text-[10px] text-muted-foreground/60">
          sent as
        </span>
        <Select
          value={String(row.type)}
          onValueChange={(v) => onUpdate(channelKey, row.key, { type: Number(v) })}
        >
          <SelectTrigger
            aria-label={`LPP type for ${where}`}
            className={cn(
              "gap-1 border-0 bg-transparent px-1 font-mono text-[11px] sm:text-[10px] shadow-none hover:underline underline-offset-2 focus-visible:text-primary [&>svg]:size-3",
              issue ? "text-warning" : "text-muted-foreground",
            )}
          >
            <SelectValue>{typeName}</SelectValue>
          </SelectTrigger>
          <SelectContent className="rounded-none">
            {types.map((t) => (
              <SelectItem
                key={t.code}
                value={String(t.code)}
                className="font-mono text-xs rounded-none"
              >
                {t.name}
                {t.unit ? ` (${t.unit})` : ""}
                {t.step ? ` · steps of ${t.step}` : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {issue ? (
        <p className="col-span-2 sm:col-span-3 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning">
          {issue}
        </p>
      ) : null}
    </div>
  );
});
