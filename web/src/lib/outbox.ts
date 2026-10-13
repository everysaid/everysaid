// What this device wrote that the server has not taken yet (it could not be reached: down, no
// network, the answer lost): kept in IndexedDB, a file with it, until the server has it. Sent again
// with the same id when the connection comes back (the server sends one id only once), or by the
// user's word; one the server refused stays, with its reason, to be sent again or discarded.
// Once the server has a message it keeps it itself, if it cannot go yet (GET /api/chats/{id}/outbox).
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "./api";

export interface Unsent {
  id: string;
  chatId: string;
  text: string;
  service: string | null;
  conversationId?: number | null;   // through this conversation of the chat (their handle, where they have more than one on the service)
  replyTo: number | null;
  mentions: { start: number; length: number; address_id: number }[];
  file: Blob | null;
  fileName: string | null;
  animated?: boolean;               // the file sent as a GIF
  createdAt: number;
  state: "waiting" | "failed";      // waiting: for the server; failed: refused by it (error)
  error: string | null;
}

const DB = "everysaid-outbox";
const STORE = "unsent";
let opened: Promise<IDBDatabase> | null = null;

function db(): Promise<IDBDatabase> {
  opened ??= new Promise((ok, fail) => {
    const r = indexedDB.open(DB, 1);
    r.onupgradeneeded = () => r.result.createObjectStore(STORE, { keyPath: "id" });
    r.onsuccess = () => ok(r.result);
    r.onerror = () => fail(r.error);
  });
  return opened;
}

async function tx<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const d = await db();
  return new Promise((ok, fail) => {
    const r = fn(d.transaction(STORE, mode).objectStore(STORE));
    r.onsuccess = () => ok(r.result);
    r.onerror = () => fail(r.error);
  });
}

const listeners = new Set<() => void>();
const changed = () => listeners.forEach((f) => f());

/** One the server keeps, to send when it can (GET /api/chats/{id}/outbox). */
export interface Kept {
  id: string;
  text: string;
  service: string;
  file_name: string;
  file_type?: string;
  created_at: number;
  state: "queued" | "sending" | "failed";
  error: { code?: string; text?: string; params?: Record<string, unknown> } | null;
}

/** What the server keeps of a chat to send, asked again every 5 s while something is on its way: not
 *  only when the server's events say so (they may not come, on a connection that died without saying). */
export function useKept(chatId: string) {
  return useQuery({ queryKey: ["outbox", chatId], queryFn: () => api.get<{ items: Kept[] }>(`/api/chats/${chatId}/outbox`),
    refetchInterval: (q) => (q.state.data?.items.some((k) => k.state !== "failed") ? 5000 : false) });
}

// those being sent from the chat now (not sent again from here meanwhile)
const inFlight = new Set<string>();
export const isFlying = (id: string) => inFlight.has(id);
export function flying(id: string, on: boolean) {
  if (on) inFlight.add(id);
  else inFlight.delete(id);
  changed();
}

export async function keepUnsent(u: Unsent) {
  await tx("readwrite", (s) => s.put(u));
  changed();
}

export async function dropUnsent(id: string) {
  await tx("readwrite", (s) => s.delete(id));
  changed();
}

export async function allUnsent(): Promise<Unsent[]> {
  return ((await tx("readonly", (s) => s.getAll())) as Unsent[]).sort((a, b) => a.createdAt - b.createdAt);
}

/** This device's unsent messages of a chat, as they change. */
export function useUnsent(chatId: string): Unsent[] {
  const [list, setList] = useState<Unsent[]>([]);
  useEffect(() => {
    let live = true;
    const load = () => allUnsent().then((all) => { if (live) setList(all.filter((u) => u.chatId === chatId)); }).catch(() => {});
    load();
    listeners.add(load);
    return () => { live = false; listeners.delete(load); };
  }, [chatId]);
  return list;
}

/** Whether a failure means the server was not reached (or did not answer, or failed itself: 500, as
 *  while it is stopped), rather than a refusal: kept, and sent again with its id (sent once). */
export function unreached(e: unknown) {
  return !(e instanceof ApiError) || [500, 502, 503, 504].includes(e.status);
}

export interface Went { conversation_id?: number; messages?: unknown[]; queued?: boolean; id?: string }

/** Sends a message with its id: what the server said (queued: it keeps it, to send when it can).
 *  An answer that does not come (lost on the way) is asked for again with the same id, every 15
 *  seconds: the server sends a message once, and says again what became of it. Not one with a file:
 *  its first sending may still be under way, and the file would go up again each time. */
export function sendUnsent(u: Unsent): Promise<Went> {
  if (u.file) return postUnsent(u);
  return new Promise((ok, fail) => {
    let over = false;
    const end = (f: () => void) => { if (!over) { over = true; clearInterval(again); f(); } };
    const ask = () => postUnsent(u).then((w) => end(() => ok(w)), (e) => end(() => fail(e)));
    const again = setInterval(ask, 15_000);
    ask();
  });
}

function postUnsent(u: Unsent): Promise<Went> {
  if (u.file) {
    const form = new FormData();
    form.append("text", u.text);
    form.append("client_id", u.id);
    if (u.service) form.append("service", u.service);
    if (u.conversationId) form.append("conversation_id", String(u.conversationId));
    if (u.replyTo) form.append("reply_to", String(u.replyTo));
    if (u.mentions.length) form.append("mentions", JSON.stringify(u.mentions));
    if (u.animated) form.append("animated", "1");
    form.append("file", u.file, u.fileName ?? "file");
    return api.form(`/api/chats/${u.chatId}/send`, form);
  }
  return api.post(`/api/chats/${u.chatId}/send`, { text: u.text, client_id: u.id, service: u.service, conversation_id: u.conversationId ?? undefined, reply_to: u.replyTo ?? undefined,
    mentions: u.mentions.length ? u.mentions : undefined });
}

let flushing = false;

/** Sends again what waits for the server (all chats): once the connection is back, or asked. */
export async function flushUnsent(only?: string) {
  if (flushing) return;
  flushing = true;
  try {
    for (const u of await allUnsent()) {
      if ((only ? u.id !== only : u.state !== "waiting") || inFlight.has(u.id)) continue;
      try {
        await sendUnsent(u);
        await dropUnsent(u.id);          // the server has it: sent, or kept by it
      } catch (e) {
        if (unreached(e)) {
          if (u.state !== "waiting") await keepUnsent({ ...u, state: "waiting", error: null });
          break;                          // still not reached: the rest after it, in order, later
        }
        await keepUnsent({ ...u, state: "failed", error: (e as Error).message });
      }
    }
  } finally {
    flushing = false;
  }
}
