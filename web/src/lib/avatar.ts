/** A person's or a group's photo (null: initials); its key in the address, so a new photo is fetched anew. */
export function avatarUrl(c: { id?: string; type: string; person_id?: number | null; avatar?: string | null }) {
  if (!c.avatar) return null;
  if (c.type === "person") return c.person_id ? personAvatarUrl({ id: c.person_id, avatar: c.avatar }) : null;
  return c.id ? `/api/chats/${c.id}/avatar?v=${c.avatar}` : null;
}

export function personAvatarUrl(p: { id: number; avatar?: string | null }) {
  return p.avatar ? `/api/avatar/${p.id}?v=${p.avatar}` : null;
}

/** The photo a pin of a person's photo source would show (for the choice). */
export function pinnedAvatarUrl(personId: number, pin: string, key?: string | null) {
  return `/api/avatar/${personId}?pin=${encodeURIComponent(pin)}&v=${key ?? ""}`;
}
