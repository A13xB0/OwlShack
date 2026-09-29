// How a sensor's status reads on the page: its state, its readings sorted by role, and how each value is written.
import type { Sensor, SensorReading } from "@/lib/sensorsApi";

export type SensorState = "healthy" | "calibrating" | "waiting" | "stale" | "failing";

export const SENSOR_STATES: SensorState[] = ["healthy", "calibrating", "waiting", "stale", "failing"];

// The poll pushes every 5 s (app.sensorPollInterval), so three missed pushes mean the updates have stopped.
export const STALE_SECS = 15;

// Ranked so each sensor counts once; the age is the host's own at sending, so a phone's clock never moves it.
export function stateOf(s: Sensor): SensorState {
  if (s.error) return "failing";
  if (s.ageSecs === null) return "waiting";
  if (s.ageSecs > s.staleAfterSecs) return "stale";
  return s.readings.some(uncalibrated) ? "calibrating" : "healthy";
}

// A calibration flag not yet set, or an accuracy under 2 of 3, which has not yet seen both clean and polluted air.
export function uncalibrated(r: SensorReading): boolean {
  if (r.role !== "calibration") return false;
  if (r.format === "flag") return !r.value;
  return r.format === "count" && r.value < 2;
}

export interface CardReadings {
  headline: SensorReading[];
  detail: SensorReading[];
  calibration: SensorReading[];
  flags: SensorReading[];
}

// The index leads its card, as the one number the part exists for.
const LEADS = "iaq";

export function splitReadings(readings: SensorReading[]): CardReadings {
  const out: CardReadings = { headline: [], detail: [], calibration: [], flags: [] };
  for (const r of readings) {
    if (r.role === "calibration") out.calibration.push(r);
    else if (r.format === "flag") out.flags.push(r);
    else if (r.role === "detail") out.detail.push(r);
    else out.headline.push(r);
  }
  // A card with no headline leads with its details, so nothing it reports is ever small by default.
  if (out.headline.length === 0) [out.headline, out.detail] = [out.detail, []];
  out.headline.sort((a, b) => Number(b.metric === LEADS) - Number(a.metric === LEADS));
  return out;
}

// Bosch's published bands for the air quality index.
export function iaqBand(v: number): string {
  if (v <= 50) return "excellent";
  if (v <= 100) return "good";
  if (v <= 150) return "lightly polluted";
  if (v <= 200) return "moderately polluted";
  if (v <= 250) return "heavily polluted";
  if (v <= 350) return "severely polluted";
  return "extremely polluted";
}

export const ACCURACY = ["settling", "low", "medium", "high"];

export function ageText(secs: number | null): string {
  if (secs === null) return "never read";
  if (secs < 60) return `${Math.floor(secs)}s`;
  if (secs < 3600) return `${Math.round(secs / 60)} min`;
  return `${Math.round(secs / 3600)} h`;
}

const WHOLE = new Set(["iaq", "static_iaq"]);

// A flag reads as yes or no, a count and an index as a whole number, and ohms scale to kΩ or MΩ.
export function displayReading(r: SensorReading): { value: string; unit?: string } {
  if (r.format === "flag") return { value: r.value ? "yes" : "no" };
  if (r.format === "count" || WHOLE.has(r.metric)) return { value: String(Math.round(r.value)), unit: r.unit };
  if (r.unit === "Ω" && Math.abs(r.value) >= 1000) {
    const [scale, unit] = Math.abs(r.value) >= 1e6 ? [1e6, "MΩ"] : [1e3, "kΩ"];
    return { value: formatNumber(r.value / scale), unit };
  }
  return { value: formatNumber(r.value), unit: r.unit };
}

// Four significant figures, up to two decimals below 100, with trailing zeros dropped: 0.120125 V and 0.120375 V stay apart, 0.00004 is not 0, and 36 is not 36.00.
function formatNumber(v: number): string {
  if (v === 0) return "0";
  const mag = Math.floor(Math.log10(Math.abs(v)));
  const s = v.toFixed(Math.min(6, Math.max(mag < 2 ? 2 : 0, 3 - mag)));
  return s.includes(".") ? s.replace(/\.?0+$/, "") : s;
}

export const readingLabel = (r: SensorReading) => r.label || r.metric.replace(/_/g, " ");
