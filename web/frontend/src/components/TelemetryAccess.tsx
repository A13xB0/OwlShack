import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { apiErrorMessage } from "@/lib/apiError";
import { configApi, type ConfigCompanion, type TelemetryMode } from "@/lib/configApi";
import { cn } from "@/lib/utils";
import type { MonitorMetadata } from "@/components/MonitoringSettings";

// The firmware's permission bits (SensorManager.h), which a contact's grant is stored as.
const PERM_BASE = 0x01;
const PERM_LOCATION = 0x02;
const PERM_ENVIRONMENT = 0x04;

const MODES: { value: TelemetryMode; label: string }[] = [
  { value: "deny", label: "No one" },
  { value: "selected", label: "Chosen contacts" },
  { value: "contacts", label: "Every contact" },
];

// note says when a class has nothing to send; a firmware node sends a position only from its GPS.
const CLASSES: { key: "base" | "location" | "environment"; bit: number; label: string; short: string; note?: string }[] = [
  { key: "base", bit: PERM_BASE, label: "Battery and device", short: "device" },
  {
    key: "location",
    bit: PERM_LOCATION,
    label: "Position",
    short: "position",
    note: "Only a GPS fix is sent, and this host has no GPS.",
  },
  { key: "environment", bit: PERM_ENVIRONMENT, label: "Sensor readings", short: "sensors" },
];

type Modes = Record<(typeof CLASSES)[number]["key"], TelemetryMode>;

interface Contact {
  peerPubkey: string;
  name: string;
  metadata?: MonitorMetadata;
}

