import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ChevronLeft, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { SensorCatalogue, type Entry } from "@/components/SensorCatalogue";
import { BindingEditor } from "@/components/BindingEditor";
import {
  DurationInput,
  ListInput,
  TestPanel,
  fieldProblem,
  listRows,
  nameFor,
  spanWords,
  writeRows,
  type RowResults,
} from "@/components/SensorFields";
import {
  createSensor,
  discoverSensors,
  testSensor,
  updateSensor,
  type Sensor,
  type SensorBinding,
  type SensorField,
  type SensorKind,
  type SensorScan,
  type SensorTest,
} from "@/lib/sensorsApi";

// shown mirrors the server's rule: a field with a condition applies only while it holds, and is dropped on save otherwise.
const shown = (f: SensorField, options: Record<string, string>) =>
  !f.when || f.when.values.includes(options[f.when.key] ?? "");

// A pick is the part being configured, whether it came from the scan or the catalogue.
interface Pick {
  kind: SensorKind;
  name: string;
  options: Record<string, string>;
  // bindings are the other sensors this one reads; empty for anything that talks to hardware.
  bindings: SensorBinding[];
}

function seedOptions(
  kind: SensorKind,
  from: Record<string, string> | undefined,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of kind.fields) out[f.key] = from?.[f.key] ?? f.default ?? "";
  return out;
}

// seedPick fills the form to edit a sensor; a kind this build no longer carries still opens.
function seedPick(editing: Sensor, kinds: SensorKind[]): Pick {
  const k = kinds.find((x) => x.provider === editing.provider && x.kind === editing.kind);
  return {
    kind: k ?? {
      kind: editing.kind,
      provider: editing.provider,
      label: editing.kind,
      fields: [],
      testable: false,
    },
    name: editing.name,
    options: k ? seedOptions(k, editing.options) : { ...editing.options },
    bindings: editing.bindings,
  };
}

// snapshot is what an edit would save, so an unchanged form can say so.
const snapshot = (p: Pick) => JSON.stringify([p.name.trim(), p.options, p.bindings]);

// The server refuses the same names on save; these say so before it is asked.
const MAX_NAME = 64;
const CONTROL = /[\p{Cc}\p{Bidi_Control}]/u;

// blockedBy says why the form cannot be saved yet, in the operator's words, or null when it can.
function blockedBy(pick: Pick | null, sensors: Sensor[], editingId?: number, stored: string[] = []): string | null {
  if (!pick) return "Choose a part first.";
  const name = pick.name.trim();
  if (!name) return "Give it a name.";
  if ([...name].length > MAX_NAME) return `Keep the name to ${MAX_NAME} characters.`;
  if (CONTROL.test(name)) return "Take the control characters out of the name.";
  const twin = sensors.find((s) => s.id !== editingId && s.name.toLowerCase() === name.toLowerCase());
  if (twin) return `Another sensor is already called ${twin.name}.`;
  // In the order the form asks, so the reason points at the first thing to fix.
  if (pick.kind.binds && pick.bindings.length === 0) return "Choose at least one sensor for it to read.";
  if (pick.kind.binds && pick.bindings.some((b) => !b.name.trim() || !b.sensorId)) {
    return "Finish every reading it uses.";
  }
  return fieldsProblem(pick, stored, () => true);
}

// effective is the options as the server will hold them: each field's default where it is left empty.
function effective(pick: Pick): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of pick.kind.fields) out[f.key] = pick.options[f.key] || f.default || "";
  return out;
}

// fieldsProblem is the first shown field, of those include picks, that the server would refuse, in form order.
function fieldsProblem(pick: Pick, stored: string[], include: (f: SensorField) => boolean): string | null {
  const opts = effective(pick);
  const label = (key: string) => pick.kind.fields.find((x) => x.key === key)?.label ?? key;
  for (const f of pick.kind.fields) {
    if (!shown(f, opts) || !include(f)) continue;
    const v = opts[f.key];
    // A secret already stored counts as filled in, as the page never holds it to show.
    if (f.required && f.type !== "list" && !v.trim() && !(f.secret && stored.includes(f.key))) return `Fill in ${f.label}.`;
    const problem = fieldProblem(f, v, opts, label);
    if (problem) return problem;
  }
  return null;
}

