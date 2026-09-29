import { useEffect, useRef, useState } from "react";
import {
  Antenna,
  AudioLines,
  Bot,
  Download,
  Gauge,
  LayoutDashboard,
  MapPinned,
  Menu,
  MessagesSquare,
  Moon,
  Radar,
  Radio,
  Rss,
  Settings,
  Sun,
  Thermometer,
  Users,
  Waves,
} from "lucide-react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { SystemStatus } from "@/components/SystemStatus";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";
import { Button } from "@/components/ui/button";
import { PeerAvatar } from "@/components/PeerAvatar";
import { useApiList } from "@/hooks/useApiList";
import { useInstallPrompt } from "@/hooks/useInstallPrompt";
import { type ConfigCompanion } from "@/lib/configApi";
import { useTheme } from "@/lib/theme";
import { truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useCompanionsChanged } from "@/lib/companionsChanged";
import {
  companionIdFromRef,
  companionPath,
  findByRef,
} from "@/lib/companionRef";

type IconType = React.ComponentType<React.SVGProps<SVGSVGElement>>;

interface NavItem {
  to: string;
  label: string;
  icon: IconType;
  match: (path: string) => boolean;
}

const PRIMARY: NavItem[] = [
  {
    to: "/",
    label: "Overview",
    icon: LayoutDashboard,
    match: (p) => p === "/",
  },
  { to: "/peers", label: "Peers", icon: Users, match: (p) => p === "/peers" },
  { to: "/map", label: "Map", icon: MapPinned, match: (p) => p === "/map" },
];

const TELEMETRY: NavItem[] = [
  {
    to: "/packets",
    label: "Packets",
    icon: AudioLines,
    match: (p) => p === "/packets",
  },
  {
    to: "/traces",
    label: "Trace",
    icon: Radar,
    match: (p) => p === "/traces",
  },
  {
    to: "/monitoring",
    label: "Monitoring",
    icon: Gauge,
    match: (p) => p.startsWith("/monitoring"),
  },
  {
    to: "/radio",
    label: "Radio",
    icon: Antenna,
    match: (p) => p === "/radio",
  },
  {
    to: "/discover",
    label: "Discover",
    icon: Radar,
    match: (p) => p === "/discover",
  },
  {
    to: "/sensors",
    label: "Sensors",
    icon: Thermometer,
    match: (p) => p === "/sensors",
  },
];

const COMMS_EXTRA: NavItem[] = [
  {
    to: "/repeater",
    label: "Repeater",
    icon: Radio,
    match: (p) => p === "/repeater",
  },
  {
    to: "/mqtt",
    label: "MQTT",
    icon: Rss,
    match: (p) => p === "/mqtt",
  },
];

const SYSTEM: NavItem[] = [
  {
    to: "/settings",
    label: "Settings",
    icon: Settings,
    match: (p) => p === "/settings",
  },
];

function ThemeToggle() {
  const { resolved, setTheme } = useTheme();
  const isDark = resolved === "dark";
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setTheme(isDark ? "light" : "dark")}
      className="w-full justify-start gap-2 text-xs uppercase tracking-widest font-mono text-muted-foreground hover:text-foreground"
    >
      {isDark ? (
        <Sun className="size-3.5" />
      ) : (
        <Moon className="size-3.5" />
      )}
      <span>{isDark ? "light" : "dark"} mode</span>
    </Button>
  );
}

// Only Chromium offers an install prompt we can replay, and only when not already installed.
function InstallButton() {
  const { canInstall, promptInstall } = useInstallPrompt();
  if (!canInstall) return null;
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => void promptInstall()}
      className="w-full justify-start gap-2 text-xs uppercase tracking-widest font-mono text-primary hover:text-primary hover:bg-primary/10"
    >
      <Download className="size-3.5" />
      <span>install app</span>
    </Button>
  );
}