// TelemetryAccess owns who may read this companion; a saved map reaches nobody until a class is opened here.
export function TelemetryAccess({
  companionId,
  companionRef,
  onSaved,
}: {
  companionId: number;
  companionRef: string;
  onSaved?: () => void;
}) {
  const [saved, setSaved] = useState<Modes | null>(null);
  const [modes, setModes] = useState<Modes | null>(null);
  // A failed load is an error, never "No one": saving it would revoke every class the node grants.
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [contacts, setContacts] = useState<Contact[] | null>(null);
  const [contactsError, setContactsError] = useState<string | null>(null);

  const seq = useRef(0);
  const load = useCallback(async () => {
    const id = ++seq.current;
    setLoadError(null);
    try {
      const r = await fetch("/api/config/companions");
      if (!r.ok) throw new Error(await apiErrorMessage(r, "Failed to load who may read it"));
      const c = ((await r.json()) as ConfigCompanion[]).find((x) => x.id === companionId);
      if (!c) throw new Error("This companion is no longer configured.");
      const next: Modes = {
        base: c.telemetryBase,
        location: c.telemetryLocation,
        environment: c.telemetryEnvironment,
      };
      if (seq.current !== id) return;
      setSaved(next);
      setModes(next);
    } catch (e) {
      if (seq.current === id) setLoadError(e instanceof Error ? e.message : "Failed to load who may read it");
    }
  }, [companionId]);

  useEffect(() => {
    void load();
    return () => {
      seq.current++;
    };
  }, [load]);

  // Grants follow the saved modes, since they save on click and would otherwise land on a class still set to deny.
  const chosen = useMemo(() => CLASSES.filter((c) => saved?.[c.key] === "selected"), [saved]);
  const unsavedChosen = CLASSES.some((c) => modes?.[c.key] === "selected" && saved?.[c.key] !== "selected");
  const needContacts = chosen.length > 0;

  useEffect(() => {
    if (!needContacts || contacts !== null || contactsError !== null) return;
    let active = true;
    fetch(`/api/companions/${encodeURIComponent(companionRef)}/contacts`)
      .then(async (r) => {
        if (!r.ok) throw new Error(await apiErrorMessage(r, "Failed to load the contacts"));
        return r.json();
      })
      .then((cs: Contact[]) => active && setContacts(cs || []))
      .catch((e) => active && setContactsError(e instanceof Error ? e.message : "Failed to load the contacts"));
    return () => {
      active = false;
    };
  }, [needContacts, contacts, contactsError, companionRef]);

  const dirty = useMemo(
    () => saved !== null && modes !== null && CLASSES.some((c) => saved[c.key] !== modes[c.key]),
    [saved, modes],
  );

  const save = useCallback(async () => {
    if (!modes) return;
    setSaving(true);
    try {
      await configApi.setCompanionTelemetry(companionId, {
        base: modes.base,
        location: modes.location,
        environment: modes.environment,
      });
      setSaved(modes);
      toast.success("Telemetry access saved");
      onSaved?.();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save telemetry access");
    } finally {
      setSaving(false);
    }
  }, [companionId, modes, onSaved]);

  // Shown at once and sent one at a time from the latest state, or two quick toggles race and the later drops the first.
  const latest = useRef(contacts);
  latest.current = contacts;
  const queue = useRef(Promise.resolve());
  const grant = useCallback(
    (pubkey: string, bit: number, on: boolean) => {
      const before = (latest.current?.find((c) => c.peerPubkey === pubkey)?.metadata?.telemPerms ?? 0) & 0xff;
      const telemPerms = on ? before | bit : before & ~bit;
      setContacts((cs) =>
        (cs ?? []).map((c) =>
          c.peerPubkey === pubkey ? { ...c, metadata: { ...c.metadata, telemPerms } } : c,
        ),
      );
      queue.current = queue.current.then(async () => {
        try {
          const r = await fetch(
            `/api/companions/${encodeURIComponent(companionRef)}/contacts/${encodeURIComponent(pubkey)}`,
            {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ telemPerms }),
            },
          );
          if (!r.ok) throw new Error(await apiErrorMessage(r));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : "Failed to save the grant");
          // Read back what was stored rather than guess which toggle failed.
          setContacts(null);
        }
      });
    },
    [companionRef],
  );

  return (
    <section className="panel">
      <header className="border-b border-border px-4 py-3">
        <h2 className="font-mono text-sm uppercase tracking-widest">Who may read it</h2>
        <p className="font-mono text-[11px] sm:text-[10px] text-muted-foreground/70">
          Only contacts are ever answered. Battery and device gates the whole reply, so denying it
          silences the rest.
        </p>
      </header>

      {loadError ? (
        <div className="px-4 py-3">
          <LoadErrorAlert message={loadError} onRetry={() => void load()} />
        </div>
      ) : saved === null || modes === null ? (
        <div className="px-4 py-3">
          <Skeleton className="h-20 w-full" />
        </div>
      ) : (
        <div className="space-y-3 px-4 py-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {CLASSES.map((c) => (
              <div key={c.key} className="min-w-0 space-y-1">
                <Label
                  htmlFor={`telemetry-${c.key}`}
                  className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
                >
                  {c.label}
                </Label>
                <Select
                  value={modes[c.key]}
                  onValueChange={(v) => setModes((m) => m && { ...m, [c.key]: v as TelemetryMode })}
                >
                  <SelectTrigger
                    id={`telemetry-${c.key}`}
                    className="w-full min-w-0 rounded-none border-border bg-background font-mono text-xs"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="rounded-none font-mono text-xs">
                    {MODES.map((m) => (
                      <SelectItem key={m.value} value={m.value} className="rounded-none font-mono text-xs">
                        {m.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {c.note ? (
                  <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">{c.note}</p>
                ) : null}
              </div>
            ))}
          </div>

          <div className="flex items-center justify-end gap-2">
            {unsavedChosen ? (
              <span className="mr-auto font-mono text-[11px] sm:text-[10px] text-muted-foreground/70">
                Save, then choose which contacts may read it.
              </span>
            ) : null}
            <Button
              size="xs"
              onClick={save}
              disabled={!dirty || saving}
              className="font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              {saving ? "Saving..." : "Save access"}
            </Button>
          </div>

          {needContacts ? (
            <div className="space-y-1.5 border-t border-border pt-3">
              <span className="label-overline block">Chosen contacts</span>
              {contactsError ? (
                <LoadErrorAlert message={contactsError} onRetry={() => setContactsError(null)} />
              ) : contacts === null ? (
                <Skeleton className="h-8 w-full" />
              ) : contacts.length === 0 ? (
                <p className="font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
                  This companion has no contacts yet, so nothing can be granted.
                </p>
              ) : (
                contacts.map((ct) => (
                  <div
                    key={ct.peerPubkey}
                    className="flex flex-wrap items-center justify-between gap-2 border border-border/60 px-2.5 py-1.5"
                  >
                    <span className="min-w-0 truncate font-mono text-xs">
                      {ct.name || ct.peerPubkey.slice(0, 12)}
                    </span>
                    <div className="flex shrink-0 gap-1">
                      {chosen.map((c) => {
                        const on = ((ct.metadata?.telemPerms ?? 0) & c.bit) !== 0;
                        return (
                          <button
                            key={c.key}
                            type="button"
                            aria-pressed={on}
                            aria-label={`${c.short} for ${ct.name || ct.peerPubkey.slice(0, 12)}`}
                            onClick={() => grant(ct.peerPubkey, c.bit, !on)}
                            className={cn(
                              // A phone's thumb needs the height; the ring shows where the keyboard is.
                              "min-h-9 sm:min-h-0 border px-2 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em] outline-none focus-visible:ring-2 focus-visible:ring-ring",
                              on
                                ? "border-primary bg-primary/10 text-primary"
                                : "border-border text-muted-foreground/70 hover:text-foreground",
                            )}
                          >
                            {c.short}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                ))
              )}
            </div>
          ) : null}
        </div>
      )}
    </section>
  );
}
