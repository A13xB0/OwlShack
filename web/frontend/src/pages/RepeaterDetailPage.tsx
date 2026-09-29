import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  Battery,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  Copy,
  Gauge,
  Inbox,
  Info,
  KeyRound,
  LogOut,
  MapPin,
  MoreVertical,
  Network,
  Plus,
  RefreshCw,
  Route,
  Send,
  Settings as SettingsIcon,
  Shield,
  Signal,
  Terminal,
  Trash2,
  Users,
  Wifi,
  Zap,
} from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PathDialog, type PathInfo } from "@/components/PathDialog";
import { SignalStrength } from "@/components/SignalStrength";
import { TelemetryPanel } from "@/components/TelemetryPanel";
import { SeriesPanel } from "@/components/SeriesPanel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { PositionPicker } from "@/components/PositionPicker";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Tabs,
  TabsContent,
} from "@/components/ui/tabs";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  formatBattery,
  formatSecsAgo,
  formatUptime,
  truncateMid,
} from "@/lib/format";
import { cn } from "@/lib/utils";
import { ErrEventBadges } from "@/components/ErrEventBadges";
import {
  MonitoringSettings,
  type MonitorMetadata,
} from "@/components/MonitoringSettings";
import { cliCommandsFor, cliConfigKeysFor, type CliRole } from "@/lib/cliCatalog";
import {
  AddAccessDialog,
  AlertBitToggle,
  PERM_ALERTS_HI,
  PERM_ALERTS_LO,
  PERM_ALERTS_MASK,
  PERM_ROLE_MASK,
  ROLE_OPTIONS,
  CurrentRoleItem,
  RepeaterTab,
  RepeaterTabsList,
  StatTile,
  roleLabel,
  rolePillClass,
} from "@/components/RepeaterUI";
import { apiErrorMessage } from "@/lib/apiError";

interface Contact {
  peerPubkey: string;
  name: string;
  type: string;
  addedAt: string;
  metadata?: MonitorMetadata;
}

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lat: number;
  lon: number;
  lastSeen: string;
  snr: number | null;
  rssi: number | null;
}

interface Session {
  loggedIn?: boolean;
  pubkeyHex?: string;
  isAdmin?: boolean;
  permissions?: number; // ACL byte from the login reply (role in the low 2 bits)
  role?: string; // rooms: admin | read-write | read-only
  loggedInAt?: string;
}


interface Status {
  batteryMv: number;
  queueLen: number;
  noiseFloor: number;
  lastRssi: number;
  packetsRecv: number;
  packetsSent: number;
  txAirSecs: number;
  rxAirSecs: number;
  uptimeSecs: number;
  floodTx: number;
  directTx: number;
  floodRx: number;
  directRx: number;
  errEvents: number;
  lastSnr: number;
  directDups: number;
  floodDups: number;
  recvErrors: number;
  chanUtil: number;
  posted?: number; // room servers only
  postPushes?: number;
}

type TabKey =
  | "status"
  | "monitoring"
  | "terminal"
  | "neighbors"
  | "owner"
  | "telemetry"
  | "access"
  | "settings";

interface CliEntry {
  kind: "input" | "output" | "error";
  text: string;
  ts: number;
}

interface NeighborEntry {
  pubkeyPrefix: string;
  secsAgo: number;
  snr: number;
  name?: string;
  type?: string;
}

export type AdminNodeKind = "repeater" | "sensor" | "room";

