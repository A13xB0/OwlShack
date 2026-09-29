import { useEffect, useState } from "react";
import { AlertTriangle, Loader2, Save } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/PageHeader";
import { LoadErrorAlert } from "@/components/LoadErrorAlert";
import { SectionTitle } from "@/components/SectionTitle";
import { PATH_HASH_SIZE_OPTIONS, SelectField, TextField } from "@/components/ConfigFields";
import { BACKEND_LABELS, backendFor, ConnectionFields } from "@/components/ConnectionFields";
import { RadioPresetSelect } from "@/components/RadioPresetSelect";
import { BackupPanel } from "@/components/BackupPanel";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useApiObject } from "@/hooks/useApiObject";
import { setTiles, type MapDarkStyle, type MapProvider } from "@/lib/leaflet";
import { configApi, type Settings, type SpiBoard } from "@/lib/configApi";

const BANDWIDTHS = [7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500];
const LOG_LEVELS = ["trace", "debug", "info", "warn", "error"];
const MAP_PROVIDERS: { value: MapProvider; label: string }[] = [
  { value: "osm", label: "OpenStreetMap" },
  { value: "carto", label: "CARTO" },
];
const MAP_DARK_STYLES: { value: MapDarkStyle; label: string }[] = [
  { value: "original", label: "Original" },
  { value: "simplified", label: "Simplified" },
];

