import type { TFunction } from "i18next";

/** What a notice says (the archive's `notice` table; the codes are in docs/design.md): its code and
 *  values, a person in them as {address, name, person_id, me} or the owner as {self: true}. */
export interface Notice {
  code: string;
  args: Record<string, unknown>;
}

type Person = { self?: boolean; me?: boolean; name?: string | null } | null | undefined;
type Action = Record<string, unknown> & { type?: string };

const isMe = (p: Person) => !!p && (!!p.self || !!p.me);

/** A time in words: the largest unit it is a whole number of (a week, 8 hours, 30 seconds). */
export function durationWords(seconds: number, t: TFunction): string {
  for (const [unit, size] of [["weeks", 604800], ["days", 86400], ["hours", 3600], ["minutes", 60]] as const) {
    if (seconds >= size && seconds % size === 0) return t(`notice.duration.${unit}`, { count: seconds / size });
  }
  return t("notice.duration.seconds", { count: seconds });
}

/** The lines a notice says, in the user's language: one for each action of a group's change. */
export function noticeLines(n: Notice, t: TFunction): string[] {
  const a = n.args;
  const by = a.by as Person;
  const name = (p: Person) => (isMe(p) && !p?.name ? t("notice.you") : p?.name || t("common.unknown"));
  // _self: the owner did it; _you: it was done to the owner; _anon: who did it is not known
  const say = (key: string, who?: Person, extra: Record<string, unknown> = {}) =>
    t(`notice.${key}`, { context: isMe(by) ? "self" : isMe(who) ? "you" : !by ? "anon" : undefined, by: name(by), who: name(who), ...extra });
  const timer = (s: unknown) => (typeof s === "number" && s > 0 ? say("timer", undefined, { duration: durationWords(s, t) }) : say("timerOff"));
  switch (n.code) {
    case "group": {
      const actions = (Array.isArray(a.actions) ? a.actions : []) as Action[];
      if (!actions.length) return [say("group.changed")];
      return actions.map((x) => {
        const who = x.who as Person;
        // the owner adding themselves: their joining
        if (x.type === "added" && isMe(who) && isMe(by)) return t("notice.group.joined", { context: "you" });
        // the one who acted is the one it is about: say it of them
        const own = (key: string) => t(`notice.group.${key}`, { context: isMe(who) ? "you" : undefined, who: name(who) });
        switch (x.type) {
          case "joined": case "joined_link": case "left": case "invite_accepted": case "invite_declined": case "requested": case "request_withdrawn":
            return own(x.type);
          case "added": case "removed": case "invite_revoked": case "request_approved": case "request_denied": case "banned": case "unbanned":
            return say(`group.${x.type}`, who);
          case "invited":
            return who ? say("group.invited", who) : say("group.invitedSomeone");
          case "admin":
            return say(x.on ? "group.adminOn" : "group.adminOff", who);
          case "title": case "created":
            return say(`group.${x.type}`, undefined, { title: String(x.title ?? "") });
          case "topic": // a forum's topic: made, renamed, closed or opened again
            if (x.closed === true) return say("group.topicClosed");
            if (x.closed === false) return say("group.topicOpened");
            return say(x.created ? "group.topic" : "group.topicTitle", undefined, { title: String(x.title ?? "") });
          case "approval": // on or off where the service said which
            return say(x.on === true ? "group.approvalOn" : x.on === false ? "group.approvalOff" : "group.approval");
          case "avatar":
            return say(x.removed ? "group.avatarRemoved" : "group.avatar");
          case "description": case "link_reset": case "ended":
            return say(`group.${x.type}`);
          case "timer":
            return timer(x.seconds);
          case "access_info": case "access_members":
            return say(`group.${x.type}`, undefined, { level: t(`notice.level.${String(x.level)}`, { defaultValue: "—" }) });
          case "access_link":
            return say(x.level === "off" || x.level === "unknown" ? "group.linkOff" : x.level === "admins" ? "group.linkApproval" : "group.linkOn");
          case "announcements":
            return say(x.on ? "group.announcementsOn" : "group.announcementsOff");
          default:
            return say("group.changed");
        }
      });
    }
    case "timer":
      return [timer(a.seconds)];
    case "pin":
      return [typeof a.seconds === "number" && a.seconds > 0 ? say("pinFor", undefined, { duration: durationWords(a.seconds, t) }) : say("pin")];
    case "group_call":
      if (typeof a.seconds === "number" && a.seconds > 0) return [t("notice.group_callLasted", { duration: durationWords(a.seconds, t) })];
      if (typeof a.scheduled === "number" && a.scheduled > 0)
        return [t("notice.group_callScheduled", { date: new Date(a.scheduled * 1000).toLocaleString() })];
      return [say("group_call")];
    case "group_call_invite":
      return [say("group_call_invite", undefined, { who: (Array.isArray(a.who) ? (a.who as Person[]) : []).map(name).join(", ") })];
    case "unpin": case "poll_end": case "payment": case "gift": case "unsupported": case "unreadable":
    case "signed_up": case "screenshot": case "view_once":
      return [say(n.code)];
    case "story": // a story shared, or one that mentions the owner
      return [a.mention ? say("story_mention") : t("notice.story")];
    case "story_reply": case "story_reaction":
      return [t(`notice.${n.code}`)];
  }
  return [];
}

/** A poll's options with their votes, where the notice is a poll's. */
export function pollOf(n: Notice | null | undefined): { question: string; options: { text: string; votes: number | null }[]; multiple: boolean; ended: boolean; voters: number | null } | null {
  if (!n || n.code !== "poll") return null;
  const options = (Array.isArray(n.args.options) ? n.args.options : []) as { text?: string; votes?: number | null }[];
  return {
    question: String(n.args.question ?? ""),
    // votes null: not known (the service gave only how many voted)
    options: options.map((o) => ({ text: String(o.text ?? ""), votes: o.votes == null ? null : Number(o.votes) })),
    multiple: !!n.args.multiple,
    ended: !!n.args.ended,
    voters: typeof n.args.voters === "number" ? n.args.voters : null,
  };
}
