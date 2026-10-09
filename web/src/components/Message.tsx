import { memo, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { Check, CheckCheck, CornerUpLeft, FileText, Forward, ImageOff, MapPin, Pencil, Phone, Pin, PhoneIncoming, PhoneMissed, PhoneOutgoing, Play, Plus, SmilePlus, Trash2, Video } from "lucide-react";
import { thumbUrl, type Attachment, type CallItem, type Mention, type MessageItem, type Receipts } from "@/lib/api";
import { bytes, duration, time } from "@/lib/format";
import { service } from "@/lib/services";
import { noticeLines, pollOf } from "@/lib/notice";
import { cn } from "@/lib/utils";
import { Avatar, Menu, MenuContent, MenuItem, MenuTrigger } from "./ui";

const URL_RE = /(https?:\/\/[^\s<>"')\]]+)/g;
const BIDI = /[\u202a-\u202e\u2066-\u2069]/g;      // direction marks around a name (Viber writes "\u202a@Name\u202c")

/** Text with its links made clickable (http and https only), the people it names shown by name
 * (a link to them), and search matches marked. */
export function RichText({ text, marks, mentions }: { text: string; marks?: [string, boolean][]; mentions?: Mention[] }) {
  if (marks?.length) {
    return <>{marks.map(([s, m], i) => (m ? <mark key={i} className="mark">{s}</mark> : <span key={i}>{s}</span>))}</>;
  }
  const parts = text.split(URL_RE);
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <a key={i} href={p} target="_blank" rel="noopener noreferrer" className="break-all underline decoration-current/40 underline-offset-2 hover:decoration-current">
            {p}
          </a>
        ) : (
          <Named key={i} text={p} mentions={mentions} />
        ),
      )}
    </>
  );
}

/** A piece of text with each mention's token (as the service writes it) said as the person's name. */
function Named({ text, mentions }: { text: string; mentions?: Mention[] }) {
  const { t } = useTranslation();
  const known = (mentions ?? []).filter((m) => m.token && text.includes(m.token));
  if (!known.length) return <span>{text}</span>;
  const by = new Map(known.map((m) => [m.token!, m]));
  // each a whole: a name not inside a longer word (one name the start of another), a number not inside a longer one
  const re = new RegExp(`(?<![\\p{L}\\p{N}_])(${[...by.keys()].sort((a, b) => b.length - a.length)
    .map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})(?![\\p{L}\\p{N}_])`, "u");
  return (
    <span>
      {text.split(re).map((p, i) => {
        const m = i % 2 === 1 ? by.get(p) : undefined;
        if (!m) return p;
        const bare = p.replace(BIDI, "");
        const name = m.me ? t("common.me") : m.name || bare.replace(/^@/, "");
        const label = bare.startsWith("@") ? `@${name}` : name;
        return m.person_id && !m.me ? (
          <Link key={i} to="/people/$personId" params={{ personId: String(m.person_id) }} data-mention className="mention" title={bare}>{label}</Link>
        ) : (
          <span key={i} data-mention className="mention" title={bare}>{label}</span>
        );
      })}
    </span>
  );
}

/** ✓ sent, ✓✓ delivered to all it went to, coloured when all read it; a button to who and when. */
function Ticks({ r, onInfo }: { r: Receipts; onInfo?: () => void }) {
  const { t } = useTranslation();
  const read = r.read >= r.to, delivered = r.delivered >= r.to;
  const label = read ? t("chat.readAll") : delivered ? t("chat.deliveredAll") : t("chat.sent");
  const Icon = delivered ? CheckCheck : Check;
  return (
    <button data-ticks={read ? "read" : delivered ? "delivered" : "sent"} onClick={onInfo} aria-label={`${label} · ${t("chat.messageInfo")}`}
      title={label} className="-my-1 rounded px-0.5 py-1 hover:bg-white/15">
      <Icon className={cn("size-3.5", read && "text-tick-read")} />
    </button>
  );
}

