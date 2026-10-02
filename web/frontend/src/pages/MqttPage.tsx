import { useEffect, useState } from "react";
import {
  CircleDashed,
  Loader2,
  Pencil,
  Plus,
  Rss,
  Save,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import { InlineConfirm } from "@/components/InlineConfirm";
import { SelectField, SwitchRow, TextField } from "@/components/ConfigFields";
import { Button } from "@/components/ui/button";
import { HeaderButton } from "@/components/HeaderButton";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { timeAgo } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useApiList } from "@/hooks/useApiList";
import { useApiObject } from "@/hooks/useApiObject";
import {
  configApi,
  type Broker,
  type BrokerInput,
  type ConfigCompanion,
  type MqttSettings,
} from "@/lib/configApi";

// GET /api/mqtt/status, keyed by broker name — the observer never sees the config surrogate id.
interface BrokerStatus {
  name: string;
  connected: boolean;
  enabled: boolean;
  lastError?: string;
  lastErrorTs?: number;
  connectedTs?: number;
  published: number;
  dropped: number;
}

interface MqttStatus {
  running: boolean;
  brokers: BrokerStatus[];
}

// Every broker field but the secret, which is entered separately (blank = keep the stored one).
type BrokerDraft = Omit<BrokerInput, "password">;

const EMPTY_BROKER: BrokerDraft = {
  name: "",
  enabled: true,
  dedup: true,
  transport: "tcp",
  host: "",
  port: 8883,
  packetTopic: "",
  statusTopic: "",
  disallowedPacketTypes: null,
  retainStatus: true,
  tlsEnabled: true,
  tlsInsecure: false,
  authType: "token",
  username: "",
  path: "/",
  audience: "",
};

// Mirrors meshcoretomqtt's broker presets: github.com/Cisien/meshcoretomqtt presets/
function tokenBrokerPreset(name: string, host: string): BrokerDraft {
  return {
    ...EMPTY_BROKER,
    name,
    host,
    port: 443,
    transport: "websockets",
    audience: host,
    packetTopic: "meshcore/{IATA}/{PUBLIC_KEY}/packets",
    statusTopic: "meshcore/{IATA}/{PUBLIC_KEY}/status",
  };
}

const BROKER_PRESETS: { value: string; label: string; broker: BrokerDraft }[] =
  [
    {
      value: "letsmesh-us",
      label: "LetsMesh US",
      broker: tokenBrokerPreset("letsmesh-us", "mqtt-us-v1.letsmesh.net"),
    },
    {
      value: "letsmesh-eu",
      label: "LetsMesh EU",
      broker: tokenBrokerPreset("letsmesh-eu", "mqtt-eu-v1.letsmesh.net"),
    },
    {
      value: "waev-a",
      label: "Waev App (A)",
      broker: tokenBrokerPreset("waev-a", "mqtt-a.waev.app"),
    },
    {
      value: "waev-b",
      label: "Waev App (B)",
      broker: tokenBrokerPreset("waev-b", "mqtt-b.waev.app"),
    },
    {
      value: "meshmapper",
      label: "MeshMapper",
      broker: tokenBrokerPreset("meshmapper", "mqtt.meshmapper.net"),
    },
  ];

function brokerToDraft(b: Broker): BrokerDraft {
  return {
    name: b.name,
    enabled: b.enabled,
    dedup: b.dedup,
    transport: b.transport,
    host: b.host,
    port: b.port,
    packetTopic: b.packetTopic ?? "",
    statusTopic: b.statusTopic ?? "",
    disallowedPacketTypes: b.disallowedPacketTypes,
    retainStatus: b.retainStatus,
    tlsEnabled: b.tlsEnabled,
    tlsInsecure: b.tlsInsecure,
    authType: b.authType,
    username: b.username,
    path: b.path,
    audience: b.audience,
  };
}

