import { useMemo, useState } from "react";
import {
  Bot,
  CircleDashed,
  ExternalLink,
  HelpCircle,
  Loader2,
  MapPin,
  Pencil,
  Plus,
  Save,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import { InlineConfirm } from "@/components/InlineConfirm";
import { ChannelMultiSelect } from "@/components/ChannelMultiSelect";
import {
  Field,
  PATH_HASH_SIZE_OPTIONS,
  SelectField,
  SwitchRow,
  TextField,
} from "@/components/ConfigFields";
import { StringListField } from "@/components/StringListField";
import { BotTestPanel } from "@/components/BotTestPanel";
import { PositionPicker, round6 } from "@/components/PositionPicker";
import { RegionPicker } from "@/components/RegionPicker";
import { PeerListField, type PickablePeer } from "@/components/PeerPicker";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useApiList } from "@/hooks/useApiList";
import {
  configApi,
  type ConfigChannel,
  type ConfigCompanion,
  type Trigger,
  type TriggerLocation,
} from "@/lib/configApi";

const TYPE_OPTS = [
  { value: "group", label: "Group message (match & reply)" },
  { value: "dm", label: "Direct message (match & reply)" },
  { value: "cron", label: "Cron (scheduled broadcast)" },
  { value: "rss", label: "RSS/Atom feed (new items)" },
  { value: "cap", label: "CAP alerts (emergency feed)" },
];

// rss and cap both poll a feed on a schedule; only what they do with an item differs.
const isFeedType = (t: string) => t === "rss" || t === "cap";

type Where = "anywhere" | "near" | "regions";
const WHERE_OPTS = [
  { value: "anywhere", label: "Anywhere" },
  { value: "near", label: "Near a point" },
  { value: "regions", label: "In regions" },
];

// Must match config.MaxLocationRadiusKm.
const MAX_LOCATION_RADIUS_KM = 500;

// A number typed into a field, or null while it is blank or not one.
function parseNumber(s: string): number | null {
  const n = Number(s.trim());
  return s.trim() !== "" && Number.isFinite(n) ? n : null;
}

function parseLocation(lat: string, lon: string, km: string): TriggerLocation | null {
  const la = parseNumber(lat);
  const lo = parseNumber(lon);
  const r = parseNumber(km);
  if (la === null || lo === null || r === null) return null;
  if (la < -90 || la > 90 || lo < -180 || lo > 180) return null;
  if (r < 0 || r > MAX_LOCATION_RADIUS_KM) return null;
  return { lat: la, lon: lo, radiusKm: r };
}

// Mirroring answers with the size the message arrived on, so it only means anything for a trigger
// that is answering one. Cron and the feeds start the conversation themselves.
const answersAMessage = (t: string) => t === "group" || t === "dm";


const POLL_UNITS = [
  { value: "m", label: "minutes" },
  { value: "h", label: "hours" },
];

// A feed's poll interval is stored in the same `schedule` column as a cron spec, as the "@every"
// descriptor cron already understands, so the two fields here round-trip through one string.
function parsePollInterval(schedule: string | null | undefined) {
  const m = /^@every (\d+)([mh])$/.exec(schedule ?? "");
  return m ? { every: m[1], unit: m[2] } : { every: "5", unit: "m" };
}

// "@every 15m" reads as machinery; "every 15m" reads as English.
const humanSchedule = (s: string) => s.replace(/^@every /, "every ");

// Practical regex examples for bot authors. Patterns use Go's RE2 engine.
type RegexExample = { pattern: string; desc: string };

const CHAT_REGEX_EXAMPLES: RegexExample[] = [
  { pattern: "(?i)^!bot$", desc: 'exactly "!bot", any case (not "!bottle")' },
  { pattern: "(?i)^ping", desc: 'starts with "ping" — "Ping", "ping me!"' },
  { pattern: "(?i)\\bweather\\b", desc: 'the whole word "weather" anywhere' },
  { pattern: "(?i)^(test|ping)$", desc: 'exactly "test" or "ping"' },
  { pattern: "(?i)help", desc: 'contains "help" anywhere in the message' },
  {
    pattern: "(?i)^!echo (?P<text>.+)",
    desc: "capture the rest as {{.Match.text}} for the reply",
  },
];