// LoopVideo is a GIF (sent as a short video without sound): played in place, silent and over and
// over, while it is in view; fetched only once it first comes into view.
function LoopVideo({ a, className }: { a: Attachment; className: string }) {
  const ref = useRef<HTMLVideoElement>(null);
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting) {
        setSeen(true);
        v.play().catch(() => {});
      } else v.pause();
    });
    io.observe(v);
    return () => io.disconnect();
  }, []);
  return (
    <video ref={ref} src={seen ? `/api/media/${a.sha256}/original` : undefined} poster={thumbUrl(a.sha256)}
      muted loop playsInline autoPlay preload="none" className={className} />
  );
}

export function Thumb({ a, onOpen, className, loop }: { a: Attachment; onOpen?: () => void; className?: string; loop?: boolean }) {
  const { t } = useTranslation();
  const isVideo = a.mime?.startsWith("video/");
  const size = "block max-h-80 min-h-24 w-full min-w-40 max-w-72 object-cover";
  if (a.available === "gone") {
    return (
      // a line, not a picture's place: nothing is there to see
      <div data-gone className="flex items-center gap-2 py-0.5 text-sm opacity-70">
        <ImageOff className="size-4 shrink-0" />{t("chat.gone")}
      </div>
    );
  }
  return (
    <button onClick={onOpen} className={cn("relative block overflow-hidden rounded-xl bg-black/10", className)} aria-label={isVideo ? t("kind.video") : t("kind.image")}>
      {isVideo && loop ? <LoopVideo a={a} className={size} /> : <img
        src={thumbUrl(a.sha256)}
        alt=""
        loading="lazy"
        decoding="async"
        className={size}
        onError={(e) => ((e.target as HTMLImageElement).style.visibility = "hidden")}
      />}
      {isVideo && !loop && (
        <span className="absolute inset-0 grid place-items-center">
          <span className="grid size-12 place-items-center rounded-full bg-black/55 text-white backdrop-blur"><Play className="size-6 translate-x-0.5" /></span>
        </span>
      )}
    </button>
  );
}

function AttachmentView({ a, onOpen }: { a: Attachment; onOpen: (a: Attachment) => void }) {
  const { t } = useTranslation();
  const mime = a.mime ?? "";
  if (mime.startsWith("image/") || mime.startsWith("video/")) return <Thumb a={a} onOpen={() => onOpen(a)} />;
  if (mime.startsWith("audio/")) {
    return a.available === "gone" ? (
      <span className="text-xs opacity-70">{t("kind.voice")} · {t("chat.gone")}</span>
    ) : (
      <audio controls preload="none" src={`/api/media/${a.sha256}/original`} className="h-10 w-64 max-w-full" />
    );
  }
  return (
    <a
      href={a.available === "gone" ? undefined : `/api/media/${a.sha256}/original`}
      download={a.name ?? ""}
      className="flex items-center gap-3 rounded-xl bg-black/10 px-3 py-2 text-sm"
    >
      <FileText className="size-8 shrink-0 opacity-80" />
      <span className="min-w-0">
        <span className="block truncate font-medium">{a.name || mime || t("kind.file")}</span>
        <span className="text-xs opacity-70">{a.available === "gone" ? t("chat.gone") : bytes(a.size)}</span>
      </span>
    </a>
  );
}

export const SystemLine = memo(function SystemLine({ m }: { m: MessageItem }) {
  const { t } = useTranslation();
  const lines = m.notice ? noticeLines(m.notice, t) : [];
  return (
    <div className="flex justify-center px-4 py-1">
      <span className="max-w-[80%] rounded-2xl bg-panel/80 px-3 py-1 text-center text-xs text-muted shadow-sm backdrop-blur">
        {lines.length ? lines.map((l, i) => <span key={i} className="block">{l}{i === lines.length - 1 && <> · {time(m.ts)}</>}</span>)
          : <>{m.text || m.subtype || "—"} · {time(m.ts)}</>}
      </span>
    </div>
  );
});

