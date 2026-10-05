import { useEffect, useState } from "react";
import { QueryClient } from "@tanstack/react-query";

export type LiveEvent =
  | { type: "hello" | "ping" | "changed" }
  | { type: "new"; chats: Record<string, number>; calls: number }
  | { type: "plugin"; instance: number; running?: string | null; live?: boolean }
  | { type: "plugin_log"; instance: number; line: string };

type Listener = (e: LiveEvent) => void;
const listeners = new Set<Listener>();
let socket: WebSocket | null = null;
let connected = false;
const statusListeners = new Set<(c: boolean) => void>();

/** One WebSocket for the whole app, reconnecting with a growing pause; events refresh the queries. */
export function connectEvents(qc: QueryClient) {
  let pause = 1000;
  let stopped = false;
  const open = () => {
    if (stopped) return;
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    socket = new WebSocket(`${proto}//${location.host}/api/events`);
    socket.onopen = () => {
      pause = 1000;
      connected = true;
      statusListeners.forEach((f) => f(true));
    };
    socket.onmessage = (m) => {
      const e = JSON.parse(m.data) as LiveEvent;
      if (e.type === "new") {
        qc.invalidateQueries({ queryKey: ["chats"] });
        for (const id of Object.keys(e.chats)) qc.invalidateQueries({ queryKey: ["stream", id] });
        if (e.calls) qc.invalidateQueries({ queryKey: ["calls"] });
      } else if (e.type === "changed") {
        qc.invalidateQueries();
      } else if (e.type === "plugin") {
        qc.invalidateQueries({ queryKey: ["plugins"] });
      }
      listeners.forEach((f) => f(e));
    };
    socket.onclose = () => {
      connected = false;
      statusListeners.forEach((f) => f(false));
      if (!stopped) setTimeout(open, pause);
      pause = Math.min(pause * 2, 30000);
    };
  };
  open();
  return () => {
    stopped = true;
    socket?.close();
  };
}

export function onEvent(f: Listener) {
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

export function useConnected() {
  const [c, setC] = useState(connected);
  useEffect(() => {
    statusListeners.add(setC);
    return () => {
      statusListeners.delete(setC);
    };
  }, []);
  return c;
}
