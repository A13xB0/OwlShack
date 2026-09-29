import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Activity,
  AlertCircle,
  ArrowLeft,
  Ban,
  CheckCheck,
  ChevronLeft,
  ArrowDown,
  ChevronRight,
  Copy,
  CornerUpLeft,
  ExternalLink,
  Hash,
  Antenna,
  Pencil,
  Loader2,
  LogIn,
  Megaphone,
  MessageSquare,
  MoreHorizontal,
  MoreVertical,
  Plus,
  Radar,
  Radio,
  RadioTower,
  Reply,
  RotateCw,
  Search,
  Send,
  Trash2,
  UserPlus,
  Users,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { useCompanionRef } from "@/hooks/useCompanions";
import { useResume } from "@/lib/resume";
import { useWebSocket } from "@/hooks/useWebSocket";
import { PageHeader } from "@/components/PageHeader";
import { ConnectionPill, PeerTypePill } from "@/components/StatusIndicator";
import { PeerAvatar } from "@/components/PeerAvatar";
import { snrTextClass } from "@/components/SignalStrength";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { ChatHeaderMenu } from "@/components/ChatHeaderMenu";
import { CoordLink } from "@/components/CoordLink";
import { AddChannelDialog } from "@/components/AddChannelDialog";
import { postChannel } from "@/lib/channelsApi";
import { ComposerAttachMenu } from "@/components/ComposerAttachMenu";
import { EmojiButton, EmojiMartPanel, EmojiPicker } from "@/components/EmojiPicker";
import { useEmojiAutocomplete } from "@/hooks/useEmojiAutocomplete";
import { useMentionAutocomplete } from "@/hooks/useMentionAutocomplete";
import { useIsMobile } from "@/hooks/use-mobile";
import {
  AddContactDialog,
  type ContactPrefill,
  type ContactType,
} from "@/components/AddContactDialog";
import { formatClockTime, formatDateTime, formatShortTime, timeAgo, truncateMid } from "@/lib/format";
import { contactDetailPath } from "@/lib/routes";
import { cn } from "@/lib/utils";

interface Conversation {
  id: string;
  type: string;
  name: string;
  channel: string;
  lastMessage?: {
    text: string;
    sender: string;
    direction: string;
    timestamp: string;
  };
  unreadCount: number;
  lastActive?: string;
  lastMessageId?: number;
  peerType?: string;
  isRepeater?: boolean;
  pubkey?: string;
}

interface Message {
  id?: number;
  channel: string;
  sender: string;
  text: string;
  direction: string;
  timestamp: string;
  snr?: number | null;
  rssi?: number | null;
  repeatCount?: number | null;
  hops?: number | null;
  pathHashSize?: number | null;
  status?: string | null;
}

// `targets` maps a channel's hash-stripped, lowercased name to its real configured name.
interface ChannelLinkCtx {
  targets: Map<string, string>;
  onOpen: (realName: string) => void;
  onAdd: (bareName: string) => void;
}

// GET /rooms/{pubkey}/session: presence of pubkeyHex means logged in, same rule as repeaters.
interface RoomSession {
  loggedIn?: boolean;
  pubkeyHex?: string;
  isAdmin?: boolean;
  role?: string;
}

interface PathHop {
  hash: string;
  peerNames?: string[];
}

interface PathInfo {
  // Absent when the hop count could not be measured (a direct-routed packet consumes its path).
  hops?: number | null;
  pathHashSize: number;
  sender: string;
  path: PathHop[];
}

interface EchoEntry {
  id: number;
  receivedAt: string;
  hops: number;
  pathHashSize: number;
  path: PathHop[];
  snr?: number | null;
  rssi?: number | null;
}

// One time we heard a packet: the original reception, or one distinct-path repeat.
interface Hearing {
  key: string;
  receivedAt: string;
  // Undefined means the hop count could not be measured, not that there were none.
  hops?: number | null;
  path: PathHop[];
  snr?: number | null;
  isOriginal: boolean;
}

// The final repeater in the path is the one closest to us. An empty path means we heard it off the
// air, EXCEPT on a direct-routed packet: each hop consumes its entry, so the count is absent rather
// than zero. Naming the routing mode says what happened without inventing a hop count.
function hearingVia(path: PathHop[], hops?: number | null): string {
  if (path.length === 0) return hops == null ? "Direct route · hops unknown" : "Direct";
  const last = path[path.length - 1];
  return last.peerNames?.[0] || `#${last.hash}`;
}

// First-heard first; the original is only included when its path was loaded.
function buildHearings(
  path: PathInfo | null,
  echoes: EchoEntry[] | null,
  msg: Message | undefined,
): Hearing[] {
  const list: Hearing[] = [];
  if (path && msg) {
    list.push({
      key: "rx",
      receivedAt: msg.timestamp,
      hops: path.hops,
      path: path.path,
      snr: msg.snr,
      isOriginal: true,
    });
  }
  for (const e of echoes ?? []) {
    list.push({
      key: `echo-${e.id}`,
      receivedAt: e.receivedAt,
      hops: e.hops,
      path: e.path,
      snr: e.snr,
      isOriginal: false,
    });
  }
  return list.sort((a, b) => tsValue(a.receivedAt) - tsValue(b.receivedAt));
}

type SortMode = "recent" | "name" | "unread";
type ModalKind = "path" | "echoes" | "rxPaths";

interface ModalState {
  kind: ModalKind;
  message: Message;
  backTo?: ModalState;
}

const MENTION_RE = /@\[([^\]]+)\]/g;

// Alternatives are tried in order at each position, so tokens never overlap (a URL swallows its own "#frag").
const TOKEN_RE = new RegExp(
  [
    "(?<contact><[0-9a-fA-F]{64}:[1-4]:[^>]*>)",
    "(?<url>https?:\\/\\/[^\\s<>]+)",
    "(?<coord>(?<![\\d.])-?\\d{1,3}\\.\\d+\\s*,\\s*-?\\d{1,3}\\.\\d+(?![\\d.]))",
    "(?<mention>@\\[[^\\]]+\\])",
    "(?<channel>(?<![\\w#])#\\w[\\w-]*)",
  ].join("|"),
  "g",
);

const CONTACT_TYPE_BY_INT: Record<string, ContactType> = {
  "1": "CHAT",
  "2": "REPEATER",
  "3": "ROOM",
  "4": "SENSOR",
};

const COORD_RE = /^(-?\d{1,3}\.\d+)\s*,\s*(-?\d{1,3}\.\d+)$/;