/** A poll with how many chose each option. */
function PollView({ poll }: { poll: NonNullable<ReturnType<typeof pollOf>> }) {
  const { t } = useTranslation();
  const most = Math.max(1, ...poll.options.map((o) => o.votes ?? 0));
  return (
    <div className="min-w-48">
      <div className="font-medium">{poll.question}</div>
      {poll.multiple && <div className="text-xs opacity-70">{t("notice.multiple")}</div>}
      {poll.ended && <div className="text-xs opacity-70">{t("notice.ended")}</div>}
      {poll.voters != null && poll.voters > 0 && <div className="text-xs opacity-70">{t("notice.voters", { count: poll.voters })}</div>}
      <ul className="mt-1 space-y-1">
        {poll.options.map((o, i) => (
          <li key={i} className="text-sm">
            <div className="flex justify-between gap-3"><span>{o.text}</span>{o.votes != null && <span className="shrink-0 text-xs opacity-70">{t("notice.votes", { count: o.votes })}</span>}</div>
            {o.votes != null && <div className="mt-0.5 h-1 rounded-full bg-current/15"><div className="h-1 rounded-full bg-current/60" style={{ width: `${(o.votes / most) * 100}%` }} /></div>}
          </li>
        ))}
      </ul>
    </div>
  );
}

export const CallLine = memo(function CallLine({ c }: { c: CallItem }) {
  const { t } = useTranslation();
  const missed = !c.outgoing && !c.answered;
  const Icon = c.video ? Video : missed ? PhoneMissed : c.outgoing ? PhoneOutgoing : c.answered ? PhoneIncoming : Phone;
  const label = c.detail && c.detail !== "missed" ? t(`call.${c.detail}`, { defaultValue: c.detail })
    : missed ? t("call.missed") : c.outgoing ? t("call.outgoing") : t("call.incoming");
  return (
    <div className="flex justify-center px-4 py-1.5">
      <span className={cn("inline-flex items-center gap-2 rounded-full border border-line bg-panel px-3.5 py-1.5 text-xs shadow-sm",
        missed && "text-danger")}>
        <Icon className="size-4" />
        <span className="font-medium">{c.video ? t("call.video") + " · " : ""}{label}</span>
        {c.duration > 0 && <span className="text-muted">{duration(c.duration)}</span>}
        {c.attempts > 1 && <span className="text-muted">{t("call.times", { n: c.attempts })}</span>}
        <span className="text-muted">· {service(c.service).name} · {time(c.ts)}</span>
      </span>
    </div>
  );
});

export interface BubbleProps {
  m: MessageItem;
  group: boolean;            // a group chat: senders' names and avatars
  first: boolean;            // first of a run by the same sender
  last: boolean;             // last of a run
  showService: boolean;      // the service changed: say it
  highlight?: boolean;
  onOpen: (a: Attachment, m: MessageItem) => void;
  onJump: (id: number) => void;
  onReply?: (m: MessageItem) => void;    // where an answer to this message can be sent
  onInfo?: (m: MessageItem) => void;     // who got and read it (the user's own messages)
  // what can be done to it through its service now: a reaction ("" takes the user's back), an edit,
  // a deletion for everyone (the user's own)
  react?: { quick: string[]; all: string[] | null; free: boolean; on: (m: MessageItem, emoji: string) => void };
  onEdit?: (m: MessageItem) => void;
  onDelete?: (m: MessageItem) => void;
}

// what is offered where a service takes any emoji, after its own quick ones
const COMMON = ["👍", "❤️", "😂", "😮", "😢", "🙏", "👎", "😡", "🔥", "🎉", "👏", "🥰", "😍", "🤣", "😊", "😁", "😉", "😎",
  "🤔", "🙄", "😅", "😭", "😱", "🤯", "🥳", "🤩", "😘", "🤗", "🙈", "💪", "👌", "✌️", "🤝", "🙌", "💯", "✅", "❌", "⭐",
  "💔", "💙", "💚", "💛", "🌹", "☕", "🍻", "🎂", "😴", "🤢", "🫠", "👀"];

/** One emoji, as typed or pasted (a flag, a skin tone, a family are one). */
export function oneEmoji(s: string): string | null {
  const t = s.trim();
  if (!t) return null;
  const parts = [...new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(t)];
  return parts.length === 1 && /\p{Extended_Pictographic}|\p{Regional_Indicator}/u.test(t) ? t : null;
}

