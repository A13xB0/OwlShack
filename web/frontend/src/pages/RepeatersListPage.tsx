import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useCompanionRef } from "@/hooks/useCompanions";
import {
  CircleDashed,
  Crosshair,
  KeyRound,
  Plus,
  Radio,
  Search,
} from "lucide-react";
import { toast } from "sonner";
import { BackLink } from "@/components/BackLink";
import { Button } from "@/components/ui/button";
import { HeaderButton } from "@/components/HeaderButton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { InlineConfirm } from "@/components/InlineConfirm";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { PageHeader } from "@/components/PageHeader";
import { PeerAvatar } from "@/components/PeerAvatar";
import { PeerTypePill } from "@/components/StatusIndicator";
import { useWebSocket } from "@/hooks/useWebSocket";
import { isPeerDelete } from "@/lib/peerWs";
import { timeAgo, truncateMid } from "@/lib/format";
import { cn } from "@/lib/utils";

interface Contact {
  peerPubkey: string;
  name: string;
  type: string;
  addedAt: string;
  metadata?: {
    isRepeater?: boolean;
    repeaterPassword?: string;
  };
}

interface Peer {
  pubkey: string;
  name: string;
  type: string;
  lastSeen: string;
}

const HEX64 = /^[0-9a-fA-F]{64}$/;

export function RepeatersListPage() {
  const { ref } = useParams();
  const { ref: companion, name: companionName } = useCompanionRef(ref);

  const [contacts, setContacts] = useState<Contact[] | null>(null);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const onWsPeer = useCallback((topic: string, data: unknown) => {
    if (topic !== "peers" || !data) return;
    if (isPeerDelete(data)) {
      const gone = new Set(data.pubkeys.map((k) => k.toLowerCase()));
      setPeers((prev) => prev.filter((p) => !gone.has(p.pubkey.toLowerCase())));
      return;
    }
    const p = data as Peer;
    setPeers((prev) => {
      const idx = prev.findIndex((x) => x.pubkey === p.pubkey);
      if (idx === -1) return [...prev, p];
      const next = prev.slice();
      next[idx] = { ...next[idx], ...p };
      return next;
    });
  }, []);

  useWebSocket(["peers"], onWsPeer);

  const load = useCallback(() => {
    if (!companion) return;
    setLoading(true);
    setError(null);
    Promise.all([
      fetch(`/api/companions/${encodeURIComponent(companion)}/contacts`).then(
        (r) => {
          if (!r.ok) throw new Error("contacts");
          return r.json();
        },
      ),
      fetch("/api/peers").then((r) => {
        if (!r.ok) throw new Error("peers");
        return r.json();
      }),
    ])
      .then(([cs, ps]) => {
        setContacts(cs || []);
        setPeers(ps || []);
      })
      .catch(() => setError("Failed to load repeaters"))
      .finally(() => setLoading(false));
  }, [companion]);

  useEffect(() => {
    load();
  }, [load]);

  const repeaters = useMemo(() => {
    if (!contacts) return [];
    return contacts
      .filter(
        (c) => c.type === "REPEATER" || c.metadata?.isRepeater === true,
      )
      .sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  }, [contacts]);

  const knownPubkeys = useMemo(
    () => new Set((contacts || []).map((c) => c.peerPubkey.toLowerCase())),
    [contacts],
  );

  const candidatePeers = useMemo(
    () =>
      peers.filter(
        (p) =>
          p.type === "REPEATER" && !knownPubkeys.has(p.pubkey.toLowerCase()),
      ),
    [peers, knownPubkeys],
  );

  const removeContact = useCallback(
    async (pubkey: string) => {
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts/${pubkey}`,
          { method: "DELETE" },
        );
        if (!r.ok) {
          const err = await r.json().catch(() => ({}));
          throw new Error(err.error || `HTTP ${r.status}`);
        }
        toast.success("Repeater removed");
        setConfirmRemove(null);
        load();
      } catch (e) {
        toast.error(
          `Failed to remove: ${e instanceof Error ? e.message : "error"}`,
        );
      }
    },
    [companion, load],
  );

  const addContact = useCallback(
    async (pubkey: string) => {
      try {
        const r = await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ pubkey }),
          },
        );
        if (!r.ok) {
          const err = await r.json().catch(() => ({}));
          throw new Error(err.error || `HTTP ${r.status}`);
        }
        // mark as repeater explicitly so it's recognised even if peer type is missing
        await fetch(
          `/api/companions/${encodeURIComponent(companion)}/contacts/${pubkey}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ isRepeater: true }),
          },
        ).catch(() => {});
        toast.success("Repeater added");
        setAddOpen(false);
        load();
      } catch (e) {
        toast.error(
          `Failed to add: ${e instanceof Error ? e.message : "error"}`,
        );
      }
    },
    [companion, load],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3">
        <BackLink
          to={`/companions/${encodeURIComponent(companion)}`}
          label={companionName || "companion"}
        />

        <PageHeader
          title="Repeaters"
          meta={
            contacts && (
              <span className="font-mono text-sm text-muted-foreground tabular-nums">
                {repeaters.length} configured
              </span>
            )
          }
          actions={
            <HeaderButton tone="primary" icon={Plus} onClick={() => setAddOpen(true)}>
              add repeater
            </HeaderButton>
          }
          className="mb-0"
        />
      </div>

      {loading && <RepeatersSkeleton />}

      {error && <LoadErrorAlert message={error} onRetry={load} />}

      {!loading && !error && contacts && (
        <section className="panel overflow-hidden">
          <div className="flex items-center justify-between px-4 py-3 border-b border-border">
            <div className="space-y-0.5">
              <span className="label-overline block">Roster</span>
              <h2 className="font-mono text-sm uppercase tracking-widest">
                Managed repeaters
              </h2>
            </div>
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
              {repeaters.length}
            </span>
          </div>

          {repeaters.length === 0 ? (
            <div className="px-6 py-16 text-center">
              <Radio className="size-8 mx-auto mb-3 text-muted-foreground/40" />
              <p className="font-mono text-sm uppercase tracking-widest text-muted-foreground">
                No repeaters yet
              </p>
              <p className="mt-2 text-xs text-muted-foreground/70">
                Add a repeater to log in, run CLI commands, and adjust radio
                settings.
              </p>
              <Button
                variant="default"
                size="sm"
                onClick={() => setAddOpen(true)}
                className="mt-4 font-mono text-xs uppercase tracking-widest"
              >
                <Plus className="size-3.5" />
                Add repeater
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {repeaters.map((c) => (
                <RepeaterRow
                  key={c.peerPubkey}
                  companion={companion}
                  contact={c}
                  confirming={confirmRemove === c.peerPubkey}
                  onAskRemove={() => setConfirmRemove(c.peerPubkey)}
                  onCancel={() => setConfirmRemove(null)}
                  onConfirm={() => removeContact(c.peerPubkey)}
                />
              ))}
            </div>
          )}
        </section>
      )}

      <AddRepeaterDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        candidates={candidatePeers}
        knownPubkeys={knownPubkeys}
        onAdd={addContact}
      />
    </div>
  );
}

