import { useEffect, useState } from "react";
import { SelectField, TextField } from "@/components/ConfigFields";
import {
  boardHint,
  boardOption,
  configApi,
  defaultBoard,
  type SerialPort,
  type SpiBoard,
} from "@/lib/configApi";

// The stored setting is still one URL string; these controls only spare the operator from typing it.
const BACKENDS = [
  { value: "kiss", label: "KISS modem (serial / TCP)" },
  { value: "openhop", label: "openHop Modem (serial / TCP)" },
  { value: "spi", label: "SPI radio hat" },
];

const TRANSPORTS = [
  { value: "serial", label: "Serial / USB" },
  { value: "tcp", label: "Network (TCP)" },
];

// The buses a Pi header exposes; a hat's own port comes from the board list and is preselected.
const SPI_PORTS = ["SPI0.0", "SPI0.1", "SPI1.0", "SPI1.1", "SPI1.2"];

const DEFAULT_SERIAL = "/dev/ttyACM0";
const KISS_BAUD = "115200";
// openHop firmware runs its serial link at this rate on every board, and at no other.
const OPENHOP_BAUD = "921600";

export function connectionScheme(c: string): "serial" | "tcp" | "spi" | "openhop" {
  if (c.startsWith("tcp://")) return "tcp";
  if (c.startsWith("spi://")) return "spi";
  if (c.startsWith("openhop://")) return "openhop";
  return "serial";
}

// backendFor derives the radio backend from the connection string rather than the stored
// connectionType. Only the connection string decides which driver the server loads, so a stored
// backend that disagrees with it is stale, not a preference.
export function backendFor(connection: string): string {
  const scheme = connectionScheme(connection);
  return scheme === "spi" || scheme === "openhop" ? scheme : "kiss";
}

// BACKEND_LABELS is the one-word name a section eyebrow uses for the chosen backend.
export const BACKEND_LABELS: Record<string, string> = {
  kiss: "kiss modem",
  openhop: "openhop modem",
  spi: "spi radio",
};


