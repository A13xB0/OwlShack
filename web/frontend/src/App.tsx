import { lazy, Suspense } from "react";
import { Route, Routes } from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { useApiList } from "@/hooks/useApiList";
import { useApiObject } from "@/hooks/useApiObject";
import { type ConfigCompanion, type Settings } from "@/lib/configApi";
import { DashboardPage } from "@/pages/DashboardPage";
import { PeersPage } from "@/pages/PeersPage";

const CompanionsPage = lazy(() =>
  import("@/pages/CompanionsPage").then((m) => ({ default: m.CompanionsPage })),
);
const MapPage = lazy(() =>
  import("@/pages/MapPage").then((m) => ({ default: m.MapPage })),
);
const PacketsPage = lazy(() =>
  import("@/pages/PacketsPage").then((m) => ({ default: m.PacketsPage })),
);
const TracesPage = lazy(() =>
  import("@/pages/TracesPage").then((m) => ({ default: m.TracesPage })),
);
const CompanionDetailPage = lazy(() =>
  import("@/pages/CompanionDetailPage").then((m) => ({
    default: m.CompanionDetailPage,
  })),
);
const ContactsPage = lazy(() =>
  import("@/pages/ContactsPage").then((m) => ({ default: m.ContactsPage })),
);
const ContactDetailPage = lazy(() =>
  import("@/pages/ContactDetailPage").then((m) => ({
    default: m.ContactDetailPage,
  })),
);
const CompanionTelemetryPage = lazy(() =>
  import("@/pages/CompanionTelemetryPage").then((m) => ({
    default: m.CompanionTelemetryPage,
  })),
);
const ChannelsPage = lazy(() =>
  import("@/pages/ChannelsPage").then((m) => ({ default: m.ChannelsPage })),
);
const RepeaterDetailPage = lazy(() =>
  import("@/pages/RepeaterDetailPage").then((m) => ({
    default: m.RepeaterDetailPage,
  })),
);
const RepeatersListPage = lazy(() =>
  import("@/pages/RepeatersListPage").then((m) => ({
    default: m.RepeatersListPage,
  })),
);
const MonitoringPage = lazy(() =>
  import("@/pages/MonitoringPage").then((m) => ({
    default: m.MonitoringPage,
  })),
);
const MonitoringDetailPage = lazy(() =>
  import("@/pages/MonitoringDetailPage").then((m) => ({
    default: m.MonitoringDetailPage,
  })),
);
const BotsPage = lazy(() =>
  import("@/pages/BotsPage").then((m) => ({ default: m.BotsPage })),
);
const MqttPage = lazy(() =>
  import("@/pages/MqttPage").then((m) => ({ default: m.MqttPage })),
);
const RadioPage = lazy(() =>
  import("@/pages/RadioPage").then((m) => ({ default: m.RadioPage })),
);
const RadioHealthPage = lazy(() =>
  import("@/pages/RadioHealthPage").then((m) => ({ default: m.RadioHealthPage })),
);
const SensorsPage = lazy(() =>
  import("@/pages/SensorsPage").then((m) => ({ default: m.SensorsPage })),
);
const DiscoverPage = lazy(() =>
  import("@/pages/DiscoverPage").then((m) => ({ default: m.DiscoverPage })),
);
const RepeaterNodePage = lazy(() =>
  import("@/pages/RepeaterNodePage").then((m) => ({
    default: m.RepeaterNodePage,
  })),
);
// Lazy so Leaflet, pulled in by the position picker, stays out of the main bundle.
const SetupWizard = lazy(() =>
  import("@/components/SetupWizard").then((m) => ({ default: m.SetupWizard })),
);

function PageFallback() {
  return (
    <div className="space-y-6">
      <div className="h-12 w-2/3 bg-muted animate-pulse" />
      <div className="h-32 w-full bg-muted animate-pulse" />
    </div>
  );
}

export function App() {
  const { item: settings, reload: reloadSettings } = useApiObject<Settings>(
    "/api/config/settings",
    "Failed to load settings",
  );
  const { items: companions, reload: reloadCompanions } =
    useApiList<ConfigCompanion>("/api/config/companions", "Failed to load companions");

  // Both fetches must land first, or an existing install flashes the wizard.
  const needsSetup =
    settings != null &&
    companions != null &&
    settings.setupComplete !== true &&
    companions.length === 0;

  return (
    <AppShell>
      {needsSetup && settings && (
        <Suspense fallback={null}>
          <SetupWizard
            settings={settings}
            onComplete={() => {
              reloadSettings();
              reloadCompanions();
            }}
          />
        </Suspense>
      )}
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/peers" element={<PeersPage />} />
          <Route path="/map" element={<MapPage />} />
          <Route path="/packets" element={<PacketsPage />} />
          <Route path="/traces" element={<TracesPage />} />
          <Route path="/monitoring" element={<MonitoringPage />} />
          <Route path="/monitoring/:pubkey" element={<MonitoringDetailPage />} />
          <Route path="/bots" element={<BotsPage />} />
          <Route path="/mqtt" element={<MqttPage />} />
          <Route path="/radio" element={<RadioHealthPage />} />
          <Route path="/sensors" element={<SensorsPage />} />
          <Route path="/discover" element={<DiscoverPage />} />
          <Route path="/settings" element={<RadioPage />} />
          <Route path="/repeater" element={<RepeaterNodePage />} />
          <Route path="/companions" element={<CompanionsPage />} />
          <Route path="/companions/:ref" element={<CompanionDetailPage />} />
          <Route
            path="/companions/:ref/contacts"
            element={<ContactsPage />}
          />
          <Route
            path="/companions/:ref/contacts/:pubkey"
            element={<ContactDetailPage />}
          />
          <Route
            path="/companions/:ref/telemetry"
            element={<CompanionTelemetryPage />}
          />
          <Route
            path="/companions/:ref/channels"
            element={<ChannelsPage />}
          />
          <Route
            path="/companions/:ref/repeaters"
            element={<RepeatersListPage />}
          />
          <Route
            path="/companions/:ref/repeaters/:pubkey"
            element={<RepeaterDetailPage />}
          />
          <Route
            path="/companions/:ref/sensors/:pubkey"
            element={<RepeaterDetailPage kind="sensor" />}
          />
          <Route
            path="/companions/:ref/rooms/:pubkey"
            element={<RepeaterDetailPage kind="room" />}
          />
        </Routes>
      </Suspense>
    </AppShell>
  );
}
