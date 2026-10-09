// What this device wrote that the server has not taken yet (it could not be reached: down, no
// network, the answer lost): kept in IndexedDB, a file with it, until the server has it. Sent again
// with the same id when the connection comes back (the server sends one id only once), or by the
// user's word; one the server refused stays, with its reason, to be sent again or discarded.
// Once the server has a message it keeps it itself, if it cannot go yet (GET /api/chats/{id}/outbox).
import { useEffect, useState } from "react";
import { api, ApiError } from "./api";

export interface Unsent {
  id: string;
  chatId: string;
  text: string;
  service: string | null;
  replyTo: number | null;
  mentions: { start: number; length: number; address_id: number }[];
  file: Blob | null;
  fileName: string | null;
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

// those being sent from the chat now (shown there as they are sent, not here)
const inFlight = new Set<string>();
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
    const load = () => allUnsent().then((all) => { if (live) setList(all.filter((u) => u.chatId === chatId && !inFlight.has(u.id))); }).catch(() => {});
    load();
    listeners.add(load);
    return () => { live = false; listeners.delete(load); };
  }, [chatId]);
  return list;
}

/** Whether a failure means the server was not reached (or did not answer), rather than a refusal. */
export function unreached(e: unknown) {
  return !(e instanceof ApiError) || [502, 503, 504].includes(e.status);
}

export interface Went { conversation_id?: number; messages?: unknown[]; queued?: boolean; id?: string }

/** Sends a message with its id: what the server said (queued: it keeps it, to send when it can). */
export function sendUnsent(u: Unsent): Promise<Went> {
  if (u.file) {
    const form = new FormData();
    form.append("text", u.text);
    form.append("client_id", u.id);
    if (u.service) form.append("service", u.service);
    if (u.replyTo) form.append("reply_to", String(u.replyTo));
    if (u.mentions.length) form.append("mentions", JSON.stringify(u.mentions));
    form.append("file", u.file, u.fileName ?? "file");
    return api.form(`/api/chats/${u.chatId}/send`, form);
  }
  return api.post(`/api/chats/${u.chatId}/send`, { text: u.text, client_id: u.id, service: u.service, reply_to: u.replyTo ?? undefined,
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