// UI matching only: the firmware keys a channel's PSK off the exact name, so `#Weather` ≠ `#weather`.
function channelKey(name: string): string {
  return name.replace(/^#/, "").toLowerCase();
}

// null when out of range, so the caller leaves the token as plain text.
function parseCoord(s: string): { lat: number; lon: number } | null {
  const m = COORD_RE.exec(s.trim());
  if (!m) return null;
  const lat = parseFloat(m[1]);
  const lon = parseFloat(m[2]);
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  if (lat < -90 || lat > 90 || lon < -180 || lon > 180) return null;
  return { lat, lon };
}

// Trailing sentence punctuation usually isn't part of a URL ("see https://x.").
const TRAILING_PUNCT_RE = /[.,;:!?'")\]}]+$/;
function splitTrailingPunct(url: string): { url: string; trailing: string } {
  const m = TRAILING_PUNCT_RE.exec(url);
  if (!m) return { url, trailing: "" };
  return { url: url.slice(0, m.index), trailing: url.slice(m.index) };
}
// How close to the end still counts as following the thread.
const BOTTOM_SLACK_PX = 80;

const SORT_KEY = "companion-sort";

const HEADER_ACTION_CLASS =
  "inline-flex items-center font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground hover:text-primary px-2.5 py-1.5 sm:py-0.5 border border-border relative before:absolute before:inset-x-0 before:-inset-y-2 before:content-[''] sm:before:hidden";

// Rendered as inline chips on desktop and as menu items in the mobile overflow menu.
const COMPANION_NAV = [
  { seg: "contacts", label: "contacts", Icon: Users },
  { seg: "channels", label: "channels", Icon: Hash },
  { seg: "repeaters", label: "repeaters", Icon: Radio },
  { seg: "telemetry", label: "telemetry", Icon: Activity },
] as const;

type AdvertMode = "flood" | "zerohop";

// Flood = mesh-wide (repeaters rebroadcast); zero-hop = direct neighbours only.
function useAdvert(companion: string) {
  const [busy, setBusy] = useState<AdvertMode | null>(null);
  const send = useCallback(
    async (mode: AdvertMode) => {
      setBusy(mode);
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/advert`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ mode }),
          },
        );
        if (!r.ok) {
          const e = await r.json().catch(() => ({}));
          throw new Error(e.error || `HTTP ${r.status}`);
        }
        toast.success(
          mode === "flood" ? "Flood advert sent" : "Zero-hop advert sent",
        );
      } catch (e) {
        toast.error(`Advert failed: ${e instanceof Error ? e.message : "error"}`);
      } finally {
        setBusy(null);
      }
    },
    [companion],
  );
  return { busy, send };
}

function AdvertItems({
  busy,
  onSend,
}: {
  busy: AdvertMode | null;
  onSend: (mode: AdvertMode) => void;
}) {
  return (
    <>
      <DropdownMenuItem
        onClick={() => onSend("flood")}
        disabled={busy !== null}
        className="gap-2"
      >
        <RadioTower className="size-3.5 text-muted-foreground" />
        <div className="flex flex-col">
          <span className="font-mono text-xs">Flood advert</span>
          <span className="text-[10px] text-muted-foreground">
            mesh-wide · rebroadcast
          </span>
        </div>
      </DropdownMenuItem>
      <DropdownMenuItem
        onClick={() => onSend("zerohop")}
        disabled={busy !== null}
        className="gap-2"
      >
        <Antenna className="size-3.5 text-muted-foreground" />
        <div className="flex flex-col">
          <span className="font-mono text-xs">Zero-hop advert</span>
          <span className="text-[10px] text-muted-foreground">
            direct neighbours only
          </span>
        </div>
      </DropdownMenuItem>
    </>
  );
}

// Desktop shows inline chips; mobile collapses them into one "⋯" menu so the header stays one row.
function CompanionActions({ companion }: { companion: string }) {
  const { busy, send } = useAdvert(companion);
  const path = (seg: string) =>
    `/companions/${encodeURIComponent(companion)}/${seg}`;
  const editPath = `/companions?edit=${encodeURIComponent(companion)}`;

  return (
    <>
      <div className="hidden sm:flex items-center gap-2">
        {COMPANION_NAV.map(({ seg, label, Icon }) => (
          <Link key={seg} to={path(seg)} className={HEADER_ACTION_CLASS}>
            <Icon className="size-3 mr-1.5" /> {label}
          </Link>
        ))}
        <Link to={editPath} className={HEADER_ACTION_CLASS} title="Edit name, position, advert interval">
          <Pencil className="size-3 mr-1.5" /> edit
        </Link>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              disabled={busy !== null}
              className={cn(HEADER_ACTION_CLASS, "disabled:opacity-50")}
            >
              {busy ? (
                <Loader2 className="size-3 mr-1.5 animate-spin" />
              ) : (
                <Megaphone className="size-3 mr-1.5" />
              )}
              advert
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            className="rounded-none border-border w-56"
          >
            <AdvertItems busy={busy} onSend={send} />
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Companion actions"
            className={cn(HEADER_ACTION_CLASS, "sm:hidden")}
          >
            <MoreHorizontal className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="end"
          className="rounded-none border-border w-56"
        >
          {COMPANION_NAV.map(({ seg, label, Icon }) => (
            <DropdownMenuItem key={seg} asChild className="gap-2">
              <Link to={path(seg)}>
                <Icon className="size-3.5 text-muted-foreground" />
                <span className="font-mono text-xs uppercase tracking-[0.12em]">
                  {label}
                </span>
              </Link>
            </DropdownMenuItem>
          ))}
          <DropdownMenuItem asChild className="gap-2">
            <Link to={editPath}>
              <Pencil className="size-3.5 text-muted-foreground" />
              <span className="font-mono text-xs uppercase tracking-[0.12em]">
                edit companion
              </span>
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <AdvertItems busy={busy} onSend={send} />
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}

// Every cache below is per-companion: remount on switch, or one companion's rows leak into another's thread.
export function CompanionDetailPage() {
  const { ref } = useParams<{ ref: string }>();
  return <CompanionChat key={ref ?? ""} />;
}

function CompanionChat() {
  const { ref } = useParams<{ ref: string }>();
  const {
    ref: companionRef,
    id: companionId,
    name: companionName,
  } = useCompanionRef(ref);
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const activeChannel = searchParams.get("channel");

  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loadingList, setLoadingList] = useState(true);
  const [loadingMsgs, setLoadingMsgs] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<SortMode>(() => {
    try {
      return (window.localStorage.getItem(SORT_KEY) as SortMode | null) ?? "recent";
    } catch {
      return "recent";
    }
  });
  const [composer, setComposer] = useState("");
  const [sending, setSending] = useState(false);
  const [msgSearch, setMsgSearch] = useState("");
  const [msgSearchOpen, setMsgSearchOpen] = useState(false);
  const [contextMsg, setContextMsg] = useState<Message | null>(null);
  const [contextPos, setContextPos] = useState<{ x: number; y: number } | null>(
    null,
  );
  const [modal, setModal] = useState<ModalState | null>(null);
  const [modalLoading, setModalLoading] = useState(false);
  const [modalPath, setModalPath] = useState<PathInfo | null>(null);
  const [modalEchoes, setModalEchoes] = useState<EchoEntry[] | null>(null);
  const [ownPubkey, setOwnPubkey] = useState<string | null>(null);
  const [ownPos, setOwnPos] = useState<{ lat: number; lon: number } | null>(null);
  const isMobile = useIsMobile();
  const [emojiOpen, setEmojiOpen] = useState(false);
  const [addContactOpen, setAddContactOpen] = useState(false);
  const [addContactPrefill, setAddContactPrefill] =
    useState<ContactPrefill | null>(null);
  const [addChannelOpen, setAddChannelOpen] = useState(false);
  const [addChannelPrefill, setAddChannelPrefill] = useState("");

  const messagesEndRef = useRef<HTMLDivElement>(null);
  const scrollContainerRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const messageCacheRef = useRef<Map<string, Message[]>>(new Map());
  // Channels whose full history has been paged back to the start — stop fetching.
  const reachedStartRef = useRef<Set<string>>(new Set());
  // Set while a scroll-up prepend is in flight, so auto-scroll-to-bottom skips.
  const pendingPrependRef = useRef<{ prevHeight: number; prevTop: number } | null>(
    null,
  );
  const skipAutoScrollRef = useRef(false);
  // The pane's height as of the last scroll decision. Comparing the reader's position against the
  // height *before* this render is what says whether they were at the end of the thread — asking
  // afterwards counts the arriving message itself as distance, and a flag kept from scroll events
  // is only ever as good as the last event observed.
  const lastPaneHeightRef = useRef(0);
  const [unreadBelow, setUnreadBelow] = useState(0);

  const activeConversation = useMemo(
    () => conversations.find((c) => c.channel === activeChannel) ?? null,
    [conversations, activeChannel],
  );

  // The WS handler's row-update-vs-reload decision must stay out of the setConversations updater — StrictMode invokes updaters twice.
  const knownChannelsRef = useRef<Set<string>>(new Set());
  knownChannelsRef.current = useMemo(
    () => new Set(conversations.map((c) => c.channel)),
    [conversations],
  );

  const isContact = activeConversation?.type === "contact";
  const isRoom = activeConversation?.peerType === "ROOM";
  // Rooms silently truncate posts at 151 chars (firmware MAX_POST_TEXT_LEN).
  const charLimit = isRoom
    ? 151
    : isContact
      ? 155
      : Math.max(0, 153 - companionName.length);

  const roomPubkey = isRoom ? (activeConversation?.pubkey ?? null) : null;
  const [roomSession, setRoomSession] = useState<RoomSession | null>(null);
  const [roomSessionLoading, setRoomSessionLoading] = useState(false);

  useEffect(() => {
    setRoomSession(null);
    if (!roomPubkey || !companionRef) return;
    let cancelled = false;
    setRoomSessionLoading(true);
    fetch(
      `/api/companions/${encodeURIComponent(companionRef)}/rooms/${roomPubkey}/session`,
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((s: RoomSession | null) => {
        if (!cancelled) setRoomSession(s);
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRoomSessionLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [companionRef, roomPubkey]);

  // /participants is the historical rx-sender list; loaded messages add anyone posting mid-session.
  const mentionEnabled = !!activeConversation && (!isContact || isRoom);
  const convoId = activeConversation?.id ?? null;
  const [fetchedParticipants, setFetchedParticipants] = useState<string[]>([]);

  useEffect(() => {
    setFetchedParticipants([]);
    if (!mentionEnabled || !convoId || !companionRef) return;
    let cancelled = false;
    fetch(
      `/api/companions/${encodeURIComponent(companionRef)}/conversations/${encodeURIComponent(convoId)}/participants`,
    )
      .then((r) => (r.ok ? r.json() : []))
      .then((d: string[]) => {
        if (!cancelled) setFetchedParticipants(d || []);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [mentionEnabled, convoId, companionRef]);

  const mentionNames = useMemo(() => {
    if (!mentionEnabled) return [];
    const set = new Set(fetchedParticipants);
    for (const m of messages) {
      if (m.direction === "rx" && m.sender && m.sender !== companionName)
        set.add(m.sender);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [mentionEnabled, fetchedParticipants, messages, companionName]);

  const roomLoggedIn =
    !!roomSession && roomSession.loggedIn !== false && !!roomSession.pubkeyHex;
  const roomReadOnly = roomLoggedIn && roomSession?.role === "read-only";
  // Sensor firmware would run anything typed back as a CLI command, so the composer locks.
  const isSensorThread = activeConversation?.peerType === "SENSOR";
  const composerLocked = roomReadOnly || isSensorThread;
  const lockedHint = isSensorThread
    ? "sensor alerts only — sensors don't accept chat"
    : "read-only access";

  useEffect(() => {
    try {
      window.localStorage.setItem(SORT_KEY, sort);
    } catch {
      /* best-effort */
    }
  }, [sort]);

  // silent skips the skeleton, so a refresh behind the user's back never blanks the thread list.
  const loadConversations = useCallback(
    async (silent = false): Promise<Conversation[]> => {
      if (!companionRef) return [];
      if (!silent) {
        setLoadingList(true);
        setListError(null);
      }
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/conversations`,
        );
        if (!r.ok) throw new Error("conversations");
        const data: Conversation[] = (await r.json()) || [];
        setConversations(data);
        setListError(null);
        return data;
      } catch {
        if (!silent) setListError("Failed to load conversations");
        return [];
      } finally {
        if (!silent) setLoadingList(false);
      }
    },
    [companionRef],
  );

  useEffect(() => {
    loadConversations();
  }, [loadConversations]);

  // Own public key, to flag self-add attempts on shared-contact cards.
  useEffect(() => {
    if (!companionRef) return;
    let cancelled = false;
    fetch("/api/companions")
      .then((r) => (r.ok ? r.json() : []))
      .then(
        (
          list: {
            id: number;
            name: string;
            pubkey?: string;
            lat?: number;
            lon?: number;
          }[],
        ) => {
          if (cancelled) return;
          const me = (list || []).find((c) => c.id === companionId);
          setOwnPubkey(me?.pubkey ?? null);
          setOwnPos(
            me && me.lat != null && me.lon != null
              ? { lat: me.lat, lon: me.lon }
              : null,
          );
        },
      )
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [companionRef, companionId]);

  const onAddContact = useCallback((prefill: ContactPrefill) => {
    setAddContactPrefill(prefill);
    setAddContactOpen(true);
  }, []);

  const addChannel = useCallback(
    async (channelName: string, privateKey?: string) => {
      try {
        await postChannel(companionRef, channelName, privateKey);
      } catch (e) {
        toast.error(
          `Failed to add channel: ${e instanceof Error ? e.message : "failed"}`,
        );
        return;
      }
      toast.success(`Channel "${channelName}" added`);
      setAddChannelOpen(false);
      // The firmware normalizes a public channel name ("ham" → "#ham"), so open what actually landed.
      const updated = await loadConversations();
      const target = updated.find(
        (c) =>
          c.type === "channel" &&
          channelKey(c.channel) === channelKey(channelName),
      );
      setSearchParams({ channel: target ? target.channel : channelName });
    },
    [companionRef, loadConversations, setSearchParams],
  );

  // The roster already includes every configured channel, seeded server-side.
  const configuredChannels = useMemo(
    () =>
      conversations
        .filter((c) => c.type === "channel")
        .map((c) => ({ name: c.channel })),
    [conversations],
  );

  // Unknown hashtags route through the pre-filled Add Channel dialog.
  const channelCtx = useMemo<ChannelLinkCtx>(() => {
    const targets = new Map<string, string>();
    for (const c of configuredChannels) targets.set(channelKey(c.name), c.name);
    return {
      targets,
      onOpen: (realName) => setSearchParams({ channel: realName }),
      onAdd: (bareName) => {
        setAddChannelPrefill(bareName);
        setAddChannelOpen(true);
      },
    };
  }, [configuredChannels, setSearchParams]);

  // The ?compose= param is stripped after use so it isn't re-applied.
  useEffect(() => {
    const text = searchParams.get("compose");
    if (!text) return;
    setComposer(text);
    const next = new URLSearchParams(searchParams);
    next.delete("compose");
    setSearchParams(next, { replace: true });
    window.setTimeout(() => composerRef.current?.focus(), 0);
  }, [searchParams, setSearchParams]);

  const lastIdOf = useCallback((arr: Message[]): number => {
    let max = 0;
    for (const m of arr) {
      if (typeof m.id === "number" && m.id > max) max = m.id;
    }
    return max;
  }, []);

  const initialLoadMessages = useCallback(
    async (channel: string) => {
      if (!companionRef) return;
      setLoadingMsgs(true);
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/messages?channel=${encodeURIComponent(channel)}&limit=100`,
        );
        if (!r.ok) throw new Error("messages");
        const data: Message[] = await r.json();
        const ordered = (data || []).slice().reverse();
        messageCacheRef.current.set(channel, ordered);
        setMessages(ordered);
      } catch {
        toast.error("Failed to load messages");
      } finally {
        setLoadingMsgs(false);
      }
    },
    [companionRef],
  );

  const backfillMessages = useCallback(
    async (channel: string) => {
      if (!companionRef) return;
      const existing = messageCacheRef.current.get(channel) || [];
      const afterId = lastIdOf(existing);
      if (afterId <= 0) {
        await initialLoadMessages(channel);
        return;
      }
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/messages?channel=${encodeURIComponent(channel)}&afterId=${afterId}&limit=500`,
        );
        if (!r.ok) return;
        const delta: Message[] = await r.json();
        if (!delta || delta.length === 0) return;
        const merged = mergeMessages(existing, delta);
        messageCacheRef.current.set(channel, merged);
        if (channel === activeChannel) setMessages(merged);
      } catch {
        // backfill is best-effort
      }
    },
    [companionRef, lastIdOf, initialLoadMessages, activeChannel],
  );

  // Scroll-up paging; pendingPrependRef carries the metrics that preserve the viewport.
  const loadOlderMessages = useCallback(
    async (channel: string) => {
      if (!companionRef) return;
      if (reachedStartRef.current.has(channel)) return;
      const existing = messageCacheRef.current.get(channel) || [];
      // existing is ascending (oldest first) — find the smallest real id.
      let oldest = 0;
      for (const m of existing) {
        if (typeof m.id === "number" && m.id > 0) {
          oldest = m.id;
          break;
        }
      }
      if (oldest <= 0) return;
      // Measured before the spinner toggles container height, as the post-prepend measurement also excludes it.
      const el = scrollContainerRef.current;
      const prevHeight = el?.scrollHeight ?? 0;
      const prevTop = el?.scrollTop ?? 0;
      setLoadingOlder(true);
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/messages?channel=${encodeURIComponent(channel)}&beforeId=${oldest}&limit=100`,
        );
        if (!r.ok) return;
        const older: Message[] = await r.json();
        if (!older || older.length === 0) {
          reachedStartRef.current.add(channel);
          return;
        }
        if (older.length < 100) reachedStartRef.current.add(channel);
        const orderedOlder = older.slice().reverse();
        const merged = mergeMessages(orderedOlder, existing);
        messageCacheRef.current.set(channel, merged);
        if (channel === activeChannel) {
          if (el) {
            pendingPrependRef.current = { prevHeight, prevTop };
            skipAutoScrollRef.current = true;
          }
          setMessages(merged);
        }
      } catch {
        // best-effort; user can scroll up again to retry
      } finally {
        setLoadingOlder(false);
      }
    },
    [companionRef, activeChannel],
  );

  const handleMessagesScroll = useCallback(() => {
    const el = scrollContainerRef.current;
    if (!el || !activeChannel) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK_PX) {
      setUnreadBelow(0);
    }
    if (
      el.scrollTop <= 80 &&
      !loadingOlder &&
      !loadingMsgs &&
      !reachedStartRef.current.has(activeChannel)
    ) {
      loadOlderMessages(activeChannel);
    }
  }, [activeChannel, loadingOlder, loadingMsgs, loadOlderMessages]);

  useEffect(() => {
    if (!activeChannel) {
      setMessages([]);
      return;
    }
    const cached = messageCacheRef.current.get(activeChannel);
    if (cached) {
      setMessages(cached);
      setLoadingMsgs(false);
      backfillMessages(activeChannel);
    } else {
      initialLoadMessages(activeChannel);
    }
  }, [activeChannel, initialLoadMessages, backfillMessages]);

  // The highest id reported read per conversation, so watching a thread keeps marking it read
  // without re-posting the same id on every render.
  const reportedReadRef = useRef<Map<string, number>>(new Map());

  useEffect(() => {
    if (!activeChannel || !activeConversation) return;
    // Deliberately not gated on unreadCount. That is zeroed locally as soon as the thread opens, so
    // gating on it stopped every message that arrived while the thread was on screen from ever
    // advancing last_read_id — they were read, the server never heard, and they came back unread on
    // the next visit. Track the high-water mark instead.
    const highest = messages.reduce((max, m) => (m.id && m.id > max ? m.id : max), 0);
    if (highest === 0) return;
    if ((reportedReadRef.current.get(activeConversation.id) ?? 0) >= highest) return;
    reportedReadRef.current.set(activeConversation.id, highest);

    fetch(
      `/api/companions/${encodeURIComponent(companionRef)}/conversations/${encodeURIComponent(activeConversation.id)}/read`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ lastReadId: highest }),
      },
    ).catch(() => {
      // Let a later render retry rather than stranding the high-water mark on a failed request.
      reportedReadRef.current.delete(activeConversation.id);
    });

    if (activeConversation.unreadCount !== 0) {
      setConversations((prev) =>
        prev.map((c) =>
          c.id === activeConversation.id ? { ...c, unreadCount: 0 } : c,
        ),
      );
    }
  }, [activeChannel, activeConversation, messages, companionRef]);

  const handleWsMessage = useCallback(
    (topic: string, data: unknown) => {
      if (topic !== "messages" || !data || typeof data !== "object") return;
      const payload = data as Message & {
        companion?: string;
        companionId?: number;
        action?: string;
      };
      // Match on the id where both sides have one: it is in the URL, so it is known on the first
      // render, and a rename cannot change it. Falling back to the name rather than dropping keeps
      // a payload that predates companionId visible instead of silently stalling the thread.
      const mine =
        payload.companionId != null && companionId != null
          ? payload.companionId === companionId
          : payload.companion === companionName ||
            payload.companion === companionRef;
      if (!mine) return;

      if (payload.action === "repeatCount" && payload.id != null) {
        const updateRepeat = (m: Message): Message =>
          m.id === payload.id
            ? { ...m, repeatCount: payload.repeatCount ?? m.repeatCount }
            : m;
        if (payload.channel) {
          const cached = messageCacheRef.current.get(payload.channel);
          if (cached) {
            messageCacheRef.current.set(payload.channel, mapChanged(cached, updateRepeat));
          }
        }
        if (payload.channel === activeChannel) {
          setMessages((prev) => mapChanged(prev, updateRepeat));
        }
        return;
      }

      if (payload.action === "status" && payload.id != null) {
        const updateStatus = (m: Message): Message =>
          m.id === payload.id
            ? { ...m, status: (payload as { status?: string }).status ?? m.status }
            : m;
        if (payload.channel) {
          const cached = messageCacheRef.current.get(payload.channel);
          if (cached) {
            messageCacheRef.current.set(payload.channel, mapChanged(cached, updateStatus));
          }
        }
        if (payload.channel === activeChannel) {
          setMessages((prev) => mapChanged(prev, updateStatus));
        }
        return;
      }

      const incoming: Message = {
        id: payload.id,
        channel: payload.channel,
        sender: payload.sender,
        text: payload.text,
        direction: payload.direction,
        timestamp: payload.timestamp,
        snr: payload.snr,
        rssi: payload.rssi,
        repeatCount: payload.repeatCount,
        hops: payload.hops,
        pathHashSize: payload.pathHashSize,
        status: (payload as { status?: string }).status,
      };
      if (!incoming.channel || !incoming.timestamp) return;

      const cached = messageCacheRef.current.get(incoming.channel);
      if (cached) {
        messageCacheRef.current.set(
          incoming.channel,
          mergeMessages(cached, [incoming]),
        );
      }

      if (incoming.channel === activeChannel) {
        setMessages((prev) => mergeMessages(prev, [incoming]));
      }

      if (!knownChannelsRef.current.has(incoming.channel)) {
        loadConversations(true);
        return;
      }

      setConversations((prev) => {
        const idx = prev.findIndex((c) => c.channel === incoming.channel);
        if (idx === -1) return prev;
        const updated = [...prev];
        const target = { ...updated[idx] };
        target.lastMessage = {
          text: incoming.text,
          sender: incoming.sender,
          direction: incoming.direction,
          timestamp: incoming.timestamp,
        };
        target.lastActive = incoming.timestamp;
        if (typeof incoming.id === "number") target.lastMessageId = incoming.id;
        if (
          incoming.direction === "rx" &&
          incoming.channel !== activeChannel
        ) {
          target.unreadCount = (target.unreadCount || 0) + 1;
        }
        updated[idx] = target;
        return updated;
      });
    },
    [
      companionRef,
      companionId,
      companionName,
      activeChannel,
      loadConversations,
    ],
  );

  const { connected, pending } = useWebSocket(["messages"], handleWsMessage);

  // A gap in the stream leaves this thread and the roster short of whatever arrived during it.
  // Other channels catch up on re-open, via the same backfill.
  useResume(
    useCallback(() => {
      if (activeChannel) backfillMessages(activeChannel);
      loadConversations(true);
    }, [activeChannel, backfillMessages, loadConversations]),
  );

  const initialScrollDoneRef = useRef(false);
  const lastAutoScrollIdRef = useRef(0);
  useEffect(() => {
    initialScrollDoneRef.current = false;
    lastAutoScrollIdRef.current = 0;
    lastPaneHeightRef.current = 0;
    setUnreadBelow(0);
    setMsgSearch("");
    setMsgSearchOpen(false);
  }, [activeChannel]);

  // Restores scroll after a prepend, before paint, so the viewport doesn't jump.
  useLayoutEffect(() => {
    const pending = pendingPrependRef.current;
    if (!pending) return;
    pendingPrependRef.current = null;
    const el = scrollContainerRef.current;
    if (!el) return;
    el.scrollTop = pending.prevTop + (el.scrollHeight - pending.prevHeight);
  }, [messages]);

  useEffect(() => {
    const pane = scrollContainerRef.current;
    if (loadingMsgs || messages.length === 0) return;
    const wasAtEnd =
      !pane ||
      lastPaneHeightRef.current === 0 ||
      lastPaneHeightRef.current - pane.scrollTop - pane.clientHeight <=
        BOTTOM_SLACK_PX;
    if (pane) lastPaneHeightRef.current = pane.scrollHeight;
    if (skipAutoScrollRef.current) {
      // this messages change was a scroll-up prepend — don't yank to the bottom.
      skipAutoScrollRef.current = false;
      return;
    }
    // Only something arriving at the bottom moves the view. A status or repeat-count update, or a
    // re-delivered duplicate, changes the array while adding nothing to read — and yanking the
    // reader to the bottom for one is indistinguishable from a message they never got.
    const newest = lastIdOf(messages);
    if (newest !== 0 && newest <= lastAutoScrollIdRef.current) return;
    const previous = lastAutoScrollIdRef.current;
    lastAutoScrollIdRef.current = newest;

    if (!initialScrollDoneRef.current) {
      messagesEndRef.current?.scrollIntoView({ behavior: "instant" as ScrollBehavior });
      initialScrollDoneRef.current = true;
      setUnreadBelow(0);
      return;
    }
    // Follow the thread only for a reader already at the end of it, or one who just posted. A
    // reader scrolled back through history keeps their place and is told what arrived instead.
    const ownSend = messages[messages.length - 1]?.direction === "tx";
    if (wasAtEnd || ownSend) {
      messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
      setUnreadBelow(0);
      return;
    }
    setUnreadBelow(
      (n) =>
        n +
        messages.filter((m) => typeof m.id === "number" && m.id > previous).length,
    );
  }, [messages, loadingMsgs, lastIdOf]);

  const jumpToLatest = useCallback(() => {
    setUnreadBelow(0);
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, []);

  useEffect(() => {
    if (!contextMsg) return;
    const dismiss = (e: MouseEvent | TouchEvent) => {
      const target = (
        "touches" in e ? document.elementFromPoint((e as TouchEvent).touches[0].clientX, (e as TouchEvent).touches[0].clientY) : e.target
      ) as HTMLElement | null;
      if (target?.closest("[data-context-menu]")) return;
      setContextMsg(null);
      setContextPos(null);
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        setContextMsg(null);
        setContextPos(null);
      }
    };
    window.addEventListener("mousedown", dismiss);
    window.addEventListener("touchstart", dismiss);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", dismiss);
      window.removeEventListener("touchstart", dismiss);
      window.removeEventListener("keydown", onKey);
    };
  }, [contextMsg]);

  // Repeater threads live on the Repeaters page, and the search filter must not change this total.
  const threadCount = useMemo(
    () => conversations.filter((c) => !c.isRepeater).length,
    [conversations],
  );

  const filteredConversations = useMemo(() => {
    const q = search.trim().toLowerCase();
    let arr = conversations.filter((c) => !c.isRepeater);
    if (q) {
      arr = arr.filter(
        (c) =>
          c.name.toLowerCase().includes(q) ||
          c.lastMessage?.text.toLowerCase().includes(q),
      );
    }
    const isPublic = (c: Conversation) =>
      c.name.toLowerCase() === "public" || c.channel.toLowerCase() === "public";
    const pinPublic = (a: Conversation, b: Conversation) => {
      const ap = isPublic(a) ? 0 : 1;
      const bp = isPublic(b) ? 0 : 1;
      return ap - bp;
    };
    if (sort === "name") {
      arr.sort((a, b) => pinPublic(a, b) || a.name.localeCompare(b.name));
    } else if (sort === "unread") {
      arr.sort((a, b) => {
        const p = pinPublic(a, b);
        if (p !== 0) return p;
        if (a.unreadCount !== b.unreadCount)
          return b.unreadCount - a.unreadCount;
        return convRecency(a, b);
      });
    } else {
      arr.sort((a, b) => pinPublic(a, b) || convRecency(a, b));
    }
    return arr;
  }, [conversations, search, sort]);

  const filteredMessages = useMemo(() => {
    if (!msgSearch.trim()) return messages;
    const q = msgSearch.toLowerCase();
    return messages.filter(
      (m) =>
        m.text.toLowerCase().includes(q) ||
        m.sender.toLowerCase().includes(q),
    );
  }, [messages, msgSearch]);

  const groupedMessages = useMemo(() => groupMessages(filteredMessages), [filteredMessages]);

  const openConversation = useCallback(
    (c: Conversation) => {
      if (c.isRepeater && c.pubkey) {
        navigate(contactDetailPath(companionRef, c.pubkey, true));
        return;
      }
      setSearchParams({ channel: c.channel });
    },
    [companionRef, navigate, setSearchParams],
  );

  const closeChat = useCallback(() => {
    const params = new URLSearchParams(searchParams);
    params.delete("channel");
    setSearchParams(params);
  }, [searchParams, setSearchParams]);

  const emojiAC = useEmojiAutocomplete({
    textareaRef: composerRef,
    setText: setComposer,
  });
  const mentionAC = useMentionAutocomplete({
    textareaRef: composerRef,
    setText: setComposer,
    names: mentionNames,
  });

  const send = useCallback(async () => {
    if (!activeConversation || !composer.trim() || sending) return;
    const text = composer.trim();
    if (text.length > charLimit) {
      toast.error(`Message too long (${text.length}/${charLimit})`);
      return;
    }
    setSending(true);
    try {
      const r = await fetch(
        `/api/companions/${encodeURIComponent(companionRef)}/messages`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            channel: activeConversation.channel,
            text,
          }),
        },
      );
      if (!r.ok) throw new Error("send");
      setComposer("");
      setEmojiOpen(false);
    } catch {
      toast.error("Send failed");
    } finally {
      setSending(false);
    }
  }, [activeConversation, composer, sending, charLimit, companionRef]);

  const onComposerKey = useCallback(
    (e: KeyboardEvent<HTMLTextAreaElement>) => {
      if (mentionAC.handleKeyDown(e)) return;
      if (emojiAC.handleKeyDown(e)) return;
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        send();
      }
    },
    [mentionAC.handleKeyDown, emojiAC.handleKeyDown, send],
  );

  // focus=false keeps the caret without summoning the mobile keyboard over the emoji panel.
  const insertAtCursor = useCallback((text: string, focus = true) => {
    const el = composerRef.current;
    if (!el) {
      setComposer((c) => c + text);
      return;
    }
    const start = el.selectionStart ?? el.value.length;
    const end = el.selectionEnd ?? el.value.length;
    setComposer(el.value.slice(0, start) + text + el.value.slice(end));
    requestAnimationFrame(() => {
      if (focus) el.focus();
      const pos = start + text.length;
      el.setSelectionRange(pos, pos);
    });
  }, []);

  const onMessageContext = useCallback(
    (e: React.MouseEvent, m: Message) => {
      e.preventDefault();
      setContextMsg(m);
      setContextPos({ x: e.clientX, y: e.clientY });
    },
    [],
  );

  const handleCopy = useCallback(async (m: Message) => {
    try {
      await navigator.clipboard.writeText(m.text);
      toast.success("Copied");
    } catch {
      toast.error("Copy failed");
    }
  }, []);

  const handleReply = useCallback(
    (m: Message) => {
      const isOwn = m.direction === "tx" || m.sender === companionName;
      const next = isOwn
        ? `${m.text
            .split("\n")
            .map((line) => `> ${line}`)
            .join("\n")}\n`
        : `@[${m.sender}] `;
      setComposer(next);
      requestAnimationFrame(() => {
        const el = composerRef.current;
        if (!el) return;
        el.focus();
        const end = el.value.length;
        el.setSelectionRange(end, end);
      });
    },
    [companionName],
  );

  const handleDelete = useCallback(
    async (m: Message) => {
      if (!m.id) return;
      try {
        const r = await fetch(`/api/messages/${m.id}`, { method: "DELETE" });
        if (!r.ok) throw new Error("delete");
        setMessages((prev) => prev.filter((x) => x.id !== m.id));
        const cached = messageCacheRef.current.get(m.channel);
        if (cached) {
          messageCacheRef.current.set(
            m.channel,
            cached.filter((x) => x.id !== m.id),
          );
        }
        toast.success("Deleted");
      } catch {
        toast.error("Delete failed");
      }
    },
    [],
  );

  const handleRetry = useCallback(
    async (m: Message) => {
      if (!m.id) return;
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/messages/${m.id}/retry`,
          { method: "POST" },
        );
        if (!r.ok) throw new Error("retry");
        setMessages((prev) => prev.filter((x) => x.id !== m.id));
        const cached = messageCacheRef.current.get(m.channel);
        if (cached) {
          messageCacheRef.current.set(
            m.channel,
            cached.filter((x) => x.id !== m.id),
          );
        }
      } catch {
        toast.error("Retry failed");
      }
    },
    [companionRef],
  );

  const handleBlock = useCallback(
    async (m: Message) => {
      if (!activeConversation) return;
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companionRef)}/conversations/${encodeURIComponent(activeConversation.id)}/block`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ sender: m.sender }),
          },
        );
        if (!r.ok) throw new Error("block");
        toast.success(`Blocked ${m.sender}`);
      } catch {
        toast.error("Block failed");
      }
    },
    [activeConversation, companionRef],
  );

  const openModal = useCallback(
    async (kind: ModalKind, m: Message, backTo?: ModalState) => {
      if (!m.id) {
        toast.error("Message missing id");
        return;
      }
      setModal({ kind, message: m, backTo });
      setModalPath(null);
      setModalEchoes(null);
      setModalLoading(true);
      try {
        if (kind === "path") {
          const r = await fetch(
            `/api/companions/${encodeURIComponent(companionRef)}/messages/${m.id}/path?channel=${encodeURIComponent(m.channel)}`,
          );
          if (!r.ok) throw new Error("path");
          setModalPath(await r.json());
        } else if (kind === "echoes") {
          const r = await fetch(`/api/messages/${m.id}/echoes`);
          if (!r.ok) throw new Error("echoes");
          setModalEchoes(await r.json());
        } else if (kind === "rxPaths") {
          const [pr, er] = await Promise.all([
            fetch(
              `/api/companions/${encodeURIComponent(companionRef)}/messages/${m.id}/path?channel=${encodeURIComponent(m.channel)}`,
            ),
            fetch(`/api/messages/${m.id}/echoes`),
          ]);
          if (pr.ok) setModalPath(await pr.json());
          if (er.ok) setModalEchoes(await er.json());
        }
      } catch {
        toast.error("Failed to load");
      } finally {
        setModalLoading(false);
      }
    },
    [companionRef],
  );

  const closeModal = useCallback(() => {
    setModal(null);
    setModalPath(null);
    setModalEchoes(null);
  }, []);

  // rxPaths loads the original path and the echoes; the TX echoes modal loads echoes only.
  const hearings = useMemo(
    () => buildHearings(modalPath, modalEchoes, modal?.message),
    [modalPath, modalEchoes, modal?.message],
  );

  const composerLen = composer.length;
  const overLimit = composerLen > charLimit;
  // 90px is three rows: 42 for the first, 24 for each after.
  const [composerTall, setComposerTall] = useState(false);
  useLayoutEffect(() => {
    const el = composerRef.current;
    if (el) setComposerTall(el.getBoundingClientRect().height >= 90);
  }, [composer]);
  // On mobile the count only ever sits beside Send, and only once it earns the space: three rows
  // in, or near the limit, which a long companion name can reach sooner and would disable Send.
  const countBesideSend =
    isMobile && (composerTall || composerLen > charLimit * 0.85);
  const charCounter = (
    <span
      className={cn(
        "font-mono text-[10px] tabular-nums",
        overLimit
          ? "text-destructive"
          : composerLen > charLimit * 0.85
            ? "text-warning"
            : "text-muted-foreground/60",
      )}
    >
      {composerLen}/{charLimit}
    </span>
  );

  return (
    <div className={cn("h-[calc(100dvh-var(--app-header)-var(--bottom-nav))] -mt-6 -mb-[calc(1.5rem+var(--bottom-nav))] -mx-4 sm:-mx-6 flex flex-col overflow-hidden", activeChannel && "max-lg:*:first:hidden")}>
      <div className="shrink-0 px-4 sm:px-6 pt-6">
        <PageHeader
          title="Messages"
          meta={
            <span className="font-mono text-sm text-muted-foreground tabular-nums">
              {threadCount} thread
              {threadCount === 1 ? "" : "s"}
            </span>
          }
          trailing={<ConnectionPill connected={connected} pending={pending} />}
          actions={<CompanionActions companion={companionRef} />}
        />
      </div>

      {listError && (
        <div className="shrink-0 px-4 sm:px-6">
          <Alert variant="destructive">
            <AlertTitle className="font-mono uppercase tracking-widest">
              Error
            </AlertTitle>
            <AlertDescription>
              {listError}
              <Button
                variant="ghost"
                size="sm"
                onClick={() => loadConversations()}
                className="ml-2 h-7 text-xs uppercase tracking-widest"
              >
                retry
              </Button>
            </AlertDescription>
          </Alert>
        </div>
      )}

      <section className={cn(
        "grid grid-cols-1 lg:grid-cols-[20rem_minmax(0,1fr)] gap-px bg-border border border-border flex-1 min-h-0 overflow-hidden mx-4 sm:mx-6 mb-6",
        activeChannel && "max-lg:border-0 max-lg:mx-0 max-lg:mb-0",
      )}>
        {/* Conversation list */}
        <div
          className={cn(
            "bg-card flex flex-col min-h-0",
            activeChannel && "hidden lg:flex",
          )}
        >
          <div className="px-4 py-3 border-b border-border flex items-center justify-between gap-2">
            <span className="label-overline">Threads</span>
            <Select
              value={sort}
              onValueChange={(v) => setSort(v as SortMode)}
            >
              <SelectTrigger
                size="sm"
                className="font-mono text-[10px] uppercase tracking-widest h-7 px-2 rounded-none border-border"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem
                  value="recent"
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  Recent
                </SelectItem>
                <SelectItem
                  value="name"
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  Name
                </SelectItem>
                <SelectItem
                  value="unread"
                  className="font-mono text-xs uppercase tracking-[0.08em]"
                >
                  Unread
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="px-3 py-3 border-b border-border">
            <div className="relative">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground/50" />
              <Input
                value={search}
                onChange={(e: ChangeEvent<HTMLInputElement>) =>
                  setSearch(e.target.value)
                }
                placeholder="search"
                className="pl-8 h-8 font-mono text-base md:text-xs rounded-none border-border bg-background"
              />
            </div>
          </div>
          <div className="flex-1 overflow-y-auto divide-y divide-border/60">
            {loadingList ? (
              <ListSkeleton />
            ) : filteredConversations.length === 0 ? (
              <div className="p-6 text-center font-mono text-xs text-muted-foreground/60 uppercase tracking-widest">
                No threads
              </div>
            ) : (
              filteredConversations.map((c) => (
                <ConversationRow
                  key={c.id}
                  convo={c}
                  active={c.channel === activeChannel}
                  onClick={() => openConversation(c)}
                />
              ))
            )}
          </div>
        </div>

        {/* Chat panel */}
        <div
          className={cn(
            "bg-card flex flex-col min-h-0",
            !activeChannel && "hidden lg:flex",
          )}
        >
          {activeConversation ? (
            <>
              <div className="px-4 py-3 border-b border-border flex items-center gap-3">
                <button
                  type="button"
                  onClick={closeChat}
                  className="lg:hidden p-3 -m-2 text-muted-foreground hover:text-foreground"
                >
                  <ChevronLeft className="size-4" />
                </button>
                <PeerAvatar name={activeConversation.name} size="md" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-semibold truncate">
                      {activeConversation.name}
                    </span>
                    {activeConversation.peerType && (
                      <PeerTypePill type={activeConversation.peerType} />
                    )}
                  </div>
                  <div className="text-mono-xs text-muted-foreground">
                    {activeConversation.type === "channel"
                      ? "broadcast channel"
                      : isRoom
                        ? roomLoggedIn
                          ? `room server · ${roomSession?.role ?? "joined"}`
                          : "room server · not joined"
                        : "direct message"}
                    {activeConversation.lastActive && (
                      <>
                        {" "}
                        · last {timeAgo(activeConversation.lastActive)}
                      </>
                    )}
                  </div>
                </div>
                <ChatHeaderMenu
                  companion={companionRef}
                  conversation={activeConversation}
                  onSearchToggle={() => {
                    setMsgSearchOpen((v) => !v);
                    if (msgSearchOpen) setMsgSearch("");
                  }}
                  onMessagesCleared={() => {
                    setMessages([]);
                    messageCacheRef.current.delete(activeConversation.channel);
                    reachedStartRef.current.delete(activeConversation.channel);
                  }}
                />
              </div>

              {msgSearchOpen && (
                <div className="px-3 py-2 border-b border-border flex items-center gap-2 bg-card">
                  <Search className="size-3.5 text-muted-foreground/50 shrink-0" />
                  <Input
                    value={msgSearch}
                    onChange={(e) => setMsgSearch(e.target.value)}
                    placeholder="Search messages..."
                    className="h-7 font-mono text-base md:text-xs rounded-none border-border bg-background"
                    autoFocus
                  />
                  <button
                    type="button"
                    onClick={() => { setMsgSearchOpen(false); setMsgSearch(""); }}
                    className="text-muted-foreground hover:text-foreground shrink-0"
                  >
                    <X className="size-3.5" />
                  </button>
                  {msgSearch && (
                    <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60 shrink-0">
                      {filteredMessages.length}
                    </span>
                  )}
                </div>
              )}

              <div className="relative flex-1 min-h-0 flex flex-col">
                <div
                  ref={scrollContainerRef}
                  onScroll={handleMessagesScroll}
                  className="flex-1 flex flex-col justify-end-safe overflow-y-auto overflow-x-hidden px-4 py-4 space-y-4 bg-background/30"
                >
                  {!loadingMsgs && loadingOlder && (
                    <div className="flex items-center justify-center py-2 text-muted-foreground/60">
                      <Loader2 className="size-4 animate-spin" />
                    </div>
                  )}
                  {loadingMsgs ? (
                    <MessagesSkeleton />
                  ) : groupedMessages.length === 0 ? (
                    <div className="h-full flex flex-col items-center justify-center text-center text-muted-foreground/50 gap-3">
                      <MessageSquare className="size-8" strokeWidth={1.2} />
                      <span className="font-mono text-xs uppercase tracking-[0.12em]">
                        No transmissions on record
                      </span>
                    </div>
                  ) : (
                    groupedMessages.map((group, gi) => (
                      <MessageGroup
                        key={`${group.sender}-${gi}-${group.messages[0].timestamp}`}
                        group={group}
                        ownName={companionName}
                        ownPubkey={ownPubkey}
                        onContext={onMessageContext}
                        onReply={handleReply}
                        onRetry={handleRetry}
                        onAddContact={onAddContact}
                        channelCtx={channelCtx}
                      />
                    ))
                  )}
                  <div ref={messagesEndRef} />
                </div>

                {unreadBelow > 0 && (
                  <button
                    type="button"
                    onClick={jumpToLatest}
                    className="absolute inset-x-0 bottom-3 mx-auto flex w-fit items-center gap-1.5 rounded-sm border border-primary/40 bg-card px-3 py-1.5 font-mono text-[10px] uppercase tracking-[0.12em] text-primary transition-colors hover:bg-primary/10"
                  >
                    <ArrowDown className="size-3" />
                    <span className="tabular-nums">{unreadBelow}</span>
                    {unreadBelow === 1 ? "new message" : "new messages"}
                  </button>
                )}
              </div>

              {isRoom && !roomLoggedIn ? (
                <RoomJoinBar
                  companion={companionRef}
                  pubkey={roomPubkey ?? ""}
                  checking={roomSessionLoading}
                  onJoined={setRoomSession}
                />
              ) : (
              <div className="relative border-t border-border bg-card">
                {emojiAC.dropdown}
                {mentionAC.dropdown}
                <div className="px-3 py-2 flex items-end gap-2">
                  <ComposerAttachMenu
                    companion={companionRef}
                    ownPubkey={ownPubkey}
                    ownName={companionName}
                    ownLat={ownPos?.lat}
                    ownLon={ownPos?.lon}
                    onInsert={insertAtCursor}
                  />
                  <div className="relative flex-1 min-w-0">
                    {/* min-h-9 is load-bearing: it overrides the Textarea base's min-h-16. */}
                    <Textarea
                      ref={composerRef}
                      rows={1}
                      value={composer}
                      onChange={(e: ChangeEvent<HTMLTextAreaElement>) => {
                        setComposer(e.target.value);
                        const caret =
                          e.target.selectionStart ?? e.target.value.length;
                        emojiAC.handleChange(e.target.value, caret);
                        mentionAC.handleChange(e.target.value, caret);
                      }}
                      onKeyDown={onComposerKey}
                      onBlur={() => {
                        emojiAC.close();
                        mentionAC.close();
                      }}
                      placeholder={composerLocked ? lockedHint : "transmit…"}
                      disabled={composerLocked}
                      className="resize-none rounded-none border-border font-mono text-base md:text-sm leading-6 min-h-9 max-h-25 pr-11 bg-background"
                    />
                    {isMobile ? (
                      <EmojiButton
                        active={emojiOpen}
                        onClick={() => setEmojiOpen((o) => !o)}
                      />
                    ) : (
                      <EmojiPicker onSelect={insertAtCursor} />
                    )}
                  </div>
                  <div className="flex flex-col items-end gap-1">
                    {countBesideSend ? charCounter : null}
                    <Button
                      onClick={send}
                      aria-label="Send"
                      disabled={
                        sending || !composer.trim() || overLimit || composerLocked
                      }
                      size="icon"
                      className="rounded-none size-10.5 before:-inset-x-0.5"
                    >
                      <Send className="size-6" />
                    </Button>
                  </div>
                </div>
                {isMobile ? null : (
                  <div className="px-3 pb-2 flex items-center justify-between">
                    <span className="text-mono-xs text-muted-foreground/60">
                      {isSensorThread
                        ? "alerts from this sensor — use Manage to configure it"
                        : roomReadOnly
                          ? "read-only access — posting disabled"
                          : "Enter sends · Shift+Enter newline"}
                    </span>
                    {charCounter}
                  </div>
                )}
                {isMobile && emojiOpen && (
                  <div className="border-t border-border">
                    <EmojiMartPanel
                      fullWidth
                      onSelect={(e) => insertAtCursor(e, false)}
                    />
                  </div>
                )}
              </div>
              )}
            </>
          ) : (
            <div className="h-full grid place-items-center text-center p-8">
              <div className="space-y-3">
                <Radar
                  className="size-10 mx-auto text-muted-foreground/40"
                  strokeWidth={1.2}
                />
                <div className="font-mono text-sm uppercase tracking-[0.12em] text-muted-foreground">
                  Select a thread to begin
                </div>
                <div className="text-mono-xs text-muted-foreground/60">
                  Choose a conversation from the panel.
                </div>
              </div>
            </div>
          )}
        </div>
      </section>

      {contextMsg && contextPos && (
        <ContextMenu
          msg={contextMsg}
          pos={contextPos}
          ownName={companionName}
          onCopy={() => {
            handleCopy(contextMsg);
            setContextMsg(null);
          }}
          onReply={() => {
            handleReply(contextMsg);
            setContextMsg(null);
          }}
          onViewPaths={() => {
            openModal("rxPaths", contextMsg);
            setContextMsg(null);
          }}
          onEchoes={() => {
            openModal("echoes", contextMsg);
            setContextMsg(null);
          }}
          onBlock={() => {
            handleBlock(contextMsg);
            setContextMsg(null);
          }}
          onDelete={() => {
            handleDelete(contextMsg);
            setContextMsg(null);
          }}
        />
      )}

      <AddContactDialog
        companion={companionRef}
        companionName={companionName}
        open={addContactOpen}
        onOpenChange={setAddContactOpen}
        initial={addContactPrefill ?? undefined}
        ownPubkey={ownPubkey ?? undefined}
      />

      <AddChannelDialog
        open={addChannelOpen}
        onOpenChange={setAddChannelOpen}
        existing={configuredChannels}
        onAdd={addChannel}
        prefillName={addChannelPrefill}
      />

      <Dialog open={!!modal} onOpenChange={(o) => !o && closeModal()}>
        <DialogContent className="rounded-none border-border bg-card max-w-xl">
          <DialogHeader>
            <div className="flex items-center gap-2">
              {modal?.backTo && (
                <button
                  type="button"
                  onClick={() => {
                    const back = modal.backTo!;
                    setModal(back);
                    if (back.kind === "path") openModal("path", back.message);
                    else if (back.kind === "echoes")
                      openModal("echoes", back.message);
                    else openModal("rxPaths", back.message);
                  }}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <ArrowLeft className="size-4" />
                </button>
              )}
              <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
                {modal?.kind === "path" && "Receive path"}
                {modal?.kind === "echoes" && "Echo observations"}
                {modal?.kind === "rxPaths" && "Reception paths"}
              </DialogTitle>
            </div>
            <DialogDescription className="font-mono text-xs text-muted-foreground">
              {modal?.message.sender}
              {modal?.message.timestamp && (
                <>
                  {" "}
                  · {formatDateTime(modal.message.timestamp)}
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[60dvh] overflow-y-auto">
            {modalLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-8 w-full" />
                <Skeleton className="h-8 w-3/4" />
                <Skeleton className="h-8 w-2/3" />
              </div>
            ) : (
              <>
                {modal?.kind === "path" && modalPath && (
                  <PathTimeline path={modalPath} />
                )}
                {(modal?.kind === "rxPaths" || modal?.kind === "echoes") && (
                  <HearingList hearings={hearings} />
                )}
              </>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// Row id over timestamp: a room stamps posts with its own RTC, which can be years out.
function convRecency(a: Conversation, b: Conversation): number {
  if (a.lastMessageId && b.lastMessageId) return b.lastMessageId - a.lastMessageId;
  return tsValue(b.lastActive) - tsValue(a.lastActive);
}

function tsValue(iso?: string): number {
  if (!iso) return 0;
  return new Date(iso).getTime() || 0;
}

// Like map, but hands back the original array when nothing changed. A new array identity is what
// the auto-scroll reads as "new traffic", so an update for a message that is not loaded must not
// manufacture one.
function mapChanged(arr: Message[], fn: (m: Message) => Message): Message[] {
  let changed = false;
  const next = arr.map((m) => {
    const updated = fn(m);
    if (updated !== m) changed = true;
    return updated;
  });
  return changed ? next : arr;
}

function mergeMessages(existing: Message[], incoming: Message[]): Message[] {
  if (incoming.length === 0) return existing;
  const byId = new Map<number, number>();
  const byKey = new Map<string, number>();
  existing.forEach((m, i) => {
    if (typeof m.id === "number" && m.id > 0) byId.set(m.id, i);
    else byKey.set(`${m.timestamp}:${m.sender}:${m.text}`, i);
  });
  const out = existing.slice();
  let appended = false;
  for (const m of incoming) {
    const idMatch = typeof m.id === "number" && m.id > 0 ? byId.get(m.id) : undefined;
    if (idMatch != null) {
      out[idMatch] = { ...out[idMatch], ...m };
      continue;
    }
    const k = `${m.timestamp}:${m.sender}:${m.text}`;
    const keyMatch = byKey.get(k);
    if (keyMatch != null) {
      out[keyMatch] = { ...out[keyMatch], ...m };
      continue;
    }
    out.push(m);
    appended = true;
    if (typeof m.id === "number" && m.id > 0) byId.set(m.id, out.length - 1);
    else byKey.set(k, out.length - 1);
  }
  if (appended) {
    // Messages with no id yet sort last: they are the newest.
    out.sort((a, b) => {
      const ia = typeof a.id === "number" && a.id > 0 ? a.id : Number.MAX_SAFE_INTEGER;
      const ib = typeof b.id === "number" && b.id > 0 ? b.id : Number.MAX_SAFE_INTEGER;
      if (ia !== ib) return ia - ib;
      return tsValue(a.timestamp) - tsValue(b.timestamp);
    });
  }
  return out;
}

interface MessageGroupShape {
  sender: string;
  direction: string;
  messages: Message[];
}

function groupMessages(messages: Message[]): MessageGroupShape[] {
  const out: MessageGroupShape[] = [];
  for (const m of messages) {
    const last = out[out.length - 1];
    if (
      last &&
      last.sender === m.sender &&
      last.direction === m.direction &&
      Math.abs(
        new Date(m.timestamp).getTime() -
          new Date(last.messages[last.messages.length - 1].timestamp).getTime(),
      ) <
        5 * 60 * 1000
    ) {
      last.messages.push(m);
    } else {
      out.push({ sender: m.sender, direction: m.direction, messages: [m] });
    }
  }
  return out;
}

function ConversationRow({
  convo,
  active,
  onClick,
}: {
  convo: Conversation;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        // content-visibility lets the browser skip layout/paint offscreen.
        "w-full px-3 py-2.5 flex items-start gap-3 text-left transition-colors group [content-visibility:auto] [contain-intrinsic-size:auto_64px]",
        active
          ? "bg-primary/10 border-l-2 border-primary"
          : "hover:bg-muted/40 border-l-2 border-transparent",
      )}
    >
      <PeerAvatar name={convo.name} size="md" />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex items-center justify-between gap-2">
          <span
            className={cn(
              "truncate font-medium text-sm",
              convo.unreadCount > 0 && "text-foreground",
            )}
          >
            {convo.name}
          </span>
          <span className="font-mono text-[10px] tabular-nums text-muted-foreground/70 shrink-0">
            {convo.lastActive ? formatShortTime(convo.lastActive) : ""}
          </span>
        </div>
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-xs text-muted-foreground">
            {convo.lastMessage ? (
              <>
                {convo.lastMessage.direction === "tx" && (
                  <span className="text-primary/80 font-mono uppercase tracking-widest mr-1">
                    me ›
                  </span>
                )}
                {convo.lastMessage.text.replace(MENTION_RE, "@$1")}
              </>
            ) : (
              <span className="italic text-muted-foreground/50">
                no messages
              </span>
            )}
          </span>
          {convo.unreadCount > 0 && (
            <span className="shrink-0 font-mono text-[10px] tabular-nums px-1.5 py-0.5 bg-primary/15 text-primary border border-primary/30">
              {convo.unreadCount}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1.5 pt-0.5">
          {convo.peerType && <PeerTypePill type={convo.peerType} />}
          {convo.isRepeater && (
            <span className="font-mono text-[9px] uppercase tracking-widest px-1 py-0.5 border border-warning/40 text-warning bg-warning/5">
              repeater
            </span>
          )}
          {convo.type === "channel" && (
            <span className="font-mono text-[9px] uppercase tracking-widest text-muted-foreground/60">
              # channel
            </span>
          )}
        </div>
      </div>
    </button>
  );
}

// Replaces the composer for a room thread until a login session exists.
function RoomJoinBar({
  companion,
  pubkey,
  checking,
  onJoined,
}: {
  companion: string;
  pubkey: string;
  checking: boolean;
  onJoined: (s: RoomSession) => void;
}) {
  const [password, setPassword] = useState("");
  const [savePw, setSavePw] = useState(false);
  const [savedPw, setSavedPw] = useState("");
  const [metadata, setMetadata] = useState<Record<string, unknown> | null>(
    null,
  );
  const [joining, setJoining] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setPassword("");
    setSavedPw("");
    setSavePw(false);
    setMetadata(null);
    fetch(
      `/api/companions/${encodeURIComponent(companion)}/contacts/${pubkey}`,
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((c: { metadata?: Record<string, unknown> } | null) => {
        if (cancelled || !c) return;
        const meta = c.metadata ?? {};
        setMetadata(meta);
        const pw =
          typeof meta.roomPassword === "string" ? meta.roomPassword : "";
        setSavedPw(pw);
        if (pw) setSavePw(true);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [companion, pubkey]);

  const join = useCallback(async () => {
    setJoining(true);
    try {
      const pw = password || savedPw;
      const r = await fetch(
        `/api/companions/${encodeURIComponent(companion)}/rooms/${pubkey}/login`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ password: pw }),
        },
      );
      if (!r.ok) {
        const err = (await r.json().catch(() => null)) as {
          error?: string;
        } | null;
        throw new Error(err?.error || "Join failed");
      }
      const result = (await r.json()) as { role?: string };

      const desired = savePw ? pw : "";
      if (metadata && (metadata.roomPassword ?? "") !== desired) {
        await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts/${pubkey}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ roomPassword: desired }),
          },
        ).catch(() => {});
      }

      const sr = await fetch(
        `/api/companions/${encodeURIComponent(companion)}/rooms/${pubkey}/session`,
      );
      const sess = sr.ok ? ((await sr.json()) as RoomSession) : null;
      onJoined(
        sess && sess.pubkeyHex
          ? sess
          : { pubkeyHex: pubkey, role: result.role },
      );
      toast.success(`Joined room${result.role ? ` · ${result.role}` : ""}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to join room");
    } finally {
      setJoining(false);
    }
  }, [companion, pubkey, password, savedPw, savePw, metadata, onJoined]);

  return (
    <div className="border-t border-border bg-card px-3 py-3 space-y-2">
      <div className="flex items-center gap-2 font-mono text-[11px] uppercase tracking-[0.12em] text-muted-foreground">
        <LogIn className="size-3.5" />
        Join room to post &amp; sync messages
      </div>
      <div className="flex items-center gap-2">
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void join();
          }}
          placeholder={
            savedPw ? "•••••• (saved)" : "room password (blank to re-auth)"
          }
          disabled={joining}
          className="h-9 font-mono text-sm rounded-none border-border bg-background"
        />
        <Button
          onClick={join}
          disabled={joining || checking}
          size="sm"
          className="rounded-none h-9 font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {joining ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <LogIn className="size-3.5" />
          )}
          join
        </Button>
      </div>
      <label className="flex items-center gap-2 cursor-pointer text-mono-xs text-muted-foreground w-fit">
        <Switch checked={savePw} onCheckedChange={setSavePw} />
        save password to contact
      </label>
    </div>
  );
}

