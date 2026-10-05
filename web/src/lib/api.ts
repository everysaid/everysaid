// The server's API. Every change carries X-Chronika: 1 (the server refuses changes without it); every
// request says the interface's language (X-Lang), in which the server words a source's errors.
import i18n from "./i18n";

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

/** A server's error in the user's language: its code (errors.*), a source's own words, or the reason. */
function explain(d: { code?: string; params?: Record<string, unknown>; text?: string } | unknown) {
  const e = (d ?? {}) as { code?: string; params?: Record<string, unknown>; text?: string };
  if (e.text) return e.text;
  if (!e.code) return i18n.t("errors.failed");               // e.g. a malformed request
  const said = i18n.t(`errors.${e.code}`, { ...e.params, defaultValue: e.code });
  return e.code === "failed" && e.params?.reason ? `${said}: ${e.params.reason}` : said;
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: {
      "X-Lang": i18n.language,
      ...(method !== "GET" ? { "X-Chronika": "1" } : {}),
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) {
    let msg = r.statusText;
    try {
      const j = await r.json();
      msg = typeof j.detail === "string" ? j.detail : explain(j.detail);
    } catch {
      /* not JSON */
    }
    if (r.status === 401) window.dispatchEvent(new Event("chronika:logged-out"));
    throw new ApiError(r.status, msg);
  }
  return r.json() as Promise<T>;
}

export const api = {
  get: <T>(p: string) => call<T>("GET", p),
  post: <T>(p: string, b?: unknown) => call<T>("POST", p, b ?? {}),
  patch: <T>(p: string, b: unknown) => call<T>("PATCH", p, b),
  put: <T>(p: string, b: unknown) => call<T>("PUT", p, b),
  del: <T>(p: string) => call<T>("DELETE", p),
};

export function qs(params: Record<string, string | number | boolean | undefined | null>) {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? `?${s}` : "";
}

// ---- types ----------------------------------------------------------------------------------------

export type ChatType = "person" | "group" | "conversation";

export interface LastItem {
  type: "message" | "call";
  ts: number;
  outgoing: boolean;
  kind?: string;
  text?: string;
  service: string;
  sender?: string | null;
  subtype?: string | null;
  deleted?: boolean;
  answered?: boolean;
  video?: boolean;
  detail?: string | null;
}

export interface ChatSummary {
  id: string;
  type: ChatType;
  title: string;
  person_id?: number | null;
  conversation_id?: number | null;
  services: string[];
  last_ts: number;
  last: LastItem | null;
  unread: number;
  pinned: boolean;
  muted: boolean;
  archived: boolean;
  avatar: boolean;
}

export interface Handle {
  kind: string;
  value: string;
  service: string | null;
  label: string;
  address_id: number;
}

export interface Person {
  id: number;
  name: string;
  given_name: string | null;
  note: string | null;
  handles: Handle[];
  me: boolean;
  contact: { id: number; name: string; organization: string | null } | null;
  contacts: { id: number; name: string }[];      // more than one contact lists their handles
  name_from: string;                             // user, contacts, <service>/<kind>, handle
  name_source: string | null;                    // where the user pinned it to come from
  self_named: boolean;                           // a name they chose themselves
  aka: { name: string; source: string; first_seen: number; last_seen: number; current: boolean }[];
  avatar: boolean;
  stats: { messages: number; calls: number; first: number | null; last: number | null; by_service: Record<string, number> };
  groups: { chat_id: string; title: string | null }[];
}

export type StateField = "archived" | "muted" | "pinned" | "read_until";

export interface ChatDetail {
  id: string;
  type: ChatType;
  title: string;
  services: string[];
  sendable: string[];       // the services something can send to now
  replyable: string[];      // those where an answer to a given message can be sent
  person_id?: number | null;
  conversation_id?: number | null;
  conversations: number[];
  last_ts: number;
  pinned: boolean;
  muted: boolean;
  archived: boolean;
  state_from: Partial<Record<StateField, string | null>>;                    // "user" or the service that decided
  state_reports: Partial<Record<StateField, { service: string; value: number | boolean }[]>>;
  state_user: Partial<Record<StateField, { value: number; set_at: number; always: number }>>;
  person?: Person;
  members?: { person_id: number | null; name: string | null }[];
}

