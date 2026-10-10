import { api, type ChatSummary } from "./api";
import type { LiveNotification } from "./events";

export const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
/** The switch is there wherever notifications are: without push (Ferdium, Chromium) the page shows them itself. */
export const notifySupported = () => "Notification" in window;

// A browser without a push service: this device is notified by the open page (the "new" event), not by push.
const LOCAL = "notify-local";
export const localNotify = () => localStorage.getItem(LOCAL) === "1" && "Notification" in window && Notification.permission === "granted";

// The notifications this page showed itself, by chat: closed once the chat is read.
const shown = new Map<string, { note: Notification; ts: number }>();

/** A notification the page shows itself (no push): a click opens its chat. */
export function showLocal(n: LiveNotification, open: (chat: string) => void) {
  const note = new Notification(n.title, { body: n.body, tag: n.tag, icon: "/icon-192.png" });
  note.onclick = () => {
    window.focus();
    open(n.chat);
    note.close();
  };
  shown.set(n.chat, { note, ts: n.ts });
}

/** The notifications of chats read since (here or on another device) closed: the page's own and the
 *  pushes', while the app is open. Only where the list has the message notified of (ts), read. */
export async function closeRead(chats: ChatSummary[]) {
  const read = new Map(chats.filter((c) => !c.unread).map((c) => [c.id, c.last_ts]));
  const isRead = (chat: unknown, ts: unknown) => typeof chat === "string" && typeof ts === "number" && (read.get(chat) ?? -1) >= ts;
  for (const [chat, s] of shown) {
    if (isRead(chat, s.ts)) {
      s.note.close();
      shown.delete(chat);
    }
  }
  if (!("serviceWorker" in navigator)) return;
  const reg = await navigator.serviceWorker.getRegistration();
  for (const n of (await reg?.getNotifications().catch(() => [])) ?? []) {
    const d = n.data as { chat?: string; ts?: number } | null;
    if (isRead(d?.chat, d?.ts)) n.close();
  }
}

export const isIos = () => /iPhone|iPad|iPod/.test(navigator.userAgent);
export const isStandalone = () =>
  matchMedia("(display-mode: standalone)").matches || (navigator as any).standalone === true;

function urlB64ToUint8Array(s: string) {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

export async function currentSubscription() {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration();
  return reg ? reg.pushManager.getSubscription() : null;
}

export async function enablePush() {
  const perm = await Notification.requestPermission();
  if (perm !== "granted") throw new Error("denied");
  if (!pushSupported()) return localStorage.setItem(LOCAL, "1");
  const reg = await navigator.serviceWorker.ready;
  const { key } = await api.get<{ key: string }>("/api/push/key");
  let sub: PushSubscription;
  try {
    sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlB64ToUint8Array(key) });
  } catch (e: any) {
    // no push service (Electron, so Ferdium; Chromium without Google's; Brave with it turned off)
    if (e?.name !== "AbortError") throw e;
    return localStorage.setItem(LOCAL, "1");
  }
  localStorage.removeItem(LOCAL);
  await api.post("/api/push/subscribe", sub.toJSON());
}

export async function disablePush() {
  localStorage.removeItem(LOCAL);
  const sub = await currentSubscription();
  if (sub) {
    await api.post("/api/push/unsubscribe", { endpoint: sub.endpoint });
    await sub.unsubscribe();
  }
}