function RepeaterRow({
  companion,
  contact,
  confirming,
  onAskRemove,
  onCancel,
  onConfirm,
}: {
  companion: string;
  contact: Contact;
  confirming: boolean;
  onAskRemove: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const hasPassword = !!contact.metadata?.repeaterPassword;
  return (
    <div className="group flex items-center gap-2 sm:gap-4 px-3 sm:px-4 py-3 hover:bg-muted/40 transition-colors">
      <Link
        to={`/companions/${encodeURIComponent(companion)}/repeaters/${contact.peerPubkey}`}
        className="flex items-center gap-3 flex-1 min-w-0"
      >
        <PeerAvatar name={contact.name || contact.peerPubkey} size="md" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 min-w-0">
            <h3 className="font-mono text-sm font-semibold truncate">
              {contact.name || (
                <span className="text-muted-foreground italic">unknown</span>
              )}
            </h3>
            {hasPassword && (
              <span
                className="shrink-0 inline-flex items-center gap-1 font-mono text-[9px] uppercase tracking-[0.12em] text-primary border border-primary/30 px-1.5 py-0.5"
                title="Saved password"
              >
                <KeyRound className="size-2.5" /> creds
              </span>
            )}
          </div>
          <code className="font-mono text-xs text-muted-foreground block truncate">
            {truncateMid(contact.peerPubkey, 8, 6)} · added{" "}
            {timeAgo(contact.addedAt)}
          </code>
        </div>
        <Crosshair className="hidden sm:block size-3.5 text-muted-foreground/40 group-hover:text-primary transition-colors shrink-0" />
      </Link>
      <div className="flex items-center shrink-0">
        <InlineConfirm
          confirming={confirming}
          onAskRemove={onAskRemove}
          onCancel={onCancel}
          onConfirm={onConfirm}
          iconOnly
          ariaLabel="Remove repeater"
        />
      </div>
    </div>
  );
}

type SortMode = "recent" | "name";

function AddRepeaterDialog({
  open,
  onOpenChange,
  candidates,
  knownPubkeys,
  onAdd,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  candidates: Peer[];
  knownPubkeys: Set<string>;
  onAdd: (pubkey: string) => Promise<void>;
}) {
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<SortMode>("recent");
  const [manualKey, setManualKey] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) {
      setSearch("");
      setSort("recent");
      setManualKey("");
      setSubmitting(false);
    }
  }, [open]);

  const available = useMemo(() => {
    let pool = candidates;
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      pool = pool.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.pubkey.toLowerCase().includes(q),
      );
    }
    if (sort === "recent") {
      pool = [...pool].sort(
        (a, b) =>
          new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime(),
      );
    } else {
      pool = [...pool].sort((a, b) =>
        (a.name || "").localeCompare(b.name || ""),
      );
    }
    return pool;
  }, [candidates, search, sort]);

  const manualValid = HEX64.test(manualKey.trim());
  const manualAlreadyAdded =
    manualValid && knownPubkeys.has(manualKey.trim().toLowerCase());

  const handleManualSubmit = async () => {
    if (!manualValid || manualAlreadyAdded) return;
    setSubmitting(true);
    await onAdd(manualKey.trim().toLowerCase());
    setSubmitting(false);
  };

  const handlePeerAdd = async (pubkey: string) => {
    setSubmitting(true);
    await onAdd(pubkey);
    setSubmitting(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="rounded-none border-border bg-card max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono uppercase tracking-[0.08em] text-sm">
            Add repeater
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground">
            Adopt a discovered repeater or paste its public key.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <span className="label-overline block">Discovered repeaters</span>

            <div className="relative">
              <Search className="size-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground/60 pointer-events-none" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="search name or pubkey…"
                className="pl-8 rounded-none font-mono text-base md:text-xs h-8"
              />
            </div>

            <div className="flex items-center justify-between">
              <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
                {available.length} available
              </span>
              <div className="inline-flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70">
                <span>sort</span>
                <button
                  type="button"
                  onClick={() => setSort("recent")}
                  className={cn(
                    "px-1.5 py-0.5 border transition-colors",
                    sort === "recent"
                      ? "border-primary/40 text-primary bg-primary/5"
                      : "border-border hover:text-foreground",
                  )}
                >
                  recent
                </button>
                <button
                  type="button"
                  onClick={() => setSort("name")}
                  className={cn(
                    "px-1.5 py-0.5 border transition-colors",
                    sort === "name"
                      ? "border-primary/40 text-primary bg-primary/5"
                      : "border-border hover:text-foreground",
                  )}
                >
                  name
                </button>
              </div>
            </div>

            <div className="border border-border max-h-64 overflow-y-auto divide-y divide-border">
              {available.length === 0 ? (
                <div className="px-4 py-8 text-center">
                  <CircleDashed className="size-5 mx-auto mb-2 text-muted-foreground/40" />
                  <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                    {candidates.length === 0
                      ? "No candidates"
                      : "No matching repeaters"}
                  </p>
                </div>
              ) : (
                available.map((p) => (
                  <div
                    key={p.pubkey}
                    className="flex items-center gap-3 px-3 py-2 hover:bg-muted/40 transition-colors"
                  >
                    <PeerAvatar name={p.name || p.pubkey} size="sm" />
                    <div className="min-w-0 flex-1 space-y-0.5">
                      <div className="flex items-center gap-2">
                        <span className="text-xs font-medium truncate">
                          {p.name || (
                            <span className="text-muted-foreground italic">
                              unknown
                            </span>
                          )}
                        </span>
                        <PeerTypePill type={p.type} />
                      </div>
                      <div className="flex items-center gap-2 text-mono-xs text-muted-foreground">
                        <code className="font-mono text-[10px] text-muted-foreground">
                          {truncateMid(p.pubkey, 6, 4)}
                        </code>
                        <span className="text-muted-foreground/50">·</span>
                        <span className="tabular-nums">
                          {timeAgo(p.lastSeen)}
                        </span>
                      </div>
                    </div>
                    <Button
                      variant="ghost"
                      size="xs"
                      disabled={submitting}
                      onClick={() => handlePeerAdd(p.pubkey)}
                      className="font-mono uppercase tracking-widest text-primary hover:text-primary"
                    >
                      <Plus className="size-3" /> add
                    </Button>
                  </div>
                ))
              )}
            </div>
          </div>

          <div className="border-t border-border pt-4 space-y-2">
            <Label
              htmlFor="manual-pubkey"
              className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground"
            >
              Manual pubkey
            </Label>
            <div className="flex gap-2">
              <Input
                id="manual-pubkey"
                value={manualKey}
                onChange={(e) => setManualKey(e.target.value)}
                placeholder="64-character hex…"
                spellCheck={false}
                autoCorrect="off"
                autoCapitalize="off"
                aria-invalid={
                  manualKey.length > 0 && (!manualValid || manualAlreadyAdded)
                }
                className="rounded-none font-mono text-base md:text-xs h-8 flex-1"
              />
              <Button
                variant="default"
                size="sm"
                onClick={handleManualSubmit}
                disabled={!manualValid || manualAlreadyAdded || submitting}
                className="font-mono uppercase tracking-widest"
              >
                <Plus className="size-3" /> add
              </Button>
            </div>
            {manualKey.length > 0 && !manualValid && (
              <p className="text-[10px] text-destructive font-mono">
                must be 64 hex characters
              </p>
            )}
            {manualAlreadyAdded && (
              <p className="text-[10px] text-destructive font-mono">
                already a contact
              </p>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RepeatersSkeleton() {
  return (
    <div className="panel">
      <div className="px-4 py-3 border-b border-border">
        <Skeleton className="h-3 w-24 mb-2" />
        <Skeleton className="h-4 w-40" />
      </div>
      <div className="divide-y divide-border">
        {[...Array(3)].map((_, i) => (
          <div key={i} className="flex items-center gap-3 px-4 py-3">
            <Skeleton className="size-9" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-3 w-56" />
            </div>
            <Skeleton className="h-7 w-7" />
          </div>
        ))}
      </div>
    </div>
  );
}