export function RadioPage() {
  const { item: settings, loading, error, reload } = useApiObject<Settings>(
    "/api/config/settings",
    "Failed to load settings",
  );
  const [saving, setSaving] = useState(false);

  const [connectionType, setConnectionType] = useState("kiss");
  const [connection, setConnection] = useState("");
  const [baudRate, setBaudRate] = useState("115200");
  const [spiBoard, setSpiBoard] = useState("");
  // Blank on every load: the server never returns the token, only whether one is stored.
  const [modemToken, setModemToken] = useState("");
  const [boards, setBoards] = useState<SpiBoard[]>([]);
  const [freq, setFreq] = useState("");
  const [bw, setBw] = useState("");
  const [sf, setSf] = useState("");
  const [cr, setCr] = useState("");
  const [tx, setTx] = useState("");
  const [listenAddr, setListenAddr] = useState("");
  const [mapProvider, setMapProvider] = useState<MapProvider>("osm");
  const [mapDarkStyle, setMapDarkStyle] = useState<MapDarkStyle>("original");
  const [mapTileKey, setMapTileKey] = useState("");
  const [pathHashSize, setPathHashSize] = useState("1");
  // Percentage, like the firmware's `set dutycycle`. Blank = the 50% default.
  const [dutyCycle, setDutyCycle] = useState("");
  const [logLevel, setLogLevel] = useState("info");

  useEffect(() => {
    if (!settings) return;
    setConnectionType(backendFor(settings.connection ?? ""));
    setConnection(settings.connection ?? "");
    setBaudRate(String(settings.baudRate ?? 115200));
    setSpiBoard(settings.spiBoard ?? "");
    setFreq(settings.freq != null ? String(settings.freq) : "");
    setBw(settings.bw != null ? String(settings.bw) : "");
    setSf(settings.sf != null ? String(settings.sf) : "");
    setCr(settings.cr != null ? String(settings.cr) : "");
    setTx(settings.tx != null ? String(settings.tx) : "");
    setListenAddr(settings.listenAddr ?? "");
    setMapProvider(settings.mapProvider);
    setMapDarkStyle(settings.mapDarkStyle);
    setMapTileKey(settings.mapTileKey ?? "");
    setPathHashSize(String(settings.pathHashSize ?? 1));
    setDutyCycle(settings.dutyCycle != null ? String(settings.dutyCycle) : "");
    setLogLevel(settings.logLevel ?? "info");
  }, [settings]);

  // Fetched once on mount: the list is compiled into the binary, so it cannot
  // change while the page is open.
  useEffect(() => {
    configApi
      .getSpiBoards()
      .then(setBoards)
      .catch(() => setBoards([]));
  }, []);

  const spi = connectionType === "spi";

  const onSave = async () => {
    if (!settings) return;
    setSaving(true);
    try {
      await configApi.putSettings({
        // Round-trip the connection type so a radio save never resets it.
        connectionType,
        connection,
        baudRate: parseInt(baudRate, 10) || 115200,
        // Sent only for an SPI radio. Omitted for KISS so the stored board
        // survives a switch to serial and back.
        ...(spi ? { spiBoard } : {}),
        freq: parseFloat(freq) || null,
        bw: parseFloat(bw) || null,
        sf: parseInt(sf, 10) || null,
        cr: parseInt(cr, 10) || null,
        tx: tx === "" ? null : parseInt(tx, 10),
        listenAddr: listenAddr || null,
        mapProvider,
        mapDarkStyle,
        mapTileKey: mapTileKey.trim(), // "" clears
        // Only send a token when one was typed; blank keeps the stored secret.
        ...(modemToken !== "" ? { modemToken } : {}),
        pathHashSize: parseInt(pathHashSize, 10) || 1,
        dutyCycle: dutyCycle.trim() === "" ? null : Number(dutyCycle),
        logLevel: logLevel || null,
        // setupComplete omitted on purpose: the server keeps the stored value.
      });
      toast.success("Radio settings saved");
      setTiles(mapProvider, mapTileKey.trim(), mapDarkStyle);
      reload();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save settings");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-8">
      <PageHeader
        eyebrow="system"
        title="Settings"
        actions={
          <Button
            size="sm"
            onClick={onSave}
            disabled={saving || loading || !settings}
            className="rounded-none font-mono text-[11px] uppercase tracking-[0.12em]"
          >
            {saving ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <Save className="size-3.5" />
            )}
            save
          </Button>
        }
      />

      {error && <LoadErrorAlert message={error} onRetry={reload} />}

      <div className="flex items-start gap-2 border border-warning/40 bg-warning/5 px-3 py-2 font-mono text-[11px] text-warning">
        <AlertTriangle className="size-3.5 shrink-0 mt-0.5" />
        <span>
          Changing connection or radio parameters reconnects the modem and
          restarts all companions (repeater/room sessions drop). Log level
          applies instantly; the web address needs a process restart.
        </span>
      </div>

      {loading ? (
        <Skeleton className="h-64 w-full rounded-none" />
      ) : settings ? (
        <>
          <section className="panel">
            <SectionTitle
              eyebrow={BACKEND_LABELS[connectionType] ?? "kiss modem"}
              title="Connection"
            />
            <div className="p-4 grid grid-cols-1 sm:grid-cols-2 gap-4">
              <ConnectionFields
                connectionType={connectionType}
                setConnectionType={setConnectionType}
                connection={connection}
                setConnection={setConnection}
                baudRate={baudRate}
                setBaudRate={setBaudRate}
                spiBoard={spiBoard}
                setSpiBoard={setSpiBoard}
                boards={boards}
                modemToken={modemToken}
                setModemToken={setModemToken}
                modemTokenSet={settings?.modemTokenSet ?? false}
              />
            </div>
          </section>

          <section className="panel">
            <SectionTitle eyebrow="rf parameters" title="LoRa Radio" />
            <div className="p-4 space-y-4">
              <RadioPresetSelect
                freq={freq}
                bw={bw}
                sf={sf}
                cr={cr}
                onApply={(p) => {
                  setFreq(String(p.freq));
                  setBw(String(p.bw));
                  setSf(String(p.sf));
                  setCr(String(p.cr));
                  // Only when the preset states one: most do not, and defaulting would silently
                  // rewrite a working node's routing to 1 byte.
                  if (p.pathHashSize) setPathHashSize(String(p.pathHashSize));
                }}
              />
              <div className="grid grid-cols-1 sm:grid-cols-3 lg:grid-cols-5 gap-4">
                <TextField
                  label="Frequency (MHz)"
                  value={freq}
                  onChange={setFreq}
                  placeholder="917.375"
                />
              <SelectField
                label="Bandwidth (kHz)"
                value={bw}
                options={BANDWIDTHS.map((b) => ({
                  value: String(b),
                  label: `${b} kHz`,
                }))}
                onChange={setBw}
              />
              <SelectField
                label="Spreading factor"
                value={sf}
                options={Array.from({ length: 8 }, (_, i) => ({
                  value: String(i + 5),
                  label: `SF${i + 5}`,
                }))}
                onChange={setSf}
              />
              <SelectField
                label="Coding rate"
                value={cr}
                options={Array.from({ length: 4 }, (_, i) => ({
                  value: String(i + 5),
                  label: `4/${i + 5}`,
                }))}
                onChange={setCr}
              />
              <TextField
                label="TX power (dBm)"
                value={tx}
                onChange={setTx}
                placeholder="0-22"
              />
              <SelectField
                label="Path hash size"
                value={pathHashSize}
                options={PATH_HASH_SIZE_OPTIONS}
                onChange={setPathHashSize}
                hint="default for every node here · some regions run 2 bytes"
              />
              <TextField
                label="TX duty cycle %"
                value={dutyCycle}
                onChange={setDutyCycle}
                placeholder="50"
                hint="share of each hour this radio may transmit · blank = 50% (the firmware default) · set 1 where a 1% limit applies, e.g. EU868 (0.1 for its stricter sub-bands)"
              />
              </div>
            </div>
          </section>

          <section className="panel">
            <SectionTitle eyebrow="process" title="Service" />
            <div className="p-4 grid grid-cols-1 sm:grid-cols-2 gap-4">
              <TextField
                label="Web listen address"
                value={listenAddr}
                onChange={setListenAddr}
                placeholder=":8080"
                hint="default :8080 · HOST/PORT env vars override this · applies on next process restart"
              />
              <SelectField
                label="Log level"
                value={logLevel}
                options={LOG_LEVELS.map((l) => ({ value: l, label: l }))}
                onChange={setLogLevel}
                hint="-v / -vv flags override this"
              />
              <SelectField
                label="Map tiles"
                value={mapProvider}
                options={MAP_PROVIDERS}
                onChange={(v) => setMapProvider(v as MapProvider)}
                hint={
                  mapProvider === "osm" ? (
                    <>
                      free, no key · run by volunteers under a{" "}
                      <a
                        href="https://operations.osmfoundation.org/policies/tiles/"
                        target="_blank"
                        rel="noreferrer"
                        className="text-primary underline underline-offset-2 hover:text-primary/80"
                      >
                        light-use policy
                      </a>
                    </>
                  ) : (
                    "light and dark styles"
                  )
                }
              />
              {mapProvider === "osm" ? (
                <SelectField
                  label="Dark mode map style"
                  value={mapDarkStyle}
                  options={MAP_DARK_STYLES}
                  onChange={(v) => setMapDarkStyle(v as MapDarkStyle)}
                  hint="OpenStreetMap has no dark style of its own · original inverts its colours, simplified is a plainer dark map with navy water · light mode is unchanged"
                />
              ) : null}
              {mapProvider === "carto" ? (
                <TextField
                  label="CARTO API key"
                  type="password"
                  value={mapTileKey}
                  onChange={setMapTileKey}
                  placeholder="blank = keyless tiles"
                  hint={
                    <>
                      CARTO now needs a key from{" "}
                      <a
                        href="https://carto.com/basemaps/apikey/"
                        target="_blank"
                        rel="noreferrer"
                        className="text-primary underline underline-offset-2 hover:text-primary/80"
                      >
                        carto.com/basemaps/apikey
                      </a>{" "}
                      (free tier: 5M tiles/month) · the key is visible to anyone using this UI
                    </>
                  }
                />
              ) : null}
            </div>
          </section>

          <BackupPanel />
        </>
      ) : null}
    </div>
  );
}