/** The actions of a message: its service's quick reactions, any other it takes, answer, edit,
 * delete for everyone. Opened by its button, or by a long press on a touch screen (open). */
function Actions({ m, react, onReply, onEdit, onDelete, open, onOpenChange }: {
  m: MessageItem; react?: BubbleProps["react"]; onReply?: (m: MessageItem) => void; onEdit?: (m: MessageItem) => void;
  onDelete?: (m: MessageItem) => void; open: boolean; onOpenChange: (o: boolean) => void;
}) {
  const { t } = useTranslation();
  const [more, setMore] = useState(false);
  const [typed, setTyped] = useState("");
  useEffect(() => { if (!open) { setMore(false); setTyped(""); } }, [open]);
  const mine = m.reactions.find((r) => r.mine)?.emoji ?? null;
  const put = (e: string) => { onOpenChange(false); react?.on(m, e === mine ? "" : e); };
  const others = react ? (react.free ? [...react.quick, ...COMMON.filter((e) => !react.quick.includes(e))] : react.all ?? []) : [];
  const typedOne = oneEmoji(typed);
  return (
    <Menu open={open} onOpenChange={onOpenChange}>
      <MenuTrigger asChild>
        <button data-actions aria-label={t("chat.actions")} title={t("chat.actions")}
          className="self-center rounded-full p-1.5 text-muted opacity-0 transition hover:bg-panel-2 hover:text-fg focus:opacity-100 group-hover/msg:opacity-100 data-[state=open]:opacity-100">
          <SmilePlus className="size-4" />
        </button>
      </MenuTrigger>
      <MenuContent align={m.outgoing ? "end" : "start"} className="w-72">
        {react && (
          <div data-reactions className="flex items-center gap-0.5 px-1 py-1">
            {react.quick.slice(0, 6).map((e) => (
              <button key={e} data-react={e} onClick={() => put(e)} aria-label={`${t("chat.react")} ${e}`}
                className={cn("grid size-9 place-items-center rounded-full text-xl hover:bg-panel-2", e === mine && "bg-accent/15")}>{e}</button>
            ))}
            {others.length > 6 || react.free ? (
              <button data-more-reactions onClick={() => setMore((x) => !x)} aria-label={t("chat.moreReactions")} title={t("chat.moreReactions")}
                className="grid size-9 place-items-center rounded-full text-muted hover:bg-panel-2 hover:text-fg"><Plus className="size-5" /></button>
            ) : null}
          </div>
        )}
        {react && more && (
          <div data-reaction-picker className="border-t border-line px-1 pb-1 pt-1.5">
            <div className="grid max-h-48 grid-cols-8 overflow-y-auto">
              {others.slice(react.free ? 0 : 6).map((e) => (
                <button key={e} onClick={() => put(e)} aria-label={`${t("chat.react")} ${e}`}
                  className={cn("grid size-8 place-items-center rounded-lg text-lg hover:bg-panel-2", e === mine && "bg-accent/15")}>{e}</button>
              ))}
            </div>
            {react.free && (
              <form className="mt-1 flex items-center gap-1 px-1" onSubmit={(e) => { e.preventDefault(); if (typedOne) put(typedOne); }}>
                <input data-emoji-input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={t("chat.emojiHint")}
                  onKeyDown={(e) => e.stopPropagation()}
                  className="h-8 min-w-0 flex-1 rounded-lg border border-line bg-panel-2 px-2 text-sm outline-none focus:border-accent" />
                <button type="submit" disabled={!typedOne} className="h-8 rounded-lg px-2 text-sm font-medium text-accent disabled:opacity-40">{t("chat.react")}</button>
              </form>
            )}
          </div>
        )}
        {mine && react && <MenuItem onSelect={() => react.on(m, "")}><span className="w-5 text-center">{mine}</span>{t("chat.removeReaction")}</MenuItem>}
        {onReply && <MenuItem icon={<CornerUpLeft className="size-4" />} onSelect={() => onReply(m)}>{t("chat.reply")}</MenuItem>}
        {onEdit && <MenuItem icon={<Pencil className="size-4" />} onSelect={() => onEdit(m)}>{t("chat.edit")}</MenuItem>}
        {onDelete && <MenuItem danger icon={<Trash2 className="size-4" />} onSelect={() => onDelete(m)}>{t("chat.deleteForAll")}</MenuItem>}
      </MenuContent>
    </Menu>
  );
}

