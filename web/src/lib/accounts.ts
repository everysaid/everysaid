import type { ChatDetail } from "./api";
import { dateOnly } from "./format";

export type Account = NonNullable<ChatDetail["accounts"]>[number];

// how one of a person's accounts on a service is said in short, never by a service's bare id: its
// number, address or username where it has one, else the user's own name for it, else the name it
// goes by with when it was last active
export function accountShort(a: Account): string {
  return a.handle ?? a.label ?? [a.name, a.last_ts ? dateOnly(a.last_ts) : null].filter(Boolean).join(" · ");
}
