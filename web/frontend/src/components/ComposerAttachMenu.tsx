import { useEffect, useMemo, useRef, useState } from "react";
import { Contact, MapPin, Plus, Send, UserRound } from "lucide-react";
import L from "leaflet";
import { themeTileLayer, useThemeTiles } from "@/lib/leaflet";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerTypePill } from "@/components/StatusIndicator";

const TYPE_INT: Record<string, number> = {
  CHAT: 1,
  REPEATER: 2,
  ROOM: 3,
  SENSOR: 4,
};

interface ContactRow {
  peerPubkey: string;
  name: string;
  type?: string;
}

type AttachDialog = "contact" | "location" | null;

export function ComposerAttachMenu({
  companion,
  ownPubkey,
  ownName,
  ownLat,
  ownLon,
  onInsert,
}: {
  companion: string;
  ownPubkey: string | null;
  ownName: string;
  ownLat?: number | null;
  ownLon?: number | null;
  onInsert: (text: string) => void;
}) {
  const [dialog, setDialog] = useState<AttachDialog>(null);

  const hasPosition =
    typeof ownLat === "number" &&
    typeof ownLon === "number" &&
    !(ownLat === 0 && ownLon === 0);

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Attach"
            className="relative size-10.5 shrink-0 grid place-items-center text-muted-foreground hover:text-foreground border border-border bg-background hover:bg-muted/60 before:absolute before:-inset-0.5 before:content-[''] sm:before:hidden"
          >
            <Plus className="size-6" strokeWidth={1.8} />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" side="top" className="rounded-none min-w-52">
          {ownPubkey && (
            <DropdownMenuItem
              onClick={() => onInsert(`<${ownPubkey}:1:${ownName}>`)}
              className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
            >
              <UserRound className="size-3.5" />
              My Contact Info
            </DropdownMenuItem>
          )}
          {hasPosition && (
            <DropdownMenuItem
              onClick={() => onInsert(`${ownLat!.toFixed(5)},${ownLon!.toFixed(5)}`)}
              className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
            >
              <MapPin className="size-3.5" />
              My Current Position
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator className="bg-border" />
          <DropdownMenuItem
            onClick={() => setDialog("contact")}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
          >
            <Contact className="size-3.5" />
            Share Contact
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => setDialog("location")}
            className="font-mono text-xs uppercase tracking-[0.08em] rounded-none"
          >
            <MapPin className="size-3.5" />
            Share Location from Map
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <ShareContactDialog
        open={dialog === "contact"}
        onClose={() => setDialog(null)}
        companion={companion}
        onPick={(c) => {
          const t = TYPE_INT[(c.type ?? "CHAT").toUpperCase()] ?? 1;
          onInsert(`<${c.peerPubkey}:${t}:${c.name}>`);
          setDialog(null);
        }}
      />
      <ShareLocationDialog
        open={dialog === "location"}
        onClose={() => setDialog(null)}
        initialLat={hasPosition ? ownLat! : undefined}
        initialLon={hasPosition ? ownLon! : undefined}
        onPick={(lat, lon) => {
          onInsert(`${lat.toFixed(5)},${lon.toFixed(5)}`);
          setDialog(null);
        }}
      />
    </>
  );
}

function ShareContactDialog({
  open,
  onClose,
  companion,
  onPick,
}: {
  open: boolean;
  onClose: () => void;
  companion: string;
  onPick: (c: ContactRow) => void;
}) {
  const [contacts, setContacts] = useState<ContactRow[]>([]);
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (!open) return;
    fetch(`/api/companions/${encodeURIComponent(companion)}/contacts`)
      .then((r) => (r.ok ? r.json() : []))
      .then((cs: ContactRow[]) => setContacts(cs || []))
      .catch(() => setContacts([]));
  }, [open, companion]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = q
      ? contacts.filter(
          (c) =>
            (c.name || "").toLowerCase().includes(q) ||
            c.peerPubkey.toLowerCase().includes(q),
        )
      : contacts;
    return [...list].sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  }, [contacts, query]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-sm">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Share Contact
          </DialogTitle>
        </DialogHeader>
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search contacts…"
          className="rounded-none border-border font-mono text-xs"
        />
        <div className="max-h-72 overflow-y-auto divide-y divide-border border border-border">
          {filtered.length === 0 ? (
            <div className="px-3 py-8 text-center font-mono text-xs uppercase tracking-[0.12em] text-muted-foreground/60">
              No contacts
            </div>
          ) : (
            filtered.map((c) => (
              <button
                key={c.peerPubkey}
                type="button"
                onClick={() => onPick(c)}
                className="w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-muted/50"
              >
                <PeerAvatar name={c.name || "unknown"} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-sm truncate">{c.name || "unknown"}</span>
                    {c.type && <PeerTypePill type={c.type} />}
                  </div>
                  <code className="font-mono text-[10px] text-muted-foreground">
                    {c.peerPubkey.slice(0, 12)}…
                  </code>
                </div>
              </button>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ShareLocationDialog({
  open,
  onClose,
  initialLat,
  initialLon,
  onPick,
}: {
  open: boolean;
  onClose: () => void;
  initialLat?: number;
  initialLon?: number;
  onPick: (lat: number, lon: number) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<L.Map | null>(null);
  const tileRef = useRef<L.TileLayer | null>(null);
  const markerRef = useRef<L.Marker | null>(null);
  const [picked, setPicked] = useState<{ lat: number; lon: number } | null>(
    initialLat != null && initialLon != null
      ? { lat: initialLat, lon: initialLon }
      : null,
  );

  useEffect(() => {
    if (!open || !containerRef.current) return;
    const center: [number, number] =
      initialLat != null && initialLon != null
        ? [initialLat, initialLon]
        : [0, 0];
    const map = L.map(containerRef.current).setView(center, initialLat != null ? 12 : 2);
    tileRef.current = themeTileLayer().addTo(map);
    if (initialLat != null && initialLon != null) {
      markerRef.current = L.marker([initialLat, initialLon]).addTo(map);
    }
    map.on("click", (e: L.LeafletMouseEvent) => {
      const { lat, lng } = e.latlng;
      setPicked({ lat, lon: lng });
      if (markerRef.current) markerRef.current.setLatLng([lat, lng]);
      else markerRef.current = L.marker([lat, lng]).addTo(map);
    });
    mapRef.current = map;
    // Dialog animates in; let it settle before Leaflet measures.
    const t = window.setTimeout(() => map.invalidateSize(), 150);
    return () => {
      window.clearTimeout(t);
      map.remove();
      mapRef.current = null;
      tileRef.current = null;
      markerRef.current = null;
    };
  }, [open, initialLat, initialLon]);

  useThemeTiles(mapRef, tileRef);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="rounded-none border-border bg-card max-w-md">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm uppercase tracking-[0.12em]">
            Share Location
          </DialogTitle>
        </DialogHeader>
        <div ref={containerRef} className="h-64 w-full border border-border" />
        <div className="flex items-center justify-between gap-3">
          <span className="font-mono text-xs text-muted-foreground tabular-nums">
            {picked
              ? `${picked.lat.toFixed(5)}, ${picked.lon.toFixed(5)}`
              : "Tap the map to pick a point"}
          </span>
          <Button
            size="sm"
            disabled={!picked}
            onClick={() => picked && onPick(picked.lat, picked.lon)}
            className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            <Send className="size-3.5" />
            Share
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
