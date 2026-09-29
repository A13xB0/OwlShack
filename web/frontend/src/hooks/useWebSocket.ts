import { useEffect, useRef, useState } from "react";

import { notifyResume } from "@/lib/resume";

type MessageHandler = (topic: string, data: unknown) => void;

// Wait for any inbound message after an app-level ping before declaring the socket half-open.
const PROBE_TIMEOUT_MS = 5000;

// notify: false for a socket that only watches the connection, so a reconnect doesn't refresh every view twice.
export function useWebSocket(topics: string[], onMessage?: MessageHandler, notify = true) {
  const [connected, setConnected] = useState(false);
  // Cleared by the first open OR the first close, so an unreachable server still reaches "offline".
  const [pending, setPending] = useState(true);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectRef = useRef<number | null>(null);
  const probeRef = useRef<number | null>(null);
  const backoffRef = useRef(1000);
  const lastMsgRef = useRef(0);
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;

  const topicsKey = topics.join(",");

  useEffect(() => {
    let mounted = true;
    let everOpened = false;

    const connect = () => {
      const current = wsRef.current;
      if (
        current &&
        (current.readyState === WebSocket.OPEN ||
          current.readyState === WebSocket.CONNECTING)
      )
        return;

      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const wsUrl = import.meta.env.DEV
        ? "ws://localhost:8080/api/ws"
        : `${protocol}//${window.location.host}/api/ws`;

      const ws = new WebSocket(wsUrl);
      wsRef.current = ws;

      ws.onopen = () => {
        if (!mounted) return;
        setConnected(true);
        setPending(false);
        backoffRef.current = 1000;
        for (const topic of topics) {
          ws.send(JSON.stringify({ action: "subscribe", topic }));
        }
        // Nothing replays what the stream missed while it was down.
        if (everOpened && notify) notifyResume();
        everOpened = true;
      };

      ws.onmessage = (event) => {
        if (!mounted) return;
        lastMsgRef.current = Date.now();
        try {
          const data = JSON.parse(event.data);
          const topic = data.topic;
          if (!topic) return;
          if (onMessageRef.current) onMessageRef.current(topic, data.data);
        } catch (err) {
          console.error("ws parse error", err);
        }
      };

      ws.onclose = () => {
        // resume() may already have replaced this socket; its close must not null out the new one.
        if (!mounted || wsRef.current !== ws) return;
        setConnected(false);
        setPending(false);
        wsRef.current = null;
        reconnectRef.current = window.setTimeout(() => {
          backoffRef.current = Math.min(backoffRef.current * 1.5, 30000);
          connect();
        }, backoffRef.current);
      };

      ws.onerror = () => {
        ws.close();
      };
    };

    // A mobile socket can survive a suspend half-open: still readyState OPEN, receiving nothing.
    const resume = () => {
      if (!mounted || document.visibilityState === "hidden") return;
      const ws = wsRef.current;
      if (!ws || ws.readyState >= WebSocket.CLOSING) {
        if (reconnectRef.current) {
          clearTimeout(reconnectRef.current);
          reconnectRef.current = null;
        }
        backoffRef.current = 1000;
        connect();
        return;
      }
      if (ws.readyState === WebSocket.OPEN && probeRef.current == null) {
        const sentAt = Date.now();
        try {
          ws.send(JSON.stringify({ action: "ping" }));
        } catch {
          ws.close();
          return;
        }
        probeRef.current = window.setTimeout(() => {
          probeRef.current = null;
          if (wsRef.current === ws && lastMsgRef.current < sentAt) {
            // Half-open: close so onclose reconnects on the freshly reset backoff.
            backoffRef.current = 1000;
            ws.close();
          }
        }, PROBE_TIMEOUT_MS);
      }
    };

    connect();
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("focus", resume);
    window.addEventListener("online", resume);
    window.addEventListener("pageshow", resume);

    return () => {
      mounted = false;
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("focus", resume);
      window.removeEventListener("online", resume);
      window.removeEventListener("pageshow", resume);
      if (reconnectRef.current) clearTimeout(reconnectRef.current);
      if (probeRef.current) clearTimeout(probeRef.current);
      reconnectRef.current = null;
      probeRef.current = null;
      wsRef.current?.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topicsKey]);

  return { connected, pending };
}
