import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Activity,
  AlertTriangle,
  Battery,
  CircleDashed,
  Clock,
  Copy,
  Inbox,
  Loader2,
  Network,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Send,
  Settings as SettingsIcon,
  Shield,
  Signal,
  Users,
  Wifi,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import { PeerAvatar } from "@/components/PeerAvatar";
import { SignalStrength } from "@/components/SignalStrength";
import { ConnectionPill, PeerTypePill } from "@/components/StatusIndicator";
import { TelemetryMapEditor } from "@/components/TelemetryMapEditor";
import { PeerDetailSheet, type PeerLike } from "@/components/PeerDetailSheet";
import {
  AddAccessDialog,
  PERM_ROLE_MASK,
  ROLE_OPTIONS,
  CurrentRoleItem,
  RepeaterTab,
  RepeaterTabsList,
  StatTile,
  roleLabel,
  rolePillClass,
} from "@/components/RepeaterUI";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Field, TextField, SelectField, SwitchRow, PATH_HASH_SIZE_OPTIONS } from "@/components/ConfigFields";
import { PositionPicker, round6 } from "@/components/PositionPicker";
import { Switch } from "@/components/ui/switch";
import { InlineConfirm } from "@/components/InlineConfirm";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent } from "@/components/ui/tabs";
import { useApiObject } from "@/hooks/useApiObject";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useApiList } from "@/hooks/useApiList";
import { useCompanions } from "@/hooks/useCompanions";
import { usePeerDetailSheet } from "@/hooks/usePeerDetailSheet";
import {
  configApi,
  type ConfigRepeater,
  type RepeaterNodeNeighbor,
  type RepeaterNodeStats,
  type RepeaterAclEntry,
} from "@/lib/configApi";
import { formatBattery, formatSecsAgo, formatUptime, truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";

const LOOP_LEVELS = ["off", "minimal", "moderate", "strict"];

const DISCOVER_COOLDOWN_SECS = 30;

const repeaterNode = { kind: "repeater", id: 1 } as const;

type TabKey = "status" | "neighbors" | "access" | "telemetry" | "settings";

interface Live {
  stats: RepeaterNodeStats | null;
  neighbors: RepeaterNodeNeighbor[];
  acl: RepeaterAclEntry[];
}

export function RepeaterNodePage() {
  const { item: rep, loading, error, reload } = useApiObject<ConfigRepeater>(
    "/api/config/repeater",
    "Failed to load repeater",
  );
  const { items: peers } = useApiList<PeerLike>("/api/peers", "Failed to load peers");
  const companions = useCompanions();
  const { selectPeer, sheetProps } = usePeerDetailSheet(peers ?? []);

  const [tab, setTab] = useState<TabKey>("status");
  const [busy, setBusy] = useState<"advert" | "discover" | null>(null);
  const [cooldown, setCooldown] = useState(0);
  const [live, setLive] = useState<Live>({ stats: null, neighbors: [], acl: [] });
  const [refreshing, setRefreshing] = useState(false);

  const refreshLive = useCallback(async () => {
    setRefreshing(true);
    const get = async <T,>(url: string, fallback: T): Promise<T> => {
      try {
        const r = await fetch(url);
        return r.ok ? ((await r.json()) ?? fallback) : fallback;
      } catch {
        return fallback;
      }
    };
    const [stats, neighbors, acl] = await Promise.all([
      get<RepeaterNodeStats | null>("/api/repeater/status", null),
      get<RepeaterNodeNeighbor[]>("/api/repeater/neighbors", []),
      get<RepeaterAclEntry[]>("/api/repeater/acl", []),
    ]);
    setLive({ stats, neighbors, acl });
    setRefreshing(false);
  }, []);

  const onWsNeighbor = useCallback((topic: string, data: unknown) => {
    if (topic !== "repeaterNeighbors" || !data) return;
    const n = data as RepeaterNodeNeighbor;
    setLive((prev) => ({
      ...prev,
      neighbors: [n, ...prev.neighbors.filter((x) => x.pubkey !== n.pubkey)],
    }));
  }, []);
  useWebSocket(["repeaterNeighbors"], onWsNeighbor);

  const running = rep?.running ?? false;
  useEffect(() => {
    if (!running) {
      setLive({ stats: null, neighbors: [], acl: [] });
      return;
    }
    refreshLive();
    const id = setInterval(refreshLive, 5000);
    return () => clearInterval(id);
  }, [running, refreshLive]);

  const onAdvert = async (flood: boolean) => {
    setBusy("advert");
    try {
      await configApi.repeaterAdvert(flood);
      toast.success(flood ? "Flood advert sent" : "Zero-hop advert sent");
      setTimeout(refreshLive, 500);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to advertise");
    } finally {
      setBusy(null);
    }
  };

  const onDiscover = async () => {
    setBusy("discover");
    try {
      await configApi.repeaterDiscover();
      setCooldown(DISCOVER_COOLDOWN_SECS);
      toast.success("Discovery sent · neighbours may take ~60s to respond");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Discovery failed");
    } finally {
      setBusy(null);
    }
  };

  useEffect(() => {
    if (cooldown <= 0) return;
    const id = setTimeout(() => setCooldown((c) => c - 1), 1000);
    return () => clearTimeout(id);
  }, [cooldown]);

  const configured = rep?.configured ?? false;

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="system"
        title="Repeater"
        actions={
          configured ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!running || busy !== null}
                  className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
                >
                  {busy === "advert" ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Send className="size-3.5" />
                  )}
                  advertise
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="rounded-sm">
                <DropdownMenuItem
                  onClick={() => onAdvert(true)}
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  Flood advert
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => onAdvert(false)}
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  Zero-hop / direct
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : undefined
        }
      />

      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      {/* Only the first load: a reload after a save would unmount the tabs and the unsaved map with them. */}
      {loading && !rep ? (
        <Skeleton className="h-64 w-full rounded-none" />
      ) : !configured || !rep ? (
        <CreateCard onCreated={reload} />
      ) : (
        <>
          <section className="panel p-4 flex items-center gap-4">
            <PeerAvatar name={rep.name} size="lg" />
            <div className="min-w-0 flex-1 space-y-1">
              <div className="flex items-center gap-2 flex-wrap">
                <span className="font-mono text-base font-semibold uppercase tracking-[0.06em]">
                  {rep.name}
                </span>
                <PeerTypePill type="REPEATER" />
                <ConnectionPill connected={running} />
              </div>
              <code className="block font-mono text-xs text-muted-foreground truncate" title={rep.pubkey}>
                {truncateMid(rep.pubkey, 10, 8)}
              </code>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                navigator.clipboard.writeText(rep.pubkey);
                toast.success("Pubkey copied");
              }}
              className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
            >
              <Copy className="size-3" /> copy
            </Button>
          </section>

          {!running && (
            <div className="flex items-start gap-2 border border-warning/40 bg-warning/5 px-3 py-2 font-mono text-[11px] text-warning">
              <AlertTriangle className="size-3.5 shrink-0 mt-0.5" />
              <span>
                Repeater is configured but not running. Check the logs — a bad
                identity or radio error prevents startup.
              </span>
            </div>
          )}

          <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)} className="space-y-4">
            <RepeaterTabsList>
              <RepeaterTab value="status" icon={<Signal className="size-3" />}>
                Status
              </RepeaterTab>
              <RepeaterTab value="neighbors" icon={<Users className="size-3" />}>
                Neighbors
              </RepeaterTab>
              <RepeaterTab value="access" icon={<Shield className="size-3" />}>
                Access
              </RepeaterTab>
              <RepeaterTab value="telemetry" icon={<Activity className="size-3" />}>
                Telemetry
              </RepeaterTab>
              <RepeaterTab value="settings" icon={<SettingsIcon className="size-3" />}>
                Settings
              </RepeaterTab>
            </RepeaterTabsList>

            <TabsContent value="status" className="mt-0">
              <StatusTab
                stats={live.stats}
                running={running}
                refreshing={refreshing}
                onRefresh={refreshLive}
              />
            </TabsContent>
            <TabsContent value="neighbors" className="mt-0">
              <NeighborsTab
                neighbors={live.neighbors}
                peers={peers ?? []}
                running={running}
                busy={busy === "discover" ? "discover" : refreshing ? "refresh" : null}
                cooldown={cooldown}
                onRefresh={refreshLive}
                onDiscover={onDiscover}
                onSelect={selectPeer}
              />
            </TabsContent>
            <TabsContent value="access" className="mt-0">
              <AccessTab
                acl={live.acl}
                peers={peers ?? []}
                running={running}
                refreshing={refreshing}
                onRefresh={refreshLive}
              />
            </TabsContent>
            {/* Kept mounted, or switching tabs throws away an unsaved map; it reads SQLite, not the radio, so nothing is spammed. */}
            <TabsContent
              value="telemetry"
              forceMount
              className="mt-0 data-[state=inactive]:hidden"
            >
              <TelemetryMapEditor node={repeaterNode} />
            </TabsContent>
            <TabsContent value="settings" className="mt-0">
              <SettingsTab rep={rep} reload={reload} />
            </TabsContent>
          </Tabs>
        </>
      )}

      <PeerDetailSheet {...sheetProps} companions={companions} />
    </div>
  );
}