function NavSection({
  label,
  items,
  pathname,
}: {
  label: string;
  items: NavItem[];
  pathname: string;
}) {
  const { isMobile, setOpenMobile } = useSidebar();
  return (
    <SidebarGroup>
      <SidebarGroupLabel className="font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground/70">
        {label}
      </SidebarGroupLabel>
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((item) => {
            const active = item.match(pathname);
            const Icon = item.icon;
            return (
              <SidebarMenuItem key={item.to}>
                <SidebarMenuButton
                  asChild
                  isActive={active}
                  tooltip={item.label}
                  className={cn(
                    "rounded-sm font-mono text-[12px] uppercase tracking-[0.08em] transition-colors h-8",
                    active &&
                      "bg-primary/10 text-primary border-l-2 border-primary rounded-l-none data-[active=true]:bg-primary/10",
                  )}
                >
                  <Link
                    to={item.to}
                    onClick={() => isMobile && setOpenMobile(false)}
                  >
                    <Icon className="size-4" strokeWidth={1.6} />
                    <span>{item.label}</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            );
          })}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

// Sub-items auto-hide when the sidebar collapses to icons (SidebarMenuSub is icon-collapse-hidden).
function CommsSection({
  companions,
  pathname,
}: {
  companions: ConfigCompanion[];
  pathname: string;
}) {
  const { isMobile, setOpenMobile } = useSidebar();
  const closeMobile = () => isMobile && setOpenMobile(false);

  // The selected companion (if any) from /companions/<ref>[/...].
  const seg = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  const activeItem =
    seg[0] === "companions" && seg[1]
      ? findByRef(seg[1], companions)
      : undefined;

  return (
    <SidebarGroup>
      <SidebarGroupLabel className="font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground/70">
        Comms
      </SidebarGroupLabel>
      <SidebarGroupContent>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              // Active only on the management list; a selected companion lights its own sub-item.
              isActive={pathname === "/companions"}
              tooltip="Companions"
              className={cn(
                "rounded-sm font-mono text-[12px] uppercase tracking-[0.08em] transition-colors h-8",
                pathname === "/companions" &&
                  "bg-primary/10 text-primary border-l-2 border-primary rounded-l-none data-[active=true]:bg-primary/10",
              )}
            >
              <Link to="/companions" onClick={closeMobile}>
                <MessagesSquare className="size-4" strokeWidth={1.6} />
                <span>Companions</span>
              </Link>
            </SidebarMenuButton>
            {companions.length > 0 && (
              <SidebarMenuSub>
                {companions.map((c) => {
                  const active = activeItem === c;
                  return (
                    <SidebarMenuSubItem key={c.id}>
                      <SidebarMenuSubButton
                        asChild
                        isActive={active}
                        className={cn(
                          "h-7 gap-2 font-mono text-xs",
                          active &&
                            "bg-primary/10 text-primary data-[active=true]:bg-primary/10 data-[active=true]:text-primary",
                        )}
                      >
                        <Link
                          to={companionPath(c)}
                          onClick={closeMobile}
                          title={c.name}
                        >
                          <PeerAvatar name={c.name} size="xs" />
                          <span>{c.name}</span>
                        </Link>
                      </SidebarMenuSubButton>
                    </SidebarMenuSubItem>
                  );
                })}
              </SidebarMenuSub>
            )}
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              isActive={pathname === "/bots"}
              tooltip="Bots"
              className={cn(
                "rounded-sm font-mono text-[12px] uppercase tracking-[0.08em] transition-colors h-8",
                pathname === "/bots" &&
                  "bg-primary/10 text-primary border-l-2 border-primary rounded-l-none data-[active=true]:bg-primary/10",
              )}
            >
              <Link to="/bots" onClick={closeMobile}>
                <Bot className="size-4" strokeWidth={1.6} />
                <span>Bots</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
          {COMMS_EXTRA.map((item) => {
            const active = item.match(pathname);
            return (
              <SidebarMenuItem key={item.to}>
                <SidebarMenuButton
                  asChild
                  isActive={active}
                  tooltip={item.label}
                  className={cn(
                    "rounded-sm font-mono text-[12px] uppercase tracking-[0.08em] transition-colors h-8",
                    active &&
                      "bg-primary/10 text-primary border-l-2 border-primary rounded-l-none data-[active=true]:bg-primary/10",
                  )}
                >
                  <Link to={item.to} onClick={closeMobile}>
                    <item.icon className="size-4" strokeWidth={1.6} />
                    <span>{item.label}</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            );
          })}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const pathname = location.pathname;

  // Config has no WS topic, and the roster only changes on `/companions*`, so refresh around that nav.
  const { items: companions, reload: reloadCompanions } =
    useApiList<ConfigCompanion>(
      "/api/config/companions",
      "Failed to load companions",
    );
  const prevPath = useRef(pathname);
  useEffect(() => {
    const from = prevPath.current;
    prevPath.current = pathname;
    if (from === pathname) return; // initial mount: useApiList already fetched
    if (from.startsWith("/companions") || pathname.startsWith("/companions")) {
      reloadCompanions();
    }
  }, [pathname, reloadCompanions]);
  useCompanionsChanged(reloadCompanions);

  // A link made before refs existed still resolves, but swapping it for the ref on arrival means
  // the next rename cannot break the bookmark the user just followed.
  const navigate = useNavigate();
  useEffect(() => {
    const seg = pathname.split("/").filter(Boolean).map(decodeURIComponent);
    if (seg[0] !== "companions" || !seg[1]) return;
    if (companionIdFromRef(seg[1]) != null) return;
    const c = (companions ?? []).find((x) => x.name === seg[1]);
    if (!c) return;
    const tail = seg.slice(2).map(encodeURIComponent).join("/");
    navigate(
      companionPath(c, tail ? `/${tail}` : "") + location.search + location.hash,
      { replace: true },
    );
  }, [pathname, companions, navigate, location.search, location.hash]);

  return (
    <SidebarProvider
      defaultOpen
      style={
        {
          "--sidebar-width": "13.5rem",
          "--sidebar-width-mobile": "16rem",
        } as React.CSSProperties
      }
    >
      <Sidebar
        variant="sidebar"
        collapsible="icon"
        className="border-r border-sidebar-border"
      >
        <SidebarHeader className="border-b border-sidebar-border h-14 px-3 justify-center">
          <Link to="/" className="flex items-center gap-2.5">
            <div className="relative size-8 grid place-items-center rounded-sm bg-primary/10 border border-primary/30 shrink-0">
              <span className="owl-mark h-4 w-[22px] bg-primary" aria-hidden="true" />
              <span className="absolute -top-0.5 -right-0.5 size-1.5 rounded-full bg-primary scan-pulse" />
            </div>
            <div className="flex flex-col leading-tight group-data-[collapsible=icon]:hidden">
              <span className="font-mono text-[11px] uppercase tracking-[0.18em] text-muted-foreground">
                OwlShack
              </span>
              <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-primary">
                operator console
              </span>
            </div>
          </Link>
        </SidebarHeader>

        <SidebarContent className="gap-1">
          <NavSection label="Network" items={PRIMARY} pathname={pathname} />
          <Separator className="mx-3 bg-sidebar-border/50" />
          <NavSection
            label="Telemetry"
            items={TELEMETRY}
            pathname={pathname}
          />
          <Separator className="mx-3 bg-sidebar-border/50" />
          <CommsSection companions={companions ?? []} pathname={pathname} />
          <Separator className="mx-3 bg-sidebar-border/50" />
          <NavSection label="System" items={SYSTEM} pathname={pathname} />
        </SidebarContent>

        <SidebarFooter className="border-t border-sidebar-border px-2 py-2 gap-1 group-data-[collapsible=icon]:hidden">
          <SystemStatus />
          <InstallButton />
          <ThemeToggle />
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>

      <SidebarInset className="bg-background overflow-hidden flex flex-col">
        <header className="shrink-0 z-30 h-14 pt-safe box-content flex items-center gap-3 border-b border-border bg-background/80 backdrop-blur supports-backdrop-filter:bg-background/60 px-4">
          <SidebarTrigger className="size-10 md:size-8 -ml-2 md:ml-0" />
          <Separator orientation="vertical" className="h-5" />
          <div className="flex items-center gap-2 font-mono text-[11px] uppercase tracking-[0.14em] text-muted-foreground min-w-0 overflow-hidden">
            <Waves className="size-3.5 text-primary shrink-0" />
            <span className="text-foreground truncate hidden sm:inline">{routeLabel(pathname, companions ?? [])}</span>
            <span className="text-foreground truncate sm:hidden">{routeLabelShort(pathname, companions ?? [])}</span>
          </div>
          <div className="ml-auto flex items-center gap-3 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
            <ClockBadge />
          </div>
        </header>

        <main className="relative flex-1 overflow-y-auto overflow-x-hidden px-4 sm:px-6 pt-6 pb-[calc(1.5rem+var(--bottom-nav))] max-w-[100vw]">
          <div className="relative z-10">{children}</div>
        </main>
        <BottomNav companions={companions ?? []} pathname={pathname} />
      </SidebarInset>
    </SidebarProvider>
  );
}

// The companion segment is a "<id>-<slug>" ref, not a name, so the header has to resolve it or it
// reads the link rather than the companion. The raw segment stands in until the roster loads.
function companionLabel(seg: string, companions: ConfigCompanion[]): string {
  return findByRef(seg, companions)?.name ?? seg;
}

function routeLabel(pathname: string, companions: ConfigCompanion[]): string {
  if (pathname === "/") return "overview";
  const seg = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  if (seg.length === 0) return "overview";
  const who = seg[1] ? companionLabel(seg[1], companions) : "";
  if (seg[0] === "companions" && seg[1] && seg[2] === "repeaters")
    return `companions / ${who} / repeater`;
  if (seg[0] === "companions" && seg[1] && seg[2] === "sensors")
    return `companions / ${who} / sensor`;
  if (seg[0] === "companions" && seg[1] && seg[2] === "rooms")
    return `companions / ${who} / room`;
  if (seg[0] === "companions" && seg[1] && seg[2])
    return `companions / ${who} / ${seg[2]}`;
  if (seg[0] === "companions" && seg[1]) return `companions / ${who}`;
  if (seg[0] === "monitoring" && seg[1]) return `monitoring / ${truncateMid(seg[1], 8, 4)}`;
  return seg.join(" / ");
}

// The phone header has room for one segment only.
function routeLabelShort(pathname: string, companions: ConfigCompanion[]): string {
  if (pathname === "/") return "overview";
  const seg = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  if (seg[0] === "companions" && seg[1] && seg[2] === "repeaters") return "repeater";
  if (seg[0] === "companions" && seg[1] && seg[2] === "sensors") return "sensor";
  if (seg[0] === "companions" && seg[1] && seg[2] === "rooms") return "room";
  if (seg[0] === "companions" && seg[1] && seg[2]) return seg[2];
  if (seg[0] === "companions" && seg[1]) return companionLabel(seg[1], companions);
  if (seg[0] === "monitoring" && seg[1]) return "monitoring";
  return seg[seg.length - 1] ?? "overview";
}

// --bottom-nav (index.css) reserves this bar's height so content never hides behind it.
function BottomNav({
  companions,
  pathname,
}: {
  companions: ConfigCompanion[];
  pathname: string;
}) {
  const { setOpenMobile } = useSidebar();
  const messagesTo =
    companions.length === 1
      ? companionPath(companions[0])
      : "/companions";
  const items: NavItem[] = [
    PRIMARY[0],
    {
      to: messagesTo,
      label: "Messages",
      icon: MessagesSquare,
      match: (p) => p.startsWith("/companions"),
    },
    PRIMARY[1],
    PRIMARY[2],
  ];
  const itemClass =
    "flex flex-1 flex-col items-center justify-center gap-1 h-14 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground transition-colors";
  return (
    <nav
      aria-label="Primary"
      className="md:hidden fixed inset-x-0 bottom-0 z-30 flex border-t border-border bg-background/95 backdrop-blur supports-backdrop-filter:bg-background/80 pb-safe"
    >
      {items.map((item) => {
        const active = item.match(pathname);
        return (
          <Link
            key={item.label}
            to={item.to}
            aria-current={active ? "page" : undefined}
            className={cn(itemClass, active && "text-primary")}
          >
            <item.icon className="size-5" />
            {item.label}
          </Link>
        );
      })}
      <button
        type="button"
        onClick={() => setOpenMobile(true)}
        className={itemClass}
        aria-label="More navigation"
      >
        <Menu className="size-5" />
        More
      </button>
    </nav>
  );
}

function ClockBadge() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(id);
  }, []);
  const time = now.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
  return (
    <span className="hidden md:inline-flex items-center gap-1.5">
      <span className="size-1.5 rounded-full bg-primary scan-pulse" />
      <span className="text-foreground tabular-nums">{time}</span>
    </span>
  );
}