function MessageGroup({
  group,
  ownName,
  ownPubkey,
  onContext,
  onReply,
  onRetry,
  onAddContact,
  channelCtx,
}: {
  group: MessageGroupShape;
  ownName: string;
  ownPubkey: string | null;
  onContext: (e: React.MouseEvent, m: Message) => void;
  onReply: (m: Message) => void;
  onRetry: (m: Message) => void;
  onAddContact: (prefill: ContactPrefill) => void;
  channelCtx: ChannelLinkCtx;
}) {
  const isTx = group.direction === "tx";
  return (
    <div
      className={cn(
        "flex gap-2",
        isTx ? "flex-row-reverse" : "flex-row",
      )}
    >
      <div className="shrink-0 pt-0.5">
        <PeerAvatar name={group.sender} size="sm" />
      </div>
      <div
        className={cn(
          "max-w-[90%] sm:max-w-[68%] flex flex-col gap-1",
          isTx ? "items-end" : "items-start",
        )}
      >
        <div className="flex items-baseline gap-2 px-1">
          <span className="font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
            {isTx ? "me" : group.sender}
          </span>
          <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
            {formatShortTime(group.messages[0].timestamp)}
          </span>
        </div>
        {group.messages.map((m, i) => (
          <MessageBubble
            key={m.id ?? `${m.timestamp}-${i}`}
            msg={m}
            isTx={isTx}
            ownName={ownName}
            ownPubkey={ownPubkey}
            onContext={onContext}
            onReply={onReply}
            onRetry={onRetry}
            onAddContact={onAddContact}
            channelCtx={channelCtx}
          />
        ))}
      </div>
    </div>
  );
}