export const Bubble = memo(function Bubble({ m, group, first, last, showService, highlight, onOpen, onJump, onReply, onInfo, react, onEdit, onDelete }: BubbleProps) {
  const { t } = useTranslation();
  const [menu, setMenu] = useState(false);
  // deleted for everyone: the notice, as the service shows it; what it was, on a tap
  const [reveal, setReveal] = useState(false);
  const hidden = m.deleted && !reveal;
  const hasActions = m.id > 0 && !m.deleted && !!(react || onEdit || onDelete);
  const press = useRef<ReturnType<typeof setTimeout> | null>(null);   // a long press opens the actions
  const unpress = () => { if (press.current) clearTimeout(press.current); press.current = null; };
  // a swipe to the right on a touch screen answers the message (as in the messaging apps)
  const [dx, setDx] = useState(0);
  const moved = useRef(0);              // as far as the finger went (the state may lag a frame behind)
  const start = useRef<{ x: number; y: number } | null>(null);
  const shift = (x: number) => { moved.current = x; setDx(x); };
  const swipe = onReply && m.id > 0 ? {
    onPointerDown: (e: React.PointerEvent) => {
      if (e.pointerType !== "touch") return;
      start.current = { x: e.clientX, y: e.clientY };
      if (hasActions) press.current = setTimeout(() => { start.current = null; shift(0); setMenu(true); }, 500);
    },
    onPointerMove: (e: React.PointerEvent) => {
      const s0 = start.current;
      if (!s0) return;
      const x = e.clientX - s0.x;
      if (Math.abs(x) > 8 || Math.abs(e.clientY - s0.y) > 8) unpress();
      if (Math.abs(e.clientY - s0.y) > 30 && x < 20) { start.current = null; shift(0); return; }   // a scroll, not a swipe
      shift(Math.max(0, Math.min(x, 90)));
    },
    onPointerUp: () => { unpress(); if (start.current && moved.current > 60) onReply(m); start.current = null; shift(0); },
    onPointerCancel: () => { unpress(); start.current = null; shift(0); },
  } : hasActions ? {
    onPointerDown: (e: React.PointerEvent) => { if (e.pointerType === "touch") press.current = setTimeout(() => setMenu(true), 500); },
    onPointerMove: unpress, onPointerUp: unpress, onPointerCancel: unpress,
  } : {};
  const actions = hasActions && (
    <Actions m={m} react={react} onReply={onReply} onEdit={onEdit} onDelete={onDelete} open={menu} onOpenChange={setMenu} />
  );
  const replyButton = onReply && m.id > 0 && (
    <button data-reply onClick={() => onReply(m)} aria-label={t("chat.reply")} title={t("chat.reply")}
      className="self-center rounded-full p-1.5 text-muted opacity-0 transition hover:bg-panel-2 hover:text-fg focus:opacity-100 group-hover/msg:opacity-100">
      <CornerUpLeft className="size-4" />
    </button>
  );
  const out = m.outgoing;
  const svc = service(m.service);
  const media = m.attachments.filter((a) => a.mime?.startsWith("image/") || a.mime?.startsWith("video/"));
  const others = m.attachments.filter((a) => !media.includes(a));
  // pictures alone: the time over them (not when none of them is there any more: a line of text then)
  const onlyMedia = !(m.deleted && !reveal) && media.some((a) => a.available !== "gone") && !m.text && !others.length && !m.reply;
  const label = m.kind !== "text" && !m.attachments.length && !m.text && !m.location ? t(`kind.${m.kind}`, { defaultValue: m.kind }) : null;
  const poll = pollOf(m.notice);
  return (
    <div id={`m${m.id}`} className={cn("group/msg flex gap-2 px-3 md:px-6", out ? "justify-end" : "justify-start", first ? "mt-2" : "mt-0.5")}>
      {out && actions}
      {out && replyButton}
      {group && !out && (
        <div className="w-8 shrink-0 self-end">{last && <Avatar name={m.sender || "?"} size={32} />}</div>
      )}
      <div {...swipe} style={dx ? { transform: `translateX(${dx}px)` } : undefined}
        className={cn("flex max-w-[min(78%,42rem)] touch-pan-y flex-col", out ? "items-end" : "items-start", !dx && "transition-transform")}>
        {group && !out && first && m.sender && (
          <span className="mb-0.5 ml-3 text-xs font-semibold" style={{ color: nameColor(m.sender) }}
            title={m.sender_self_named ? t("people.selfNamed") : undefined}>{m.sender_self_named && "~ "}{m.sender}</span>
        )}
        <div
          className={cn(
            "relative rounded-bubble text-[15px] leading-snug shadow-sm",
            out ? "bg-bubble-out text-bubble-out-fg" : "border border-line/60 bg-bubble-in",
            out ? (last ? "rounded-br-md" : "") : last ? "rounded-bl-md" : "",
            onlyMedia ? "p-1" : "px-3 py-2",
            m.deleted && "italic opacity-70",
            highlight && "flash",
          )}
        >
          {hidden ? (
            <button data-deleted onClick={() => setReveal(true)} title={t("chat.showDeleted")}
              className="flex items-center gap-1.5 text-left opacity-80">
              <Trash2 className="size-3.5 shrink-0" />{t("chat.deleted")}
            </button>
          ) : <>
          {m.forwarded && (
            <div className="mb-1 flex items-center gap-1 text-xs opacity-70"><Forward className="size-3" />{m.forward_from ? t("chat.forwardedFrom", { name: m.forward_from }) : t("chat.forwarded")}</div>
          )}
          {["story_reply", "story_reaction", "story", "unsupported"].includes(m.notice?.code ?? "") && (
            <div className="mb-1 text-xs opacity-70">{noticeLines(m.notice!, t)[0]}</div>
          )}
          {m.reply && (
            <button
              onClick={() => onJump(m.reply!.id)}
              className={cn("mb-1.5 block w-full rounded-lg border-l-[3px] px-2 py-1 text-left text-xs",
                out ? "border-white/70 bg-white/15" : "border-accent bg-accent/8")}
            >
              <span className="flex items-center gap-1 font-semibold"><CornerUpLeft className="size-3" />{m.reply.outgoing ? t("common.me") : m.reply.sender || "…"}</span>
              <span className="line-clamp-2 opacity-80">{m.reply.text || t(`kind.${m.reply.kind}`, { defaultValue: m.reply.kind })}</span>
            </button>
          )}
          {!m.reply && m.reply_text && (
            <div className={cn("mb-1.5 rounded-lg border-l-[3px] px-2 py-1 text-xs opacity-80", out ? "border-white/70 bg-white/15" : "border-accent bg-accent/8")}>
              {m.reply_text}
            </div>
          )}
          {media.length > 0 && (
            <div className={cn("grid gap-1", media.length > 1 && "grid-cols-2")}>
              {media.map((a) => <Thumb key={a.sha256} a={a} loop={m.subtype === "gif"} onOpen={() => onOpen(a, m)} />)}
            </div>
          )}
          {others.map((a) => <div key={a.sha256} className="my-1"><AttachmentView a={a} onOpen={(x) => onOpen(x, m)} /></div>)}
          {m.location && (
            <a
              href={m.location.lat != null ? `https://www.openstreetmap.org/?mlat=${m.location.lat}&mlon=${m.location.lon}#map=16/${m.location.lat}/${m.location.lon}` : undefined}
              target="_blank" rel="noopener noreferrer"
              className={cn("my-1 flex items-center gap-2 rounded-xl px-3 py-2 text-sm", out ? "bg-white/15" : "bg-accent/8")}
            >
              <MapPin className="size-5 shrink-0" />
              <span>{m.location.place || t("kind.location")}{m.location.lat != null && <span className="block text-xs opacity-70">{m.location.lat.toFixed(4)}, {m.location.lon?.toFixed(4)}</span>}</span>
            </a>
          )}
          {poll && !m.deleted ? <PollView poll={poll} /> : (m.text || label || m.deleted) && (
            <div className={cn("whitespace-pre-wrap break-words [overflow-wrap:anywhere]", onlyMedia && "px-2 pb-1")}>
              {m.deleted && !m.text ? t("chat.deleted") : m.text ? <RichText text={m.text} marks={m.highlight} mentions={m.mentions} /> : <span className="opacity-70">{label}</span>}
            </div>
          )}
          {m.deleted && <div className="mt-0.5 text-xs opacity-70">{t("chat.deleted")}</div>}
          </>}
          <div className={cn("mt-0.5 flex items-center justify-end gap-1 text-[11px] leading-none", out ? "text-bubble-out-fg/70" : "text-muted", onlyMedia && "absolute bottom-2 right-2.5 rounded-full bg-black/45 px-1.5 py-1 text-white")}>
            {showService && <span className="flex items-center gap-1"><span className="size-1.5 rounded-full" style={{ background: svc.color }} />{svc.name} ·</span>}
            {m.pinned && <Pin className="size-3" aria-label={t("chat.pinned")} />}
            {m.edited && <span>{t("chat.edited")} ·</span>}
            {m.status === "sending" && <span>{t("chat.sending")} ·</span>}
            {m.status === "failed" && <span className="text-danger">{t("chat.sendFailed")} ·</span>}
            <span>{time(m.ts)}</span>
            {out && m.receipts && <Ticks r={m.receipts} onInfo={onInfo && (() => onInfo(m))} />}
          </div>
        </div>
        {m.reactions.length > 0 && !hidden && (
          // over the bubble's lower edge, at its start (the time is at its end); the bubble is
          // positioned, so this must be too, and above it
          <div className="relative z-10 -mt-2 mb-1 flex flex-wrap justify-start gap-1 self-stretch px-2.5">
            {groupReactions(m).map((r) => {
              // the user's own taken back with a tap, another's put as theirs, where the service takes it
              const tap = react && !m.deleted && m.id > 0 && (r.mine || react.free || react.all === null || react.all.includes(r.emoji)) && r.emoji !== "❔"
                ? () => react.on(m, r.mine ? "" : r.emoji) : undefined;
              const cls = cn("inline-flex items-center gap-0.5 rounded-full bg-panel px-1.5 py-1 text-[13px] leading-none shadow-sm ring-2 ring-bg",
                r.mine && "bg-accent/15", tap && "hover:bg-panel-2");
              const body = <>{r.emoji}{r.count > 1 && <span className="ml-0.5 text-muted">{r.count}</span>}</>;
              return tap ? (
                <button key={r.key} title={r.mine ? t("chat.removeReaction") : r.who} data-reaction data-mine={r.mine || undefined} onClick={tap} className={cls}>{body}</button>
              ) : (
                <span key={r.key} title={r.who} data-reaction className={cls}>{body}</span>
              );
            })}
          </div>
        )}
      </div>
      {!out && replyButton}
      {!out && actions}
    </div>
  );
});

function groupReactions(m: MessageItem) {
  const out = new Map<string, { key: string; emoji: string; count: number; mine: boolean; who: string }>();
  for (const r of m.reactions) {
    const e = r.emoji || "❔";
    const x = out.get(e) ?? { key: e, emoji: e, count: 0, mine: false, who: "" };
    x.count += r.count;
    x.mine ||= r.mine;
    if (r.who) x.who = x.who ? `${x.who}, ${r.who}` : r.who;
    out.set(e, x);
  }
  return [...out.values()];
}

const NAME_COLORS = ["#e17055", "#00a884", "#6c5ce7", "#0984e3", "#d63031", "#e84393", "#00b894", "#fdcb6e", "#a29bfe", "#55efc4"];
function nameColor(name: string) {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) | 0;
  return NAME_COLORS[Math.abs(h) % NAME_COLORS.length];
}
