import { useEffect, useRef, useState } from "react";
import { Loader2, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  configApi,
  type TriggerLocation,
  type TriggerTestItem,
  type TriggerTestRender,
} from "@/lib/configApi";
import { formatDateTime, timeAgo } from "@/lib/format";
import { cn } from "@/lib/utils";

// Long enough that typing a template does not send a request per keystroke.
const RENDER_DEBOUNCE_MS = 400;

export function BotTestPanel({
  companionId,
  senderName,
  type,
  url,
  match,
  template,
  location,
  regions,
  disabledReason,
}: {
  companionId: number;
  // the companion's name, which a channel message is prefixed with.
  senderName: string;
  type: string;
  url: string;
  match: string[];
  template: string;
  location: TriggerLocation | null;
  regions: string[] | null;
  // what to fill in before a test could work; null when it can run.
  disabledReason: string | null;
}) {
  const [items, setItems] = useState<TriggerTestItem[] | null>(null);
  // The feed the items came from; editing the address or type makes them someone else's.
  const [fetchedFor, setFetchedFor] = useState("");
  const [fetching, setFetching] = useState(false);
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [render, setRender] = useState<TriggerTestRender | null>(null);
  const [renderFailed, setRenderFailed] = useState<string | null>(null);
  const [rendering, setRendering] = useState(false);
  const seq = useRef(0);

  const feedKey = `${type} ${url.trim()}`;
  const current = items !== null && fetchedFor === feedKey;
  const matchKey = match.map((s) => s.trim()).filter(Boolean).join("\n");

  const fetchItems = async () => {
    setFetching(true);
    setFetchError(null);
    try {
      const got = await configApi.testTriggerItems({
        companionId,
        type,
        url: url.trim(),
        match: matchKey ? matchKey.split("\n") : [],
        template,
        location,
        regions,
      });
      setItems(got);
      setFetchedFor(feedKey);
      setSelected(got[0]?.id ?? null);
    } catch (e) {
      setItems(null);
      setFetchError(e instanceof Error ? e.message : "Fetching the feed failed");
    } finally {
      setFetching(false);
    }
  };

  // Re-render the picked item as the template, patterns or companion change; only the latest answer is shown.
  useEffect(() => {
    if (!current || selected === null) return;
    // Half-typed, the location is missing, and rendering without it would read as anywhere.
    if (disabledReason !== null) {
      seq.current++;
      setRender(null);
      setRenderFailed(null);
      setRendering(false);
      return;
    }
    const mine = ++seq.current;
    setRendering(true);
    const timer = setTimeout(async () => {
      try {
        const res = await configApi.testTriggerRender({
          companionId,
          type,
          url: url.trim(),
          match: matchKey ? matchKey.split("\n") : [],
          template,
          location,
          regions,
          itemId: selected,
        });
        if (mine !== seq.current) return;
        setRender(res);
        setRenderFailed(null);
      } catch (e) {
        if (mine !== seq.current) return;
        setRender(null);
        setRenderFailed(e instanceof Error ? e.message : "Rendering failed");
      } finally {
        if (mine === seq.current) setRendering(false);
      }
    }, RENDER_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [current, selected, companionId, type, url, matchKey, template, location, regions, disabledReason]);

  const cut = render ? render.message.slice(render.channelText.length) : "";
  const verdict = renderVerdict(render);

  return (
    <section className="space-y-2.5 border border-border bg-muted/40 p-3">
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <span className="label-overline block">Test</span>
          <p className="font-mono text-[11px] leading-relaxed text-muted-foreground/80 sm:text-[10px]">
            {disabledReason ??
              "Fetch the feed and pick an item to see what this bot would send. Nothing is sent."}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={fetchItems}
          disabled={fetching || disabledReason !== null}
          className="shrink-0 rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
        >
          {fetching ? <Loader2 className="size-3.5 animate-spin" /> : null}
          {current ? "Fetch again" : "Fetch items"}
        </Button>
      </div>

      {fetchError ? <ErrorLine text={fetchError} /> : null}

      {items !== null && !current && !fetchError ? (
        <p className="font-mono text-[11px] text-muted-foreground">
          The feed address changed. Fetch again to test against it.
        </p>
      ) : null}

      {current && items.length === 0 ? (
        <p className="font-mono text-[11px] text-muted-foreground">
          The feed has no items right now, so there is nothing to render.
        </p>
      ) : null}

      {current && items.length > 0 ? (
        <ul
          role="listbox"
          aria-label="Feed items"
          className="max-h-44 divide-y divide-border overflow-y-auto border border-border bg-card"
        >
          {items.map((it) => {
            const on = it.id === selected;
            return (
              <li key={it.id} role="option" aria-selected={on}>
                <button
                  type="button"
                  onClick={() => setSelected(it.id)}
                  className={cn(
                    "flex w-full items-baseline gap-2 border-l-2 px-2 py-1.5 text-left",
                    on
                      ? "border-primary bg-primary/10"
                      : "border-transparent hover:bg-muted/60",
                  )}
                >
                  <span
                    className={cn(
                      "min-w-0 flex-1 truncate font-mono text-[11px]",
                      on ? "text-foreground" : "text-muted-foreground",
                    )}
                  >
                    {it.title || it.link || it.id}
                  </span>
                  {it.published ? (
                    <span
                      title={formatDateTime(it.published)}
                      className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70"
                    >
                      {timeAgo(it.published)}
                    </span>
                  ) : null}
                </button>
              </li>
            );
          })}
        </ul>
      ) : null}

      {current && selected !== null ? (
        <div className="space-y-2" aria-live="polite">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px]">
            {render && !render.renderError ? (
              <span
                className={cn(
                  "border px-1.5 py-0.5 uppercase tracking-[0.1em]",
                  verdict.sends
                    ? "border-primary/45 text-primary"
                    : "border-warning/50 text-warning",
                )}
              >
                {verdict.label}
              </span>
            ) : null}
            {render && !render.renderError ? (
              <span className="tabular-nums text-muted-foreground">
                {render.bytes} bytes · a channel keeps {render.channelLimit}
              </span>
            ) : null}
            {rendering ? (
              <Loader2 className="size-3 animate-spin text-muted-foreground" aria-label="Rendering" />
            ) : null}
          </div>

          {renderFailed ? <ErrorLine text={renderFailed} /> : null}
          {render?.renderError ? (
            <ErrorLine text={`Template: ${render.renderError}`} />
          ) : null}

          {render && !render.renderError ? (
            <>
              <p className="border border-border bg-card px-2.5 py-2 font-mono text-sm leading-relaxed whitespace-pre-wrap wrap-anywhere">
                <span className="text-muted-foreground">{senderName}: </span>
                {render.message === "" ? (
                  <span className="text-muted-foreground/70 italic">
                    (the template renders nothing)
                  </span>
                ) : (
                  <>
                    {render.channelText}
                    {cut ? (
                      <span className="text-destructive/80 line-through decoration-destructive/60">
                        {cut}
                      </span>
                    ) : null}
                  </>
                )}
              </p>
              {cut ? (
                <p className="font-mono text-[11px] leading-snug text-muted-foreground">
                  A channel cuts the struck-through end.{" "}
                  {render.bytes > render.dmLimit
                    ? `A DM over ${render.dmLimit} bytes is not sent at all.`
                    : "A DM would carry all of it."}
                </p>
              ) : null}
              {Object.keys(render.captures).length > 0 ? (
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-[11px]">
                  {Object.entries(render.captures).map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="text-muted-foreground">{`{{.Match.${k}}}`}</dt>
                      <dd className="min-w-0 truncate" title={v}>
                        {v}
                      </dd>
                    </div>
                  ))}
                </dl>
              ) : null}
            </>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

// The location is checked before the patterns, so it is the reason given when both would skip.
function renderVerdict(render: TriggerTestRender | null): { sends: boolean; label: string } {
  if (render?.placement === "noShape")
    return { sends: false, label: "No map shape in this alert" };
  if (render?.placement === "outside") return { sends: false, label: "Outside the chosen area" };
  if (!render?.matched) return { sends: false, label: "Patterns skip this item" };
  return { sends: true, label: "Would send" };
}

function ErrorLine({ text }: { text: string }) {
  return (
    <p
      role="alert"
      className="flex items-start gap-1.5 font-mono text-[11px] leading-snug text-destructive"
    >
      <TriangleAlert className="mt-px size-3 shrink-0" />
      <span className="min-w-0 wrap-anywhere">{text}</span>
    </p>
  );
}