function MessageBubble({
  msg,
  isTx,
  ownName,
  ownPubkey,
  onContext,
  onReply,
  onRetry,
  onAddContact,
  channelCtx,
}: {
  msg: Message;
  isTx: boolean;
  ownName: string;
  ownPubkey: string | null;
  onContext: (e: React.MouseEvent, m: Message) => void;
  onReply: (m: Message) => void;
  onRetry: (m: Message) => void;
  onAddContact: (prefill: ContactPrefill) => void;
  channelCtx: ChannelLinkCtx;
}) {
  const longPressTimer = useRef<number | null>(null);
  const longPressTriggered = useRef(false);
  const [tapped, setTapped] = useState(false);

  const onTouchStart = (e: React.TouchEvent) => {
    longPressTriggered.current = false;
    const t = e.touches[0];
    longPressTimer.current = window.setTimeout(() => {
      longPressTriggered.current = true;
      const fakeEvt = {
        preventDefault: () => {},
        clientX: t.clientX,
        clientY: t.clientY,
      } as unknown as React.MouseEvent;
      onContext(fakeEvt, msg);
    }, 500);
  };
  const onTouchEnd = () => {
    if (longPressTimer.current) {
      window.clearTimeout(longPressTimer.current);
      longPressTimer.current = null;
    }
  };

  const onBubbleClick = () => {
    if (longPressTriggered.current) return;
    setTapped((v) => !v);
  };

  return (
    <div
      className={cn(
        "group/msg flex items-center gap-1.5",
        isTx ? "flex-row-reverse" : "flex-row",
      )}
    >
      <div
        onContextMenu={(e) => onContext(e, msg)}
        onTouchStart={onTouchStart}
        onTouchEnd={onTouchEnd}
        onTouchMove={onTouchEnd}
        onClick={onBubbleClick}
        className={cn(
          "px-3 py-1.5 rounded-sm border text-sm leading-snug whitespace-pre-wrap wrap-break-word select-none sm:select-auto",
          isTx
            ? "bg-primary/15 border-primary/30 text-foreground"
            : "bg-card border-border",
        )}
      >
        <MessageText
          text={msg.text}
          ownName={ownName}
          ownPubkey={ownPubkey}
          onAddContact={onAddContact}
          channelCtx={channelCtx}
        />
        {(msg.snr != null ||
          (msg.repeatCount != null && msg.repeatCount > 0) ||
          (isTx && msg.status)) && (
          <div
            className={cn(
              "mt-1 flex items-center gap-2 font-mono text-[10px] tabular-nums",
              isTx ? "justify-end" : "justify-start",
            )}
          >
            {!isTx && msg.snr != null && (
              <span className={snrTextClass(msg.snr)}>{msg.snr.toFixed(1)}dB</span>
            )}
            {msg.hops != null && msg.hops > 0 && (
              // Hash size only means something alongside a path, so it rides the hops span.
              <span
                className="text-muted-foreground/60"
                title={msg.pathHashSize != null ? `${msg.pathHashSize}-byte path hashes` : undefined}
              >
                {msg.hops} hop{msg.hops > 1 ? "s" : ""}
                {msg.pathHashSize != null && ` · ${msg.pathHashSize}B`}
              </span>
            )}
            {msg.repeatCount != null && msg.repeatCount > 0 && (
              // On RX this counts the first reception plus each repeat; on TX only the echoes.
              <span
                className="text-success/80 inline-flex items-center gap-1"
                title={
                  isTx
                    ? `Repeated ${msg.repeatCount}×`
                    : `Heard ${msg.repeatCount + 1}× total`
                }
              >
                <Reply className="size-2.5" />×
                {isTx ? msg.repeatCount : msg.repeatCount + 1}
              </span>
            )}
            {isTx && msg.status === "sending" && (
              <span className="text-muted-foreground/60 inline-flex items-center gap-1">
                <Loader2 className="size-2.5 animate-spin" />sending
              </span>
            )}
            {isTx && msg.status === "delivered" && (
              <span className="text-success/80 inline-flex items-center gap-1">
                <CheckCheck className="size-2.5" />delivered
              </span>
            )}
            {isTx && msg.status === "failed" && (
              <span className="text-destructive inline-flex items-center gap-1">
                <AlertCircle className="size-2.5" />failed
                <button
                  type="button"
                  onClick={() => onRetry(msg)}
                  className="ml-1 inline-flex items-center gap-0.5 text-primary hover:text-primary/80"
                >
                  <RotateCw className="size-2.5" />retry
                </button>
              </span>
            )}
          </div>
        )}
      </div>
      <div
        className={cn(
          "flex items-center gap-0.5 transition-opacity",
          // Hidden must also mean untappable: opacity alone still took the tap.
          tapped
            ? "opacity-100"
            : "opacity-0 pointer-events-none group-hover/msg:opacity-100 group-hover/msg:pointer-events-auto focus-within:opacity-100 focus-within:pointer-events-auto",
          isTx ? "flex-row-reverse" : "flex-row",
        )}
      >
        <button
          type="button"
          aria-label="Reply"
          onClick={() => onReply(msg)}
          className="size-7 grid place-items-center text-muted-foreground/70 hover:text-primary hover:bg-muted/60 border border-transparent hover:border-border rounded-sm"
        >
          <CornerUpLeft className="size-3.5" strokeWidth={1.6} />
        </button>
        <button
          type="button"
          aria-label="More"
          onClick={(e) => {
            const rect = (e.currentTarget as HTMLButtonElement).getBoundingClientRect();
            const fake = {
              preventDefault: () => {},
              clientX: rect.left,
              clientY: rect.bottom + 4,
            } as unknown as React.MouseEvent;
            onContext(fake, msg);
          }}
          className="size-7 grid place-items-center text-muted-foreground/70 hover:text-foreground hover:bg-muted/60 border border-transparent hover:border-border rounded-sm"
        >
          <MoreVertical className="size-3.5" strokeWidth={1.6} />
        </button>
      </div>
    </div>
  );
}