export interface Attachment {
  sha256: string;
  mime: string | null;
  size: number;
  available: "local" | "library" | "gone";
}

export interface Reaction {
  emoji: string | null;
  code: string | null;
  count: number;
  mine: boolean;
  who: string | null;
}

export interface MessageItem {
  type: "message";
  id: number;
  ts: number;
  cursor: string;
  service: string;
  conversation_id: number;
  outgoing: boolean;
  kind: string;
  subtype: string | null;
  text: string | null;
  sender_id: number | null;
  sender: string | null;
  sender_self_named?: boolean;  // a name they chose themselves (shown marked)
  reply_to: number | null;
  reply_text: string | null;
  reply?: { id: number; text: string; outgoing: boolean; sender: string | null; kind: string };
  edited: boolean;
  deleted: boolean;
  forwarded: boolean;
  starred: boolean;
  status: string | null;
  keyed?: boolean;          // the service's own id is known: an answer to it can be sent
  location: { lat: number | null; lon: number | null; place: string | null } | null;
  reactions: Reaction[];
  attachments: Attachment[];
  chat_id?: string;
  chat_title?: string;
  highlight?: [string, boolean][];
}

export interface CallItem {
  type: "call";
  id: number;
  ts: number;
  cursor: string;
  service: string;
  outgoing: boolean;
  answered: boolean;
  duration: number;
  detail: string | null;
  video: boolean;
  attempts: number;
  with: string | null;
  chat_id?: string | null;
}

export type StreamItem = MessageItem | CallItem;

export interface StreamPage {
  items: StreamItem[];
  has_older: boolean;
  has_newer: boolean;
}

export interface MediaItem {
  sha256: string;
  mime: string | null;
  size: number;
  available: "local" | "library" | "gone";
  message_id: number;
  ts: number;
  chat_id: string | null;
  decision: "keep" | "remove" | "library" | null;
}

export interface SettingField {
  key: string;
  label: string;
  type: "text" | "path" | "url" | "number" | "bool" | "secret" | "select";
  required: boolean;
  default: unknown;
  help: string;
  options: string[];
}

export interface PluginManifest {
  id: string;
  name: string;
  kind: "source" | "library" | "contacts";
  services: string[];
  description: string;
  modes: string[];
  platforms: string[];
  available: boolean;
  needs: string[];
  settings: SettingField[];
  can_send: boolean;
  actions: { id: string; label: string }[];
  has_chats: boolean;
}

export interface PluginChat {
  id: number;
  kind: string;
  title: string | null;
  archived: boolean;
  messages: number;
  first: number;
  last: number;
  import: boolean;
  media: boolean;
}

export interface PluginInstance {
  id: number;
  plugin: string;
  kind: "source" | "library" | "contacts";
  label: string;
  settings: Record<string, unknown>;
  enabled: boolean;
  is_default: boolean;
  last_run: number | null;
  last_status: string | null;
  known: boolean;
  name: string;
  running: string | null;
  ready: boolean;
  ready_text: string;
  live_capable: boolean;
  live: boolean;
  can_send: boolean;
  log: string[];
}

export interface Stats {
  messages: number;
  calls: number;
  people: number;
  groups: number;
  by_service: Record<string, number>;
  calls_by_service: Record<string, number>;
  by_year: Record<string, number>;
  first: number | null;
  last: number | null;
  top_people: { chat_id: string; title: string; messages: number }[];
}

export interface Account {
  user: { id: number; name: string; archive: string; created_at: number };
  passkeys: { id: string; name: string | null; created_at: number; last_used: number | null }[];
  sessions: { id: string; created_at: number; last_seen: number; agent: string; ip: string; via: string; current: boolean }[];
  recovery_left: number;
  audit: { ts: number; event: string; detail: string | null }[];
  has_password: boolean;
}

/** Where people's names come from, most trusted first (the plugins' weights, or the user's order). */
export interface NameSources {
  order: { id: string; label: string; weight: number }[];
  default: { id: string; label: string; weight: number }[];
  custom: boolean;
}