const RSS_REGEX_EXAMPLES: RegexExample[] = [
  { pattern: "title:(?i)warning", desc: 'the title mentions "warning"' },
  {
    pattern: "title:(?i)(flood|slip|closure)",
    desc: "any one of several words — alternation is how you say OR",
  },
  { pattern: "category:(?i)^alerts$", desc: "one of the item's categories" },
  {
    pattern: "title:(?i)magnitude (?P<mag>[0-9.]+)",
    desc: "capture the number as {{.Match.mag}}",
  },
];

// Fields must be named, or a severity filter would match the word loose in a description.
const CAP_REGEX_EXAMPLES: RegexExample[] = [
  {
    pattern: "severity:^(Extreme|Severe)$",
    desc: "only the two highest severities",
  },
  {
    pattern: "urgency:^Immediate$",
    desc: "only alerts needing immediate action",
  },
  { pattern: "msgtype:^(Alert|Update)$", desc: "skip Cancel and Ack messages" },
  { pattern: "event:(?i)tsunami", desc: "the alert is about a tsunami" },
  {
    pattern: "area:(?i)(?P<area>Northland|Auckland)",
    desc: "capture the region as {{.Match.area}}",
  },
  {
    pattern: "geocode:(?m)^UGC=TX",
    desc: "US NWS alerts for Texas, by the zone codes the NWS names areas with",
  },
  {
    pattern: "geocode:(?m)^EMMA_ID=DE028$",
    desc: "one Meteoalarm area, for a feed that sends codes instead of map shapes",
  },
];

const regexExamplesFor = (t: string): RegexExample[] =>
  t === "cap"
    ? CAP_REGEX_EXAMPLES
    : t === "rss"
      ? RSS_REGEX_EXAMPLES
      : CHAT_REGEX_EXAMPLES;

// The fields a pattern may name; the server-side vocabulary is internal/config/feedfields.go.
const RSS_MATCH_FIELDS = [
  "title",
  "description",
  "content",
  "link",
  "author",
  "category",
];
const CAP_MATCH_FIELDS = [
  "event",
  "headline",
  "description",
  "instruction",
  "severity",
  "urgency",
  "certainty",
  "msgtype",
  "status",
  "area",
  "geocode",
  "sender",
  "category",
];

const fieldOptions = (t: string) =>
  (t === "cap"
    ? CAP_MATCH_FIELDS
    : t === "rss"
      ? RSS_MATCH_FIELDS
      : undefined
  )?.map((f) => ({ value: f, label: f }));

// What a pattern is actually run against — different enough per type to be worth spelling out.
const MATCH_SUBJECT: Record<string, string> = {
  cap: "Each pattern applies to one field of the alert. Patterns on the same field are alternatives; different fields must all match.",
  rss: "Each pattern applies to one field of the item. Patterns on the same field are alternatives; different fields must all match.",
};

function RegexHelp({ type }: { type: string }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="inline-flex items-center gap-1 font-mono text-[10px] uppercase tracking-widest text-muted-foreground hover:text-foreground"
        >
          <HelpCircle className="size-3.5" />
          examples
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        className="w-104 max-w-[calc(100vw-2rem)] rounded-none p-0"
      >
        <div className="border-b border-border px-3 py-2">
          <p className="font-mono text-[11px] uppercase tracking-widest text-foreground">
            Match pattern examples
          </p>
          <p className="mt-1 font-mono text-[10px] leading-relaxed text-muted-foreground/70">
            {MATCH_SUBJECT[type] ?? "Matched against the message text."}{" "}
            Patterns match anywhere unless anchored with ^ and $.
          </p>
        </div>
        <div className="divide-y divide-border">
          {regexExamplesFor(type).map((ex) => (
            <div key={ex.pattern} className="px-3 py-2">
              <code className="font-mono text-xs text-primary">
                {ex.pattern}
              </code>
              <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">
                {ex.desc}
              </p>
            </div>
          ))}
        </div>
        <div className="space-y-1.5 border-t border-border px-3 py-2">
          <p className="font-mono text-[10px] leading-relaxed text-muted-foreground/70">
            (?i) ignore case · (?m) ^ and $ match each line · \b word boundary ·
            (a|b) a or b
          </p>
          <a
            href="https://regex101.com/?flavor=golang"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 font-mono text-[11px] text-primary underline-offset-2 hover:underline"
          >
            test a pattern on regex101
            <ExternalLink className="size-3" />
          </a>
        </div>
      </PopoverContent>
    </Popover>
  );
}