function MessageText({
  text,
  ownName,
  ownPubkey,
  onAddContact,
  channelCtx,
}: {
  text: string;
  ownName: string;
  ownPubkey: string | null;
  onAddContact: (prefill: ContactPrefill) => void;
  channelCtx: ChannelLinkCtx;
}) {
  const parts: ReactNode[] = [];
  let last = 0;
  let key = 0;
  for (const match of text.matchAll(TOKEN_RE)) {
    const idx = match.index ?? 0;
    const token = match[0];
    const g = match.groups ?? {};

    // Skipped without consuming, so an out-of-range coordinate falls into the next text slice.
    const coord = g.coord ? parseCoord(token) : null;
    if (g.coord && !coord) continue;

    if (idx > last) parts.push(text.slice(last, idx));

    if (g.contact) {
      const m = /^<([0-9a-fA-F]{64}):([1-4]):([^>]*)>$/.exec(token);
      if (m) {
        parts.push(
          <ContactCard
            key={`c-${key++}`}
            pubkey={m[1].toLowerCase()}
            type={CONTACT_TYPE_BY_INT[m[2]]}
            name={m[3]}
            ownPubkey={ownPubkey}
            onAdd={onAddContact}
          />,
        );
      } else {
        parts.push(token);
      }
    } else if (g.url) {
      const { url, trailing } = splitTrailingPunct(token);
      parts.push(<UrlLink key={`u-${key++}`} url={url} />);
      if (trailing) parts.push(trailing);
    } else if (g.coord && coord) {
      parts.push(
        <CoordLink key={`g-${key++}`} lat={coord.lat} lon={coord.lon} raw={token} />,
      );
    } else if (g.mention) {
      const target = token.slice(2, -1);
      const isSelf = target === ownName;
      parts.push(
        <span
          key={`m-${key++}`}
          className={cn(
            "px-1 -mx-0.5 rounded-sm font-medium",
            isSelf
              ? "bg-warning/20 text-warning"
              : "text-primary font-semibold",
          )}
        >
          @{target}
        </span>,
      );
    } else if (g.channel) {
      parts.push(
        <ChannelChip key={`ch-${key++}`} raw={token} channels={channelCtx} />,
      );
    }
    last = idx + token.length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

// A hashtag carries no key, so an unknown channel goes through the Add Channel dialog.
function ChannelChip({
  raw,
  channels,
}: {
  raw: string;
  channels: ChannelLinkCtx;
}) {
  const bare = raw.slice(1); // strip the '#' sigil (for display + prefill)
  // channelKey, so this can't drift from how the target map was keyed.
  const target = channels.targets.get(channelKey(raw));
  const isAdded = target !== undefined;
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        if (isAdded) channels.onOpen(target);
        else channels.onAdd(bare);
      }}
      title={isAdded ? `Open ${raw}` : `Add channel ${raw}`}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-sm px-1 -mx-0.5 font-medium align-baseline",
        isAdded
          ? "text-primary hover:bg-primary/10"
          : "text-info hover:bg-info/10",
      )}
    >
      <Hash className="size-3 shrink-0" />
      {bare}
      {!isAdded && <Plus className="size-2.5 shrink-0 opacity-70" />}
    </button>
  );
}

