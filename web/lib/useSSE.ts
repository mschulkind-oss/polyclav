"use client";

import { useEffect, useRef, useState } from "react";

export type SSEHandlers = Record<string, (data: unknown) => void>;

/**
 * useSSE subscribes to the daemon's /api/events stream and dispatches
 * each named SSE event's JSON payload to the matching handler. Returns
 * the live connection state (drives the header dot).
 *
 * EventSource auto-reconnects on transient errors; when the browser
 * gives up entirely (readyState CLOSED — e.g. the daemon was down at
 * connect time), the hook tears down and retries every 2s so a restarted
 * daemon heals without a reload. Handlers are kept in a ref: callers may
 * pass a fresh object every render without re-subscribing, but the SET
 * of event names is fixed by the first render.
 */
export function useSSE(url: string, handlers: SSEHandlers): boolean {
  const [connected, setConnected] = useState(false);
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  useEffect(() => {
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let disposed = false;

    const connect = () => {
      es = new EventSource(url);
      es.onopen = () => setConnected(true);
      es.onerror = () => {
        setConnected(false);
        if (!disposed && es && es.readyState === EventSource.CLOSED) {
          es.close();
          es = null;
          retry = setTimeout(connect, 2000);
        }
      };
      for (const type of Object.keys(handlersRef.current)) {
        es.addEventListener(type, (ev: MessageEvent) => {
          let data: unknown;
          try {
            data = JSON.parse(ev.data);
          } catch {
            return; // malformed frame — skip, keep the stream alive
          }
          handlersRef.current[type]?.(data);
        });
      }
    };

    connect();
    return () => {
      disposed = true;
      if (retry !== undefined) clearTimeout(retry);
      es?.close();
    };
  }, [url]);

  return connected;
}
