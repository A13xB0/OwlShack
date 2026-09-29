import { useCallback, useState } from "react";
import { useParams } from "react-router-dom";
import { BackLink } from "@/components/BackLink";
import { PageHeader } from "@/components/PageHeader";
import { TelemetryAccess } from "@/components/TelemetryAccess";
import { TelemetryMapEditor } from "@/components/TelemetryMapEditor";
import { useCompanionRef } from "@/hooks/useCompanions";

// A companion's channel map lives with the companion: it is what this node says about itself.
export function CompanionTelemetryPage() {
  const { ref } = useParams();
  const { ref: companion, id, name } = useCompanionRef(ref);
  // Access and the map are saved separately, so the map panel re-reads whether it is being sent.
  const [accessSaved, setAccessSaved] = useState(0);
  const onAccessSaved = useCallback(() => setAccessSaved((n) => n + 1), []);

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3">
        <BackLink
          to={`/companions/${encodeURIComponent(companion)}`}
          label={name || "companion"}
        />
        <PageHeader eyebrow="Sent over the mesh" title="Telemetry" className="mb-0" />
      </div>

      {id == null ? (
        <p className="panel px-4 py-4 font-mono text-[11px] sm:text-[10px] leading-relaxed text-muted-foreground/70">
          Loading this companion...
        </p>
      ) : (
        <>
          <TelemetryAccess
            companionId={id}
            companionRef={companion}
            onSaved={onAccessSaved}
          />
          <TelemetryMapEditor node={{ kind: "companion", id }} reloadToken={accessSaved} />
        </>
      )}
    </div>
  );
}