// Adding routes through the pre-filled Add Contact modal so the user reviews first.
function ContactCard({
  pubkey,
  type,
  name,
  ownPubkey,
  onAdd,
}: {
  pubkey: string;
  type?: ContactType;
  name: string;
  ownPubkey: string | null;
  onAdd: (prefill: ContactPrefill) => void;
}) {
  const isSelf = !!ownPubkey && pubkey.toLowerCase() === ownPubkey.toLowerCase();
  const displayName = name.trim() || "unknown";
  return (
    <div className="my-1 max-w-[18rem] select-text rounded-sm border border-border bg-background/60 p-2.5">
      <div className="flex items-center gap-2">
        <PeerAvatar name={displayName} size="sm" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-sm font-medium">{displayName}</span>
            {type && <PeerTypePill type={type} />}
          </div>
          <code className="mt-0.5 block truncate font-mono text-[10px] text-muted-foreground">
            {truncateMid(pubkey, 6, 6)}
          </code>
        </div>
      </div>
      {isSelf ? (
        <div className="mt-2 border border-dashed border-border py-1.5 text-center font-mono text-[10px] uppercase tracking-widest text-muted-foreground/70">
          This is you
        </div>
      ) : (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onAdd({ pubkey, name: name.trim(), type });
          }}
          className="mt-2 inline-flex w-full items-center justify-center gap-1.5 border border-primary/40 bg-primary/10 py-1.5 font-mono text-[11px] uppercase tracking-widest text-primary transition-colors hover:bg-primary/20"
        >
          <UserPlus className="size-3.5" /> Add Contact
        </button>
      )}
    </div>
  );
}