function TabToolbar({
  label,
  count,
  children,
}: {
  label: string;
  count?: number;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-2">
      <div className="flex items-baseline gap-3">
        <span className="label-overline hidden sm:inline">{label}</span>
        {count !== undefined && count > 0 && (
          <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
            {count}
          </span>
        )}
      </div>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}

function RefreshButton({
  spinning,
  disabled,
  onClick,
}: {
  spinning: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      disabled={disabled}
      className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
    >
      <RefreshCw className={cn("size-3", spinning && "animate-spin")} />
      refresh
    </Button>
  );
}

function Empty({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center gap-2 py-10 text-muted-foreground">
      <CircleDashed className="size-5" />
      <span className="font-mono text-[11px] uppercase tracking-[0.12em]">{text}</span>
    </div>
  );
}

function StatusTab({
  stats,
  running,
  refreshing,
  onRefresh,
}: {
  stats: RepeaterNodeStats | null;
  running: boolean;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  const dash = "—";
  const [confirmClear, setConfirmClear] = useState(false);
  const clearStats = async () => {
    setConfirmClear(false);
    try {
      await configApi.clearRepeaterStats();
      toast.success("Stats reset");
      onRefresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to reset stats");
    }
  };
  return (
    <div className="space-y-4">
      <TabToolbar label="live relay stats · auto-refreshes every 5s">
        {confirmClear ? (
          <div className="inline-flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em]">
            <span className="text-muted-foreground mr-1">Reset counters?</span>
            <Button variant="destructive" size="xs" onClick={clearStats} className="font-mono uppercase tracking-widest">
              yes
            </Button>
            <Button variant="ghost" size="xs" onClick={() => setConfirmClear(false)} className="font-mono uppercase tracking-widest">
              no
            </Button>
          </div>
        ) : (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setConfirmClear(true)}
            disabled={!running || !stats}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <RotateCcw className="size-3" />
            clear stats
          </Button>
        )}
        <RefreshButton spinning={refreshing} disabled={!running} onClick={onRefresh} />
      </TabToolbar>
      {!stats ? (
        <div className="panel">
          <Empty text={running ? "waiting for stats" : "node not running"} />
        </div>
      ) : (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-px bg-border border border-border">
          <StatTile
            label="Uptime"
            value={formatUptime(stats.uptimeSecs)}
            icon={<Clock className="size-3.5" />}
          />
          <StatTile
            label="Battery"
            value={stats.batteryMv != null ? formatBattery(stats.batteryMv) : dash}
            icon={<Battery className="size-3.5" />}
          />
          <StatTile
            label="Noise floor"
            value={stats.noiseFloor != null ? `${stats.noiseFloor}dB` : dash}
            icon={<Wifi className="size-3.5" />}
          />
          <StatTile
            label="Last SNR"
            value={stats.lastSnr != null ? `${stats.lastSnr.toFixed(1)}dB` : dash}
            icon={<Signal className="size-3.5" />}
          />
          <StatTile
            label="Last RSSI"
            value={stats.lastRssi != null ? `${stats.lastRssi}` : dash}
            icon={<Signal className="size-3.5" />}
          />
          <StatTile
            label="TX queue"
            value={`${stats.txQueueLen}`}
            icon={<Inbox className="size-3.5" />}
          />
          <StatTile label="Pkts recv" value={`${stats.packetsReceived}`} />
          <StatTile label="Pkts relayed" value={`${stats.packetsForwarded}`} accent />
          <StatTile label="Flood TX" value={`${stats.floodTx}`} />
          <StatTile label="Flood RX" value={`${stats.floodRx}`} />
          <StatTile label="Direct TX" value={`${stats.directTx}`} />
          <StatTile label="Direct RX" value={`${stats.directRx}`} />
          <StatTile label="TX air" value={formatUptime(stats.txAirSecs)} />
          <StatTile label="RX air" value={formatUptime(stats.rxAirSecs)} />
          <StatTile label="Flood dups" value={`${stats.floodDups}`} />
          <StatTile label="Direct dups" value={`${stats.directDups}`} />
          <StatTile
            label="Neighbours"
            value={`${stats.neighbors}`}
            icon={<Users className="size-3.5" />}
          />
        </div>
      )}
    </div>
  );
}

function NeighborsTab({
  neighbors,
  peers,
  running,
  busy,
  cooldown,
  onRefresh,
  onDiscover,
  onSelect,
}: {
  neighbors: RepeaterNodeNeighbor[];
  peers: PeerLike[];
  running: boolean;
  busy: "refresh" | "discover" | null;
  cooldown: number;
  onRefresh: () => void;
  onDiscover: () => void;
  onSelect: (pubkey: string) => void;
}) {
  const peerByKey = useMemo(() => {
    const m = new Map<string, PeerLike>();
    for (const p of peers) m.set(p.pubkey.toLowerCase(), p);
    return m;
  }, [peers]);

  return (
    <div className="space-y-3">
      <TabToolbar label="neighbors" count={neighbors.length}>
        <Button
          variant="outline"
          size="sm"
          onClick={onDiscover}
          disabled={!running || busy !== null || cooldown > 0}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          {busy === "discover" ? (
            <RefreshCw className="size-3 animate-spin" />
          ) : (
            <Network className="size-3" />
          )}
          {cooldown > 0 ? `discover ${cooldown}s` : "discover"}
        </Button>
        <RefreshButton
          spinning={busy === "refresh"}
          disabled={!running || busy !== null}
          onClick={onRefresh}
        />
      </TabToolbar>
      <div className="panel divide-y divide-border">
        {neighbors.length === 0 ? (
          <Empty
            text={
              !running
                ? "node not running"
                : busy === "discover"
                  ? "discovering…"
                  : "no repeater neighbours heard yet"
            }
          />
        ) : (
          neighbors.map((n) => {
            const peer = peerByKey.get(n.pubkey.toLowerCase());
            const name = n.name || peer?.name;
            return (
              <button
                type="button"
                key={n.pubkey}
                onClick={() => peer && onSelect(peer.pubkey)}
                disabled={!peer}
                className="w-full text-left px-4 py-2.5 flex items-center gap-3 hover:bg-muted/30 disabled:hover:bg-transparent"
              >
                <PeerAvatar name={name || n.pubkey.slice(0, 12)} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm truncate">
                      {name || <span className="text-muted-foreground italic">unknown</span>}
                    </span>
                    <PeerTypePill type={peer?.type || "REPEATER"} />
                  </div>
                  <code className="font-mono text-[10px] text-muted-foreground/70">
                    {n.pubkey.slice(0, 16)}…
                  </code>
                </div>
                <div className="flex flex-col items-end gap-0.5">
                  <SignalStrength snr={n.snr} size="md" />
                  <div className="font-mono text-[10px] text-muted-foreground/70 tabular-nums">
                    {formatSecsAgo(n.secsAgo)}
                  </div>
                </div>
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}

function AccessTab({
  acl,
  peers,
  running,
  refreshing,
  onRefresh,
}: {
  acl: RepeaterAclEntry[];
  peers: PeerLike[];
  running: boolean;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [confirmRevoke, setConfirmRevoke] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const aclOp = async (pubkey: string, fn: () => Promise<unknown>, ok: string) => {
    setBusyKey(pubkey);
    try {
      await fn();
      toast.success(ok);
      setConfirmRevoke(null);
      onRefresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed");
    } finally {
      setBusyKey(null);
    }
  };
  const revoke = (pubkey: string) =>
    aclOp(pubkey, () => configApi.revokeRepeaterAcl(pubkey), "Access revoked");
  const setPerm = (pubkey: string, perm: number) =>
    aclOp(pubkey, () => configApi.setRepeaterAcl(pubkey, perm), `Role set to ${roleLabel(perm)}`);

  const knownPrefixes = useMemo(
    () => new Set(acl.map((c) => c.pubkey.toLowerCase().slice(0, 12))),
    [acl],
  );

  const now = Math.floor(Date.now() / 1000);
  return (
    <div className="space-y-3">
      <TabToolbar label="access control" count={acl.length}>
        <Button
          variant="default"
          size="sm"
          onClick={() => setAddOpen(true)}
          disabled={!running}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          <Plus className="size-3" /> add
        </Button>
        <RefreshButton spinning={refreshing} disabled={!running} onClick={onRefresh} />
      </TabToolbar>
      <div className="panel divide-y divide-border">
        {acl.length === 0 ? (
          <Empty text={running ? "no admin clients yet" : "node not running"} />
        ) : (
          acl.map((c) => (
            <div key={c.pubkey} className="px-4 py-3 flex items-center gap-3">
              <PeerAvatar name={c.name || c.pubkey.slice(0, 12)} size="sm" />
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-mono text-sm">
                    {c.name || <span className="text-muted-foreground italic">unknown</span>}
                  </span>
                  <span
                    className={cn(
                      "inline-flex items-center gap-1 font-mono text-[9px] uppercase tracking-[0.12em] px-1.5 py-0.5 border",
                      rolePillClass(c.permission),
                    )}
                  >
                    <Shield className="size-2.5" />
                    {roleLabel(c.permission)}
                  </span>
                </div>
                <code className="font-mono text-[10px] text-muted-foreground/70 block truncate">
                  {c.pubkey.slice(0, 16)}…
                </code>
              </div>
              <div className="flex items-center gap-2 shrink-0">
                <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                  {c.lastSeen > 0 ? formatSecsAgo(Math.max(0, now - c.lastSeen)) : "—"}
                </span>
                <Select
                  value={String(c.permission & PERM_ROLE_MASK)}
                  onValueChange={(v) => setPerm(c.pubkey, parseInt(v, 10))}
                  disabled={busyKey !== null}
                >
                  <SelectTrigger className="rounded-none font-mono text-[10px] uppercase tracking-widest h-7 w-32 border-border bg-background">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="rounded-none font-mono text-xs">
                    <CurrentRoleItem perms={c.permission} />
                    {ROLE_OPTIONS.map((opt) => (
                      <SelectItem
                        key={opt.value}
                        value={opt.value}
                        className="rounded-none font-mono text-xs"
                      >
                        {opt.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {busyKey === c.pubkey ? (
                  <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
                ) : (
                  <InlineConfirm
                    confirming={confirmRevoke === c.pubkey}
                    onAskRemove={() => setConfirmRevoke(c.pubkey)}
                    onCancel={() => setConfirmRevoke(null)}
                    onConfirm={() => revoke(c.pubkey)}
                    iconOnly
                    ariaLabel={`revoke ${c.name || c.pubkey.slice(0, 12)}`}
                  />
                )}
              </div>
            </div>
          ))
        )}
      </div>
      <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/60">
        clients are added when they log in with the admin or guest password, or
        granted here; a granted peer logs in without a password at its role.
      </p>

      <AddAccessDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        peers={peers}
        knownPrefixes={knownPrefixes}
        onAdd={async (pubkey, perms) => {
          await setPerm(pubkey, perms);
          setAddOpen(false);
        }}
      />
    </div>
  );
}

function SettingsTab({ rep, reload }: { rep: ConfigRepeater; reload: () => void }) {
  const [saving, setSaving] = useState<"node" | "relay" | "admin" | null>(null);
  const [busyDelete, setBusyDelete] = useState(false);
  const [regionBusy, setRegionBusy] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmRegion, setConfirmRegion] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [lat, setLat] = useState("");
  const [lon, setLon] = useState("");
  const [advertInterval, setAdvertInterval] = useState("");
  const [floodAdvertInterval, setFloodAdvertInterval] = useState("");
  const [floodMax, setFloodMax] = useState("");
  const [floodMaxUnscoped, setFloodMaxUnscoped] = useState("");
  const [floodMaxAdvert, setFloodMaxAdvert] = useState("");
  const [loopDetect, setLoopDetect] = useState("off");
  const [defaultRegion, setDefaultRegion] = useState("");
  const [pathHashSize, setPathHashSize] = useState("");
  const [txDelay, setTxDelay] = useState("");
  const [directTxDelay, setDirectTxDelay] = useState("");
  const [rxDelay, setRxDelay] = useState("");
  const [multiAcks, setMultiAcks] = useState("");
  const [disableFwd, setDisableFwd] = useState(false);
  const [newRegion, setNewRegion] = useState("");
  const [ownerInfo, setOwnerInfo] = useState("");
  const [adminPw, setAdminPw] = useState({ value: "", dirty: false });
  const [guestPw, setGuestPw] = useState({ value: "", dirty: false });

  useEffect(() => {
    setName(rep.name);
    setLat(rep.latitude != null ? String(rep.latitude) : "");
    setLon(rep.longitude != null ? String(rep.longitude) : "");
    setAdvertInterval(fromSecs(rep.advertInterval, 60));
    setFloodAdvertInterval(fromSecs(rep.floodAdvertInterval, 3600));
    setFloodMax(rep.floodMax != null ? String(rep.floodMax) : "");
    setFloodMaxUnscoped(rep.floodMaxUnscoped != null ? String(rep.floodMaxUnscoped) : "");
    setFloodMaxAdvert(rep.floodMaxAdvert != null ? String(rep.floodMaxAdvert) : "");
    setLoopDetect(rep.loopDetect ?? "off");
    setDefaultRegion(rep.defaultRegion ?? "");
    setPathHashSize(rep.pathHashSize != null ? String(rep.pathHashSize) : "");
    setTxDelay(rep.txDelayFactor != null ? String(rep.txDelayFactor) : "");
    setDirectTxDelay(rep.directTxDelayFactor != null ? String(rep.directTxDelayFactor) : "");
    setRxDelay(rep.rxDelayBase != null ? String(rep.rxDelayBase) : "");
    setMultiAcks(rep.multiAcks != null ? String(rep.multiAcks) : "");
    setDisableFwd(rep.disableFwd ?? false);
    setNewRegion("");
    setOwnerInfo(rep.ownerInfo);
    setPrivateKey("");
    setAdminPw({ value: "", dirty: false });
    setGuestPw({ value: "", dirty: false });
  }, [rep]);

  const num = (s: string): number | null => {
    const t = s.trim();
    if (t === "") return null;
    const n = Number(t);
    return Number.isFinite(n) ? n : null;
  };

  // Advert intervals are stored in seconds but entered in the firmware's CLI units.
  const toSecs = (s: string, perUnit: number): number | null => {
    const n = num(s);
    return n === null ? null : n * perUnit;
  };
  const fromSecs = (v: number | null | undefined, perUnit: number): string =>
    v != null ? String(v / perUnit) : "";

  const section = async (
    which: "node" | "relay" | "admin",
    fn: () => Promise<unknown>,
    ok: string,
  ) => {
    setSaving(which);
    try {
      await fn();
      toast.success(ok);
      reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(null);
    }
  };

  const saveNode = () =>
    section(
      "node",
      () =>
        configApi.updateRepeaterNode({
          name: name.trim(),
          ...(privateKey.trim() !== "" ? { privateKey: privateKey.trim() } : {}),
          latitude: num(lat),
          longitude: num(lon),
        }),
      "Node saved",
    );

  const saveRelay = () =>
    section(
      "relay",
      () =>
        configApi.updateRepeaterRelay({
          disableFwd,
          floodMax: num(floodMax),
          floodMaxUnscoped: num(floodMaxUnscoped),
          floodMaxAdvert: num(floodMaxAdvert),
          loopDetect,
          pathHashSize: num(pathHashSize),
          txDelayFactor: num(txDelay),
          directTxDelayFactor: num(directTxDelay),
          rxDelayBase: num(rxDelay),
          multiAcks: num(multiAcks),
          defaultRegion,
          advertInterval: toSecs(advertInterval, 60),
          floodAdvertInterval: toSecs(floodAdvertInterval, 3600),
        }),
      "Relay policy saved",
    );

  const saveAdmin = () =>
    section(
      "admin",
      () =>
        configApi.updateRepeaterAdmin({
          ownerInfo,
          ...(adminPw.dirty ? { adminPassword: adminPw.value } : {}),
          ...(guestPw.dirty ? { guestPassword: guestPw.value } : {}),
        }),
      "Owner & access saved",
    );

  const onDelete = async () => {
    setBusyDelete(true);
    try {
      await configApi.deleteRepeater();
      toast.success("Repeater removed");
      setConfirmDelete(false);
      reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to remove repeater");
    } finally {
      setBusyDelete(false);
    }
  };

  const regionOp = async (rn: string, fn: () => Promise<unknown>, ok?: string) => {
    setRegionBusy(rn);
    try {
      await fn();
      if (ok) toast.success(ok);
      reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Region update failed");
    } finally {
      setRegionBusy(null);
    }
  };

  const regions = rep.regions ?? [];
  const addRegion = () => {
    const rn = newRegion.trim();
    if (rn === "" || regions.some((r) => r.name === rn)) return;
    regionOp(rn, () => configApi.addRepeaterRegion(rn, false), "Region added");
  };

  return (
    <div className="space-y-8">
      {/* Identity + position */}
      <section className="panel">
        <SectionTitle eyebrow="identity" title="Node" />
        <div className="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
          <TextField label="Name" value={name} onChange={setName} placeholder="repeater" />
          <TextField
            label="Public key"
            value={rep.pubkey}
            onChange={() => {}}
            disabled
            hint="derived from the identity seed (read-only)"
          />
          <TextField label="Latitude" value={lat} onChange={setLat} placeholder="-27.47" />
          <TextField label="Longitude" value={lon} onChange={setLon} placeholder="153.02" />
          <PositionPicker
            className="sm:col-span-2"
            lat={parseFloat(lat)}
            lon={parseFloat(lon)}
            onPick={(la, lo) => {
              setLat(round6(la));
              setLon(round6(lo));
            }}
          />
          <div className="sm:col-span-2">
            <TextField
              label="Rotate private key"
              type="password"
              value={privateKey}
              onChange={setPrivateKey}
              placeholder="blank = keep current identity"
              hint={
                <>
                  changing this <strong>rotates the identity</strong> — the
                  current pubkey is abandoned and the mesh relearns a path
                  (use to resolve a key clash). Paste a MeshCore private key
                  (128 hex) or a 32-hex seed
                </>
              }
            />
          </div>
          <div className="sm:col-span-2">
            <SectionSave busy={saving === "node"} disabled={saving !== null} onClick={saveNode} />
          </div>
        </div>
      </section>

      {/* Relay policy */}
      <section className="panel">
        <SectionTitle eyebrow="routing" title="Relay policy" />
        <div className="space-y-4 p-4">
          <SwitchRow
            label="Disable forwarding"
            hint="stop relaying entirely (still adverts + tracks neighbours)"
            checked={disableFwd}
            onChange={setDisableFwd}
          />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <TextField
              label="Flood max hops"
              value={floodMax}
              onChange={setFloodMax}
              placeholder="64"
              hint="default 64 · relay flood up to N hops"
            />
            <TextField
              label="Plain flood max hops"
              value={floodMaxUnscoped}
              onChange={setFloodMaxUnscoped}
              placeholder="64"
              hint="default 64 · extra cap for unscoped floods only (0 = never)"
            />
            <TextField
              label="Advert max hops"
              value={floodMaxAdvert}
              onChange={setFloodMaxAdvert}
              placeholder="8"
              hint="default 8 · lower cap for adverts"
            />
            <SelectField
              label="Loop detect"
              value={loopDetect}
              options={LOOP_LEVELS.map((l) => ({ value: l, label: l }))}
              onChange={setLoopDetect}
            />
            <SelectField
              label="Path hash size"
              value={pathHashSize}
              options={[{ value: "", label: "Inherit from Settings" }, ...PATH_HASH_SIZE_OPTIONS]}
              onChange={setPathHashSize}
              hint="width of each hop hash in our flood packets"
            />
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <TextField
              label="Zero-hop advert interval (minutes)"
              value={advertInterval}
              onChange={setAdvertInterval}
              placeholder="0"
              hint="local, direct neighbours only · 0 = off (default) · else 60-240"
            />
            <TextField
              label="Flood advert interval (hours)"
              value={floodAdvertInterval}
              onChange={setFloodAdvertInterval}
              placeholder="47"
              hint="mesh-wide · 0 = off · else 3-168 · default 47"
            />
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <TextField
              label="TX delay factor"
              value={txDelay}
              onChange={setTxDelay}
              placeholder="0.5"
              hint="flood relay jitter · 0-2 · firmware txdelay"
            />
            <TextField
              label="Direct TX delay factor"
              value={directTxDelay}
              onChange={setDirectTxDelay}
              placeholder="0.3"
              hint="direct relay jitter · 0-2 · firmware direct.txdelay"
            />
            <TextField
              label="RX delay base"
              value={rxDelay}
              onChange={setRxDelay}
              placeholder="0"
              hint="hold weak floods before relaying · 0 = off · 0-20 · firmware rxdelay"
            />
            <TextField
              label="Multi ACKs"
              value={multiAcks}
              onChange={setMultiAcks}
              placeholder="0"
              hint="extra copies of relayed ACKs · firmware multi.acks"
            />
            <SelectField
              label="Advert scope"
              value={defaultRegion}
              options={[
                { value: "", label: "(unscoped)" },
                ...regions
                  .filter((rg) => rg.name !== "*" && !rg.denyFlood)
                  .map((rg) => ({ value: rg.name, label: rg.name })),
              ]}
              onChange={setDefaultRegion}
              hint="region our flood adverts are scoped to"
            />
          </div>
          <SectionSave busy={saving === "relay"} disabled={saving !== null} onClick={saveRelay} />
        </div>
      </section>

      {/* Regions */}
      <section className="panel">
        <SectionTitle eyebrow="scoping" title="Regions" />
        <div className="space-y-4 p-4">
          <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
            Transport scopes this repeater relays: it re-floods scoped packets
            whose region matches one of these (the key is derived from the
            region name). The <span className="text-foreground">*</span> scope
            is plain unscoped flood — remove it to stop relaying unscoped
            traffic, add it back to resume; deny-flood toggles it without
            removing.
          </p>
          {regions.length > 0 && (
            <div className="divide-y divide-border border border-border">
              {regions.map((rg) => (
                <div key={rg.name} className="flex items-center justify-between gap-3 px-3 py-2">
                  <span className="truncate font-mono text-sm">{rg.name}</span>
                  <div className="flex items-center gap-4">
                    {regionBusy === rg.name && (
                      <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
                    )}
                    <label className="flex cursor-pointer items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                      deny flood
                      <Switch
                        checked={rg.denyFlood}
                        disabled={regionBusy !== null}
                        onCheckedChange={() =>
                          regionOp(rg.name, () =>
                            configApi.setRepeaterRegionFlood(rg.name, !rg.denyFlood),
                          )
                        }
                      />
                    </label>
                    <InlineConfirm
                      confirming={confirmRegion === rg.name}
                      onAskRemove={() => setConfirmRegion(rg.name)}
                      onCancel={() => setConfirmRegion(null)}
                      onConfirm={() => {
                        regionOp(
                          rg.name,
                          () => configApi.removeRepeaterRegion(rg.name),
                          "Region removed",
                        );
                        setConfirmRegion(null);
                      }}
                      iconOnly
                      ariaLabel={`remove region ${rg.name}`}
                    />
                  </div>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-end gap-2">
            <div className="flex-1">
              <TextField
                label="Add region"
                value={newRegion}
                onChange={setNewRegion}
                placeholder="region name"
                hint="the transport key derives from this name"
              />
            </div>
            <Button
              size="sm"
              variant="outline"
              onClick={addRegion}
              disabled={newRegion.trim() === "" || regionBusy !== null}
              className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
            >
              {regionBusy === newRegion.trim() ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <Plus className="size-3.5" />
              )}
              add
            </Button>
          </div>
        </div>
      </section>

      {/* Owner + admin */}
      <section className="panel">
        <SectionTitle eyebrow="admin" title="Owner & access" />
        <div className="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2">
          <Field label="Owner info" hint="multi-line · the CLI shows | between lines">
            <Textarea
              value={ownerInfo}
              onChange={(e) => setOwnerInfo(e.target.value)}
              rows={3}
              placeholder="contact / location"
              className="rounded-none font-mono text-base md:text-sm border-border bg-background resize-y"
            />
          </Field>
          <div />
          <PasswordField
            label="Admin password"
            isSet={rep.adminPasswordSet}
            state={adminPw}
            onState={setAdminPw}
          />
          <PasswordField
            label="Guest password"
            isSet={rep.guestPasswordSet}
            state={guestPw}
            onState={setGuestPw}
          />
          <div className="sm:col-span-2">
            <SectionSave busy={saving === "admin"} disabled={saving !== null} onClick={saveAdmin} />
          </div>
        </div>
      </section>

      {/* Danger zone */}
      <section className="panel">
        <SectionTitle eyebrow="danger" title="Remove repeater" />
        <div className="flex items-center justify-between gap-3 p-4">
          <p className="font-mono text-[11px] text-muted-foreground">
            Stops relaying and deletes the repeater identity and settings.
          </p>
          {busyDelete ? (
            <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
          ) : (
            <InlineConfirm
              confirming={confirmDelete}
              onAskRemove={() => setConfirmDelete(true)}
              onCancel={() => setConfirmDelete(false)}
              onConfirm={onDelete}
              triggerLabel="remove repeater"
            />
          )}
        </div>
      </section>
    </div>
  );
}

function SectionSave({
  busy,
  disabled,
  onClick,
}: {
  busy: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <div className="flex justify-end">
      <Button
        size="sm"
        onClick={onClick}
        disabled={disabled}
        className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
      >
        {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
        save
      </Button>
    </div>
  );
}

type PwState = { value: string; dirty: boolean };

// Empty + untouched = keep; typed = set; explicit clear = remove.
function PasswordField({
  label,
  isSet,
  state,
  onState,
}: {
  label: string;
  isSet: boolean;
  state: PwState;
  onState: (s: PwState) => void;
}) {
  const willClear = state.dirty && state.value === "";
  const hint = willClear
    ? "will be removed on save"
    : isSet
      ? "leave blank to keep the current password"
      : "optional";
  return (
    <Field label={label} hint={hint}>
      <div className="flex items-center gap-2">
        <Input
          type="password"
          value={state.value}
          onChange={(e) => onState({ value: e.target.value, dirty: true })}
          placeholder={isSet ? "•••••• set" : "none"}
          className="h-9 font-mono text-base md:text-sm rounded-none border-border bg-background"
        />
        {isSet && !willClear && (
          <button
            type="button"
            onClick={() => onState({ value: "", dirty: true })}
            className="shrink-0 font-mono text-[10px] uppercase tracking-widest text-muted-foreground hover:text-destructive"
          >
            clear
          </button>
        )}
      </div>
    </Field>
  );
}

function CreateCard({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [adminPassword, setAdminPassword] = useState("");
  const [saving, setSaving] = useState(false);

  const onCreate = async () => {
    setSaving(true);
    try {
      await configApi.createRepeater({
        name: name.trim(),
        adminPassword,
        ...(privateKey.trim() !== "" ? { privateKey: privateKey.trim() } : {}),
      });
      toast.success("Repeater created");
      onCreated();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="panel">
      <SectionTitle eyebrow="setup" title="No repeater configured" />
      <div className="space-y-4 p-4">
        <p className="font-mono text-[11px] leading-relaxed text-muted-foreground">
          Run this node as a MeshCore repeater: it relays flood and direct
          packets across the mesh, advertises itself as a REPEATER, and tracks
          its RF neighbours.
        </p>
        <div className="max-w-md space-y-4">
          <TextField
            label="Name"
            value={name}
            onChange={setName}
            placeholder="repeater"
            hint="shown on the mesh and in adverts"
          />
          <TextField
            label="Admin password"
            type="password"
            value={adminPassword}
            onChange={setAdminPassword}
            placeholder="required"
            hint="what you log in with to administer this repeater, over the air. Max 15 characters, as the firmware stores. It cannot be blank: a blank one would let any node in range log in as admin"
          />
          <TextField
            label="Private key"
            type="password"
            value={privateKey}
            onChange={setPrivateKey}
            placeholder="blank = generate a new identity"
            hint={
              <>
                the node identity — paste a MeshCore private key (128 hex, from{" "}
                <code>get prv.key</code>) to import an existing repeater, or a
                32-hex seed. Blank generates a new one; for a vanity pubkey use{" "}
                <a
                  href="https://gessaman.com/mc-keygen/"
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary underline underline-offset-2 hover:text-primary/80"
                >
                  mc-keygen
                </a>
              </>
            }
          />
        </div>
        <Button
          size="sm"
          onClick={onCreate}
          disabled={saving || name.trim() === "" || adminPassword.trim() === ""}
          className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {saving ? <Loader2 className="size-3.5 animate-spin" /> : null}
          create repeater
        </Button>
      </div>
    </section>
  );
}