export function connectionTarget(c: string): string {
  return c.replace(/^[a-z0-9]+:\/\//, "");
}

// useSerialPorts lists what the host can see; an empty list is also what a fetch failure looks like,
// and every caller renders that the same way.
export function useSerialPorts(enabled: boolean): SerialPort[] {
  const [ports, setPorts] = useState<SerialPort[]>([]);
  useEffect(() => {
    if (!enabled) return;
    configApi.getSerialPorts().then(setPorts).catch(() => setPorts([]));
  }, [enabled]);
  return ports;
}

export interface ConnectionRow {
  label: string;
  value: string;
  sub?: string;
}

// connectionSummary describes a connection the way an operator would say it out loud. The stored
// value is a URL, but a review screen that prints the URL is asking the reader to parse a scheme.
export function connectionSummary(
  connectionType: string,
  connection: string,
  baudRate: string,
  spiBoard: string,
  boards: SpiBoard[],
  ports: SerialPort[],
  modemTokenSet = false,
): ConnectionRow[] {
  const target = connectionTarget(connection);

  if (connectionType === "spi") {
    const board = boards.find((b) => b.name === spiBoard);
    return [
      { label: "Radio backend", value: "SPI radio hat" },
      { label: "Hat", value: board?.label ?? spiBoard ?? "—" },
      { label: "SPI port", value: target || board?.spiPort || "—" },
    ];
  }

  if (connectionType === "openhop") {
    if (target.startsWith("/")) {
      return [
        { label: "Radio backend", value: "openHop Modem over serial" },
        { label: "Device", value: target || "-" },
        { label: "Baud rate", value: baudRate },
      ];
    }
    const sep = target.lastIndexOf(":");
    return [
      { label: "Radio backend", value: "openHop Modem over the network" },
      { label: "Host", value: sep > 0 ? target.slice(0, sep) : target || "-" },
      { label: "Port", value: sep > 0 ? target.slice(sep + 1) : "-" },
      { label: "Token", value: modemTokenSet ? "set" : "none" },
    ];
  }

  if (connectionScheme(connection) === "tcp") {
    const at = target.lastIndexOf(":");
    return [
      { label: "Radio backend", value: "KISS modem over the network" },
      { label: "Host", value: at > 0 ? target.slice(0, at) : target || "—" },
      { label: "Port", value: at > 0 ? target.slice(at + 1) : "—" },
    ];
  }

  const port = ports.find((p) => p.path === target);
  return [
    { label: "Radio backend", value: "KISS modem over serial" },
    // The by-id path is what gets stored, but the board's own name is what identifies it to a person.
    { label: "Device", value: port?.label ?? target ?? "—", sub: port?.label ? port.device : undefined },
    { label: "Baud rate", value: baudRate },
  ];
}

function serialHint(ports: SerialPort[], target: string): string {
  const chosen = ports.find((p) => p.path === target);
  if (chosen?.stable) return `stored as ${chosen.path}, which survives a replug`;
  return "serial devices this host can see right now";
}

export function ConnectionFields({
  connectionType,
  setConnectionType,
  connection,
  setConnection,
  baudRate,
  setBaudRate,
  spiBoard,
  setSpiBoard,
  boards,
  modemToken,
  setModemToken,
  modemTokenSet,
}: {
  connectionType: string;
  setConnectionType: (v: string) => void;
  connection: string;
  setConnection: (v: string) => void;
  baudRate: string;
  setBaudRate: (v: string) => void;
  spiBoard: string;
  setSpiBoard: (v: string) => void;
  boards: SpiBoard[];
  // modemToken is write-only: it starts blank on every load, because a read never returns it.
  modemToken: string;
  setModemToken: (v: string) => void;
  modemTokenSet: boolean;
}) {
  const spi = connectionType === "spi";
  const openhop = connectionType === "openhop";
  const ports = useSerialPorts(!spi);
  const target = connectionTarget(connection);
  // openhop carries both transports under one scheme, so the device path is what tells them apart.
  const transport = spi
    ? "spi"
    : openhop
      ? target.startsWith("/")
        ? "serial"
        : "tcp"
      : connectionScheme(connection);
  const board = boards.find((b) => b.name === spiBoard);

  const setTarget = (v: string) =>
    setConnection(`${openhop ? "openhop" : transport}://${v.trim()}`);

  // An install from before the picker stores the tty. Adopt the same device's stable name so it is
  // one device on one row, not the tty and its own by-id entry offered as two choices. Settings load
  // after this component mounts, so this reacts to the connection too, not just the port list.
  useEffect(() => {
    if (spi || openhop || connectionScheme(connection) !== "serial") return;
    const same = ports.find((p) => p.stable && p.device === connectionTarget(connection));
    if (same) setConnection(`serial://${same.path}`);
  }, [ports, connection, spi, setConnection]);

  const pickBackend = (v: string) => {
    setConnectionType(v);
    if (v !== "openhop" && baudRate === OPENHOP_BAUD) setBaudRate(KISS_BAUD);
    if (v === "spi") {
      const pick = board ?? defaultBoard(boards);
      if (pick && !spiBoard) setSpiBoard(pick.name);
      if (connectionScheme(connection) !== "spi") {
        setConnection(`spi://${pick?.spiPort ?? SPI_PORTS[0]}`);
      }
    } else if (v === "openhop") {
      if (connectionScheme(connection) !== "openhop") setConnection("openhop://");
    } else if (connectionScheme(connection) === "spi" || connectionScheme(connection) === "openhop") {
      setConnection(`serial://${ports[0]?.path ?? DEFAULT_SERIAL}`);
    }
  };

  const pickTransport = (v: string) => {
    const dev = ports[0]?.path ?? DEFAULT_SERIAL;
    if (openhop) {
      setConnection(v === "tcp" ? "openhop://" : `openhop://${dev}`);
      if (v === "serial") setBaudRate(OPENHOP_BAUD);
    } else if (v === "tcp") setConnection("tcp://");
    else setConnection(`serial://${dev}`);
  };

  const pickBoard = (name: string) => {
    setSpiBoard(name);
    const b = boards.find((x) => x.name === name);
    if (b) setConnection(`spi://${b.spiPort}`);
  };

  return (
    <>
      <SelectField
        label="Radio backend"
        value={connectionType}
        options={BACKENDS}
        onChange={pickBackend}
        hint={
          spi
            ? "A radio wired to this host's SPI bus. No MeshCore firmware involved."
            : openhop
              ? "openHop Modem firmware, which owns the radio and does its own listen-before-talk."
              : "MeshCore firmware driving the radio, over serial or TCP."
        }
      />

      {spi ? (
        <>
          {boards.length > 0 ? (
            <SelectField
              label="Radio hat"
              value={spiBoard}
              options={boards.map(boardOption)}
              onChange={pickBoard}
              hint={boardHint(board)}
            />
          ) : (
            <TextField
              label="Radio hat"
              value={spiBoard}
              onChange={setSpiBoard}
              hint="No board list available from the server."
              placeholder="ultrapeaterzero-e22p"
            />
          )}
          <SelectField
            label="SPI port"
            value={target || board?.spiPort || SPI_PORTS[0]}
            options={SPI_PORTS.map((p) => ({ value: p, label: p }))}
            onChange={(v) => setConnection(`spi://${v}`)}
            hint={
              board && target && target !== board.spiPort
                ? `This hat is wired to ${board.spiPort}; change it only if yours is on another bus.`
                : "The chip-select line this hat's NSS is wired to."
            }
          />
        </>
      ) : (
        <>
          <SelectField
            label="Transport"
            value={transport}
            options={TRANSPORTS}
            onChange={pickTransport}
          />
          {transport === "tcp" ? (
            <>
              <TextField
                label="Address"
                value={target}
                onChange={(v) => setTarget(v)}
                placeholder={openhop ? "192.168.1.50:5055" : "192.168.1.50:5000"}
                hint={
                  openhop
                    ? "host:port of an openHop Modem on the network; its own default port is 5055"
                    : "host:port of a MeshCore node exposing its KISS interface over the network"
                }
              />
              {openhop ? (
                <TextField
                  label="Token"
                  type="password"
                  value={modemToken}
                  onChange={setModemToken}
                  placeholder={modemTokenSet ? "\u2022\u2022\u2022\u2022\u2022\u2022 saved" : "blank if the modem has no token"}
                  hint={
                    modemTokenSet
                      ? "leave blank to keep the stored token"
                      : "The modem's own access token. Only network clients authenticate."
                  }
                />
              ) : null}
            </>
          ) : (
            <>
              {ports.length > 0 ? (
                <SelectField
                  label="Device"
                  value={target}
                  options={ports.map((p) => ({
                    value: p.path,
                    label: p.label ? `${p.label} · ${p.device}` : p.device,
                  }))}
                  onChange={(v) => setTarget(v)}
                  hint={serialHint(ports, target)}
                />
              ) : (
                <TextField
                  label="Device"
                  value={target}
                  onChange={(v) => setTarget(v)}
                  placeholder={DEFAULT_SERIAL}
                  hint="No serial devices detected. Plug the modem in, or type its path."
                />
              )}
              {openhop ? (
                <TextField
                  label="Baud rate"
                  value={baudRate}
                  onChange={setBaudRate}
                  disabled
                  hint="openHop Modem firmware runs its serial link at 921600 on every board."
                />
              ) : (
                <TextField
                  label="Baud rate"
                  value={baudRate}
                  onChange={setBaudRate}
                  placeholder={KISS_BAUD}
                />
              )}
            </>
          )}
        </>
      )}
    </>
  );
}