// Message text is untrusted, so opening is confirmed; the tokenizer admits only http/https.
function UrlLink({ url }: { url: string }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          onClick={(e) => e.stopPropagation()}
          className="break-all text-info underline decoration-dotted underline-offset-2 hover:text-info/80"
        >
          {url}
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        onClick={(e) => e.stopPropagation()}
        className="w-72 space-y-2 rounded-none p-3"
      >
        <p className="label-overline">Open external link?</p>
        <p className="break-all font-mono text-[11px] text-muted-foreground">
          {url}
        </p>
        <div className="flex justify-end gap-2 pt-1">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setOpen(false)}
            className="font-mono uppercase tracking-widest"
          >
            Cancel
          </Button>
          <Button
            size="xs"
            onClick={() => {
              window.open(url, "_blank", "noopener,noreferrer");
              setOpen(false);
            }}
            className="font-mono uppercase tracking-widest"
          >
            <ExternalLink className="size-3" /> Open
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// --bottom-nav is a calc() over env(), which getPropertyValue hands back unevaluated, so let the
// browser resolve it. 0px once the sidebar takes over, which is what the media query already says.
function bottomNavHeight(): number {
  const probe = document.createElement("div");
  probe.style.cssText =
    "position:absolute;visibility:hidden;height:var(--bottom-nav)";
  document.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

function ContextMenu({
  msg,
  pos,
  ownName,
  onCopy,
  onReply,
  onViewPaths,
  onEchoes,
  onBlock,
  onDelete,
}: {
  msg: Message;
  pos: { x: number; y: number };
  ownName: string;
  onCopy: () => void;
  onReply: () => void;
  onViewPaths: () => void;
  onEchoes: () => void;
  onBlock: () => void;
  onDelete: () => void;
}) {
  const isRx = msg.direction === "rx";
  const showEchoes =
    !isRx && msg.repeatCount != null && msg.repeatCount > 0;
  const ref = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState({ top: pos.y, left: pos.x });
  // The menu is 3-6 items depending on the message, so its size is measured rather than assumed,
  // and the bottom nav overlays the viewport — clamping to innerHeight alone hides the last item.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const { width, height } = el.getBoundingClientRect();
    const gutter = 8;
    const floor = window.innerHeight - bottomNavHeight() - gutter;
    setPlace({
      top:
        pos.y + height > floor
          ? Math.max(gutter, pos.y - height)
          : pos.y,
      left: Math.max(gutter, Math.min(pos.x, window.innerWidth - width - gutter)),
    });
  }, [pos]);
  return (
    <div
      ref={ref}
      role="menu"
      data-context-menu
      className="fixed z-60 min-w-40 bg-popover border border-border rounded-sm shadow-md py-1 text-sm"
      style={place}
      onClick={(e) => e.stopPropagation()}
    >
      <CtxItem icon={<Copy className="size-3.5" />} onClick={onCopy}>
        Copy
      </CtxItem>
      <CtxItem icon={<CornerUpLeft className="size-3.5" />} onClick={onReply}>
        Reply
      </CtxItem>
      {isRx && (
        <CtxItem icon={<Radar className="size-3.5" />} onClick={onViewPaths}>
          View paths
        </CtxItem>
      )}
      {showEchoes && (
        <CtxItem icon={<Reply className="size-3.5" />} onClick={onEchoes}>
          Echoes
        </CtxItem>
      )}
      {isRx && msg.sender !== ownName && (
        <CtxItem
          icon={<Ban className="size-3.5" />}
          onClick={onBlock}
          variant="destructive"
        >
          Block sender
        </CtxItem>
      )}
      <CtxItem
        icon={<Trash2 className="size-3.5" />}
        onClick={onDelete}
        variant="destructive"
      >
        Delete
      </CtxItem>
    </div>
  );
}