// requestBody is what Save and Test both send: only the fields that apply, and stored secrets left blank kept rather than cleared.
function requestBody(pick: Pick, editing: Sensor | null) {
  const fields = pick.kind.fields.filter((f) => shown(f, pick.options));
  const keep = fields
    .filter((f) => f.secret && !pick.options[f.key] && editing?.secretsSet.includes(f.key))
    .map((f) => f.key);
  const options = pick.kind.fields.length
    ? Object.fromEntries(
        fields.filter((f) => !(f.secret && !pick.options[f.key])).map((f) => [f.key, pick.options[f.key] ?? ""]),
      )
    : pick.options;
  return {
    provider: pick.kind.provider,
    kind: pick.kind.kind,
    name: pick.name.trim(),
    options,
    bindings: pick.kind.binds ? pick.bindings : undefined,
    keepSecrets: keep.length ? keep : undefined,
  };
}

export function SensorDialog({
  open,
  onOpenChange,
  editing,
  sensors,
  kinds,
  kindsError,
  onRetryKinds,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // editing is the sensor being changed, or null to add a new one.
  editing: Sensor | null;
  // sensors is what a derived sensor can read; the picker is built from it.
  sensors: Sensor[];
  // kinds is the catalogue, loaded once by the page; null until it lands or when it failed.
  kinds: SensorKind[] | null;
  kindsError: string | null;
  onRetryKinds: () => void;
  onSaved: () => void;
}) {
  const [scan, setScan] = useState<SensorScan | null>(null);
  const [query, setQuery] = useState("");
  const [pick, setPick] = useState<Pick | null>(null);
  const [scanning, setScanning] = useState(false);
  const [saving, setSaving] = useState(false);
  // Kept on the form rather than in a toast, which flattens an expression error's caret line.
  const [saveError, setSaveError] = useState<string | null>(null);
  // seed is the edited sensor as it opened, so Save stays off until something changes.
  const [seed, setSeed] = useState("");
  const [test, setTest] = useState<SensorTest | null>(null);
  const [testing, setTesting] = useState(false);

  const rescan = useCallback(async () => {
    setScanning(true);
    try {
      setScan(await discoverSensors());
    } catch (e) {
      // A scan that failed outright is a problem to show, not an empty bus.
      setScan({
        candidates: [],
        problems: [{ provider: "", label: "Sensors", reason: e instanceof Error ? e.message : "the scan failed" }],
      });
    } finally {
      setScanning(false);
    }
  }, []);

  // Editing opens on the form, even without the catalogue; adding opens on the picker.
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setSaving(false);
    setSaveError(null);
    const seeded = editing ? seedPick(editing, kinds ?? []) : null;
    setPick(seeded);
    setTest(null);
    setSeed(seeded ? snapshot(seeded) : "");
  }, [open, editing, kinds]);

  // The scan answers on its own clock; a failed one must not hold up adding a part by hand.
  useEffect(() => {
    if (!open) return;
    setScan(null);
    if (!editing) void rescan();
  }, [open, editing, rescan]);

  const save = useCallback(async () => {
    if (!pick) return;
    setSaving(true);
    setSaveError(null);
    const body = requestBody(pick, editing);
    try {
      if (editing) {
        await updateSensor(editing.id, body);
        toast.success(`Saved ${body.name}`);
      } else {
        await createSensor(body);
        toast.success(`Added ${body.name}`);
      }
      onSaved();
    } catch (e) {
      setSaveError(e instanceof Error ? e.message : "Failed to save sensor");
    } finally {
      setSaving(false);
    }
  }, [pick, editing, onSaved]);

  const runTest = useCallback(async () => {
    if (!pick) return;
    setTesting(true);
    try {
      setTest(await testSensor(editing?.id ?? 0, requestBody(pick, editing)));
    } catch (e) {
      setTest({
        error: e instanceof Error ? e.message : "The test failed",
        status: "", contentType: "", body: "", truncated: false, values: [],
      });
    } finally {
      setTesting(false);
    }
  }, [pick, editing]);

  const choose = useCallback((e: Entry) => {
    setSaveError(null);
    setTest(null);
    setPick({
      kind: e.kind,
      name: e.kind.label,
      options: seedOptions(e.kind, e.options),
      bindings: [],
    });
  }, []);

  const blocked = blockedBy(pick, sensors, editing?.id, editing?.secretsSet);
  // A test needs the connection settings, not the values it is there to help pick.
  const testBlocked = pick ? fieldsProblem(pick, editing?.secretsSet ?? [], (f) => !f.tested) : null;
  const wide = pick?.kind.fields.some((f) => f.type === "list") ?? false;
  const unchanged = editing !== null && pick !== null && snapshot(pick) === seed;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* sm:max-w-md, because the base class is sm:max-w-lg and a bare max-w-md loses to it from 640px up. */}
      <DialogContent className={cn("rounded-none border-border bg-card", wide ? "sm:max-w-xl" : "sm:max-w-md")}>
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            {editing ? "Edit sensor" : "Add sensor"}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {editing
              ? "Change its name or its settings."
              : pick
                ? "Name it and fill in its settings."
                : "Pick a part a scan matched, or search for one and set it up by hand."}
          </DialogDescription>
        </DialogHeader>

        {pick ? (
          <SensorForm
            pick={pick}
            onChange={setPick}
            onBack={
              editing
                ? null
                : () => {
                    setSaveError(null);
                    setPick(null);
                  }
            }
            sensors={sensors}
            editingId={editing?.id}
            stored={editing?.secretsSet ?? []}
            results={test ? Object.fromEntries(test.values.map((v) => [v.metric, v])) : undefined}
            before={(f, onPick, picked) =>
              f.tested && pick.kind.testable ? (
                <TestPanel
                  testing={testing}
                  result={test}
                  onTest={() => void runTest()}
                  disabledReason={testBlocked}
                  json={Boolean(f.columns?.some((c) => c.key === "path"))}
                  pickedPaths={picked}
                  onPick={onPick}
                />
              ) : null
            }
          />
        ) : (
          <SensorCatalogue
            scan={scan}
            catalogue={kinds}
            catalogueError={kindsError}
            onRetryCatalogue={onRetryKinds}
            query={query}
            onQuery={setQuery}
            scanning={scanning}
            onRescan={() => void rescan()}
            onPick={choose}
          />
        )}

        {saveError ? (
          <p
            role="alert"
            className="border border-warning/40 bg-warning/5 px-3 py-2 font-mono text-[11px] sm:text-[10px] leading-relaxed text-warning whitespace-pre-wrap wrap-break-word"
          >
            {saveError}
          </p>
        ) : null}

        <div className="flex items-center justify-end gap-2">
          {pick && blocked ? (
            <span className="mr-auto font-mono text-[11px] sm:text-[10px] text-muted-foreground/70">
              {blocked}
            </span>
          ) : null}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpenChange(false)}
            className="font-mono text-xs uppercase tracking-widest"
          >
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={blocked !== null || unchanged || saving}
            onClick={save}
            className="font-mono text-xs uppercase tracking-widest"
          >
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : null}
            {editing ? "Save changes" : "Add sensor"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SensorForm({
  pick,
  onChange,
  onBack,
  sensors,
  editingId,
  stored,
  results,
  before,
}: {
  pick: Pick;
  onChange: (p: Pick) => void;
  onBack: (() => void) | null;
  sensors: Sensor[];
  editingId?: number;
  // stored names the secrets already saved, which the form never receives.
  stored: string[];
  // results are the last test's reading of each value, shown under its row in the list a test reads.
  results?: RowResults;
  // before renders above a field, such as the test above the list it reads; onPick adds a picked path to that list.
  before: (f: SensorField, onPick: (leaf: { path: string; unit: string }) => void, picked: string[]) => ReactNode;
}) {
  const set = (key: string, v: string) => onChange({ ...pick, options: { ...pick.options, [key]: v } });
  return (
    // min-w-0: a grid item in DialogContent, or the widest field sets the column and carries the buttons past the padding.
    <section className="space-y-3 min-w-0">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <span className="label-overline block">{pick.kind.label}</span>
          {pick.kind.description ? (
            <span className="font-mono text-[11px] sm:text-[10px] text-muted-foreground/70 truncate block">
              {pick.kind.description}
            </span>
          ) : null}
        </div>
        {onBack ? (
          <Button
            variant="ghost"
            size="xs"
            onClick={onBack}
            className="shrink-0 font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <ChevronLeft className="size-3" />
            Back
          </Button>
        ) : null}
      </div>

      <div className="space-y-1.5">
        <label htmlFor="sensor-name" className="label-overline block">
          Name<span aria-hidden> *</span>
        </label>
        <Input
          id="sensor-name"
          value={pick.name}
          onChange={(e) => onChange({ ...pick, name: e.target.value })}
          placeholder="What to call it on this page"
          autoFocus
          aria-required
          className="rounded-none border-border font-mono text-base md:text-xs"
        />
      </div>

      {pick.kind.binds ? (
        <BindingEditor
          bindings={pick.bindings}
          onChange={(bindings) => onChange({ ...pick, bindings })}
          sensors={sensors}
          excludeId={editingId}
        />
      ) : null}

      {pick.kind.fields.filter((f) => shown(f, pick.options)).map((f) => {
        const rows = f.type === "list" ? listRows(pick.options[f.key]) : [];
        const pickPath = (leaf: { path: string; unit: string }) =>
          set(f.key, writeRows([...rows, { name: nameFor(leaf.path, rows.map((r) => r.name ?? "")), unit: leaf.unit, path: leaf.path }]));
        return (
          <div key={f.key} className="space-y-3">
            {before(f, pickPath, rows.map((r) => r.path ?? ""))}
            <div className="space-y-1.5">
              <label htmlFor={`sensor-${f.key}`} className="label-overline block">
                {f.label}
                {f.required ? <span aria-hidden> *</span> : null}
              </label>
              {f.type === "duration" ? (
                <DurationInput id={`sensor-${f.key}`} field={f} value={pick.options[f.key] ?? ""} onChange={(v) => set(f.key, v)} />
              ) : f.type === "list" ? (
                <ListInput
                  id={`sensor-${f.key}`}
                  field={f}
                  value={pick.options[f.key] ?? ""}
                  onChange={(v) => set(f.key, v)}
                  results={f.tested ? results : undefined}
                />
              ) : f.choices && f.choices.length > 0 ? (
                <Select
                  value={pick.options[f.key] || ""}
                  onValueChange={(v) =>
                    onChange({ ...pick, options: { ...pick.options, [f.key]: v } })
                  }
                >
                  <SelectTrigger
                    id={`sensor-${f.key}`}
                    aria-required={f.required}
                    className="rounded-none border-border font-mono text-xs w-full"
                  >
                    <SelectValue placeholder="Choose one" />
                  </SelectTrigger>
                  <SelectContent className="rounded-none">
                    {f.choices.map((c) => (
                      <SelectItem key={c} value={c} className="font-mono text-xs rounded-none">
                        {c}
                      </SelectItem>
                    ))}
                    {/* A stored value this host no longer offers, such as a bus that has gone, still shows. */}
                    {pick.options[f.key] && !f.choices.includes(pick.options[f.key]) ? (
                      <SelectItem value={pick.options[f.key]} className="font-mono text-xs rounded-none">
                        {pick.options[f.key]} (not on this host)
                      </SelectItem>
                    ) : null}
                  </SelectContent>
                </Select>
              ) : f.multiline ? (
                // field-sizing-content grows the Textarea, so a long expression needs no scrollbar.
                <Textarea
                  id={`sensor-${f.key}`}
                  value={pick.options[f.key] ?? ""}
                  onChange={(e) =>
                    onChange({ ...pick, options: { ...pick.options, [f.key]: e.target.value } })
                  }
                  placeholder={f.default}
                  rows={2}
                  spellCheck={false}
                  aria-required={f.required}
                  className="rounded-none border-border bg-transparent font-mono text-base md:text-xs min-h-0 leading-relaxed"
                />
              ) : (
                <Input
                  id={`sensor-${f.key}`}
                  type={f.secret ? "password" : "text"}
                  // new-password, or a browser fills in the operator's own login for this site.
                  autoComplete={f.secret ? "new-password" : undefined}
                  value={pick.options[f.key] ?? ""}
                  onChange={(e) =>
                    onChange({ ...pick, options: { ...pick.options, [f.key]: e.target.value } })
                  }
                  placeholder={f.secret && stored.includes(f.key) ? "Saved; type to replace it" : f.default}
                  aria-required={f.required}
                  className="rounded-none border-border font-mono text-base md:text-xs"
                />
              )}
              {f.help || f.type === "duration" ? (
                <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
                  {f.type === "duration" && f.minSecs
                    ? `From ${spanWords(f.minSecs)} to ${spanWords(f.maxSecs ?? 0)}${f.atLeast ? ", and at least as long as " + (pick.kind.fields.find((x) => x.key === f.atLeast)?.label ?? f.atLeast).toLowerCase() : ""}. `
                    : ""}
                  {f.help}
                </p>
              ) : null}
            </div>
          </div>
        );
      })}
    </section>
  );
}