export function MqttPage() {
  const {
    item: mqtt,
    loading: mqttLoading,
    error: mqttError,
    reload: reloadMqtt,
  } = useApiObject<MqttSettings>("/api/config/mqtt", "Failed to load MQTT settings");
  const {
    items: brokers,
    loading: brokersLoading,
    error: brokersError,
    reload: reloadBrokers,
  } = useApiList<Broker>("/api/config/mqtt/brokers", "Failed to load brokers");
  const { items: companions } = useApiList<ConfigCompanion>(
    "/api/config/companions",
    "Failed to load companions",
  );

  const [savingFeed, setSavingFeed] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [nodeId, setNodeId] = useState("");
  const [iataCode, setIataCode] = useState("");
  const [owner, setOwner] = useState("");
  const [email, setEmail] = useState("");
  const [statusInterval, setStatusInterval] = useState("300");
  const [editing, setEditing] = useState<Broker | "new" | null>(null);
  const [confirming, setConfirming] = useState<number | null>(null);

  useEffect(() => {
    if (!mqtt) return;
    setEnabled(mqtt.enabled !== false);
    setNodeId(mqtt.nodeCompanionId != null ? String(mqtt.nodeCompanionId) : "");
    setIataCode(mqtt.iataCode ?? "");
    setOwner(mqtt.owner ?? "");
    setEmail(mqtt.email ?? "");
    setStatusInterval(String(mqtt.statusInterval ?? 300));
  }, [mqtt]);

  // Default the feed node to the first companion when none is stored yet. Waiting for the stored
  // settings matters: when they and the companions land in one render, this effect still sees the
  // empty node from before the stored one was applied, and would overwrite it.
  useEffect(() => {
    if (mqtt && mqtt.nodeCompanionId == null && nodeId === "" && companions && companions.length > 0) {
      setNodeId(String(companions[0].id));
    }
  }, [mqtt, companions, nodeId]);

  const list = brokers ?? [];
  const loading = mqttLoading || brokersLoading;
  const error = mqttError || brokersError;

  // Connection state is runtime, not config, so it polls rather than riding the config reload.
  const [status, setStatus] = useState<MqttStatus | null>(null);
  useEffect(() => {
    let live = true;
    let inFlight = false;
    const poll = async () => {
      if (inFlight) return; // a slow poll must not stack up behind the interval
      inFlight = true;
      try {
        const r = await fetch("/api/mqtt/status");
        const json = r.ok ? await r.json() : null;
        if (live) setStatus(json);
      } catch {
        if (live) setStatus(null);
      } finally {
        inFlight = false;
      }
    };
    poll();
    const id = setInterval(poll, 5000);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, []);
  const statusFor = (name: string) =>
    status?.brokers.find((b) => b.name === name);
  const feed: FeedState = !status ? "unknown" : status.running ? "live" : "stopped";

  const saveFeed = async () => {
    setSavingFeed(true);
    try {
      await configApi.putMqtt({
        enabled,
        nodeCompanionId: nodeId === "" ? null : parseInt(nodeId, 10),
        iataCode: iataCode || null,
        owner: owner || null,
        email: email || null,
        statusInterval: parseInt(statusInterval, 10) || 300,
      });
      toast.success("MQTT settings saved");
      reloadMqtt();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save MQTT settings");
    } finally {
      setSavingFeed(false);
    }
  };

  const removeBroker = async (b: Broker) => {
    setConfirming(null);
    try {
      await configApi.deleteBroker(b.id);
      toast.success(`Broker "${b.name}" removed`);
      reloadBrokers();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to remove broker");
    }
  };

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="system"
        title="MQTT"
        meta={
          brokers && (
            <span className="flex items-center gap-3">
              <span className="font-mono text-sm text-muted-foreground tabular-nums">
                {list.length} broker{list.length === 1 ? "" : "s"}
              </span>
              {status && list.length > 0 && (
                <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                  {status.running
                    ? `${status.brokers.filter((b) => b.connected).length}/${status.brokers.filter((b) => b.enabled).length} connected`
                    : "observer stopped"}
                </span>
              )}
            </span>
          )
        }
        actions={
          <HeaderButton tone="primary" icon={Save} busy={savingFeed} onClick={saveFeed} disabled={savingFeed || loading || !mqtt}>
            {savingFeed ? "saving" : "save"}
          </HeaderButton>
        }
      />

      {error && (
        <LoadErrorAlert
          message={error}
          onRetry={() => {
            reloadMqtt();
            reloadBrokers();
          }}
        />
      )}

      {loading ? (
        <Skeleton className="h-64 w-full rounded-none" />
      ) : mqtt ? (
        <>
          <section className="panel">
            <SectionTitle eyebrow="observer" title="Feed" />
            <div className="p-4 space-y-4">
              <SwitchRow
                label="Enabled"
                hint="publish packets & status to the configured brokers"
                checked={enabled}
                onChange={setEnabled}
              />
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <SelectField
                  label="Node"
                  value={nodeId}
                  options={(companions ?? []).map((c) => ({
                    value: String(c.id),
                    label: c.name,
                  }))}
                  onChange={setNodeId}
                  hint="one node feeds MQTT — its identity signs the feed"
                />
                <TextField
                  label="IATA code"
                  value={iataCode}
                  onChange={setIataCode}
                  placeholder="AKL"
                  hint="region tag in the topic path"
                />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                <TextField label="Owner" value={owner} onChange={setOwner} />
                <TextField
                  label="Email"
                  value={email}
                  onChange={setEmail}
                  hint="included in the auth token (LetsMesh)"
                />
                <TextField
                  label="Status interval (s)"
                  value={statusInterval}
                  onChange={setStatusInterval}
                  placeholder="300"
                />
              </div>
            </div>
          </section>

          <section className="panel overflow-hidden">
            <SectionTitle
              eyebrow="endpoints"
              title="Brokers"
              trailing={
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setEditing("new")}
                  className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
                >
                  <Plus className="size-3" />
                  add broker
                </Button>
              }
            />
            {list.length === 0 ? (
              <div className="px-6 py-16 text-center space-y-3">
                <CircleDashed className="size-8 mx-auto text-muted-foreground/40" />
                <p className="font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground">
                  No brokers configured
                </p>
              </div>
            ) : (
              <div className="divide-y divide-border">
                {list.map((b) => (
                  <div key={b.id} className="flex items-center gap-4 px-4 py-3">
                    <div
                      className={cn(
                        "size-9 grid place-items-center rounded-sm border shrink-0",
                        !b.enabled || feed === "stopped"
                          ? "border-border bg-muted/40 text-muted-foreground/50"
                          : statusFor(b.name)?.connected
                            ? "border-success/40 bg-success/10 text-success"
                            : feed === "live"
                              ? "border-destructive/40 bg-destructive/10 text-destructive"
                              : "border-primary/30 bg-primary/10 text-primary",
                      )}
                    >
                      <Rss className="size-4" strokeWidth={1.6} />
                    </div>
                    <div className="min-w-0 flex-1 space-y-0.5">
                      <div className="flex items-center gap-2">
                        <span className="font-mono text-sm font-semibold truncate">
                          {b.name}
                        </span>
                        {!b.enabled ? (
                          <span className="font-mono text-[10px] uppercase tracking-widest text-muted-foreground/60">
                            disabled
                          </span>
                        ) : (
                          <BrokerStatusPill st={statusFor(b.name)} feed={feed} />
                        )}
                      </div>
                      <code className="font-mono text-xs text-muted-foreground block truncate">
                        {b.transport === "websockets" ? "ws" : "tcp"}
                        {b.tlsEnabled ? "+tls" : ""}://{b.host}:{b.port} ·{" "}
                        {b.packetTopic || "meshcore/{iata}/{pubkey}/…"} · auth{" "}
                        {b.authType || "none"}
                      </code>
                      <BrokerStatusLine
                        st={statusFor(b.name)}
                        enabled={b.enabled}
                        feed={feed}
                      />
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        onClick={() => setEditing(b)}
                        aria-label="Edit broker"
                        className="text-muted-foreground/60 hover:text-foreground"
                      >
                        <Pencil className="size-3.5" />
                      </Button>
                      <InlineConfirm
                        iconOnly
                        confirming={confirming === b.id}
                        onAskRemove={() => setConfirming(b.id)}
                        onCancel={() => setConfirming(null)}
                        onConfirm={() => removeBroker(b)}
                        ariaLabel="Remove broker"
                      />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      ) : null}

      {editing !== null && (
        <BrokerEditor
          broker={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reloadBrokers();
          }}
        />
      )}
    </div>
  );
}

function BrokerEditor({
  broker,
  onClose,
  onSaved,
}: {
  broker: Broker | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [b, setB] = useState<BrokerDraft>(
    broker ? brokerToDraft(broker) : { ...EMPTY_BROKER },
  );
  const set = <K extends keyof BrokerDraft>(k: K, v: BrokerDraft[K]) =>
    setB((prev) => ({ ...prev, [k]: v }));

  const [disallowed, setDisallowed] = useState(
    (broker?.disallowedPacketTypes ?? []).join(", "),
  );
  const [password, setPassword] = useState("");
  const [preset, setPreset] = useState("custom");
  const [saving, setSaving] = useState(false);

  const isNew = broker == null;
  const passwordSet = broker?.passwordSet ?? false;

  const applyPreset = (value: string) => {
    setPreset(value);
    const p = BROKER_PRESETS.find((x) => x.value === value);
    if (!p) return;
    setB({ ...p.broker });
    setDisallowed((p.broker.disallowedPacketTypes ?? []).join(", "));
  };

  const valid = b.name.trim() !== "" && b.host.trim() !== "" && b.port > 0;

  const submit = async () => {
    const types = disallowed
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const input: BrokerInput = {
      ...b,
      packetTopic: b.packetTopic || null,
      statusTopic: b.statusTopic || null,
      disallowedPacketTypes: types.length > 0 ? types : null,
    };
    // Only send a password when one was typed; blank keeps the stored secret.
    if (password !== "") input.password = password;
    setSaving(true);
    try {
      await configApi.saveBroker(input, broker?.id);
      toast.success(isNew ? `Broker "${b.name}" added` : "Broker saved");
      onSaved();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save broker");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none sm:max-w-xl max-h-[85dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-widest">
            {broker ? `Edit broker — ${broker.name}` : "Add broker"}
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          {isNew && (
            <SelectField
              label="Preset"
              value={preset}
              options={[
                { value: "custom", label: "Custom" },
                ...BROKER_PRESETS.map((p) => ({
                  value: p.value,
                  label: p.label,
                })),
              ]}
              onChange={applyPreset}
              hint="prefills the form — fields stay editable"
            />
          )}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <TextField
              label="Name"
              value={b.name}
              onChange={(v) => set("name", v)}
              placeholder="LetsMesh"
            />
            <SelectField
              label="Transport"
              value={b.transport || "tcp"}
              options={[
                { value: "tcp", label: "TCP" },
                { value: "websockets", label: "WebSockets" },
              ]}
              onChange={(v) => set("transport", v)}
            />
          </div>
          <div className="grid grid-cols-[1fr_8rem] gap-4">
            <TextField
              label="Host"
              value={b.host}
              onChange={(v) => set("host", v)}
              placeholder="mqtt.letsmesh.org"
            />
            <TextField
              label="Port"
              value={String(b.port || "")}
              onChange={(v) => set("port", parseInt(v, 10) || 0)}
              placeholder="8883"
            />
          </div>
          {b.transport === "websockets" && (
            <TextField
              label="WebSocket path"
              value={b.path}
              onChange={(v) => set("path", v)}
              placeholder="/"
            />
          )}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <TextField
              label="Packet topic"
              value={b.packetTopic ?? ""}
              onChange={(v) => set("packetTopic", v)}
              placeholder="meshcore/{iata}/{pubkey}/packets"
              hint="placeholders: {iata} {pubkey} {name} — {IATA}/{PUBLIC_KEY} also work"
            />
            <TextField
              label="Status topic"
              value={b.statusTopic ?? ""}
              onChange={(v) => set("statusTopic", v)}
              placeholder="meshcore/{iata}/{pubkey}/status"
              hint="blank = default structure"
            />
          </div>
          <SelectField
            label="Auth"
            value={b.authType || "none"}
            options={[
              { value: "token", label: "Token (signed JWT)" },
              { value: "basic", label: "Basic (user/pass)" },
              { value: "none", label: "None" },
            ]}
            onChange={(v) => set("authType", v)}
          />
          {b.authType === "basic" && (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <TextField
                label="Username"
                value={b.username}
                onChange={(v) => set("username", v)}
              />
              <TextField
                label="Password"
                type="password"
                value={password}
                onChange={setPassword}
                placeholder={passwordSet ? "•••••• saved" : ""}
                hint={passwordSet ? "leave blank to keep the stored password" : undefined}
              />
            </div>
          )}
          {b.authType === "token" && (
            <TextField
              label="Audience"
              value={b.audience}
              onChange={(v) => set("audience", v)}
              hint="JWT aud claim — defaults to the broker host"
            />
          )}
          <TextField
            label="Disallowed packet types"
            value={disallowed}
            onChange={setDisallowed}
            placeholder="ADVERT, TRACE"
            hint="comma-separated; these are never published"
          />
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <SwitchRow
              label="Enabled"
              checked={b.enabled}
              onChange={(v) => set("enabled", v)}
            />
            <SwitchRow
              label="Dedup"
              hint="skip already-seen packets"
              checked={b.dedup}
              onChange={(v) => set("dedup", v)}
            />
            <SwitchRow
              label="TLS"
              checked={b.tlsEnabled}
              onChange={(v) => set("tlsEnabled", v)}
            />
            <SwitchRow
              label="TLS insecure"
              hint="skip certificate verification"
              checked={b.tlsInsecure}
              onChange={(v) => set("tlsInsecure", v)}
            />
            <SwitchRow
              label="Retain status"
              hint="broker keeps the last status message"
              checked={b.retainStatus}
              onChange={(v) => set("retainStatus", v)}
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

// "No answer yet" and "observer not running" are not connection failures; only the last is red.
type FeedState = "unknown" | "stopped" | "live";

function BrokerStatusPill({ st, feed }: { st?: BrokerStatus; feed: FeedState }) {
  if (feed === "unknown") return null;
  if (feed === "stopped") {
    return (
      <span className="inline-flex items-center gap-1.5 border border-border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/60">
        <span className="size-1.5 rounded-full bg-muted-foreground/40" aria-hidden />
        feed off
      </span>
    );
  }
  const connected = !!st?.connected;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-[0.12em]",
        connected
          ? "border-success/40 bg-success/5 text-success"
          : "border-destructive/40 bg-destructive/5 text-destructive",
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full",
          connected ? "bg-success animate-pulse" : "bg-destructive",
        )}
        aria-hidden
      />
      {connected ? "connected" : "offline"}
    </span>
  );
}

// The error shows even while connected, so a flapping broker stays visible.
function BrokerStatusLine({
  st,
  enabled,
  feed,
}: {
  st?: BrokerStatus;
  enabled: boolean;
  feed: FeedState;
}) {
  if (!enabled || !st || feed !== "live") return null;
  const parts: string[] = [];
  if (st.connected) {
    if (st.connectedTs) {
      parts.push(`up ${timeAgo(new Date(st.connectedTs * 1000).toISOString())}`);
    }
  } else {
    // The observer retries a failed broker indefinitely, so it recovers on its own.
    parts.push("retrying");
  }
  if (st.published) parts.push(`${st.published} published`);
  if (st.dropped) parts.push(`${st.dropped} dropped`);
  if (!parts.length && !st.lastError) return null;
  return (
    <div className="space-y-0.5">
      {parts.length > 0 && (
        <span className="block font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/60 tabular-nums">
          {parts.join(" · ")}
        </span>
      )}
      {st.lastError && (
        <span
          className={cn(
            "block font-mono text-[10px] truncate",
            st.connected ? "text-muted-foreground/60" : "text-destructive",
          )}
          title={st.lastError}
        >
          {st.lastError}
          {st.lastErrorTs
            ? ` · ${timeAgo(new Date(st.lastErrorTs * 1000).toISOString())}`
            : ""}
        </span>
      )}
    </div>
  );
}