function CtxItem({
  icon,
  onClick,
  children,
  variant,
}: {
  icon: ReactNode;
  onClick: () => void;
  children: ReactNode;
  variant?: "destructive";
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "w-full text-left px-3 py-1.5 min-h-10 sm:min-h-0 flex items-center gap-2 font-mono text-xs uppercase tracking-[0.08em] hover:bg-muted/60",
        variant === "destructive" && "text-destructive",
      )}
    >
      <span className="text-muted-foreground/70">{icon}</span>
      {children}
    </button>
  );
}

function PathTimeline({ path }: { path: PathInfo }) {
  if (!path.path || path.path.length === 0) {
    return (
      <p className="font-mono text-xs text-muted-foreground/60 px-1">
        No path hops recorded.
      </p>
    );
  }
  return (
    <div className="space-y-2 px-1">
      <div className="flex items-center gap-3 text-mono-xs text-muted-foreground">
        <span>hops · {path.hops}</span>
        <span>hashSize · {path.pathHashSize}</span>
        <span>sender · {path.sender}</span>
      </div>
      <ol className="relative pl-6 border-l border-border">
        {path.path.map((hop, i) => (
          <li key={`${hop.hash}-${i}`} className="relative pb-3">
            <span className="absolute left-[-1.04rem] top-0.5 size-3.5 grid place-items-center bg-card border border-border font-mono text-[9px] tabular-nums">
              {i + 1}
            </span>
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-mono text-xs">
                {hop.peerNames && hop.peerNames.length > 0
                  ? hop.peerNames.join(" / ")
                  : (
                      <span className="text-muted-foreground italic">
                        unknown
                      </span>
                    )}
              </span>
              <code className="font-mono text-[10px] text-muted-foreground/70">
                {hop.hash}
              </code>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}

function HearingList({ hearings }: { hearings: Hearing[] }) {
  if (hearings.length === 0) {
    return (
      <p className="font-mono text-xs text-muted-foreground/60 px-1 mt-3">
        No reception data.
      </p>
    );
  }
  return (
    <div className="mt-3 space-y-1.5 px-1">
      <div className="text-mono-xs text-muted-foreground">
        Heard {hearings.length}× via {hearings.length === 1 ? "1 path" : `${hearings.length} paths`}
      </div>
      {hearings.map((h, i) => (
        <HearingRow key={h.key} hearing={h} first={i === 0} />
      ))}
    </div>
  );
}

// Expands to the full hop-by-hop path; a direct (0-hop) hearing has nothing to expand.
function HearingRow({ hearing, first }: { hearing: Hearing; first: boolean }) {
  const [open, setOpen] = useState(false);
  const direct = hearing.path.length === 0;
  const time = formatClockTime(hearing.receivedAt);
  return (
    <div className="border border-border bg-card/50">
      <button
        type="button"
        disabled={direct}
        onClick={() => !direct && setOpen((v) => !v)}
        className={cn(
          "w-full flex items-center gap-2 p-2.5 text-left",
          !direct && "hover:bg-muted/40",
        )}
      >
        <ChevronRight
          className={cn(
            "size-3 shrink-0 text-muted-foreground/60 transition-transform",
            open && "rotate-90",
            direct && "opacity-0",
          )}
        />
        <span className="flex items-center gap-1.5 font-mono text-xs min-w-0">
          {direct ? (
            <Radio className="size-3 shrink-0 text-success/70" />
          ) : (
            <Radar className="size-3 shrink-0 text-muted-foreground/60" />
          )}
          <span className="truncate">{hearingVia(hearing.path, hearing.hops)}</span>
        </span>
        <span className="ml-auto flex items-center gap-2 font-mono text-[10px] tabular-nums uppercase tracking-widest text-muted-foreground shrink-0">
          {first && <span className="text-primary/70">first</span>}
          <span>{hearing.hops} hop{hearing.hops === 1 ? "" : "s"}</span>
          {hearing.snr != null && (
            <span className={snrTextClass(hearing.snr)}>{hearing.snr.toFixed(1)}dB</span>
          )}
          <span>{time}</span>
        </span>
      </button>
      {open && !direct && (
        <ol className="pl-6 pr-3 pb-2.5 pt-2 border-t border-border/60 space-y-1">
          {hearing.path.map((hop, i) => (
            <li
              key={`${hop.hash}-${i}`}
              className="font-mono text-[11px] flex items-baseline justify-between gap-2"
            >
              <span>
                {hop.peerNames?.[0] || (
                  <span className="text-muted-foreground/60 italic">unknown</span>
                )}
              </span>
              <code className="text-muted-foreground/60 text-[10px]">{hop.hash}</code>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

function ListSkeleton() {
  return (
    <div className="p-3 space-y-3">
      {[...Array(5)].map((_, i) => (
        <div key={i} className="flex items-start gap-3">
          <Skeleton className="size-9 rounded-sm" />
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-2.5 w-40" />
          </div>
        </div>
      ))}
    </div>
  );
}

function MessagesSkeleton() {
  return (
    <div className="space-y-3">
      {[...Array(4)].map((_, i) => (
        <div
          key={i}
          className={cn(
            "flex gap-2",
            i % 2 === 0 ? "flex-row" : "flex-row-reverse",
          )}
        >
          <Skeleton className="size-7 rounded-sm" />
          <Skeleton className="h-10 w-48 rounded-sm" />
        </div>
      ))}
    </div>
  );
}