// kind only gates what that role's firmware answers: a sensor has no GET_STATUS, GET_NEIGHBOURS, owner info, routing settings or guest password.
export function RepeaterDetailPage({ kind = "repeater" }: { kind?: AdminNodeKind }) {
  const { ref, pubkey } = useParams<{ ref: string; pubkey: string }>();
  const decodedName = ref ?? "";
  const decodedPubkey = pubkey ? decodeURIComponent(pubkey) : "";
  const navigate = useNavigate();
  const isSensor = kind === "sensor";
  const isRoom = kind === "room";

  // The /repeaters/ path is type-agnostic; rooms have their own login, status and keep-alive under /rooms/.
  const apiBase = `/api/companions/${encodeURIComponent(decodedName)}/repeaters/${encodeURIComponent(decodedPubkey)}`;
  const roomApiBase = `/api/companions/${encodeURIComponent(decodedName)}/rooms/${encodeURIComponent(decodedPubkey)}`;
  // Shares the key chat's RoomJoinBar uses, so a password saved here joins the room in chat too.
  const passwordKey = isRoom ? "roomPassword" : "repeaterPassword";
  const savedPassword = (m?: MonitorMetadata) =>
    (m as Record<string, unknown> | undefined)?.[passwordKey] as string | undefined;

  const [contact, setContact] = useState<Contact | null>(null);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [companions, setCompanions] = useState<{ name: string; pubkey: string }[]>(
    [],
  );
  const [session, setSession] = useState<Session | null>(null);
  const [pathInfo, setPathInfo] = useState<PathInfo | null>(null);
  const [bootstrapping, setBootstrapping] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [tab, setTab] = useState<TabKey>(isSensor ? "telemetry" : "status");
  const [password, setPassword] = useState("");
  const [saveLogin, setSaveLogin] = useState(false);
  const [loggingIn, setLoggingIn] = useState(false);
  const [pathDialogOpen, setPathDialogOpen] = useState(false);

  const peerName = contact?.name || (isSensor ? "Sensor" : isRoom ? "Room" : "Repeater");
  const peerType = contact?.type || (isSensor ? "SENSOR" : isRoom ? "ROOM" : "REPEATER");
  const isAdmin = !!session?.isAdmin;
  const loggedIn =
    session?.loggedIn === true ||
    (session?.loggedIn !== false && !!session?.pubkeyHex);

  const refreshSession = useCallback(async () => {
    try {
      const r = await fetch(`${apiBase}/session`);
      if (!r.ok) throw new Error("session");
      const data: Session = await r.json();
      setSession(data);
    } catch {
      setSession({ loggedIn: false });
    }
  }, [apiBase]);

  const refreshPath = useCallback(async () => {
    try {
      const r = await fetch(`${apiBase}/path`);
      if (!r.ok) throw new Error("path");
      const data: PathInfo = await r.json();
      setPathInfo(data);
    } catch {
      setPathInfo(null);
    }
  }, [apiBase]);

  const bootstrap = useCallback(async () => {
    setBootstrapping(true);
    setError(null);
    try {
      const [contactsRes, peersRes, companionsRes] = await Promise.all([
        fetch(
          `/api/companions/${encodeURIComponent(decodedName)}/contacts`,
        ),
        fetch(`/api/peers`),
        fetch(`/api/companions`),
      ]);
      const contacts = contactsRes.ok
        ? ((await contactsRes.json()) as Contact[])
        : [];
      const allPeers = peersRes.ok ? ((await peersRes.json()) as Peer[]) : [];
      setPeers(allPeers || []);
      if (companionsRes.ok) {
        const list = (await companionsRes.json()) as {
          name: string;
          pubkey: string;
        }[];
        setCompanions(list || []);
      }
      const found =
        (contacts || []).find(
          (c) => c.peerPubkey?.toLowerCase() === decodedPubkey.toLowerCase(),
        ) || null;
      setContact(found);
      if (savedPassword(found?.metadata)) {
        setPassword(savedPassword(found?.metadata) ?? "");
        setSaveLogin(true);
      }
      await Promise.allSettled([refreshSession(), refreshPath()]);
    } catch {
      setError("Failed to load repeater context");
    } finally {
      setBootstrapping(false);
    }
  }, [decodedName, decodedPubkey, refreshSession, refreshPath]);

  useEffect(() => {
    bootstrap();
  }, [bootstrap]);

  const keepAlive = useCallback(async () => {
    const r = await fetch(`${roomApiBase}/keepalive`, { method: "POST" });
    if (!r.ok) throw new Error(await apiErrorMessage(r, "Keep-alive failed"));
  }, [roomApiBase]);

  const handleLogin = useCallback(async () => {
    setLoggingIn(true);
    try {
      const r = await fetch(`${isRoom ? roomApiBase : apiBase}/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      });
      if (!r.ok) throw new Error(await apiErrorMessage(r, "Login failed"));
      toast.success("Logged in");
      await Promise.allSettled([refreshSession(), refreshPath()]);

      if (saveLogin) {
        await fetch(
          `/api/companions/${encodeURIComponent(decodedName)}/contacts/${encodeURIComponent(decodedPubkey)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              isRepeater: kind === "repeater",
              [passwordKey]: password,
            }),
          },
        ).catch(() => {});
        setContact((c) =>
          c
            ? {
                ...c,
                metadata: {
                  ...(c.metadata || {}),
                  [passwordKey]: password,
                  isRepeater: kind === "repeater",
                },
              }
            : c,
        );
      } else if (savedPassword(contact?.metadata)) {
        // unchecked but a saved password exists — clear it
        await fetch(
          `/api/companions/${encodeURIComponent(decodedName)}/contacts/${encodeURIComponent(decodedPubkey)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              isRepeater: kind === "repeater",
              [passwordKey]: "",
            }),
          },
        ).catch(() => {});
        setContact((c) =>
          c
            ? {
                ...c,
                metadata: {
                  ...(c.metadata || {}),
                  [passwordKey]: "",
                  isRepeater: kind === "repeater",
                },
              }
            : c,
        );
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Login failed");
    } finally {
      setLoggingIn(false);
    }
  }, [
    apiBase,
    password,
    refreshSession,
    refreshPath,
    saveLogin,
    decodedName,
    decodedPubkey,
    contact,
    isRoom,
    roomApiBase,
    kind,
    passwordKey,
  ]);

  const handleLogout = useCallback(async () => {
    try {
      await fetch(`${apiBase}/session`, { method: "DELETE" });
      toast.success("Logged out");
      setSession({ loggedIn: false });
    } catch {
      toast.error("Logout failed");
    }
  }, [apiBase]);

  const handleMonitorSaved = useCallback((meta: MonitorMetadata) => {
    setContact((c) => (c ? { ...c, metadata: { ...(c.metadata || {}), ...meta } } : c));
  }, []);

  const handleResetPath = useCallback(async () => {
    try {
      const r = await fetch(`${apiBase}/path`, { method: "DELETE" });
      if (!r.ok) throw new Error("reset");
      toast.success("Path reset to flood");
      await refreshPath();
    } catch {
      toast.error("Resetting the path failed");
    }
  }, [apiBase, refreshPath]);

  const sendCli = useCallback(
    async (command: string): Promise<string> => {
      const r = await fetch(`${apiBase}/cli`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ command }),
      });
      if (!r.ok) throw new Error(await apiErrorMessage(r, "CLI error"));
      const data: { response: string } = await r.json();
      refreshPath();
      return data.response;
    },
    [apiBase, refreshPath],
  );

  if (bootstrapping) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-12 w-2/3" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle className="font-mono uppercase tracking-widest">
          Error
        </AlertTitle>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    );
  }

  return (
    <div className="space-y-4">
      <PageHeader
        title={peerName.toUpperCase()}
        meta={
          <span className="font-mono text-xs text-muted-foreground tabular-nums">
            <code>{truncateMid(decodedPubkey, 6, 6)}</code>
          </span>
        }
        actions={
          <>
            <HeaderNavChip
              to={`/companions/${encodeURIComponent(decodedName)}/${kind === "repeater" ? "repeaters" : `contacts/${encodeURIComponent(decodedPubkey)}`}`}
              label={kind === "repeater" ? "repeaters" : "contact"}
              short={kind === "repeater" ? "rptrs" : "contact"}
            />
            {!isSensor && (
              <HeaderNavChip
                to={`/companions/${encodeURIComponent(decodedName)}`}
                label="messages"
                short="msgs"
              />
            )}
            <PathBadge info={pathInfo} />
            {loggedIn && (
              <span
                className={cn(
                  "inline-flex items-center gap-1.5 px-2 py-0.5 border font-mono text-[10px] uppercase tracking-[0.12em]",
                  isAdmin
                    ? "border-warning/40 text-warning bg-warning/5"
                    : "border-success/40 text-success bg-success/5",
                )}
              >
                <Shield className="size-3" />
                {session?.role ?? (session?.permissions != null ? roleLabel(session.permissions).toLowerCase() : isAdmin ? "admin" : "guest")}
              </span>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                {/* icon-xs matches the chips beside it; its before: hit area keeps the 40px touch target. */}
                <Button
                  variant="outline"
                  size="icon-xs"
                  className="rounded-none"
                >
                  <MoreVertical className="size-3.5" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="rounded-sm">
                <DropdownMenuItem
                  onClick={() => setPathDialogOpen(true)}
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  <Route className="size-3.5" /> Edit path
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={handleResetPath}
                  disabled={!pathInfo?.hasPath}
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  <RefreshCw className="size-3.5" /> Reset path
                </DropdownMenuItem>
                {loggedIn && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      onClick={handleLogout}
                      className="font-mono text-xs uppercase tracking-[0.08em]"
                    >
                      <LogOut className="size-3.5" /> Logout
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />

      {/* Identity panel */}
      <section className="panel p-4 flex items-center gap-4">
        <PeerAvatar name={peerName} size="lg" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span
              className="min-w-0 max-w-full truncate font-mono text-base font-semibold uppercase tracking-[0.06em]"
              title={peerName}
            >
              {peerName}
            </span>
            <span className="shrink-0 font-mono text-[10px] uppercase tracking-widest px-1.5 py-0.5 border border-border text-muted-foreground">
              {peerType}
            </span>
          </div>
          <code className="block font-mono text-xs text-muted-foreground truncate" title={decodedPubkey}>
            {truncateMid(decodedPubkey, 10, 8)}
          </code>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            navigator.clipboard.writeText(decodedPubkey);
            toast.success("Pubkey copied");
          }}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          <Copy className="size-3" /> copy
        </Button>
      </section>

      {!loggedIn ? (
        <LoginCard
          password={password}
          onPasswordChange={setPassword}
          loggingIn={loggingIn}
          onLogin={handleLogin}
          saveLogin={saveLogin}
          onSaveLoginChange={setSaveLogin}
          hasSavedPassword={!!savedPassword(contact?.metadata)}
          kind={kind}
        />
      ) : (
        <Tabs
          value={tab}
          onValueChange={(v) => setTab(v as TabKey)}
          className="space-y-4"
        >
          <RepeaterTabsList>
            {!isSensor && (
              <RepeaterTab value="status" icon={<Signal className="size-3" />}>
                Status
              </RepeaterTab>
            )}
            <RepeaterTab value="monitoring" icon={<Gauge className="size-3" />}>
              Monitoring
            </RepeaterTab>
            {isAdmin && (
              <RepeaterTab value="terminal" icon={<Terminal className="size-3" />}>
                Terminal
              </RepeaterTab>
            )}
            {kind === "repeater" && (
              <RepeaterTab value="neighbors" icon={<Users className="size-3" />}>
                Neighbors
              </RepeaterTab>
            )}
            {!isAdmin && kind === "repeater" && (
              <RepeaterTab value="owner" icon={<Info className="size-3" />}>
                Owner
              </RepeaterTab>
            )}
            <RepeaterTab value="telemetry" icon={<Activity className="size-3" />}>
              Telemetry
            </RepeaterTab>
            {isAdmin && (
              <RepeaterTab value="access" icon={<Shield className="size-3" />}>
                Access
              </RepeaterTab>
            )}
            {isAdmin && (
              <RepeaterTab
                value="settings"
                icon={<SettingsIcon className="size-3" />}
              >
                Settings
              </RepeaterTab>
            )}
          </RepeaterTabsList>

          <TabsContent
            value="status"
            className="mt-0 data-[state=inactive]:hidden"
            forceMount
          >
            {!isSensor && (
              <StatusTab
                apiBase={apiBase}
                statusUrl={isRoom ? `${roomApiBase}/status` : `${apiBase}/status`}
                kind={kind}
                onKeepAlive={isRoom ? keepAlive : undefined}
                active={tab === "status"}
                onPathMayChange={refreshPath}
              />
            )}
          </TabsContent>
          <TabsContent value="monitoring" className="mt-0">
            <MonitoringSettings
              companionRef={decodedName}
              pubkey={decodedPubkey}
              kind={isSensor ? "companion" : "repeater"}
              metadata={contact?.metadata}
              onSaved={handleMonitorSaved}
            />
          </TabsContent>
          {isAdmin && (
            <TabsContent value="terminal" className="mt-0">
              <TerminalTab sendCli={sendCli} kind={kind} />
            </TabsContent>
          )}
          <TabsContent
            value="neighbors"
            className="mt-0 data-[state=inactive]:hidden"
            forceMount
          >
            <NeighborsTab
              apiBase={apiBase}
              sendCli={sendCli}
              isAdmin={isAdmin}
              peers={peers}
              active={tab === "neighbors"}
              onPathMayChange={refreshPath}
            />
          </TabsContent>
          {!isAdmin && (
            <TabsContent
              value="owner"
              className="mt-0 data-[state=inactive]:hidden"
              forceMount
            >
              <OwnerTab apiBase={apiBase} active={tab === "owner"} />
            </TabsContent>
          )}
          <TabsContent
            value="telemetry"
            className="mt-0 data-[state=inactive]:hidden"
            forceMount
          >
            <div className="space-y-8">
              <TelemetryPanel apiBase={apiBase} autoFetch={tab === "telemetry"} />
              {isSensor && <SeriesPanel apiBase={apiBase} />}
            </div>
          </TabsContent>
          {isAdmin && (
            <TabsContent
              value="access"
              className="mt-0 data-[state=inactive]:hidden"
              forceMount
            >
              <AccessTab
                apiBase={apiBase}
                peers={peers}
                companions={companions}
                active={tab === "access"}
                kind={kind}
              />
            </TabsContent>
          )}
          {isAdmin && (
            <TabsContent value="settings" className="mt-0">
              <SettingsTab
              kind={kind}
                pubkey={decodedPubkey}
                peerName={peerName}
                sendCli={sendCli}
                onReboot={() => navigate(0)}
                onLocalNameUpdate={(n) =>
                  setContact((c) => (c ? { ...c, name: n } : c))
                }
              />
            </TabsContent>
          )}
        </Tabs>
      )}

      <PathDialog
        open={pathDialogOpen}
        onOpenChange={setPathDialogOpen}
        companion={decodedName}
        pubkey={decodedPubkey}
        name={contact?.name || "this node"}
        onChanged={refreshPath}
      />
    </div>
  );
}

// The label shortens below sm so the chips plus the overflow menu stay on one line on a phone.
function HeaderNavChip({
  to,
  label,
  short,
}: {
  to: string;
  label: string;
  short: string;
}) {
  return (
    <Link
      to={to}
      className="relative before:absolute before:inset-x-0 before:-inset-y-2 before:content-[''] sm:before:hidden inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary px-2 py-1 border border-border"
    >
      <ArrowLeft className="size-3" />
      <span className="sm:hidden">{short}</span>
      <span className="hidden sm:inline">{label}</span>
    </Link>
  );
}

function PathBadge({ info }: { info: PathInfo | null }) {
  if (!info)
    return (
      <span className="inline-flex items-center gap-1.5 px-2 py-0.5 border border-border font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        <Route className="size-3" /> path · ?
      </span>
    );
  if (!info.hasPath)
    return (
      <span className="inline-flex items-center gap-1.5 px-2 py-0.5 border border-border font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        <Route className="size-3" /> flood
      </span>
    );
  if (info.directNeighbor)
    return (
      <span className="inline-flex items-center gap-1.5 px-2 py-0.5 border border-success/40 text-success bg-success/5 font-mono text-[10px] uppercase tracking-[0.12em]">
        <Route className="size-3" /> direct
      </span>
    );
  return (
    <span className="inline-flex items-center gap-1.5 px-2 py-0.5 border border-primary/40 text-primary bg-primary/5 font-mono text-[10px] uppercase tracking-[0.12em]">
      <Route className="size-3" /> {info.hops} hop
      {info.hops === 1 ? "" : "s"}
    </span>
  );
}

function LoginCard({
  password,
  onPasswordChange,
  loggingIn,
  onLogin,
  saveLogin,
  onSaveLoginChange,
  hasSavedPassword,
  kind,
}: {
  password: string;
  onPasswordChange: (v: string) => void;
  loggingIn: boolean;
  onLogin: () => void;
  saveLogin: boolean;
  onSaveLoginChange: (v: boolean) => void;
  hasSavedPassword: boolean;
  kind: AdminNodeKind;
}) {
  return (
    <section className="panel max-w-md mx-auto p-6 space-y-4">
      <div className="flex items-center gap-2 pb-2 border-b border-border">
        <KeyRound className="size-4 text-primary" />
        <span className="label-overline">authenticate</span>
      </div>
      <p className="text-xs text-muted-foreground">
        {kind === "sensor"
          ? "Sensors have no guest password: the admin password grants full access, and a blank password re-authenticates a node already granted access with setperm."
          : kind === "room"
            ? "The admin password unlocks the configuration panel; the room password gives read / write; if the room allows it, anything else logs in read-only."
            : "Login to manage this repeater. Admin login unlocks the configuration panel."}
      </p>
      <div className="space-y-2">
        <Label
          htmlFor="rp-password"
          className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
        >
          password
        </Label>
        <Input
          id="rp-password"
          type="password"
          value={password}
          onChange={(e: ChangeEvent<HTMLInputElement>) =>
            onPasswordChange(e.target.value)
          }
          onKeyDown={(e) => {
            if (e.key === "Enter") onLogin();
          }}
          autoComplete="current-password"
          placeholder={hasSavedPassword ? "•••••• (saved)" : "leave blank if none"}
          className="rounded-none font-mono border-border"
        />
      </div>
      <label className="flex items-center justify-between gap-3 py-1 cursor-pointer">
        <div className="space-y-0.5">
          <span className="font-mono text-[11px] uppercase tracking-[0.12em]">
            Save login details
          </span>
          <span className="block text-[11px] text-muted-foreground">
            Persist this password to the contact metadata.
          </span>
        </div>
        <Switch
          checked={saveLogin}
          onCheckedChange={onSaveLoginChange}
          aria-label="Save login details"
        />
      </label>
      <Button
        onClick={onLogin}
        disabled={loggingIn}
        className="w-full rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
      >
        {loggingIn ? "Authenticating…" : "Login"}
      </Button>
    </section>
  );
}

function StatusTab({
  apiBase,
  statusUrl,
  kind,
  onKeepAlive,
  active,
  onPathMayChange,
}: {
  apiBase: string;
  statusUrl?: string;
  kind: AdminNodeKind;
  onKeepAlive?: () => Promise<void>;
  active: boolean;
  onPathMayChange?: () => void;
}) {
  const isRoom = kind === "room";
  const [resyncing, setResyncing] = useState(false);
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const fetchedRef = useRef(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await fetch(statusUrl ?? `${apiBase}/status`);
      if (!r.ok) throw new Error("status");
      const data: Status = await r.json();
      setStatus(data);
      fetchedRef.current = true;
      onPathMayChange?.();
    } catch {
      setErr("Failed to fetch status");
    } finally {
      setLoading(false);
    }
  }, [apiBase, statusUrl, onPathMayChange]);

  useEffect(() => {
    if (active && !fetchedRef.current) {
      refresh();
    }
  }, [active, refresh]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="label-overline">live telemetry</span>
        <Button
          variant="outline"
          size="sm"
          onClick={refresh}
          disabled={loading}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          <RefreshCw className={cn("size-3", loading && "animate-spin")} />
          refresh
        </Button>
        {onKeepAlive && (
          <Button
            variant="outline"
            size="sm"
            disabled={resyncing}
            onClick={async () => {
              setResyncing(true);
              try {
                await onKeepAlive();
                toast.success("Keep-alive sent — posts will resume in chat");
              } catch (e) {
                toast.error(e instanceof Error ? e.message : "Keep-alive failed");
              } finally {
                setResyncing(false);
              }
            }}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
            title="Send a keep-alive so the room resumes pushing posts"
          >
            <RefreshCw className={cn("size-3", resyncing && "animate-spin")} />
            resync posts
          </Button>
        )}
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      )}
      {!status && loading && (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-px bg-border border border-border">
          {[...Array(12)].map((_, i) => (
            <div key={i} className="bg-card p-4 space-y-2">
              <Skeleton className="h-3 w-20" />
              <Skeleton className="h-6 w-16" />
            </div>
          ))}
        </div>
      )}
      {status && (
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-px bg-border border border-border">
          <StatTile
            label="Battery"
            value={formatBattery(status.batteryMv)}
            icon={<Battery className="size-3.5" />}
          />
          <StatTile
            label="Uptime"
            value={formatUptime(status.uptimeSecs)}
            icon={<Clock className="size-3.5" />}
          />
          <StatTile
            label="Noise floor"
            value={`${status.noiseFloor}dB`}
            icon={<Wifi className="size-3.5" />}
          />
          <StatTile
            label="Last SNR"
            value={`${status.lastSnr.toFixed(1)}dB`}
            icon={<Signal className="size-3.5" />}
          />
          <StatTile
            label="Last RSSI"
            value={`${status.lastRssi}`}
            icon={<Signal className="size-3.5" />}
          />
          {!isRoom && (
            <StatTile
              label="Chan util"
              value={`${status.chanUtil.toFixed(1)}%`}
              icon={<Zap className="size-3.5" />}
            />
          )}
          <StatTile
            label="Queue"
            value={`${status.queueLen}`}
            icon={<Inbox className="size-3.5" />}
          />
          {!isRoom && (
            <StatTile
              label="Recv errors"
              value={`${status.recvErrors}`}
              icon={<AlertTriangle className="size-3.5" />}
            />
          )}
          {isRoom && (
            <StatTile label="Posts stored" value={`${status.posted ?? 0}`} accent />
          )}
          {isRoom && (
            <StatTile label="Posts pushed" value={`${status.postPushes ?? 0}`} />
          )}
          <ErrEventsTile mask={status.errEvents} />
          <StatTile label="Pkts sent" value={`${status.packetsSent}`} />
          <StatTile label="Pkts recv" value={`${status.packetsRecv}`} />
          <StatTile label="Flood TX" value={`${status.floodTx}`} />
          <StatTile label="Flood RX" value={`${status.floodRx}`} />
          <StatTile label="Direct TX" value={`${status.directTx}`} />
          <StatTile label="Direct RX" value={`${status.directRx}`} />
          <StatTile
            label="TX air"
            value={formatUptime(status.txAirSecs)}
          />
          {!isRoom && (
            <StatTile
              label="RX air"
              value={formatUptime(status.rxAirSecs)}
            />
          )}
          <StatTile label="Flood dups" value={`${status.floodDups}`} />
          <StatTile label="Direct dups" value={`${status.directDups}`} />
        </div>
      )}
    </div>
  );
}

// Status `errEvents` (firmware _err_flags) is a bitmask of fatal events, not a count.
function ErrEventsTile({ mask }: { mask: number }) {
  const hasErrors = mask !== 0;
  return (
    <div className="bg-card relative px-4 py-3 flex flex-col gap-1.5 group">
      <div className="flex items-center justify-between">
        <span className="label-overline">Err events</span>
        <span
          className={cn(
            "transition-colors",
            hasErrors ? "text-warning" : "text-muted-foreground/50 group-hover:text-primary",
          )}
        >
          <AlertTriangle className="size-3.5" />
        </span>
      </div>
      {hasErrors ? (
        <ErrEventBadges mask={mask} />
      ) : (
        <span className="font-mono text-lg font-semibold leading-none text-success">none</span>
      )}
    </div>
  );
}

// Curated CLI catalogue, mirrored from MeshCore/src/helpers/CommonCLI.cpp.
interface Suggestion {
  text: string;
  hint?: string;
}

function buildSuggestions(input: string, role: CliRole): Suggestion[] {
  const CLI_CONFIG_KEYS = cliConfigKeysFor(role);
  const CLI_TOPLEVEL_COMMANDS = cliCommandsFor(role);
  const lower = input.toLowerCase();
  // get / set sub-completion: rank prefix matches first, then substring matches.
  if (lower.startsWith("get ") || lower.startsWith("set ")) {
    const prefix = lower.slice(0, 4);
    const tail = lower.slice(4).trim();
    if (tail === "") {
      return CLI_CONFIG_KEYS.map((k) => ({
        text: `${prefix}${k.key}`,
        hint: k.hint,
      }));
    }
    const starts = CLI_CONFIG_KEYS.filter((k) => k.key.startsWith(tail));
    const others = CLI_CONFIG_KEYS.filter(
      (k) => !k.key.startsWith(tail) && k.key.includes(tail),
    );
    return [...starts, ...others].map((k) => ({
      text: `${prefix}${k.key}`,
      hint: k.hint,
    }));
  }
  if (lower === "") {
    return CLI_TOPLEVEL_COMMANDS.map((c) => ({
      text: c.command,
      hint: c.hint,
    }));
  }
  const starts = CLI_TOPLEVEL_COMMANDS.filter((c) =>
    c.command.toLowerCase().startsWith(lower),
  );
  const others = CLI_TOPLEVEL_COMMANDS.filter(
    (c) =>
      !c.command.toLowerCase().startsWith(lower) &&
      c.command.toLowerCase().includes(lower),
  );
  return [...starts, ...others].map((c) => ({
    text: c.command,
    hint: c.hint,
  }));
}

function TerminalTab({
  sendCli,
  kind,
}: {
  sendCli: (cmd: string) => Promise<string>;
  kind: AdminNodeKind;
}) {
  const [history, setHistory] = useState<CliEntry[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [hIdx, setHIdx] = useState(-1);
  const [suggestionsOpen, setSuggestionsOpen] = useState(false);
  const [sIdx, setSIdx] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const endRef = useRef<HTMLDivElement>(null);

  const inputs = useMemo(
    () => history.filter((h) => h.kind === "input").map((h) => h.text),
    [history],
  );

  const suggestions = useMemo(
    () =>
      suggestionsOpen && input.trim() !== "" ? buildSuggestions(input, kind) : [],
    [input, suggestionsOpen, kind],
  );

  useEffect(() => {
    setSIdx(0);
  }, [input]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [history]);

  const applySuggestion = useCallback((s: Suggestion) => {
    setInput(s.text);
    setSuggestionsOpen(false);
    requestAnimationFrame(() => inputRef.current?.focus());
  }, []);

  const submit = useCallback(async () => {
    const cmd = input.trim();
    if (!cmd || busy) return;
    setBusy(true);
    setHistory((prev) => [...prev, { kind: "input", text: cmd, ts: Date.now() }]);
    setInput("");
    setHIdx(-1);
    try {
      const out = await sendCli(cmd);
      setHistory((prev) => [
        ...prev,
        { kind: "output", text: out || "(empty)", ts: Date.now() },
      ]);
    } catch (e) {
      setHistory((prev) => [
        ...prev,
        {
          kind: "error",
          text: e instanceof Error ? e.message : "error",
          ts: Date.now(),
        },
      ]);
    } finally {
      setBusy(false);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [input, busy, sendCli]);

  const onKey = useCallback(
    (e: KeyboardEvent<HTMLInputElement>) => {
      const sugOpen = suggestionsOpen && suggestions.length > 0;
      if (e.key === "Enter") {
        e.preventDefault();
        setSuggestionsOpen(false);
        submit();
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        if (suggestions.length === 0) {
          setSuggestionsOpen(true);
          return;
        }
        if (sugOpen) {
          applySuggestion(suggestions[sIdx] || suggestions[0]);
        } else {
          // first Tab opens the menu and pre-selects index 0
          setSuggestionsOpen(true);
          setSIdx(0);
        }
        return;
      }
      if (e.key === "Escape") {
        if (sugOpen) {
          e.preventDefault();
          setSuggestionsOpen(false);
        }
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        if (sugOpen) {
          setSIdx((i) => (i <= 0 ? suggestions.length - 1 : i - 1));
          return;
        }
        if (inputs.length === 0) return;
        const next = hIdx === -1 ? inputs.length - 1 : Math.max(0, hIdx - 1);
        setHIdx(next);
        setInput(inputs[next] ?? "");
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        if (sugOpen) {
          setSIdx((i) => (i >= suggestions.length - 1 ? 0 : i + 1));
          return;
        }
        if (inputs.length === 0) return;
        if (hIdx === -1) return;
        const next = hIdx + 1;
        if (next >= inputs.length) {
          setHIdx(-1);
          setInput("");
        } else {
          setHIdx(next);
          setInput(inputs[next] ?? "");
        }
      }
    },
    [submit, hIdx, inputs, suggestions, sIdx, suggestionsOpen, applySuggestion],
  );

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="label-overline">cli session</span>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setHistory([])}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
        >
          clear
        </Button>
      </div>
      <pre
        className="bg-background border border-border text-xs font-mono p-3 min-h-72 max-h-112 overflow-y-auto whitespace-pre-wrap wrap-break-word"
      >
        {history.length === 0 ? (
          <span className="text-muted-foreground/50">
            // type a command. try `ver`, `{kind === "repeater" ? "neighbors" : "clock"}`, `get name`. tab = autocomplete.
          </span>
        ) : (
          history.map((h, i) => (
            <div
              key={i}
              className={cn(
                "py-0.5",
                h.kind === "input" && "text-primary",
                h.kind === "error" && "text-destructive",
                h.kind === "output" && "text-foreground",
              )}
            >
              {h.kind === "input" ? `> ${h.text}` : h.text}
            </div>
          ))
        )}
        <div ref={endRef} />
      </pre>
      <div className="relative">
        {suggestionsOpen && suggestions.length > 0 && (
          <div className="absolute bottom-full left-0 right-0 mb-1 bg-popover border border-border shadow-md z-20 max-h-60 overflow-y-auto">
            {suggestions.map((s, i) => (
              <button
                key={s.text}
                type="button"
                onMouseDown={(e) => {
                  e.preventDefault();
                  applySuggestion(s);
                }}
                onMouseEnter={() => setSIdx(i)}
                className={cn(
                  "w-full text-left px-3 py-1.5 flex items-baseline gap-3 font-mono text-xs",
                  i === sIdx
                    ? "bg-primary/15 text-primary"
                    : "text-foreground hover:bg-muted/40",
                )}
              >
                <span className="flex-1 truncate">{s.text}</span>
                {s.hint && (
                  <span className="text-[10px] text-muted-foreground/70 truncate">
                    {s.hint}
                  </span>
                )}
              </button>
            ))}
            <div className="px-3 py-1 border-t border-border font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground/60 flex items-center gap-3">
              <span>↑↓ select</span>
              <span>tab apply</span>
              <span>esc close</span>
            </div>
          </div>
        )}
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs text-primary select-none">
            {">"}
          </span>
          <Input
            ref={inputRef}
            value={input}
            onChange={(e: ChangeEvent<HTMLInputElement>) => {
              setInput(e.target.value);
              setSuggestionsOpen(true);
            }}
            onKeyDown={onKey}
            onFocus={() => setSuggestionsOpen(true)}
            onBlur={() => {
              // give the click on a suggestion a chance to fire
              setTimeout(() => setSuggestionsOpen(false), 120);
            }}
            disabled={busy}
            placeholder={busy ? "executing…" : "command (tab to autocomplete)"}
            className="rounded-none font-mono text-base md:text-xs border-border bg-background flex-1"
            autoComplete="off"
            spellCheck={false}
          />
          <Button
            size="sm"
            onClick={submit}
            disabled={busy || !input.trim()}
            className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <Send className="size-3" /> run
          </Button>
        </div>
      </div>
    </div>
  );
}

const NEIGHBORS_PAGE_SIZE = 10;

function NeighborsTab({
  apiBase,
  sendCli,
  isAdmin,
  peers,
  active,
  onPathMayChange,
}: {
  apiBase: string;
  sendCli: (cmd: string) => Promise<string>;
  isAdmin: boolean;
  peers: Peer[];
  active: boolean;
  onPathMayChange?: () => void;
}) {
  const [neighbors, setNeighbors] = useState<NeighborEntry[]>([]);
  const [totalCount, setTotalCount] = useState(0);
  const [busy, setBusy] = useState<"refresh" | "more" | "discover" | null>(
    null,
  );
  const [err, setErr] = useState<string | null>(null);
  const fetchedRef = useRef(false);

  const peerByPrefix = useMemo(() => {
    const map = new Map<string, { name: string; type: string }>();
    for (const p of peers) {
      const key = p.pubkey.toLowerCase().slice(0, 12);
      if (!map.has(key)) {
        map.set(key, { name: p.name, type: p.type });
      }
    }
    return map;
  }, [peers]);

  const fetchPage = useCallback(
    async (offset: number) => {
      const r = await fetch(
        `${apiBase}/neighbors?count=${NEIGHBORS_PAGE_SIZE}&offset=${offset}`,
      );
      if (!r.ok) throw new Error(await apiErrorMessage(r));
      const data: {
        totalCount: number;
        resultsCount: number;
        neighbors?: { pubkeyPrefix: string; secsAgo: number; snr: number }[];
      } = await r.json();
      const parsed: NeighborEntry[] = (data.neighbors || []).map((n) => {
        const lookup = peerByPrefix.get(n.pubkeyPrefix.toLowerCase());
        return {
          pubkeyPrefix: n.pubkeyPrefix,
          secsAgo: n.secsAgo,
          snr: n.snr,
          name: lookup?.name,
          type: lookup?.type,
        };
      });
      return { entries: parsed, totalCount: data.totalCount };
    },
    [apiBase, peerByPrefix],
  );

  const refresh = useCallback(async () => {
    setBusy("refresh");
    setErr(null);
    try {
      const { entries, totalCount: total } = await fetchPage(0);
      setNeighbors(entries);
      setTotalCount(total);
      fetchedRef.current = true;
      onPathMayChange?.();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed");
    } finally {
      setBusy(null);
    }
  }, [fetchPage, onPathMayChange]);

  const loadMore = useCallback(async () => {
    setBusy("more");
    setErr(null);
    try {
      const { entries, totalCount: total } = await fetchPage(neighbors.length);
      setNeighbors((prev) => [...prev, ...entries]);
      setTotalCount(total);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed");
    } finally {
      setBusy(null);
    }
  }, [fetchPage, neighbors.length]);

  const discover = useCallback(async () => {
    setBusy("discover");
    try {
      await sendCli("discover.neighbors");
      toast.success("Discovery sent · neighbors may take ~60s to respond");
    } catch {
      toast.error("Discovery failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  useEffect(() => {
    if (active && !fetchedRef.current) {
      refresh();
    }
  }, [active, refresh]);

  const hasMore = neighbors.length < totalCount;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-baseline gap-3">
          <span className="label-overline">neighbors</span>
          {totalCount > 0 && (
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
              {neighbors.length} of {totalCount}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {isAdmin && (
            <Button
              variant="outline"
              size="sm"
              onClick={discover}
              disabled={busy !== null}
              className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
            >
              {busy === "discover" ? (
                <RefreshCw className="size-3 animate-spin" />
              ) : (
                <Network className="size-3" />
              )}
              discover
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={refresh}
            disabled={busy !== null}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <RefreshCw
              className={cn("size-3", busy === "refresh" && "animate-spin")}
            />
            refresh
          </Button>
        </div>
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      )}
      <div className="panel divide-y divide-border">
        {neighbors.length === 0 ? (
          <div className="py-10 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
            {busy === "refresh"
              ? "scanning…"
              : busy === "discover"
                ? "discovering…"
                : "no neighbors observed"}
          </div>
        ) : (
          neighbors.map((n) => (
            <div
              key={n.pubkeyPrefix}
              className="px-4 py-2.5 flex items-center gap-3"
            >
              <PeerAvatar name={n.name || n.pubkeyPrefix} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm">
                    {n.name || (
                      <span className="text-muted-foreground italic">
                        unknown
                      </span>
                    )}
                  </span>
                  {n.type && (
                    <span className="font-mono text-[10px] uppercase tracking-widest px-1.5 py-0.5 border border-border text-muted-foreground">
                      {n.type}
                    </span>
                  )}
                </div>
                <code className="font-mono text-[10px] text-muted-foreground/70">
                  {n.pubkeyPrefix}
                </code>
              </div>
              <div className="flex flex-col items-end gap-0.5">
                <SignalStrength snr={n.snr} size="md" />
                <div className="font-mono text-[10px] text-muted-foreground/70 tabular-nums">
                  {formatSecsAgo(n.secsAgo)}
                </div>
              </div>
            </div>
          ))
        )}
      </div>
      {hasMore && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            onClick={loadMore}
            disabled={busy !== null}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            {busy === "more" ? (
              <RefreshCw className="size-3 animate-spin" />
            ) : (
              <ChevronDown className="size-3" />
            )}
            load more · {totalCount - neighbors.length} remaining
          </Button>
        </div>
      )}
    </div>
  );
}

interface OwnerInfo {
  firmwareVersion: string;
  nodeName: string;
  ownerInfo: string;
}

function OwnerTab({
  apiBase,
  active,
}: {
  apiBase: string;
  active: boolean;
}) {
  const [info, setInfo] = useState<OwnerInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const fetchedRef = useRef(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await fetch(`${apiBase}/owner`);
      if (!r.ok) throw new Error(await apiErrorMessage(r));
      const data: OwnerInfo = await r.json();
      setInfo(data);
      fetchedRef.current = true;
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed");
    } finally {
      setLoading(false);
    }
  }, [apiBase]);

  useEffect(() => {
    if (active && !fetchedRef.current) {
      refresh();
    }
  }, [active, refresh]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="label-overline">owner info</span>
        <Button
          variant="outline"
          size="sm"
          onClick={refresh}
          disabled={loading}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
        >
          <RefreshCw className={cn("size-3", loading && "animate-spin")} />
          refresh
        </Button>
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      )}
      {!info && loading && (
        <div className="panel divide-y divide-border">
          {[...Array(3)].map((_, i) => (
            <div key={i} className="px-4 py-3 space-y-2">
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-4 w-48" />
            </div>
          ))}
        </div>
      )}
      {info && (
        <div className="panel divide-y divide-border">
          <OwnerRow label="Node name" value={info.nodeName} />
          <OwnerRow label="Firmware version" value={info.firmwareVersion} />
          <OwnerRow
            label="Owner information"
            value={info.ownerInfo}
            multiline
          />
        </div>
      )}
    </div>
  );
}

function OwnerRow({
  label,
  value,
  multiline,
}: {
  label: string;
  value: string;
  multiline?: boolean;
}) {
  const empty = !value || value.trim() === "";
  return (
    <div className="px-4 py-3 space-y-1">
      <span className="label-overline block">{label}</span>
      {empty ? (
        <span className="font-mono text-xs text-muted-foreground/60 italic">
          not set
        </span>
      ) : multiline ? (
        <pre className="font-mono text-sm whitespace-pre-wrap wrap-break-word">
          {value}
        </pre>
      ) : (
        <span className="font-mono text-sm wrap-break-word">{value}</span>
      )}
    </div>
  );
}


interface AccessEntry {
  pubkeyPrefix: string;
  permissions: number;
}


function AccessTab({
  apiBase,
  peers,
  companions,
  active,
  kind,
}: {
  apiBase: string;
  peers: Peer[];
  companions: { name: string; pubkey: string }[];
  active: boolean;
  kind: AdminNodeKind;
}) {
  const [entries, setEntries] = useState<AccessEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const fetchedRef = useRef(false);

  const peerByPrefix = useMemo(() => {
    const map = new Map<string, { name: string; pubkey: string; isSelf?: boolean }>();
    // Companions take priority (own pubkey for self-recognition)
    for (const c of companions) {
      const key = c.pubkey.toLowerCase().slice(0, 12);
      map.set(key, { name: c.name, pubkey: c.pubkey, isSelf: true });
    }
    for (const p of peers) {
      const key = p.pubkey.toLowerCase().slice(0, 12);
      if (!map.has(key)) map.set(key, { name: p.name, pubkey: p.pubkey });
    }
    return map;
  }, [peers, companions]);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await fetch(`${apiBase}/access`);
      if (!r.ok) throw new Error(await apiErrorMessage(r));
      const data: { entries?: AccessEntry[] } = await r.json();
      setEntries(data.entries || []);
      fetchedRef.current = true;
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed");
    } finally {
      setLoading(false);
    }
  }, [apiBase]);

  useEffect(() => {
    if (active && !fetchedRef.current) refresh();
  }, [active, refresh]);

  const setPerm = useCallback(
    async (targetPubkey: string, perms: number) => {
      setBusyKey(targetPubkey);
      try {
        const r = await fetch(
          `${apiBase}/access/${encodeURIComponent(targetPubkey)}`,
          {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ permissions: perms }),
          },
        );
        if (!r.ok) throw new Error(await apiErrorMessage(r));
        toast.success(`Role set to ${roleLabel(perms)}`);
        await refresh();
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed");
      } finally {
        setBusyKey(null);
      }
    },
    [apiBase, refresh],
  );

  const remove = useCallback(
    async (targetPubkey: string) => {
      setBusyKey(targetPubkey);
      try {
        const r = await fetch(
          `${apiBase}/access/${encodeURIComponent(targetPubkey)}`,
          { method: "DELETE" },
        );
        if (!r.ok) throw new Error(await apiErrorMessage(r));
        toast.success("Removed from ACL");
        setConfirmRemove(null);
        await refresh();
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed");
      } finally {
        setBusyKey(null);
      }
    },
    [apiBase, refresh],
  );

  const knownPrefixes = useMemo(
    () => new Set(entries.map((e) => e.pubkeyPrefix.toLowerCase())),
    [entries],
  );

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="label-overline">
          access control
          {kind === "room" && (
            <span className="ml-2 normal-case tracking-normal text-muted-foreground/60">
              · a room lists its admins only
            </span>
          )}
        </span>
        <div className="flex items-center gap-2">
          <Button
            variant="default"
            size="sm"
            onClick={() => setAddOpen(true)}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <Plus className="size-3" /> add
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={refresh}
            disabled={loading}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
          >
            <RefreshCw className={cn("size-3", loading && "animate-spin")} />
            refresh
          </Button>
        </div>
      </div>
      {err && (
        <Alert variant="destructive">
          <AlertTitle className="font-mono uppercase tracking-widest">
            Error
          </AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      )}
      <div className="panel divide-y divide-border">
        {entries.length === 0 ? (
          <div className="py-10 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
            {loading ? "fetching…" : "no acl entries"}
          </div>
        ) : (
          entries.map((entry) => {
            const peer = peerByPrefix.get(entry.pubkeyPrefix.toLowerCase());
            const fullPubkey = peer?.pubkey || entry.pubkeyPrefix;
            const role = entry.permissions & PERM_ROLE_MASK;
            const isBusy = busyKey === fullPubkey;
            const confirming = confirmRemove === fullPubkey;
            return (
              <div
                key={entry.pubkeyPrefix}
                className="px-4 py-3 flex items-center gap-3"
              >
                <PeerAvatar
                  name={peer?.name || entry.pubkeyPrefix}
                  size="sm"
                />
                <div className="min-w-0 flex-1 space-y-0.5">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-mono text-sm">
                      {peer?.name || (
                        <span className="text-muted-foreground italic">
                          unknown
                        </span>
                      )}
                    </span>
                    {peer?.isSelf && (
                      <span className="font-mono text-[9px] uppercase tracking-[0.12em] px-1.5 py-0.5 border border-success/40 text-success bg-success/5">
                        you
                      </span>
                    )}
                    <span
                      className={cn(
                        "inline-flex items-center gap-1 font-mono text-[9px] uppercase tracking-[0.12em] px-1.5 py-0.5 border",
                        rolePillClass(entry.permissions),
                      )}
                    >
                      <Shield className="size-2.5" />
                      {roleLabel(entry.permissions)}
                    </span>
                  </div>
                  <code className="font-mono text-[10px] text-muted-foreground/70">
                    {entry.pubkeyPrefix}
                    {!peer && " · prefix only"}
                  </code>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {peer && kind === "sensor" && (
                    <div className="flex items-center gap-px border border-border bg-border">
                      <AlertBitToggle
                        label="hi"
                        checked={(entry.permissions & PERM_ALERTS_HI) !== 0}
                        disabled={isBusy}
                        onChange={(on) =>
                          setPerm(fullPubkey, on ? entry.permissions | PERM_ALERTS_HI : entry.permissions & ~PERM_ALERTS_HI)
                        }
                      />
                      <AlertBitToggle
                        label="lo"
                        checked={(entry.permissions & PERM_ALERTS_LO) !== 0}
                        disabled={isBusy}
                        onChange={(on) =>
                          setPerm(fullPubkey, on ? entry.permissions | PERM_ALERTS_LO : entry.permissions & ~PERM_ALERTS_LO)
                        }
                      />
                    </div>
                  )}
                  {peer ? (
                    <Select
                      value={String(role)}
                      onValueChange={(v) =>
                        setPerm(fullPubkey, (kind === "sensor" ? entry.permissions & PERM_ALERTS_MASK : 0) | parseInt(v, 10))
                      }
                      disabled={isBusy}
                    >
                      <SelectTrigger className="rounded-none font-mono text-[10px] uppercase tracking-widest h-7 w-32 border-border bg-background">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent className="rounded-none font-mono text-xs">
                        <CurrentRoleItem perms={entry.permissions} />
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
                  ) : (
                    <span
                      className="font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground/60"
                      title="full pubkey unknown — cannot edit role"
                    >
                      no full key
                    </span>
                  )}
                  {confirming ? (
                    <div className="flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em]">
                      <Button
                        variant="destructive"
                        size="xs"
                        onClick={() => remove(fullPubkey)}
                        disabled={isBusy}
                        className="font-mono uppercase tracking-widest"
                      >
                        yes
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setConfirmRemove(null)}
                        className="font-mono uppercase tracking-widest"
                      >
                        no
                      </Button>
                    </div>
                  ) : (
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      onClick={() => setConfirmRemove(fullPubkey)}
                      disabled={isBusy}
                      className="text-muted-foreground/60 hover:text-destructive"
                      aria-label="Remove from ACL"
                      title="Remove from ACL"
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  )}
                </div>
              </div>
            );
          })
        )}
      </div>
      <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/60">
        firmware reports 6-byte prefixes only. a prefix is enough to remove a
        client; assigning a role needs the full public key.
      </p>

      <AddAccessDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        peers={peers}
        knownPrefixes={knownPrefixes}
        kind={kind}
        onAdd={async (pubkey, perms) => {
          await setPerm(pubkey, perms);
          setAddOpen(false);
        }}
      />
    </div>
  );
}


function SettingsTab({
  pubkey,
  peerName,
  sendCli,
  onReboot,
  onLocalNameUpdate,
  kind,
}: {
  pubkey: string;
  peerName: string;
  sendCli: (cmd: string) => Promise<string>;
  onReboot: () => void;
  onLocalNameUpdate: (name: string) => void;
  kind: AdminNodeKind;
}) {
  const cli = useCallback(
    async (cmd: string) => {
      const out = (await sendCli(cmd)).trim();
      if (CLI_ERR_RE.test(out)) throw new Error(out);
      return out;
    },
    [sendCli],
  );
  return (
    <div className="space-y-3">
      {/* key remounts the section when the advertised name changes */}
      <IdentitySection
        key={peerName}
        peerName={peerName}
        pubkey={pubkey}
        sendCli={cli}
        onLocalNameUpdate={onLocalNameUpdate}
      />

      <RadioSection sendCli={cli} />
      <PositionSection sendCli={cli} />
      <AdvertSection sendCli={cli} />
      {kind === "sensor" ? <SensorVarsSection sendCli={sendCli} /> : <NetworkSection sendCli={cli} kind={kind} />}
      <OwnerSection sendCli={cli} />
      <SecuritySection sendCli={cli} kind={kind} />

      <ActionsSection sendCli={cli} onReboot={onReboot} />
    </div>
  );
}

const CLI_ERR_RE = /^\(?(err|unknown|\?\?|not supported|can't|invalid)/i;

function stripPromptPrefix(s: string): string {
  return (s || "").replace(/^>\s*/, "").trim();
}

function IdentitySection({
  peerName,
  pubkey,
  sendCli,
  onLocalNameUpdate,
}: {
  peerName: string;
  pubkey: string;
  sendCli: (cmd: string) => Promise<string>;
  onLocalNameUpdate: (name: string) => void;
}) {
  const [name, setName] = useState(peerName);
  const [busy, setBusy] = useState(false);
  const dirty = name.trim() !== peerName.trim() && name.trim().length > 0;

  const save = useCallback(async () => {
    const next = name.trim();
    if (!next || next === peerName) return;
    setBusy(true);
    try {
      await sendCli(`set name ${next}`);
      onLocalNameUpdate(next);
      toast.success(`Name updated to "${next}"`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(false);
    }
  }, [name, peerName, sendCli, onLocalNameUpdate]);

  return (
    <SettingsSection
      title="Identity"
      eyebrow="public info"
      icon={<KeyRound className="size-3.5" />}
      defaultOpen
    >
      <div className="space-y-3">
        <div className="space-y-1">
          <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
            Name
          </Label>
          <div className="flex items-center gap-2">
            <Input
              value={name}
              onChange={(e: ChangeEvent<HTMLInputElement>) =>
                setName(e.target.value)
              }
              maxLength={31}
              className="rounded-none font-mono text-base md:text-xs border-border bg-background flex-1"
            />
            <Button
              type="button"
              size="sm"
              onClick={save}
              disabled={busy || !dirty}
              className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
            >
              <Check className="size-3" /> save
            </Button>
          </div>
        </div>
        <Field label="Pubkey" value={pubkey} readOnly copy />
      </div>
    </SettingsSection>
  );
}

const BANDWIDTH_OPTIONS: SelectOption[] = [
  { value: "7.8", label: "7.8 kHz" },
  { value: "10.4", label: "10.4 kHz" },
  { value: "15.6", label: "15.6 kHz" },
  { value: "20.8", label: "20.8 kHz" },
  { value: "31.25", label: "31.25 kHz" },
  { value: "41.7", label: "41.7 kHz" },
  { value: "62.5", label: "62.5 kHz" },
  { value: "125", label: "125 kHz" },
  { value: "250", label: "250 kHz" },
  { value: "500", label: "500 kHz" },
];

const SF_OPTIONS: SelectOption[] = Array.from({ length: 8 }, (_, i) => {
  const v = (5 + i).toString();
  return { value: v, label: `SF${v}` };
});

const CR_OPTIONS: SelectOption[] = Array.from({ length: 4 }, (_, i) => {
  const v = (5 + i).toString();
  return { value: v, label: `4/${v}` };
});

function RadioSection({ sendCli }: { sendCli: (cmd: string) => Promise<string> }) {
  const [vals, setVals] = useState({ freq: "", bw: "", sf: "", cr: "", tx: "" });
  const [busy, setBusy] = useState<SectionBusy>(null);

  // Awaited one at a time on purpose: the radio is half-duplex, so concurrent requests compete for airtime.
  const load = useCallback(async () => {
    setBusy("load");
    try {
      const radio = stripPromptPrefix(await sendCli("get radio"));
      const tx = stripPromptPrefix(await sendCli("get tx"));
      const [freq = "", bw = "", sf = "", cr = ""] = radio.split(",").map((s) => s.trim());
      setVals({ freq, bw, sf, cr, tx });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      if (vals.freq && vals.bw && vals.sf && vals.cr) {
        await sendCli(`set radio ${vals.freq},${vals.bw},${vals.sf},${vals.cr}`);
      }
      if (vals.tx) {
        await sendCli(`set tx ${vals.tx}`);
      }
      toast.success("Radio saved · reboot required");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, vals]);

  const set = (k: keyof typeof vals) => (v: string) =>
    setVals((p) => ({ ...p, [k]: v }));

  return (
    <SettingsSection
      title="Radio"
      eyebrow="rf parameters"
      icon={<Wifi className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field label="Frequency (MHz)" value={vals.freq} onChange={set("freq")} />
          <SelectField
            label="Bandwidth"
            value={vals.bw}
            options={BANDWIDTH_OPTIONS}
            onChange={set("bw")}
          />
          <SelectField
            label="Spreading factor"
            value={vals.sf}
            options={SF_OPTIONS}
            onChange={set("sf")}
          />
          <SelectField
            label="Coding rate"
            value={vals.cr}
            options={CR_OPTIONS}
            onChange={set("cr")}
          />
          <Field label="TX power (dBm)" value={vals.tx} onChange={set("tx")} />
        </div>
        <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-warning">
          Reboot required after radio changes
        </p>
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

function PositionSection({
  sendCli,
}: {
  sendCli: (cmd: string) => Promise<string>;
}) {
  const [vals, setVals] = useState({ lat: "", lon: "" });
  const [busy, setBusy] = useState<SectionBusy>(null);

  const load = useCallback(async () => {
    setBusy("load");
    try {
      const lat = stripPromptPrefix(await sendCli("get lat"));
      const lon = stripPromptPrefix(await sendCli("get lon"));
      setVals({ lat, lon });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      if (vals.lat) await sendCli(`set lat ${vals.lat}`);
      if (vals.lon) await sendCli(`set lon ${vals.lon}`);
      toast.success("Position saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, vals]);

  const setLatLon = useCallback((lat: number, lon: number) => {
    setVals({ lat: lat.toFixed(6), lon: lon.toFixed(6) });
  }, []);

  return (
    <SettingsSection
      title="Position"
      eyebrow="geo"
      icon={<MapPin className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field
            label="Latitude"
            value={vals.lat}
            onChange={(v) => setVals((p) => ({ ...p, lat: v }))}
          />
          <Field
            label="Longitude"
            value={vals.lon}
            onChange={(v) => setVals((p) => ({ ...p, lon: v }))}
          />
        </div>
        <PositionPicker
          lat={parseFloat(vals.lat)}
          lon={parseFloat(vals.lon)}
          onPick={setLatLon}
        />
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

function AdvertSection({
  sendCli,
}: {
  sendCli: (cmd: string) => Promise<string>;
}) {
  const [vals, setVals] = useState({ direct: "", flood: "" });
  const [busy, setBusy] = useState<SectionBusy>(null);

  const load = useCallback(async () => {
    setBusy("load");
    try {
      const direct = stripPromptPrefix(await sendCli("get advert.interval"));
      const flood = stripPromptPrefix(await sendCli("get flood.advert.interval"));
      setVals({ direct, flood });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      if (vals.direct) await sendCli(`set advert.interval ${vals.direct}`);
      if (vals.flood) await sendCli(`set flood.advert.interval ${vals.flood}`);
      toast.success("Advert intervals saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, vals]);

  return (
    <SettingsSection
      title="Advert intervals"
      eyebrow="beacon"
      icon={<Signal className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field
            label="Direct (mins · 0 = off · else 60-240)"
            value={vals.direct}
            onChange={(v) => setVals((p) => ({ ...p, direct: v }))}
          />
          <Field
            label="Flood (hours · 0 = off · else 3-168)"
            value={vals.flood}
            onChange={(v) => setVals((p) => ({ ...p, flood: v }))}
          />
        </div>
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

function NetworkSection({
  sendCli,
  kind,
}: {
  sendCli: (cmd: string) => Promise<string>;
  kind: AdminNodeKind;
}) {
  const [vals, setVals] = useState({
    repeat: "",
    pathHashMode: "",
    loopDetect: "",
    floodMax: "",
    floodMaxUnscoped: "",
    floodMaxAdvert: "",
  });
  const [busy, setBusy] = useState<SectionBusy>(null);

  const load = useCallback(async () => {
    setBusy("load");
    try {
      const repeat = stripPromptPrefix(await sendCli("get repeat"));
      const pathHashMode = stripPromptPrefix(await sendCli("get path.hash.mode"));
      const loopDetect = kind === "repeater" ? stripPromptPrefix(await sendCli("get loop.detect")) : "";
      const floodMax = stripPromptPrefix(await sendCli("get flood.max"));
      const floodMaxUnscoped = stripPromptPrefix(
        await sendCli("get flood.max.unscoped"),
      );
      const floodMaxAdvert = stripPromptPrefix(
        await sendCli("get flood.max.advert"),
      );
      setVals({
        repeat,
        pathHashMode,
        loopDetect,
        floodMax,
        floodMaxUnscoped,
        floodMaxAdvert,
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, kind]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      if (vals.repeat) await sendCli(`set repeat ${vals.repeat}`);
      if (vals.pathHashMode)
        await sendCli(`set path.hash.mode ${vals.pathHashMode}`);
      if (kind === "repeater" && vals.loopDetect) await sendCli(`set loop.detect ${vals.loopDetect}`);
      if (vals.floodMax) await sendCli(`set flood.max ${vals.floodMax}`);
      if (vals.floodMaxUnscoped)
        await sendCli(`set flood.max.unscoped ${vals.floodMaxUnscoped}`);
      if (vals.floodMaxAdvert)
        await sendCli(`set flood.max.advert ${vals.floodMaxAdvert}`);
      toast.success("Network saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, vals, kind]);

  return (
    <SettingsSection
      title="Routing"
      eyebrow="network"
      icon={<Network className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <SelectField
            label="Repeat"
            value={vals.repeat}
            options={[
              { value: "on", label: "On" },
              { value: "off", label: "Off" },
            ]}
            onChange={(v) => setVals((p) => ({ ...p, repeat: v }))}
          />
          <SelectField
            label="Path hash mode"
            value={vals.pathHashMode}
            options={[
              { value: "0", label: "0 · 1-byte" },
              { value: "1", label: "1 · 2-byte" },
              { value: "2", label: "2 · 3-byte" },
            ]}
            onChange={(v) => setVals((p) => ({ ...p, pathHashMode: v }))}
          />
          {kind === "repeater" && (
            <SelectField
              label="Loop detect"
              value={vals.loopDetect}
              options={[
                { value: "off", label: "Off" },
                { value: "minimal", label: "Minimal" },
                { value: "moderate", label: "Moderate" },
                { value: "strict", label: "Strict" },
              ]}
              onChange={(v) => setVals((p) => ({ ...p, loopDetect: v }))}
            />
          )}
          <Field
            label="Flood max"
            value={vals.floodMax}
            onChange={(v) => setVals((p) => ({ ...p, floodMax: v }))}
          />
          <Field
            label="Flood max unscoped"
            value={vals.floodMaxUnscoped}
            onChange={(v) => setVals((p) => ({ ...p, floodMaxUnscoped: v }))}
          />
          <Field
            label="Flood max advert"
            value={vals.floodMaxAdvert}
            onChange={(v) => setVals((p) => ({ ...p, floodMaxAdvert: v }))}
          />
        </div>
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

// The `sensor list` reply is "N vars\nkey=value…", paged with "... next:i" past 134 bytes.
function SensorVarsSection({ sendCli }: { sendCli: (cmd: string) => Promise<string> }) {
  const [vars, setVars] = useState<{ key: string; value: string }[]>([]);
  const [dirty, setDirty] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<SectionBusy>(null);
  const [note, setNote] = useState("");

  const load = useCallback(async () => {
    setBusy("load");
    try {
      const out: { key: string; value: string }[] = [];
      let start = 0;
      for (let guard = 0; guard < 20; guard++) {
        const reply = await sendCli(start ? `sensor list ${start}` : "sensor list");
        if (/^no custom var/i.test(reply)) break;
        let next = -1;
        for (const line of reply.split("\n")) {
          const m = /^([^=\s]+)=(.*)$/.exec(line);
          if (m) out.push({ key: m[1], value: m[2] });
          const nx = /^\.\.\. next:(\d+)/.exec(line);
          if (nx) next = parseInt(nx[1], 10);
        }
        if (next < 0) break;
        start = next;
      }
      setVars(out);
      setDirty({});
      setNote(out.length ? "" : "this board exposes no custom variables");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to read sensor vars");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      for (const [key, value] of Object.entries(dirty)) {
        const reply = await sendCli(`sensor set ${key} ${value}`);
        if (!/^ok/i.test(reply)) throw new Error(`${key}: ${reply}`);
      }
      toast.success("Sensor vars saved");
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save sensor vars");
    } finally {
      setBusy(null);
    }
  }, [dirty, sendCli, load]);

  return (
    <SettingsSection
      title="Sensor variables"
      eyebrow="board"
      icon={<Activity className="size-3.5" />}
    >
      <div className="space-y-3">
        {vars.map((v) => (
          <Field
            key={v.key}
            label={v.key}
            value={dirty[v.key] ?? v.value}
            onChange={(val) => setDirty((d) => ({ ...d, [v.key]: val }))}
          />
        ))}
        {note && (
          <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{note}</p>
        )}
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

function OwnerSection({
  sendCli,
}: {
  sendCli: (cmd: string) => Promise<string>;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState<SectionBusy>(null);

  const load = useCallback(async () => {
    setBusy("load");
    try {
      const out = stripPromptPrefix(await sendCli("get owner.info"));
      setText(out.replace(/\|/g, "\n"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      const encoded = text.replace(/\r?\n/g, "|");
      await sendCli(`set owner.info ${encoded}`);
      toast.success("Owner info saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, text]);

  return (
    <SettingsSection
      title="Owner information"
      eyebrow="public bio"
      icon={<KeyRound className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="space-y-1">
          <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
            Multi-line text · saved as | separated
          </Label>
          <Textarea
            value={text}
            onChange={(e: ChangeEvent<HTMLTextAreaElement>) =>
              setText(e.target.value)
            }
            rows={4}
            className="rounded-none font-mono text-base md:text-xs border-border bg-background resize-y"
            placeholder="Operator: …\nLocation: …"
          />
        </div>
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

function SecuritySection({
  sendCli,
  kind,
}: {
  sendCli: (cmd: string) => Promise<string>;
  kind: AdminNodeKind;
}) {
  const hasGuest = kind !== "sensor";
  const isRoom = kind === "room";
  const [admin, setAdmin] = useState("");
  const [guest, setGuest] = useState("");
  const [allowReadOnly, setAllowReadOnly] = useState(false);
  const [busy, setBusy] = useState<SectionBusy>(null);

  const load = useCallback(async () => {
    setBusy("load");
    try {
      if (hasGuest) {
        const guestPw = stripPromptPrefix(await sendCli("get guest.password"));
        setGuest(guestPw);
      }
      if (isRoom) {
        setAllowReadOnly(stripPromptPrefix(await sendCli("get allow.read.only")) === "on");
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Load failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, hasGuest, isRoom]);

  const save = useCallback(async () => {
    setBusy("save");
    try {
      if (admin) await sendCli(`password ${admin}`);
      if (hasGuest && guest !== "") await sendCli(`set guest.password ${guest}`);
      if (isRoom) await sendCli(`set allow.read.only ${allowReadOnly ? "on" : "off"}`);
      toast.success("Security saved");
      setAdmin("");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(null);
    }
  }, [sendCli, admin, guest, allowReadOnly, hasGuest, isRoom]);

  return (
    <SettingsSection
      title="Security"
      eyebrow="passwords"
      icon={<Shield className="size-3.5" />}
    >
      <div className="space-y-3">
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field
            label="Admin password (write-only)"
            value={admin}
            onChange={setAdmin}
            type="password"
          />
          {hasGuest && (
            <Field
              label={isRoom ? "Room password (read / write)" : "Guest password"}
              value={guest}
              onChange={setGuest}
            />
          )}
        </div>
        {isRoom && (
          <label className="flex items-center justify-between gap-3 border border-border bg-card px-3 py-2 cursor-pointer">
            <span className="space-y-0.5">
              <span className="block font-mono text-xs uppercase tracking-[0.08em]">Allow read-only</span>
              <span className="block font-mono text-[10px] text-muted-foreground/60">
                a wrong or blank room password still logs in as a read-only guest
              </span>
            </span>
            <Switch checked={allowReadOnly} onCheckedChange={setAllowReadOnly} />
          </label>
        )}
        <SectionFooter busy={busy} onLoad={load} onSave={save} />
      </div>
    </SettingsSection>
  );
}

type SectionBusy = "load" | "save" | null;

function SectionFooter({
  busy,
  onLoad,
  onSave,
}: {
  busy: SectionBusy;
  onLoad: () => void;
  onSave: () => void;
}) {
  const disabled = busy !== null;
  return (
    <div className="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        onClick={onLoad}
        disabled={disabled}
        className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
      >
        <RefreshCw
          className={cn("size-3", busy === "load" && "animate-spin")}
        />
        load
      </Button>
      <Button
        size="sm"
        onClick={onSave}
        disabled={disabled}
        className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
      >
        {busy === "save" ? (
          <RefreshCw className="size-3 animate-spin" />
        ) : (
          <Check className="size-3" />
        )}
        save
      </Button>
    </div>
  );
}

function SettingsSection({
  title,
  eyebrow,
  icon,
  children,
  defaultOpen = false,
}: {
  title: string;
  eyebrow?: string;
  icon?: ReactNode;
  children: ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="panel">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="w-full px-4 py-3 flex items-center justify-between gap-2 border-b border-border hover:bg-muted/30 transition-colors text-left"
      >
        <div className="flex items-center gap-2.5">
          {icon && <span className="text-primary">{icon}</span>}
          <div className="space-y-0.5">
            {eyebrow && (
              <span className="label-overline block">{eyebrow}</span>
            )}
            <span className="font-mono text-sm uppercase tracking-widest">
              {title}
            </span>
          </div>
        </div>
        {open ? (
          <ChevronDown className="size-4 text-muted-foreground" />
        ) : (
          <ChevronRight className="size-4 text-muted-foreground" />
        )}
      </button>
      {open && <div className="p-4">{children}</div>}
    </section>
  );
}

function Field({
  label,
  value,
  onChange,
  readOnly,
  copy,
  type = "text",
}: {
  label: string;
  value: string;
  onChange?: (v: string) => void;
  readOnly?: boolean;
  copy?: boolean;
  type?: string;
}) {
  return (
    <div className="space-y-1">
      <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </Label>
      <div className="flex items-center gap-2">
        <Input
          type={type}
          value={value}
          readOnly={readOnly}
          onChange={(e: ChangeEvent<HTMLInputElement>) =>
            onChange?.(e.target.value)
          }
          className={cn(
            "rounded-none font-mono text-base md:text-xs border-border bg-background flex-1",
            readOnly && "text-muted-foreground",
          )}
        />
        {copy && (
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            onClick={() => {
              navigator.clipboard.writeText(value);
              toast.success("Copied");
            }}
            className="rounded-none"
          >
            <Copy className="size-3" />
          </Button>
        )}
      </div>
    </div>
  );
}

interface SelectOption {
  value: string;
  label: string;
}

function SelectField({
  label,
  value,
  options,
  onChange,
  placeholder = "—",
}: {
  label: string;
  value: string;
  options: SelectOption[];
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  // A loaded value missing from the options is appended, so the device's value shows rather than a blank.
  const includesValue = value === "" || options.some((o) => o.value === value);
  const finalOptions = includesValue
    ? options
    : [...options, { value, label: `${value} (custom)` }];

  return (
    <div className="space-y-1">
      <Label className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </Label>
      <Select value={value || undefined} onValueChange={onChange}>
        <SelectTrigger className="rounded-none font-mono text-xs border-border bg-background w-full">
          <SelectValue placeholder={placeholder} />
        </SelectTrigger>
        <SelectContent className="rounded-none font-mono text-xs">
          {finalOptions.map((o) => (
            <SelectItem
              key={o.value}
              value={o.value}
              className="rounded-none font-mono text-xs"
            >
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function ActionsSection({
  sendCli,
  onReboot,
}: {
  sendCli: (cmd: string) => Promise<string>;
  onReboot: () => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmReboot, setConfirmReboot] = useState(false);

  const run = useCallback(
    async (label: string, cmd: string) => {
      setBusy(label);
      try {
        const out = await sendCli(cmd);
        toast.success(out || `${label} sent`);
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed");
      } finally {
        setBusy(null);
      }
    },
    [sendCli],
  );

  return (
    <SettingsSection
      title="Actions"
      eyebrow="commands"
      icon={<Zap className="size-3.5" />}
    >
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() => run("Advert", "advert")}
          disabled={busy === "Advert"}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] justify-start"
        >
          <Signal className="size-3" /> Send advert
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => run("Zerohop", "advert.zerohop")}
          disabled={busy === "Zerohop"}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] justify-start"
        >
          <Signal className="size-3" /> Zerohop advert
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => run("Clock", "clock sync")}
          disabled={busy === "Clock"}
          className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] justify-start"
        >
          <Clock className="size-3" /> Sync clock
        </Button>
        {confirmReboot ? (
          <div className="flex items-center gap-1">
            <Button
              variant="destructive"
              size="sm"
              onClick={async () => {
                await run("Reboot", "reboot");
                setConfirmReboot(false);
                onReboot();
              }}
              className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] flex-1"
            >
              Confirm reboot
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setConfirmReboot(false)}
              className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em]"
            >
              cancel
            </Button>
          </div>
        ) : (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setConfirmReboot(true)}
            className="rounded-none font-mono text-[10px] uppercase tracking-[0.12em] justify-start text-destructive border-destructive/40 hover:bg-destructive/10 hover:text-destructive"
          >
            <AlertTriangle className="size-3" /> Reboot
          </Button>
        )}
      </div>
    </SettingsSection>
  );
}
