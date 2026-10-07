// What the chats keep while the user is elsewhere: each chat's unsent text (on this device), where
// each was being read (this tab, a reload included), and the chat open last, for the Chats tab.

const DRAFTS = "chat-drafts";
const LAST = "chat-last";

function drafts(): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(DRAFTS) ?? "{}");
  } catch {
    return {};
  }
}

export function draft(chat: string) {
  return drafts()[chat] ?? "";
}

export function keepDraft(chat: string, text: string) {
  const all = drafts();
  if (text) all[chat] = text;
  else delete all[chat];
  localStorage.setItem(DRAFTS, JSON.stringify(all));
}

const PLACES = "chat-places";
type Place = { ts: number; cursor: string; offset?: number };     // offset: px scrolled past its top

function allPlaces(): Record<string, Place> {
  try {
    return JSON.parse(sessionStorage.getItem(PLACES) ?? "{}");
  } catch {
    return {};
  }
}

/** The item at the top when the user left the chat higher up (none: at its end). */
export function place(chat: string): Place | undefined {
  return allPlaces()[chat];
}

export function keepPlace(chat: string, at: Place | null) {
  const all = allPlaces();
  if (at) all[chat] = at;
  else delete all[chat];
  sessionStorage.setItem(PLACES, JSON.stringify(all));
}

export interface LastChat { chatId: string; hide?: string }

export function lastChat(): LastChat | null {
  try {
    return JSON.parse(localStorage.getItem(LAST) ?? "null");
  } catch {
    return null;
  }
}

export function keepLastChat(chat: LastChat | null) {
  if (chat) localStorage.setItem(LAST, JSON.stringify(chat));
  else localStorage.removeItem(LAST);
}

/** The chat list as it was left: its filters, and where it was scrolled (the open chat's height on
 * screen, else the first row in view). */
export interface ListPlace {
  filter: string;
  q: string;
  archived: boolean;
  active?: { id: string; at: number };
  top?: { id: string; offset: number };
}

const LIST = "chat-list";

export function listPlace(): ListPlace | null {
  try {
    return JSON.parse(sessionStorage.getItem(LIST) ?? "null");
  } catch {
    return null;
  }
}

export function keepListPlace(p: ListPlace) {
  sessionStorage.setItem(LIST, JSON.stringify(p));
}

/** The people list as it was left (this tab): its search, how many rows were loaded, and the first
 * row in view. */
export interface PeoplePlace {
  q: string;
  label?: number;                // the label the list was filtered by
  loaded: number;
  top?: { id: number; index: number; offset: number };
}

const PEOPLE = "people-list";

export function peoplePlace(): PeoplePlace | null {
  try {
    return JSON.parse(sessionStorage.getItem(PEOPLE) ?? "null");
  } catch {
    return null;
  }
}

export function keepPeoplePlace(p: PeoplePlace) {
  sessionStorage.setItem(PEOPLE, JSON.stringify(p));
}
