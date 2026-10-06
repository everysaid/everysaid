import { memo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { Check, CheckCheck, CornerUpLeft, FileText, Forward, ImageOff, MapPin, Phone, PhoneIncoming, PhoneMissed, PhoneOutgoing, Play, Video } from "lucide-react";
import type { Attachment, CallItem, Mention, MessageItem, Receipts } from "@/lib/api";
import { bytes, duration, time } from "@/lib/format";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Avatar } from "./ui";

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

export function Thumb({ a, onOpen, className }: { a: Attachment; onOpen?: () => void; className?: string }) {
  const { t } = useTranslation();
  const isVideo = a.mime?.startsWith("video/");
  if (a.available === "gone") {
    return (
      <div className={cn("grid h-36 w-56 place-items-center rounded-xl bg-black/10 text-xs opacity-70", className)}>
        <span className="flex flex-col items-center gap-1"><ImageOff className="size-5" />{t("chat.gone")}</span>
      </div>
    );
  }
  return (
    <button onClick={onOpen} className={cn("relative block overflow-hidden rounded-xl bg-black/10", className)} aria-label={isVideo ? t("kind.video") : t("kind.image")}>
      <img
        src={`/api/media/${a.sha256}/thumb`}
        alt=""
        loading="lazy"
        decoding="async"
        className="block max-h-80 min-h-24 w-full min-w-40 max-w-72 object-cover"
        onError={(e) => ((e.target as HTMLImageElement).style.visibility = "hidden")}
      />
      {isVideo && (
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
      download
      className="flex items-center gap-3 rounded-xl bg-black/10 px-3 py-2 text-sm"
    >
      <FileText className="size-8 shrink-0 opacity-80" />
      <span className="min-w-0">
        <span className="block truncate font-medium">{mime || t("kind.file")}</span>
        <span className="text-xs opacity-70">{a.available === "gone" ? t("chat.gone") : bytes(a.size)}</span>
      </span>
    </a>
  );
}

export const SystemLine = memo(function SystemLine({ m }: { m: MessageItem }) {
  return (
    <div className="flex justify-center px-4 py-1">
      <span className="max-w-[80%] rounded-full bg-panel/80 px-3 py-1 text-center text-xs text-muted shadow-sm backdrop-blur">
        {m.text || m.subtype || "—"} · {time(m.ts)}
      </span>
    </div>
  );
});

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
}

export const Bubble = memo(function Bubble({ m, group, first, last, showService, highlight, onOpen, onJump, onReply, onInfo }: BubbleProps) {
  const { t } = useTranslation();
  // a swipe to the right on a touch screen answers the message (as in the messaging apps)
  const [dx, setDx] = useState(0);
  const moved = useRef(0);              // as far as the finger went (the state may lag a frame behind)
  const start = useRef<{ x: number; y: number } | null>(null);
  const shift = (x: number) => { moved.current = x; setDx(x); };
  const swipe = onReply && m.id > 0 ? {
    onPointerDown: (e: React.PointerEvent) => { if (e.pointerType === "touch") start.current = { x: e.clientX, y: e.clientY }; },
    onPointerMove: (e: React.PointerEvent) => {
      const s0 = start.current;
      if (!s0) return;
      const x = e.clientX - s0.x;
      if (Math.abs(e.clientY - s0.y) > 30 && x < 20) { start.current = null; shift(0); return; }   // a scroll, not a swipe
      shift(Math.max(0, Math.min(x, 90)));
    },
    onPointerUp: () => { if (start.current && moved.current > 60) onReply(m); start.current = null; shift(0); },
    onPointerCancel: () => { start.current = null; shift(0); },
  } : {};
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
  const onlyMedia = media.length > 0 && !m.text && !others.length && !m.reply;
  const label = m.kind !== "text" && !m.attachments.length && !m.text && !m.location ? t(`kind.${m.kind}`, { defaultValue: m.kind }) : null;
  return (
    <div id={`m${m.id}`} className={cn("group/msg flex gap-2 px-3 md:px-6", out ? "justify-end" : "justify-start", first ? "mt-2" : "mt-0.5")}>
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
          {m.forwarded && (
            <div className="mb-1 flex items-center gap-1 text-xs opacity-70"><Forward className="size-3" />{t("chat.forwarded")}</div>
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
              {media.map((a) => <Thumb key={a.sha256} a={a} onOpen={() => onOpen(a, m)} />)}
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
          {(m.text || label || m.deleted) && (
            <div className={cn("whitespace-pre-wrap break-words [overflow-wrap:anywhere]", onlyMedia && "px-2 pb-1")}>
              {m.deleted && !m.text ? t("chat.deleted") : m.text ? <RichText text={m.text} marks={m.highlight} mentions={m.mentions} /> : <span className="opacity-70">{label}</span>}
            </div>
          )}
          <div className={cn("mt-0.5 flex items-center justify-end gap-1 text-[11px] leading-none", out ? "text-bubble-out-fg/70" : "text-muted", onlyMedia && "absolute bottom-2 right-2.5 rounded-full bg-black/45 px-1.5 py-1 text-white")}>
            {showService && <span className="flex items-center gap-1"><span className="size-1.5 rounded-full" style={{ background: svc.color }} />{svc.name} ·</span>}
            {m.edited && <span>{t("chat.edited")} ·</span>}
            {m.status === "sending" && <span>{t("chat.sending")} ·</span>}
            {m.status === "failed" && <span className="text-danger">{t("chat.sendFailed")} ·</span>}
            <span>{time(m.ts)}</span>
            {out && m.receipts && <Ticks r={m.receipts} onInfo={onInfo && (() => onInfo(m))} />}
          </div>
        </div>
        {m.reactions.length > 0 && (
          // over the bubble's lower edge, at its start (the time is at its end); the bubble is
          // positioned, so this must be too, and above it
          <div className="relative z-10 -mt-2 mb-1 flex flex-wrap justify-start gap-1 self-stretch px-2.5">
            {groupReactions(m).map((r) => (
              <span key={r.key} title={r.who} data-reaction
                className={cn("inline-flex items-center gap-0.5 rounded-full bg-panel px-1.5 py-1 text-[13px] leading-none shadow-sm ring-2 ring-bg",
                  r.mine && "bg-accent/15")}>
                {r.emoji}{r.count > 1 && <span className="ml-0.5 text-muted">{r.count}</span>}
              </span>
            ))}
          </div>
        )}
      </div>
      {!out && replyButton}
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
