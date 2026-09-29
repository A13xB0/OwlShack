// Local sensors on this host, unrelated to the SENSOR peer type reached over the radio.
import { apiErrorMessage } from "@/lib/apiError";

export interface SensorReading {
  metric: string;
  label?: string;
  value: number;
  unit?: string;
  // flag is a yes or no carried as 1 or 0; count is a whole number.
  format: "number" | "flag" | "count";
  // headline is shown large, detail in a list, calibration as how far a fusion has learned.
  role: "headline" | "detail" | "calibration";
}

// A Binding names one reading as an expression calls it, so renaming a sensor cannot break it.
export interface SensorBinding {
  name: string;
  sensorId: number;
  metric: string;
}

export interface Sensor {
  id: number;
  provider: string;
  kind: string;
  name: string;
  options: Record<string, string>;
  bindings: SensorBinding[];
  // reports is every metric the sensor publishes under its options, whether or not it has been read.
  reports: string[];
  readings: SensorReading[];
  // null until read once, separating a first read pending from one that reported nothing.
  at: string | null;
  // how old the readings were when sent, by the host's clock alone, so skew between clocks never reads as age; null until read once.
  ageSecs: number | null;
  // how long until a sensor that failed to open is tried again, by the host's clock; null when it is not waiting.
  retryInSecs: number | null;
  // the kind's catalogue group, such as Environment.
  category: string;
  // the secret options that hold a value; options leaves them out.
  secretsSet: string[];
  // how old a reading may be before it is out of date: the poll's for most, a web sensor's own.
  staleAfterSecs: number;
  // empty while the latest attempt worked.
  error: string;
}

export interface SensorCandidate {
  kind: string;
  provider: string;
  label: string;
  detail?: string;
  // false for a part this build cannot drive; still listed, or an unknown part reads as an empty bus.
  addable: boolean;
  // usedBy names the sensor already on this part, empty while the part is free.
  usedBy: string;
  options: Record<string, string>;
}

export interface SensorField {
  key: string;
  label: string;
  help?: string;
  default?: string;
  // when set, the value must be one of these and the form offers a picker
  choices?: string[];
  required: boolean;
  // multiline means the value wants room to grow, such as an expression.
  multiline?: boolean;
  // identifies means the value says which part this is, so a card can name it without every option.
  identifies?: boolean;
  // secret is never sent back; the sensor's secretsSet says whether one is stored.
  secret?: boolean;
  // when shows the field only while another field holds one of values.
  when?: { key: string; values: string[] };
  // duration is a length of time stored as a Go duration such as 10m; list is rows of columns stored as JSON.
  type?: "duration" | "list";
  minSecs?: number;
  maxSecs?: number;
  // atLeast names a duration field this one may not be shorter than.
  atLeast?: string;
  columns?: SensorColumn[];
  // tested means a test reads this list's rows, so the form puts the test just above it and shows each row's result.
  tested?: boolean;
}

export interface SensorColumn {
  key: string;
  label: string;
  placeholder?: string;
  required: boolean;
}

// SensorKind carries description, category and metrics so the catalogue is searchable by part number or need.
export interface SensorKind {
  kind: string;
  provider: string;
  label: string;
  description?: string;
  category?: string;
  metrics?: string[];
  // binds means this kind reads other sensors, so the form offers a binding editor.
  binds?: boolean;
  // testable means the form can try a sensor of this kind before it is saved.
  testable: boolean;
  fields: SensorField[];
}

// A provider that could not be scanned, kept apart so an unreadable bus never reads as an empty one.
export interface SensorProviderProblem {
  provider: string;
  label: string;
  reason: string;
}

export interface SensorScan {
  candidates: SensorCandidate[];
  problems: SensorProviderProblem[];
}

// An empty provider scans them all.
export async function discoverSensors(provider = ""): Promise<SensorScan> {
  const res = await fetch("/api/sensors/discover", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider }),
  });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
  const body = (await res.json()) as Partial<SensorScan>;
  return { candidates: body.candidates || [], problems: body.problems || [] };
}

// SensorInput is what the add and edit form sends. Bindings are only set by derived sensors.
export interface SensorInput {
  provider: string;
  kind: string;
  name: string;
  options: Record<string, string>;
  bindings?: SensorBinding[];
  // keepSecrets names stored secrets an edit carries over, since the page never has them to send.
  keepSecrets?: string[];
}

// A try of an unsaved sensor: what answered, the reply to pick values from, and each value or why it failed.
export interface SensorTest {
  // error is why the try as a whole failed, empty when it worked.
  error: string;
  status: string;
  contentType: string;
  body: string;
  truncated: boolean;
  values: { metric: string; value: number | null; unit: string; error: string }[];
}

// id is the sensor being edited, whose stored secrets input.keepSecrets names, or 0 when adding.
export async function testSensor(id: number, input: SensorInput): Promise<SensorTest> {
  const res = await fetch("/api/sensors/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id, ...input }),
  });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
  return (await res.json()) as SensorTest;
}

export async function createSensor(input: SensorInput): Promise<void> {
  const res = await fetch("/api/sensors", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
}

export async function updateSensor(id: number, input: SensorInput): Promise<void> {
  const res = await fetch(`/api/sensors/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
}

export async function deleteSensor(id: number): Promise<void> {
  const res = await fetch(`/api/sensors/${id}`, { method: "DELETE" });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
}

// TelemetryNode names the map's owner; each node answers for itself, so a channel means nothing without it.
export interface TelemetryNode {
  kind: string;
  id: number;
}

export interface TelemetryNodeInfo {
  node: TelemetryNode;
  name: string;
  // serves is false for a node that stores a map but cannot answer a telemetry request yet.
  serves: boolean;
  reason?: string;
}

// TelemetryMapEntry is one reading published on a channel as a type the operator chose.
export interface TelemetryMapEntry {
  node: TelemetryNode;
  channel: number;
  type: number;
  sensorId: number;
  metric: string;
}

export interface LPPType {
  code: number;
  name: string;
  unit?: string;
  // bytes is the payload plus its two-byte header: what this row costs of the budget.
  bytes: number;
  // step is the smallest change the type carries, so the loss is visible before saving.
  step: number;
}

export interface TelemetryMap {
  nodes: TelemetryNodeInfo[];
  entries: TelemetryMapEntry[];
  types: LPPType[];
  // defaults maps a metric to the type it is offered as; an absent metric has no obvious answer.
  defaults: Record<string, number>;
  selfChannel: number;
  // What the node's own channel may carry; a row there replaces the node's built-in reading.
  selfTypes: number[];
  // maxChannel is the highest channel worth offering, not the highest a byte holds.
  maxChannel: number;
  // maxBytes is the budget each node's own reply has to fit; nodes do not share it.
  maxBytes: number;
}

export const sameNode = (a: TelemetryNode, b: TelemetryNode) =>
  a.kind === b.kind && a.id === b.id;

export async function fetchTelemetryMap(): Promise<TelemetryMap> {
  const res = await fetch("/api/sensors/telemetry-map");
  if (!res.ok) throw new Error(await apiErrorMessage(res));
  return (await res.json()) as TelemetryMap;
}

// Saves one node's map; every other node's is left alone.
export async function saveTelemetryMap(
  node: TelemetryNode,
  entries: TelemetryMapEntry[],
): Promise<TelemetryMap> {
  const res = await fetch("/api/sensors/telemetry-map", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ node, entries }),
  });
  if (!res.ok) throw new Error(await apiErrorMessage(res));
  return (await res.json()) as TelemetryMap;
}
