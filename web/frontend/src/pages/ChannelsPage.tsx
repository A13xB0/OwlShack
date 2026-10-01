import { useCallback, useState } from "react";
import { useParams } from "react-router-dom";
import { useCompanionRef } from "@/hooks/useCompanions";
import { Hash, Plus, Radio } from "lucide-react";
import { toast } from "sonner";
import { BackLink } from "@/components/BackLink";
import { Button } from "@/components/ui/button";
import { HeaderButton } from "@/components/HeaderButton";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiList } from "@/hooks/useApiList";
import { InlineConfirm } from "@/components/InlineConfirm";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { PageHeader } from "@/components/PageHeader";
import { AddChannelDialog, type Channel } from "@/components/AddChannelDialog";
import { postChannel } from "@/lib/channelsApi";

export function ChannelsPage() {
  const { ref } = useParams();
  const { ref: companion, name: companionName } = useCompanionRef(ref);

  const {
    items: channels,
    loading,
    error,
    reload: load,
  } = useApiList<Channel>(
    companion
      ? `/api/companions/${encodeURIComponent(companion)}/channels`
      : null,
    "Failed to load channels",
  );
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);

  const addChannel = useCallback(
    async (channelName: string, privateKey?: string) => {
      try {
        await postChannel(companion, channelName, privateKey);
        toast.success(`Channel "${channelName}" added`);
        setDialogOpen(false);
        load();
      } catch (e) {
        const msg = e instanceof Error ? e.message : "failed";
        toast.error(`Failed to add channel: ${msg}`);
      }
    },
    [companion, load],
  );

  const removeChannel = useCallback(
    async (channelName: string) => {
      try {
        const res = await fetch(
          `/api/companions/${encodeURIComponent(
            companion,
          )}/channels/${encodeURIComponent(channelName)}`,
          { method: "DELETE" },
        );
        if (!res.ok) {
          const err = await res.json().catch(() => ({}));
          throw new Error(err.error || `HTTP ${res.status}`);
        }
        toast.success(`Channel "${channelName}" removed`);
        setConfirmRemove(null);
        load();
      } catch (e) {
        const msg = e instanceof Error ? e.message : "failed";
        toast.error(`Failed to remove channel: ${msg}`);
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
          title="Channels"
          meta={
            channels && (
              <span className="font-mono text-sm text-muted-foreground tabular-nums">
                {channels.length} configured
              </span>
            )
          }
          actions={
            <HeaderButton tone="primary" icon={Plus} onClick={() => setDialogOpen(true)}>
              add channel
            </HeaderButton>
          }
          className="mb-0"
        />
      </div>

      {loading && <ChannelsSkeleton />}

      {error && <LoadErrorAlert message={error} onRetry={load} />}

      {!loading && !error && channels && (
        <section className="panel overflow-hidden">
          <div className="flex items-center justify-between px-4 py-3 border-b border-border">
            <div className="space-y-0.5">
              <span className="label-overline block">Frequencies</span>
              <h2 className="font-mono text-sm uppercase tracking-widest">
                Subscribed channels
              </h2>
            </div>
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground/70 tabular-nums">
              {channels.length}
            </span>
          </div>

          {channels.length === 0 ? (
            <div className="px-6 py-16 text-center">
              <Radio className="size-8 mx-auto mb-3 text-muted-foreground/40" />
              <p className="font-mono text-sm uppercase tracking-widest text-muted-foreground">
                No channels yet
              </p>
              <p className="mt-2 text-xs text-muted-foreground/70">
                Subscribe to public or private channels to receive traffic.
              </p>
              <Button
                variant="default"
                size="sm"
                onClick={() => setDialogOpen(true)}
                className="mt-4 font-mono text-xs uppercase tracking-widest"
              >
                <Plus className="size-3.5" />
                Add channel
              </Button>
            </div>
          ) : (
            <div className="divide-y divide-border">
              {channels.map((ch) => (
                <ChannelRow
                  key={ch.name}
                  name={ch.name}
                  confirming={confirmRemove === ch.name}
                  onAskRemove={() => setConfirmRemove(ch.name)}
                  onCancel={() => setConfirmRemove(null)}
                  onConfirm={() => removeChannel(ch.name)}
                />
              ))}
            </div>
          )}
        </section>
      )}

      <AddChannelDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        existing={channels || []}
        onAdd={addChannel}
      />
    </div>
  );
}

function ChannelRow({
  name,
  confirming,
  onAskRemove,
  onCancel,
  onConfirm,
}: {
  name: string;
  confirming: boolean;
  onAskRemove: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3 hover:bg-muted/40 transition-colors">
      <div className="size-9 grid place-items-center rounded-sm border border-border bg-muted/40 text-muted-foreground shrink-0">
        <Hash className="size-4" strokeWidth={1.6} />
      </div>

      <div className="min-w-0 flex-1">
        <span className="font-mono text-sm font-medium tracking-[0.06em] truncate block">
          {name}
        </span>
        <span className="text-mono-xs text-muted-foreground">channel</span>
      </div>

      <div className="shrink-0">
        <InlineConfirm
          confirming={confirming}
          onAskRemove={onAskRemove}
          onCancel={onCancel}
          onConfirm={onConfirm}
          iconOnly
          ariaLabel="Remove channel"
        />
      </div>
    </div>
  );
}

function ChannelsSkeleton() {
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
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-3 w-12" />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