export function BotsPage() {
  const {
    items: triggers,
    loading,
    error,
    reload,
  } = useApiList<Trigger>("/api/config/triggers", "Failed to load bots");
  // A trigger write never changes these, so they load once on mount.
  const { items: companions } = useApiList<ConfigCompanion>(
    "/api/config/companions",
    "Failed to load companions",
  );
  const { items: channels } = useApiList<ConfigChannel>(
    "/api/config/channels",
    "Failed to load channels",
  );
  const { items: peers } = useApiList<PickablePeer>(
    "/api/peers",
    "Failed to load peers",
  );

  const [editing, setEditing] = useState<Trigger | "new" | null>(null);
  const [confirming, setConfirming] = useState<number | null>(null);

  const companionName = useMemo(() => {
    const m = new Map<number, string>();
    for (const c of companions ?? []) m.set(c.id, c.name);
    return m;
  }, [companions]);

  const channelName = useMemo(() => {
    const m = new Map<number, string>();
    for (const ch of channels ?? []) m.set(ch.id, ch.name);
    return m;
  }, [channels]);

  const ready = companions != null && channels != null;

  const removeBot = async (t: Trigger) => {
    setConfirming(null);
    try {
      await configApi.deleteTrigger(t.id);
      toast.success("Bot removed");
      reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to remove bot");
    }
  };

  const list = triggers ?? [];

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="comms"
        title="Bots"
        meta={
          triggers && (
            <span className="font-mono text-sm text-muted-foreground tabular-nums">
              {list.length} configured
            </span>
          )
        }
        actions={
          <Button
            size="sm"
            onClick={() => setEditing("new")}
            disabled={loading || !ready}
            className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <Plus className="size-3.5" />
            add bot
          </Button>
        }
      />

      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      {loading ? (
        <Skeleton className="h-48 w-full rounded-none" />
      ) : triggers ? (
        <section className="panel overflow-hidden">
          <SectionTitle eyebrow="triggers" title="Configured bots" />
          {list.length === 0 ? (
            <div className="px-6 py-16 text-center space-y-3">
              <CircleDashed className="size-8 mx-auto text-muted-foreground/40" />
              <p className="font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground">
                No bots configured
              </p>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {list.map((t) => {
                const chNames = (t.channelIds ?? [])
                  .map((id) => channelName.get(id))
                  .filter((n): n is string => !!n);
                return (
                  <div key={t.id} className="flex items-start gap-4 px-4 py-3">
                    <div className="size-9 grid place-items-center rounded-sm border border-primary/30 bg-primary/10 text-primary shrink-0">
                      <Bot className="size-4" strokeWidth={1.6} />
                    </div>
                    <div className="min-w-0 flex-1 space-y-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="font-mono text-[10px] uppercase tracking-widest px-1.5 py-0.5 border border-border bg-muted/40">
                          {t.type}
                        </span>
                        <span className="font-mono text-xs text-muted-foreground truncate">
                          {companionName.get(t.companionId) ??
                            `#${t.companionId}`}
                        </span>
                        {(t.type === "cron" || isFeedType(t.type)) &&
                          t.schedule && (
                            <code className="font-mono text-xs text-info">
                              {isFeedType(t.type)
                                ? humanSchedule(t.schedule)
                                : t.schedule}
                            </code>
                          )}
                        {isFeedType(t.type) && t.url && (
                          <code className="font-mono text-xs text-muted-foreground/70 truncate">
                            {t.url}
                          </code>
                        )}
                        {chNames.length > 0 && (
                          <span className="font-mono text-xs text-muted-foreground/70 truncate">
                            {chNames.join(", ")}
                          </span>
                        )}
                        {isFeedType(t.type) &&
                          (t.contacts?.length ?? 0) > 0 && (
                            <span className="font-mono text-xs text-muted-foreground/70">
                              {t.contacts?.length} direct
                            </span>
                          )}
                      </div>
                      {t.match && t.match.length > 0 && (
                        <div className="font-mono text-xs text-muted-foreground/70 truncate">
                          match: {t.match.join("  ·  ")}
                        </div>
                      )}
                      <code className="font-mono text-xs text-foreground/80 block truncate">
                        {t.template}
                      </code>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        onClick={() => setEditing(t)}
                        aria-label="Edit bot"
                        className="text-muted-foreground/60 hover:text-foreground"
                      >
                        <Pencil className="size-3.5" />
                      </Button>
                      <InlineConfirm
                        iconOnly
                        confirming={confirming === t.id}
                        onAskRemove={() => setConfirming(t.id)}
                        onCancel={() => setConfirming(null)}
                        onConfirm={() => removeBot(t)}
                        ariaLabel="Remove bot"
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </section>
      ) : null}

      {editing !== null && ready && (
        <BotEditor
          trigger={editing === "new" ? null : editing}
          companions={companions}
          channels={channels}
          peers={peers ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function BotEditor({
  trigger,
  companions,
  channels,
  peers,
  onClose,
  onSaved,
}: {
  trigger: Trigger | null;
  companions: ConfigCompanion[];
  channels: ConfigChannel[];
  peers: PickablePeer[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const channelById = useMemo(() => {
    const m = new Map<number, ConfigChannel>();
    for (const ch of channels) m.set(ch.id, ch);
    return m;
  }, [channels]);

  const [companionId, setCompanionId] = useState(
    trigger?.companionId ?? companions[0]?.id ?? 0,
  );
  const [type, setType] = useState(
    trigger?.type === "channel" ? "group" : (trigger?.type ?? "group"),
  );
  const [template, setTemplate] = useState(trigger?.template ?? "");
  // Tracked by name (unique per companion), resolved back to channel ids on submit.
  const [selectedChannels, setSelectedChannels] = useState<string[]>(
    (trigger?.channelIds ?? [])
      .map((id) => channelById.get(id)?.name)
      .filter((n): n is string => !!n),
  );
  const [match, setMatch] = useState<string[]>(trigger?.match ?? []);
  const [failover, setFailover] = useState(!!trigger?.failoverPattern);
  const [failoverPattern, setFailoverPattern] = useState(
    trigger?.failoverPattern || String.raw`^@\[{{.Sender | reQuote}}\].+`,
  );
  const [failoverTimeout, setFailoverTimeout] = useState(
    String(trigger?.failoverTimeout || 10),
  );
  const [contacts, setContacts] = useState<string[]>(trigger?.contacts ?? []);
  const [schedule, setSchedule] = useState(trigger?.schedule ?? "");
  const initialPoll = parsePollInterval(trigger?.schedule);
  const [pollEvery, setPollEvery] = useState(initialPoll.every);
  const [pollUnit, setPollUnit] = useState(initialPoll.unit);
  const [url, setUrl] = useState(trigger?.url ?? "");
  const [where, setWhere] = useState<Where>(
    trigger?.location ? "near" : trigger?.regions ? "regions" : "anywhere",
  );
  const [regionIds, setRegionIds] = useState<string[]>(trigger?.regions ?? []);
  const [nearLat, setNearLat] = useState(trigger?.location ? String(trigger.location.lat) : "");
  const [nearLon, setNearLon] = useState(trigger?.location ? String(trigger.location.lon) : "");
  const [nearKm, setNearKm] = useState(trigger?.location ? String(trigger.location.radiusKm) : "0");
  const [maxRetries, setMaxRetries] = useState(
    trigger?.maxRetries != null ? String(trigger.maxRetries) : "3",
  );
  const [retryTimeout, setRetryTimeout] = useState(
    trigger?.retryTimeout != null ? String(trigger.retryTimeout) : "5",
  );
  const [pathHashSize, setPathHashSize] = useState(
    trigger?.pathHashSize != null ? String(trigger.pathHashSize) : "default",
  );
  const [saving, setSaving] = useState(false);

  // Say what "default" resolves to rather than printing a number that is only right sometimes: a
  // companion with no size of its own inherits the radio setting.
  const pathHashSizeHint = useMemo(() => {
    if (pathHashSize === "0") return "answers with the size the message arrived on";
    if (pathHashSize !== "default") return undefined;
    const own = companions.find((c) => c.id === companionId)?.pathHashSize;
    return own != null
      ? `this companion sends ${own} byte${own > 1 ? "s" : ""}`
      : "this companion inherits the size from Settings";
  }, [pathHashSize, companions, companionId]);

  const near = useMemo(
    () => (type === "cap" && where === "near" ? parseLocation(nearLat, nearLon, nearKm) : null),
    [type, where, nearLat, nearLon, nearKm],
  );
  const nearInvalid = type === "cap" && where === "near" && near === null;
  const regions = type === "cap" && where === "regions" ? regionIds : null;
  const regionsMissing = regions !== null && regions.length === 0;
  const companion = companions.find((c) => c.id === companionId);
  const companionPosition =
    companion?.latitude != null && companion?.longitude != null
      ? { lat: companion.latitude, lon: companion.longitude }
      : null;

  // A trigger can only target channels its companion already has.
  const companionChannels = useMemo(
    () => channels.filter((ch) => ch.companionId === companionId),
    [channels, companionId],
  );
  const channelOptions = companionChannels.map((ch) => ch.name);
  const nameToId = useMemo(() => {
    const m = new Map<string, number>();
    for (const ch of companionChannels) m.set(ch.name, ch.id);
    return m;
  }, [companionChannels]);

  // Switching companion invalidates the picked channels (different rows).
  const changeCompanion = (idStr: string) => {
    setCompanionId(parseInt(idStr, 10));
    setSelectedChannels([]);
  };

  // A feed's patterns name fields that a chat trigger has none of, and the reverse, so carrying
  // them across a type change would only produce a save the server rejects.
  const changeType = (next: string) => {
    if (isFeedType(next) !== isFeedType(type)) setMatch([]);
    if (!answersAMessage(next) && pathHashSize === "0") setPathHashSize("default");
    setType(next);
  };

  const submit = async () => {
    const channelIds = selectedChannels
      .map((n) => nameToId.get(n))
      .filter((x): x is number => x != null);
    const patterns = match.map((s) => s.trim()).filter(Boolean);
    const senders = contacts.map((s) => s.trim()).filter(Boolean);

    setSaving(true);
    try {
      await configApi.saveTrigger(
        {
          companionId,
          type,
          template,
          channelIds,
          match: type !== "cron" && patterns.length > 0 ? patterns : null,
          contacts:
            (type === "dm" || isFeedType(type)) && senders.length > 0
              ? senders
              : null,
          schedule: isFeedType(type)
            ? `@every ${pollEvery.trim()}${pollUnit}`
            : type === "cron"
              ? schedule
              : null,
          url: isFeedType(type) ? url.trim() : null,
          location: near,
          regions,
          failoverPattern: type === "group" && failover ? failoverPattern.trim() : "",
          failoverTimeout: type === "group" && failover ? Number(failoverTimeout) : 0,
          maxRetries: parseInt(maxRetries, 10) || 3,
          retryTimeout: parseInt(retryTimeout, 10) || 5,
          pathHashSize:
            pathHashSize === "default" ? null : parseInt(pathHashSize, 10),
        },
        trigger?.id,
      );
      toast.success(trigger ? "Bot saved" : "Bot added");
      onSaved();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save bot");
    } finally {
      setSaving(false);
    }
  };

  const valid =
    template.trim() !== "" &&
    (type !== "group" ||
      !failover ||
      (failoverPattern.trim() !== "" &&
        /^\d+$/.test(failoverTimeout) &&
        Number(failoverTimeout) >= 1 &&
        Number(failoverTimeout) <= 3600)) &&
    (type === "dm" ||
      selectedChannels.length > 0 ||
      (isFeedType(type) && contacts.length > 0)) &&
    (type !== "cron" || schedule.trim() !== "") &&
    (!isFeedType(type) || /^[1-9]\d*$/.test(pollEvery.trim())) &&
    (!isFeedType(type) || /^https?:\/\/\S+$/.test(url.trim())) &&
    !nearInvalid &&
    !regionsMissing;

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none sm:max-w-xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-widest">
            {trigger ? "Edit bot" : "Add bot"}
          </DialogTitle>
        </DialogHeader>

        <div className="min-w-0 space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <SelectField
              label="Companion"
              value={String(companionId)}
              options={companions.map((c) => ({
                value: String(c.id),
                label: c.name,
              }))}
              onChange={changeCompanion}
            />
            <SelectField
              label="Type"
              value={type}
              options={TYPE_OPTS}
              onChange={changeType}
            />
          </div>

          {isFeedType(type) && (
            <TextField
              label="Feed URL"
              value={url}
              onChange={setUrl}
              placeholder={
                type === "cap"
                  ? "https://alerts.metservice.com/cap/atom"
                  : "https://example.com/feed.xml"
              }
              hint={
                type === "cap"
                  ? "a CAP feed — each entry links to the alert document, which is fetched and decoded"
                  : "RSS, Atom or JSON feed"
              }
            />
          )}

          {type === "cron" && (
            <TextField
              label="Schedule"
              value={schedule}
              onChange={setSchedule}
              placeholder='"*/30 * * * *" or "@hourly"'
              hint="standard 5-field cron"
            />
          )}

          {isFeedType(type) && (
            <div className="grid grid-cols-2 gap-4">
              <TextField
                label="Check every"
                type="number"
                value={pollEvery}
                onChange={setPollEvery}
                hint="one minute is the fastest allowed"
              />
              <SelectField
                label="Unit"
                value={pollUnit}
                options={POLL_UNITS}
                onChange={setPollUnit}
              />
            </div>
          )}

          {type !== "dm" && (
            <ChannelMultiSelect
              label="Channels"
              selected={selectedChannels}
              options={channelOptions}
              onChange={setSelectedChannels}
              hint={
                type === "cron"
                  ? "broadcast targets — pick from the companion's channels"
                  : isFeedType(type)
                    ? "broadcast targets — leave empty to send only to the contacts below"
                    : "channels to listen on — pick from the companion's channels"
              }
            />
          )}

          {type === "dm" && (
            <PeerListField
              label="Restrict to contacts (optional)"
              values={contacts}
              onChange={setContacts}
              peers={peers}
              addLabel="add contact"
              emptyHint="no contacts added: replies to DMs from anyone"
              hint="the companion's DM policy decides who reaches this bot at all; this narrows it further"
              dialogTitle="Add contact"
              dialogDescription="Pick who this bot answers. Only companions are listed: a repeater, room server or sensor never sends a plain DM."
              idPrefix="dm-sender"
            />
          )}

          {isFeedType(type) && (
            <PeerListField
              label="Send direct to (optional)"
              values={contacts}
              onChange={setContacts}
              peers={peers}
              addLabel="add recipient"
              emptyHint="no recipients: this bot only posts to the channels above"
              hint="each new item is also sent as a DM to everyone listed"
              dialogTitle="Add recipient"
              dialogDescription="Pick who receives each new item as a direct message."
              idPrefix="feed-recipient"
            />
          )}

          {type !== "cron" && (
            <StringListField
              label="Match patterns"
              values={match}
              onChange={setMatch}
              prefixOptions={fieldOptions(type)}
              placeholder={isFeedType(type) ? "(?i)warning" : "(?i)^!bot"}
              addLabel="add pattern"
              emptyHint={
                type === "dm"
                  ? "no patterns: every message from a listed sender fires this bot"
                  : isFeedType(type)
                    ? "no patterns: every new item is broadcast"
                    : "no patterns — add one so this bot can fire"
              }
              action={<RegexHelp type={type} />}
              hint={
                <>
                  {isFeedType(type)
                    ? "one regular expression per field — same field means either, different fields must all match. "
                    : "regular expressions — the bot fires when a message matches any pattern. "}
                  <a
                    href="https://regex101.com/?flavor=golang"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-primary underline-offset-2 hover:underline"
                  >
                    test on regex101
                  </a>
                </>
              }
            />
          )}

          {type === "cap" && (
            <div className="space-y-3">
              <SelectField
                label="Alerts from"
                value={where}
                options={WHERE_OPTS}
                onChange={(v) => setWhere(v as Where)}
                hint={
                  where === "anywhere"
                    ? undefined
                    : "Alerts that carry no map shape never match, so this only works with a feed that sends polygons or circles."
                }
              />
              {where === "regions" && (
                <RegionPicker
                  ids={regionIds}
                  onChange={setRegionIds}
                  suggest={
                    companionPosition && companion
                      ? { label: companion.name, ...companionPosition }
                      : null
                  }
                />
              )}
              {where === "near" && (
                <>
                  <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                    <TextField
                      label="Latitude"
                      type="number"
                      value={nearLat}
                      onChange={setNearLat}
                      placeholder="-41.2865"
                    />
                    <TextField
                      label="Longitude"
                      type="number"
                      value={nearLon}
                      onChange={setNearLon}
                      placeholder="174.7762"
                    />
                    <TextField
                      label="Margin (km)"
                      type="number"
                      value={nearKm}
                      onChange={setNearKm}
                      hint={`0 means the point must be inside; up to ${MAX_LOCATION_RADIUS_KM}`}
                    />
                  </div>
                  <PositionPicker
                    lat={parseFloat(nearLat)}
                    lon={parseFloat(nearLon)}
                    radiusKm={near?.radiusKm}
                    onPick={(la, lo) => {
                      setNearLat(round6(la));
                      setNearLon(round6(lo));
                    }}
                  />
                  {companionPosition && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setNearLat(String(companionPosition.lat));
                        setNearLon(String(companionPosition.lon));
                      }}
                      className="h-auto min-h-8 whitespace-normal text-left rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
                    >
                      <MapPin className="size-3.5" />
                      use {companion?.name}'s position
                    </Button>
                  )}
                  {nearInvalid && (nearLat.trim() !== "" || nearLon.trim() !== "") && (
                    <p className="font-mono text-[11px] text-destructive">
                      Enter a latitude of -90 to 90, a longitude of -180 to 180 and a margin of 0 to{" "}
                      {MAX_LOCATION_RADIUS_KM} km.
                    </p>
                  )}
                </>
              )}
            </div>
          )}

          {type === "group" && (
            <div className="space-y-3">
              <SwitchRow
                label="Failover reply"
                hint="Wait for another sender's response on the same channel before replying."
                checked={failover}
                onChange={setFailover}
              />
              {failover && (
                <>
                  <TextField
                    label="Wait before replying (seconds)"
                    type="number"
                    value={failoverTimeout}
                    onChange={setFailoverTimeout}
                    hint="1–3600 seconds. A matching response cancels this reply."
                  />
                  <TextField
                    label="Suppress reply when text matches"
                    value={failoverPattern}
                    onChange={setFailoverPattern}
                    hint="Regex template — {{.Sender | reQuote}} safely matches the original sender's name."
                  />
                </>
              )}
            </div>
          )}

          <Field
            label="Reply template"
            hint={
              type === "cap"
                ? 'Go template — {{.Event}} {{.Headline}} {{.Severity}} {{.Urgency}} {{.Areas}} {{.Description}} {{.Instruction}} {{.MsgType}}, {{date .Expires "15:04"}}; the whole decoded alert is on {{.Alert}} and the feed entry on {{.Item}}'
                : type === "rss"
                  ? 'Go template — {{.Title}} {{.Link}} {{.Description}} {{.Author}} {{.Feed}}, {{date .Published "15:04"}}; the whole parsed entry is on {{.Item}}'
                  : "Go template — group/dm: {{.Sender}} {{.Message}} {{.Match}} {{.SNR}} {{.Hops}}; dm also has {{.SenderPubKey}}; cron: {{.Time}}"
            }
          >
            <Textarea
              value={template}
              onChange={(e) => setTemplate(e.target.value)}
              placeholder={
                type === "cap"
                  ? "{{.Severity}} {{.Event}}: {{.Headline}} ({{.Areas}})"
                  : type === "rss"
                    ? "{{.Title}}"
                    : "@[{{.Sender}}] pong"
              }
              rows={3}
              className="resize-none rounded-none border-border font-mono text-sm bg-background"
            />
          </Field>

          {isFeedType(type) && (
            <BotTestPanel
              companionId={companionId}
              senderName={companions.find((c) => c.id === companionId)?.name ?? ""}
              type={type}
              url={url}
              match={match}
              template={template}
              location={near}
              regions={regions}
              disabledReason={
                !/^https?:\/\/\S+$/.test(url.trim())
                  ? "Enter the feed's http or https address above to test it."
                  : nearInvalid
                    ? "Finish the location above to test it."
                    : regionsMissing
                      ? "Pick at least one region above to test it."
                      : null
              }
            />
          )}

          <div className="grid grid-cols-2 sm:grid-cols-3 gap-4">
            <TextField
              label="Max retries"
              value={maxRetries}
              onChange={setMaxRetries}
            />
            <TextField
              label="Retry timeout (s)"
              value={retryTimeout}
              onChange={setRetryTimeout}
            />
            <SelectField
              label="Path hash size"
              value={pathHashSize}
              options={[
                { value: "default", label: "default (companion setting)" },
                ...(answersAMessage(type)
                  ? [{ value: "0", label: "mirror incoming" }]
                  : []),
                ...PATH_HASH_SIZE_OPTIONS,
              ]}
              onChange={setPathHashSize}
              hint={pathHashSizeHint}
            />
          </div>

          <div className="flex justify-end gap-2 pt-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={onClose}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              cancel
            </Button>
            <Button
              size="sm"
              onClick={submit}
              disabled={saving || !valid}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              {saving ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <Save className="size-3.5" />
              )}
              save
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
