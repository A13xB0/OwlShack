import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Activity,
  Check,
  Copy,
  DoorOpen,
  MapPin,
  MessageSquare,
  Pencil,
  Radio,
} from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { BackLink } from "@/components/BackLink";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PathDialog } from "@/components/PathDialog";
import { PositionPicker, round6 } from "@/components/PositionPicker";
import { PeerTypePill } from "@/components/StatusIndicator";
import { TelemetryPanel } from "@/components/TelemetryPanel";
import { advertPathInfo } from "@/components/PeerDetailSheet";
import {
  MonitoringSettings,
  type MonitorMetadata,
} from "@/components/MonitoringSettings";
import { timeAgo, truncateMid } from "@/lib/format";

interface Contact {
  peerPubkey: string;
  name: string;
  type?: string;
  lat: number;
  lon: number;
  feat1?: number;
  feat2?: number;
  outPath: string | null; // null = unknown (flood), "" = direct
  outPathHashSize?: number;
  pathHashSize: number; // bytes per hop for everything sent to this contact
  lastSeen?: string;
  addedAt: string;
  metadata?: MonitorMetadata;
}

export function ContactDetailPage() {
  const { ref, pubkey } = useParams();
  const companion = ref ?? "";
  const contactPubkey = decodeURIComponent(pubkey ?? "");

  const apiBase = `/api/companions/${encodeURIComponent(companion)}/contacts/${encodeURIComponent(contactPubkey)}`;

  const [contact, setContact] = useState<Contact | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [pathOpen, setPathOpen] = useState(false);

  useEffect(() => {
    if (!companion || !contactPubkey) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    fetch(apiBase)
      .then((r) => {
        if (r.status === 404) throw new Error("Contact not found");
        if (!r.ok) throw new Error("contact");
        return r.json();
      })
      .then((c: Contact) => {
        if (!cancelled) setContact(c);
      })
      .catch((e) => {
        if (!cancelled)
          setError(e instanceof Error ? e.message : "Failed to load contact");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [companion, contactPubkey, apiBase]);

  // After a route change, refetch in place: the full load swaps the page for a skeleton and drops fetched telemetry.
  const refreshContact = useCallback(() => {
    fetch(apiBase)
      .then((r) => (r.ok ? r.json() : null))
      .then((c: Contact | null) => c && setContact(c))
      .catch(() => {});
  }, [apiBase]);

  const copyKey = useCallback(() => {
    navigator.clipboard.writeText(contactPubkey).then(() => {
      setCopied(true);
      toast.success("Public key copied");
      window.setTimeout(() => setCopied(false), 1500);
    });
  }, [contactPubkey]);

  const displayName = contact?.name || "unknown peer";
  // Routing comes from the contact's own stored path, which is per companion.
  const route = useMemo(
    () => advertPathInfo(contact?.outPath ?? undefined, contact?.outPathHashSize),
    [contact?.outPath, contact?.outPathHashSize],
  );
  const routeLabel =
    contact?.outPath == null
      ? "Flood"
      : route.hops === 0
        ? "Direct"
        : `${route.hops} hop${route.hops === 1 ? "" : "s"}`;

  const contactType = contact?.type?.toUpperCase();
  const isRepeater = contactType === "REPEATER";
  const isSensor = contactType === "SENSOR";
  const isRoom = contactType === "ROOM";
  // Repeaters and sensors speak the admin protocol, not chat; a room speaks both.
  const manageTo = isSensor
    ? `/companions/${encodeURIComponent(companion)}/sensors/${contactPubkey}`
    : isRoom
      ? `/companions/${encodeURIComponent(companion)}/rooms/${contactPubkey}`
      : `/companions/${encodeURIComponent(companion)}/repeaters/${contactPubkey}`;
  const messageTo = `/companions/${encodeURIComponent(companion)}?channel=${encodeURIComponent(`dm:${contactPubkey}`)}`;

  return (
    <div className="space-y-8">
      <div className="flex flex-col gap-3">
        <BackLink
          to={`/companions/${encodeURIComponent(companion)}/contacts`}
          label="Contacts"
        />

        <div className="flex flex-wrap items-start gap-4">
          <PeerAvatar name={displayName} size="lg" />
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex items-center gap-2 min-w-0">
              <h1 className="text-lg font-semibold truncate">{displayName}</h1>
              {contact?.type && (
                <span className="shrink-0">
                  <PeerTypePill type={contact.type} />
                </span>
              )}
            </div>
            <code className="font-mono text-xs text-muted-foreground break-all">
              {truncateMid(contactPubkey, 10, 8)}
            </code>
          </div>
          <div className="basis-full sm:basis-auto sm:ml-auto shrink-0 flex items-center gap-2">
            {isRoom && (
              <Button
                asChild
                variant="outline"
                size="sm"
                className="font-mono text-xs uppercase tracking-widest"
              >
                <Link to={manageTo}>
                  <DoorOpen className="size-3.5" />
                  Manage
                </Link>
              </Button>
            )}
            <Button
              asChild
              variant="default"
              size="sm"
              className="font-mono text-xs uppercase tracking-widest"
            >
              {isRepeater || isSensor ? (
                <Link to={manageTo}>
                  {isSensor ? <Activity className="size-3.5" /> : <Radio className="size-3.5" />}
                  Manage
                </Link>
              ) : (
                <Link to={messageTo}>
                  <MessageSquare className="size-3.5" />
                  Message
                </Link>
              )}
            </Button>
          </div>
        </div>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {loading && (
        <div className="space-y-2">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      )}

      {!loading && !error && contact && (
        <>
          <section className="panel overflow-hidden">
            <PanelHeader eyebrow="Contact" title="Identity" />
            <div className="divide-y divide-border">
              <InfoRow label="Public key">
                <button
                  type="button"
                  onClick={copyKey}
                  className="inline-flex items-center gap-1.5 py-3 -my-3 sm:py-0 sm:my-0 font-mono text-xs hover:text-primary transition-colors"
                >
                  <span className="break-all">
                    {truncateMid(contactPubkey, 10, 10)}
                  </span>
                  {copied ? (
                    <Check className="size-3 text-success" />
                  ) : (
                    <Copy className="size-3" />
                  )}
                </button>
              </InfoRow>
              <InfoRow label="Type">
                <span className="font-mono text-xs uppercase tracking-[0.08em]">
                  {contact.type ?? "—"}
                </span>
              </InfoRow>
              <InfoRow label="Last seen">
                <span className="tabular-nums">
                  {contact.lastSeen ? timeAgo(contact.lastSeen) : "never"}
                </span>
              </InfoRow>
              <InfoRow label="Added">
                <span className="tabular-nums">{timeAgo(contact.addedAt)}</span>
              </InfoRow>
            </div>
          </section>

          <LocationPanel
            apiBase={apiBase}
            lat={contact.lat}
            lon={contact.lon}
            onSaved={(lat, lon) =>
              setContact((c) => (c ? { ...c, lat, lon } : c))
            }
          />

          <section className="panel overflow-hidden">
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <div className="space-y-0.5">
                <span className="label-overline block">Routing</span>
                <h2 className="font-mono text-sm uppercase tracking-widest">Path</h2>
              </div>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setPathOpen(true)}
                aria-label="Edit path"
                className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary"
              >
                <Pencil className="size-3" /> edit
              </Button>
            </div>
            <div className="divide-y divide-border">
              <InfoRow label="Route">
                <span className="font-mono text-xs uppercase tracking-[0.08em]">
                  {routeLabel}
                </span>
              </InfoRow>
              {route.hops > 0 && contact.outPath && (
                <InfoRow label="Out path">
                  <code className="font-mono text-xs break-all">
                    {contact.outPath}
                  </code>
                </InfoRow>
              )}
              <InfoRow label="Bytes per hop">
                <span className="font-mono text-xs uppercase tracking-[0.08em] tabular-nums">
                  {contact.pathHashSize} byte{contact.pathHashSize === 1 ? "" : "s"}
                </span>
              </InfoRow>
            </div>
          </section>
          <PathDialog
            open={pathOpen}
            onOpenChange={setPathOpen}
            companion={companion}
            pubkey={contactPubkey}
            name={displayName}
            onChanged={refreshContact}
          />

          <section className="panel p-4">
            <TelemetryPanel
              apiBase={apiBase}
              errorHint={
                isSensor
                  ? "A sensor only answers clients in its ACL — log in once via Manage, then retry."
                  : undefined
              }
            />
          </section>

          {/* Chat nodes, sensors and rooms answer the sessionless telemetry request. */}
          {(contact.type?.toUpperCase() === "CHAT" || isSensor || isRoom) && (
            <MonitoringSettings
              companionRef={companion}
              pubkey={contactPubkey}
              kind="companion"
              metadata={contact.metadata}
            />
          )}
        </>
      )}
    </div>
  );
}

// lat/lon are microdegrees, matching discovered_peers.
function LocationPanel({
  apiBase,
  lat,
  lon,
  onSaved,
}: {
  apiBase: string;
  lat: number;
  lon: number;
  onSaved: (lat: number, lon: number) => void;
}) {
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [latStr, setLatStr] = useState("");
  const [lonStr, setLonStr] = useState("");

  const hasLocation = lat !== 0 || lon !== 0;
  const latDeg = lat / 1e6;
  const lonDeg = lon / 1e6;

  const startEdit = () => {
    setLatStr(hasLocation ? latDeg.toFixed(6) : "");
    setLonStr(hasLocation ? lonDeg.toFixed(6) : "");
    setEditing(true);
  };

  const save = async () => {
    const la = parseFloat(latStr);
    const lo = parseFloat(lonStr);
    if (
      !Number.isFinite(la) ||
      !Number.isFinite(lo) ||
      la < -90 ||
      la > 90 ||
      lo < -180 ||
      lo > 180
    ) {
      toast.error("Enter a valid lat, lon");
      return;
    }
    const latUd = Math.round(la * 1e6);
    const lonUd = Math.round(lo * 1e6);
    setSaving(true);
    try {
      const res = await fetch(`${apiBase}/location`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ lat: latUd, lon: lonUd }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      toast.success("Location updated");
      onSaved(latUd, lonUd);
      setEditing(false);
    } catch (e) {
      toast.error(`Failed: ${e instanceof Error ? e.message : "error"}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="panel overflow-hidden">
      <div className="flex items-center justify-between border-b border-border px-4 py-3">
        <div className="space-y-0.5">
          <span className="label-overline block">Position</span>
          <h2 className="font-mono text-sm uppercase tracking-widest">
            Location
          </h2>
        </div>
        {!editing && (
          <Button
            variant="ghost"
            size="xs"
            onClick={startEdit}
            className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary"
          >
            <Pencil className="size-3" /> edit
          </Button>
        )}
      </div>
      {editing ? (
        <div className="space-y-3 px-4 py-3">
          <div className="grid grid-cols-2 gap-2">
            <Input
              value={latStr}
              onChange={(e) => setLatStr(e.target.value)}
              placeholder="lat"
              inputMode="decimal"
              className="font-mono text-base md:text-xs"
            />
            <Input
              value={lonStr}
              onChange={(e) => setLonStr(e.target.value)}
              placeholder="lon"
              inputMode="decimal"
              className="font-mono text-base md:text-xs"
            />
          </div>
          <PositionPicker
            lat={parseFloat(latStr)}
            lon={parseFloat(lonStr)}
            onPick={(la, lo) => {
              setLatStr(round6(la));
              setLonStr(round6(lo));
            }}
          />
          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setEditing(false)}
              className="font-mono uppercase tracking-widest"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={save}
              disabled={saving}
              className="font-mono uppercase tracking-widest"
            >
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </div>
      ) : (
        <div className="divide-y divide-border">
          <InfoRow label="Coordinates">
            {hasLocation ? (
              <code className="font-mono text-xs tabular-nums">
                {latDeg.toFixed(6)}, {lonDeg.toFixed(6)}
              </code>
            ) : (
              <span className="text-muted-foreground/60">not set</span>
            )}
          </InfoRow>
          {hasLocation && (
            <div className="px-4 py-3">
              <button
                type="button"
                onClick={() => navigate(`/map?lat=${latDeg}&lon=${lonDeg}`)}
                className="flex w-full items-center justify-between border border-border px-3 py-2.5 text-left transition-colors hover:border-primary/40 hover:bg-muted/40"
              >
                <span className="flex items-center gap-2 font-mono text-xs uppercase tracking-[0.08em]">
                  <MapPin className="size-3.5 text-primary" /> View on map
                </span>
                <span className="text-muted-foreground/50">›</span>
              </button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function PanelHeader({ eyebrow, title }: { eyebrow: string; title: string }) {
  return (
    <div className="px-4 py-3 border-b border-border space-y-0.5">
      <span className="label-overline block">{eyebrow}</span>
      <h2 className="font-mono text-sm uppercase tracking-widest">{title}</h2>
    </div>
  );
}

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-4 py-3">
      <span className="label-overline shrink-0">{label}</span>
      <div className="min-w-0 text-right text-sm">{children}</div>
    </div>
  );
}
