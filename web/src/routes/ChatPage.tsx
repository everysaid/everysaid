import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import * as Popover from "@radix-ui/react-popover";
import { ArrowDown, ArrowLeft, BellOff, CalendarDays, Check, ChevronDown, CornerUpLeft, Archive, ArchiveRestore, FileText, Info, Lock, MoreVertical, Paperclip, Pencil, Pin, PinOff, Search, SendHorizontal, X } from "lucide-react";
import { toast } from "sonner";
import { api, qs, type Attachment, type ChatDetail, type Member, type MessageItem, type StreamItem, type StreamPage } from "@/lib/api";
import { useBack } from "@/lib/back";
import { bytes, dateOnly, dayLabel, isoDay, sameDay } from "@/lib/format";
import { onEvent } from "@/lib/events";
import { useSettings, useWide } from "@/lib/hooks";
import { draft, keepDraft, keepLastChat, keepPlace, place } from "@/lib/memory";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { chatRoute } from "@/router";
import { Avatar, Button, Center, Dialog, IconButton, Menu, MenuContent, MenuItem, MenuTrigger, ServiceBadge, ServiceIcon, Spinner, Textarea } from "@/components/ui";
import { Bubble, CallLine, SystemLine } from "@/components/Message";
import { Lightbox, type LightboxItem } from "@/components/Lightbox";
import { ChatInfo } from "@/components/ChatInfo";
import { MessageInfo } from "@/components/MessageInfo";
import { DateField } from "@/components/DateField";
import { avatarUrl } from "@/components/ChatList";

const START = 1_000_000_000;
const PAGE = 80;

export function ChatPage() {
  const { chatId } = chatRoute.useParams();
  const { m, ts, hide } = chatRoute.useSearch();
  return <ChatView key={`${chatId}:${m ?? ""}:${ts ?? ""}:${hide ?? ""}`} chatId={chatId} jumpTo={m} around={ts} hide={hide} />;
}

function ChatView({ chatId, jumpTo, around, hide }: { chatId: string; jumpTo?: number; around?: number; hide?: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const wide = useWide();
  const detail = useQuery({ queryKey: ["chat", chatId], queryFn: () => api.get<ChatDetail>(`/api/chats/${chatId}`) });
  const [items, setItems] = useState<StreamItem[]>([]);
  const [first, setFirst] = useState(START);
  const [hasOlder, setHasOlder] = useState(false);
  const [hasNewer, setHasNewer] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [atBottom, setAtBottom] = useState(true);
  const atBottomRef = useRef(true);
  atBottomRef.current = atBottom;
  const [highlight, setHighlight] = useState<number | null>(jumpTo ?? null);
  const [infoOpen, setInfoOpen] = useState(false);
  const [lightbox, setLightbox] = useState<number | null>(null);
  const virt = useRef<VirtuosoHandle>(null);
  const scroller = useRef<HTMLElement | null>(null);
  const busy = useRef({ older: false, newer: false });
  const itemsRef = useRef(items);
  itemsRef.current = items;
  const sendable = detail.data?.sendable ?? [];
  const replyable = detail.data?.replyable ?? [];
  const [replyTo, setReplyTo] = useState<MessageItem | null>(null);
  const [infoOf, setInfoOf] = useState<MessageItem | null>(null);      // the message whose receipts are shown
  // where the user was reading, when they come back to the chat (not when sent to a message or a time)
  const [resume, setResume] = useState(() => (jumpTo || around ? undefined : place(chatId)));
  const hasNewerRef = useRef(false);
  hasNewerRef.current = hasNewer;
  // as the user scrolls (a reload keeps it too): the first row in view, and how far it is scrolled past
  const remember = useCallback(() => {
    const el = scroller.current;
    if (atBottomRef.current && !hasNewerRef.current) return keepPlace(chatId, null);
    if (!el) return;
    const edge = el.getBoundingClientRect().top;
    const row = [...el.querySelectorAll<HTMLElement>("[data-cursor]")].find((r) => r.getBoundingClientRect().bottom > edge + 1);
    const item = row && itemsRef.current.find((i) => i.cursor === row.dataset.cursor);
    if (item) keepPlace(chatId, { ts: item.ts, cursor: item.cursor, offset: Math.round(edge - row.getBoundingClientRect().top) });
  }, [chatId]);
  // the day at the top of the view, kept over the stream where its separator was (a separator that
  // goes past the top hides, the pill shows its day); the next day's separator pushes it up. Done in
  // the scroll event itself, not a frame later, so the two never show apart.
  const dayPill = useRef<HTMLDivElement>(null);
  const dayText = useRef<HTMLSpanElement>(null);
  const placeDay = useCallback(() => {
    const el = scroller.current;
    const pill = dayPill.current;
    if (!el || !pill || !dayText.current) return;
    const edge = el.getBoundingClientRect().top;
    const row = [...el.querySelectorAll<HTMLElement>("[data-cursor]")].find((r) => r.getBoundingClientRect().bottom > edge);
    if (!row) return;
    const label = dayLabel(Number(row.dataset.ts));
    if (dayText.current.textContent !== label) dayText.current.textContent = label;
    let next: HTMLElement | undefined;
    for (const sep of el.querySelectorAll<HTMLElement>("[data-day-sep]")) {
      const top = sep.getBoundingClientRect().top;
      sep.style.visibility = top < edge ? "hidden" : "";
      if (top >= edge && !next) next = sep;
    }
    const push = next ? Math.min(0, next.getBoundingClientRect().top - edge - pill.offsetHeight) : 0;
    pill.style.transform = push ? `translateY(${push}px)` : "";
    pill.style.right = `${el.offsetWidth - el.clientWidth}px`;   // centred over the messages, not the scrollbar
    const own = row.querySelector<HTMLElement>("[data-day-sep]");   // its day's separator still in view (the chat's start)
    pill.style.visibility = own && own.getBoundingClientRect().top >= edge ? "hidden" : "";
  }, []);
  useEffect(() => {
    const el = scroller.current;
    if (!ready || !el) return;
    el.addEventListener("scroll", placeDay, { passive: true });
    placeDay();
    return () => el.removeEventListener("scroll", placeDay);
  }, [ready, items.length > 0, placeDay]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => keepLastChat({ chatId, hide }), [chatId, hide]);
  useEffect(() => { if (ready) remember(); }, [ready, atBottom, hasNewer, remember]);
  const answer = useCallback((m: MessageItem) => {
    setEditing(null);
    setReplyTo(m);
    requestAnimationFrame(() => (document.querySelector("[data-composer-body] textarea") as HTMLElement | null)?.focus());
  }, []);

  // what is done to a message shows at once; the service's answer then comes in its place (or it
  // goes back as it was, said why)
  const [editing, setEditing] = useState<MessageItem | null>(null);
  const [deleting, setDeleting] = useState<MessageItem | null>(null);
  const patch = (id: number, f: (m: MessageItem) => MessageItem) =>
    setItems((prev) => prev.map((i) => (i.type === "message" && i.id === id ? f(i) : i)));
  const act = useCallback(async (m: MessageItem, call: () => Promise<unknown>, shown: (m: MessageItem) => MessageItem) => {
    patch(m.id, shown);
    try {
      await call();
    } catch (e) {
      patch(m.id, () => m);
      toast.error(`${t("chat.actionFailed")}: ${(e as Error).message}`);
    }
  }, [t]);
  const react = useCallback((m: MessageItem, emoji: string) => act(m, () => api.post(`/api/messages/${m.id}/reaction`, { emoji }), (i) => ({
    ...i, reactions: [...i.reactions.filter((r) => !r.mine), ...(emoji ? [{ emoji, code: null, count: 1, mine: true, who: null }] : [])],
  })), [act]);
  const startEdit = useCallback((m: MessageItem) => {
    setReplyTo(null);
    setEditing(m);
    requestAnimationFrame(() => (document.querySelector("[data-composer-body] textarea") as HTMLElement | null)?.focus());
  }, []);
  const edit = useCallback((m: MessageItem, text: string) => act(m, () => api.post(`/api/messages/${m.id}/edit`, { text }), (i) => ({ ...i, text, edited: true })), [act]);
  const remove = useCallback((m: MessageItem) => act(m, () => api.post(`/api/messages/${m.id}/delete`, {}), (i) => ({ ...i, deleted: true })), [act]);

  // groups merged into this chat or split off it: its stream is another, loaded again
  const made = detail.data?.conversations.join();
  const [generation, setGeneration] = useState(0);
  const wasMade = useRef(made);
  useEffect(() => {
    if (wasMade.current !== undefined && made !== undefined && made !== wasMade.current) setGeneration((g) => g + 1);
    if (made !== undefined) wasMade.current = made;
  }, [made]);

  // opened where it was left, with newer pages not loaded: the latest instead, here (the address is the same)
  const latest = () => {
    keepPlace(chatId, null);
    setResume(undefined);
    setReady(false);
    setGeneration((g) => g + 1);
  };

  // the first page: the latest, or around a message or a time
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        let at = around ?? resume?.ts;
        if (jumpTo && !at) at = (await api.get<MessageItem>(`/api/messages/${jumpTo}`)).ts + 1;
        const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ around: at, limit: PAGE, hide })}`);
        if (cancelled) return;
        setItems(page.items);
        setFirst(START - page.items.length);
        setHasOlder(page.has_older);
        setHasNewer(at ? page.has_newer : false);
        setReady(true);
      } catch (e: any) {
        if (!cancelled) setError(e.message);
        keepLastChat(null);                 // gone (merged, perhaps): the Chats tab no longer comes back to it
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [chatId, jumpTo, around, generation]);

  const loadOlder = useCallback(async () => {
    const cur = itemsRef.current;
    if (busy.current.older || !hasOlder || !cur.length) return;
    busy.current.older = true;
    try {
      const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ before: cur[0].cursor, limit: PAGE, hide })}`);
      setItems((prev) => [...page.items, ...prev]);
      setFirst((f) => f - page.items.length);
      setHasOlder(page.has_older);
    } finally {
      busy.current.older = false;
    }
  }, [chatId, hasOlder]);

  const loadNewer = useCallback(async (force = false) => {
    const cur = itemsRef.current;
    if (busy.current.newer || (!hasNewer && !force) || !cur.length) return;
    busy.current.newer = true;
    try {
      // after the newest the server knows (not a placeholder of a message being sent)
      const last = [...cur].reverse().find((i) => !(i.type === "message" && i.id < 0));
      if (!last) return;
      const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ after: last.cursor, limit: PAGE, hide })}`);
      setItems((prev) => {
        const seen = new Set(prev.map((i) => i.cursor));
        const fresh = page.items.filter((i) => !seen.has(i.cursor));
        // a placeholder of a sent message goes once the message itself is here: the one of the same
        // text, else (its text written otherwise by the service: mentions, a file) the oldest of the
        // conversation it went to
        const gone = new Set<number>();
        const sent = prev.filter((i): i is MessageItem => i.type === "message" && i.id < 0 && i.status === "sent");
        for (const f of fresh) {
          if (f.type !== "message" || !f.outgoing) continue;
          const p = sent.find((i) => !gone.has(i.id) && (i.text ?? "") === (f.text ?? ""))
            ?? sent.find((i) => !gone.has(i.id) && i.loose && i.conversation_id === f.conversation_id);
          if (p) gone.add(p.id);
        }
        return [...prev.filter((i) => !(i.type === "message" && gone.has(i.id))), ...fresh];
      });
      if (!force) setHasNewer(page.has_newer);
    } finally {
      busy.current.newer = false;
    }
  }, [chatId, hasNewer]);

  // after the user sends: to the end, wherever they were (the latest page first, if it is not loaded)
  const toEnd = useRef(false);
  // to the scroller's very end, and again once the new rows are measured
  const scrollEnd = () => {
    const go = () => {
      const el = scroller.current;
      if (el) el.scrollTop = el.scrollHeight;
    };
    go();
    requestAnimationFrame(() => requestAnimationFrame(go));
    setTimeout(go, 250);
  };
  // sending: the message shows at once, at the end, as being sent (the field is free for the next
  // one); it is replaced by itself when it arrives, or marked as not sent
  const send = useCallback(async (body: string, service: string | null, answered: MessageItem | null, mentions: Mentioned[], file: File | null) => {
    if (hasNewer) navigate({ to: "/chat/$chatId", params: { chatId }, search: { hide } });
    const id = -Date.now() - Math.random();
    const mark = (status: string, conversation_id = 0) => setItems((prev) => prev.map((i) => (i.type === "message" && i.id === id
      ? { ...i, status, conversation_id: conversation_id || i.conversation_id } : i)));
    const kind = !file ? "text" : file.type.startsWith("image/") ? "image" : file.type.startsWith("video/") ? "video" : "file";
    toEnd.current = true;
    setItems((prev) => [...prev, {
      type: "message", id, ts: Date.now(), cursor: `tmp:${id}`, service: service ?? "", conversation_id: 0, outgoing: true,
      kind, subtype: null, text: body, sender_id: null, sender: null, reply_to: null, reply_text: null, edited: false,
      deleted: false, forwarded: false, starred: false, status: "sending", location: null, reactions: [], attachments: [],
      loose: mentions.length > 0 || !!file,
      reply: answered ? { id: answered.id, text: answered.text ?? "", outgoing: answered.outgoing, sender: answered.sender, kind: answered.kind } : undefined,
    }]);
    try {
      const via = answered ? answered.service : service;
      let went: { conversation_id: number };
      if (file) {
        const form = new FormData();
        form.append("text", body);
        if (via) form.append("service", via);
        if (answered) form.append("reply_to", String(answered.id));
        if (mentions.length) form.append("mentions", JSON.stringify(mentions));
        form.append("file", file);
        went = await api.form(`/api/chats/${chatId}/send`, form);
      } else {
        went = await api.post(`/api/chats/${chatId}/send`, { text: body, service: via, reply_to: answered?.id, mentions: mentions.length ? mentions : undefined });
      }
      mark("sent", went.conversation_id);
      toEnd.current = true;
      await loadNewer(true);
    } catch (e) {
      mark("failed");
      toast.error(`${t("chat.sendFailed")}: ${(e as Error).message}`);
    }
  }, [hasNewer, chatId, loadNewer, navigate, t]);
  useEffect(() => {
    if (!toEnd.current) return;
    toEnd.current = false;
    scrollEnd();
  }, [items]);

  // new messages of this chat, as they arrive (when the latest are shown)
  useEffect(() => onEvent((e) => {
    if (e.type === "new" && e.chats[chatId] && !hasNewer) {
      if (atBottomRef.current) toEnd.current = true;        // at the end: stay there as they come
      loadNewer(true);
    }
  }), [chatId, hasNewer, loadNewer]);

  // something changed on messages already shown (who got and read them, reactions, edits): the
  // latest page again, its items in place of those shown
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const off = onEvent((e) => {
      if (e.type !== "changed" || hasNewer) return;
      clearTimeout(timer);
      timer = setTimeout(async () => {
        const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ limit: PAGE, hide })}`).catch(() => null);
        if (!page) return;
        const fresh = new Map(page.items.map((i) => [i.cursor, i]));
        setItems((prev) => prev.map((i) => fresh.get(i.cursor) ?? i));
      }, 300);
    });
    return () => { off(); clearTimeout(timer); };
  }, [chatId, hasNewer, hide]);

  // read: when the latest are in view
  const read = useMutation({
    mutationFn: () => api.post(`/api/chats/${chatId}/read`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["chats"] }),
  });
  const lastTs = items.length ? items[items.length - 1].ts : 0;
  // only while someone looks at it: the services may tell the others it was read
  const [looking, setLooking] = useState(() => document.visibilityState === "visible" && document.hasFocus());
  useEffect(() => {
    const look = () => setLooking(document.visibilityState === "visible" && document.hasFocus());
    document.addEventListener("visibilitychange", look);
    window.addEventListener("focus", look);
    window.addEventListener("blur", look);
    return () => {
      document.removeEventListener("visibilitychange", look);
      window.removeEventListener("focus", look);
      window.removeEventListener("blur", look);
    };
  }, []);
  useEffect(() => {
    if (!ready || !atBottom || hasNewer || !looking) return;
    const timer = setTimeout(() => read.mutate(), 800);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, atBottom, hasNewer, lastTs, chatId, looking]);

  // highlight fades
  useEffect(() => {
    if (!highlight) return;
    const timer = setTimeout(() => setHighlight(null), 2500);
    return () => clearTimeout(timer);
  }, [highlight]);

  const media = useMemo(() => {
    const out: (LightboxItem & { key: string })[] = [];
    for (const i of items) {
      if (i.type !== "message" || i.deleted) continue;     // deleted for everyone: not offered
      for (const a of i.attachments) {
        if (a.mime?.startsWith("image/") || a.mime?.startsWith("video/")) {
          out.push({ key: `${i.id}:${a.sha256}`, sha256: a.sha256, mime: a.mime, ts: i.ts, available: a.available, chat_id: chatId, message_id: i.id });
        }
      }
    }
    return out;
  }, [items, chatId]);

  const openMedia = useCallback((a: Attachment, msg: MessageItem) => {
    const i = media.findIndex((x) => x.sha256 === a.sha256 && x.message_id === msg.id);
    setLightbox(i >= 0 ? i : null);
  }, [media]);

  const jump = useCallback((id: number) => {
    const i = itemsRef.current.findIndex((x) => x.type === "message" && x.id === id);
    if (i >= 0) {
      virt.current?.scrollToIndex({ index: i, align: "center", behavior: "smooth" });
      setHighlight(id);
    } else {
      navigate({ to: "/chat/$chatId", params: { chatId }, search: { m: id, hide } });
    }
  }, [chatId, navigate]);

  const isGroup = detail.data?.type === "group";
  const reactable = detail.data?.reactions, freeReactions = detail.data?.free_reactions;
  const editable = detail.data?.editable, deletable = detail.data?.deletable;
  const title = detail.data?.title ?? "";
  // a time asked for: the item nearest to it (the day may have nothing: then the nearest day's)
  const nearest = useMemo(() => {
    if (!around || !items.length) return 0;
    const after = items.findIndex((x) => x.ts >= around);
    if (after < 0) return items.length - 1;
    return after > 0 && around - items[after - 1].ts < items[after].ts - around ? after - 1 : after;
  }, [ready]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (ready && around && items.length && !sameDay(items[nearest].ts, around))
      toast(t("chat.nearestDay", { day: dateOnly(around), near: dateOnly(items[nearest].ts) }));
  }, [ready]); // eslint-disable-line react-hooks/exhaustive-deps
  const initialIndex = useMemo(() => {
    if (jumpTo) {
      const i = items.findIndex((x) => x.type === "message" && x.id === jumpTo);
      if (i >= 0) return { index: i, align: "center" as const };
    }
    if (around) return { index: nearest, align: "center" as const };
    if (resume) {
      const i = items.findIndex((x) => x.cursor === resume.cursor);
      if (i >= 0) return { index: i, align: "start" as const, offset: resume.offset ?? 0 };
    }
    return { index: items.length - 1, align: "end" as const };
  }, [ready]); // eslint-disable-line react-hooks/exhaustive-deps
  // opened at its end: kept there while what was drawn grows to its real height (pictures, previews,
  // long texts measured), until the user scrolls, or for a few seconds
  const stick = useRef(false);
  useEffect(() => {
    stick.current = ready && !jumpTo && !around && !resume;
    if (!stick.current) return;
    const done = setTimeout(() => { stick.current = false; }, 4000);
    return () => clearTimeout(done);
  }, [ready]); // eslint-disable-line react-hooks/exhaustive-deps
  const letGo = () => { stick.current = false; };
  useEffect(() => {          // any move up (keys, a scrollbar, a jump to a message) lets it go too
    const el = scroller.current;
    if (!ready || !el) return;
    let last = el.scrollTop;
    const on = () => {
      if (el.scrollTop < last - 2) stick.current = false;
      last = el.scrollTop;
    };
    el.addEventListener("scroll", on, { passive: true });
    return () => el.removeEventListener("scroll", on);
  }, [ready, items.length > 0]); // eslint-disable-line react-hooks/exhaustive-deps

  const render = useCallback((index: number, item: StreamItem) => {
    const i = index - first;
    const prev = i > 0 ? items[i - 1] : null;
    const next = i < items.length - 1 ? items[i + 1] : null;
    const newDay = !prev || !sameDay(prev.ts, item.ts);
    const sep = newDay ? (
      <div data-day-sep className="flex justify-center py-2">
        <button
          onClick={() => document.dispatchEvent(new CustomEvent("everysaid:jumpdate"))}
          className="rounded-full bg-panel/90 px-3 py-1 text-xs font-medium text-muted shadow-sm backdrop-blur"
        >
          {dayLabel(item.ts)}
        </button>
      </div>
    ) : null;
    let body: React.ReactNode;
    if (item.type === "call") body = <CallLine c={item} />;
    else if (item.kind === "system") body = <SystemLine m={item} />;
    else {
      const svc = item.service;
      const within = (secs: number | undefined) => secs !== undefined && (secs === 0 || Date.now() - item.ts < secs * 1000);
      const own = item.outgoing && item.keyed && !item.deleted;
      const r = item.keyed && reactable && svc in reactable ? reactable[svc] : undefined;
      const reactWith = r !== undefined ? { quick: r ?? ["👍", "❤️", "😂", "😮", "😢", "🙏"], all: r, free: !!freeReactions?.[svc], on: react } : undefined;
      const same = (a: StreamItem | null) => a && a.type === "message" && a.kind !== "system" && a.outgoing === item.outgoing &&
        a.sender_id === item.sender_id && Math.abs(a.ts - item.ts) < 5 * 60_000 && sameDay(a.ts, item.ts);
      const firstOfRun = !same(prev);
      const lastOfRun = !same(next);
      const showService = !prev || prev.type !== "message" || prev.service !== item.service || newDay;
      body = <Bubble m={item} group={isGroup} first={firstOfRun} last={lastOfRun} showService={showService}
        highlight={highlight === item.id} onOpen={openMedia} onJump={jump}
        onReply={item.keyed && replyable.includes(item.service) ? answer : undefined}
        onInfo={item.outgoing && item.receipts ? setInfoOf : undefined}
        react={reactWith}
        onEdit={own && item.text && within(editable?.[svc]) ? startEdit : undefined}
        onDelete={own && within(deletable?.[svc]) ? setDeleting : undefined} />;
    }
    return <div data-cursor={item.cursor} data-ts={item.ts} className={cn(next ? "" : "pb-3")}>{sep}{body}</div>;
  }, [first, items, isGroup, highlight, openMedia, jump, replyable.join(), answer, detail.data, react, startEdit]); // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <Center><div className="space-y-2"><div className="font-semibold">{t("common.error")}</div><div className="text-sm text-muted">{error}</div></div></Center>;

  return (
    <div className="flex h-full">
      <div className="flex min-w-0 flex-1 flex-col">
        <ChatHeader chat={detail.data} wide={wide} onInfo={() => setInfoOpen((o) => !o)} onJumpDate={(d) => navigate({ to: "/chat/$chatId", params: { chatId }, search: { ts: d, hide } })}
          hidden={hide ? hide.split(",") : []} onHide={(list) => navigate({ to: "/chat/$chatId", params: { chatId }, search: { hide: list.join(",") || undefined }, replace: true })} />
        <div data-stream className="chat-bg relative min-h-0 flex-1 overflow-hidden" onWheel={letGo} onTouchStart={letGo} onKeyDown={letGo} onMouseDown={letGo}>
          {!ready ? (
            <Center><Spinner className="size-7" /></Center>
          ) : items.length === 0 ? (
            <Center><span className="text-sm text-muted">{t("common.none")}</span></Center>
          ) : (
            <Virtuoso
              ref={virt}
              scrollerRef={(el) => { scroller.current = el as HTMLElement | null; }}
              data={items}
              firstItemIndex={first}
              initialTopMostItemIndex={initialIndex}
              computeItemKey={(_, it) => it.cursor}
              itemContent={render}
              startReached={loadOlder}
              endReached={() => loadNewer()}
              atBottomStateChange={setAtBottom}
              totalListHeightChanged={() => { if (stick.current) scrollEnd(); }}
              rangeChanged={() => { if (ready) requestAnimationFrame(() => { remember(); placeDay(); }); }}
              isScrolling={(on) => { if (!on && ready) remember(); }}
              atBottomThreshold={120}
              increaseViewportBy={{ top: 800, bottom: 400 }}
              components={{
                Header: () => (
                  <div className="flex justify-center py-4">
                    {hasOlder ? <Spinner /> : <span className="rounded-full bg-panel/80 px-3 py-1 text-xs text-muted">{t("chat.start")}</span>}
                  </div>
                ),
              }}
            />
          )}
          {ready && items.length > 0 && (
            <div ref={dayPill} data-day-pill style={{ visibility: "hidden" }} className="pointer-events-none absolute left-0 top-0 z-10 flex justify-center py-2">
              <button
                onClick={() => document.dispatchEvent(new CustomEvent("everysaid:jumpdate"))}
                className="pointer-events-auto rounded-full bg-panel/90 px-3 py-1 text-xs font-medium text-muted shadow-sm backdrop-blur"
              >
                <span ref={dayText} />
              </button>
            </div>
          )}
          {ready && (!atBottom || hasNewer) && (
            <button
              onClick={() => (!hasNewer ? virt.current?.scrollToIndex({ index: items.length - 1, behavior: "smooth" })
                : jumpTo || around ? navigate({ to: "/chat/$chatId", params: { chatId }, search: { hide } })
                : latest())}
              data-latest
              className="absolute bottom-4 right-4 grid size-11 place-items-center rounded-full border border-line bg-panel shadow-lg hover:bg-panel-2"
              aria-label={t("chat.latest")}
            >
              <ArrowDown className="size-5" />
            </button>
          )}
        </div>
        {detail.data && (
          <Composer services={detail.data.services.filter((s) => service(s).messages)} sendable={sendable} replyable={detail.data.replyable} missing={detail.data.unsendable}
            mentionable={isGroup ? detail.data.mentionable ?? [] : []} fileable={detail.data.fileable ?? []} members={detail.data.members ?? []}
            chatId={chatId} onSend={send} replyTo={replyTo} onCancelReply={() => setReplyTo(null)}
            editing={editing} onEdit={edit} onCancelEdit={() => setEditing(null)}
            preferred={(!hasNewer && [...items].reverse().find((i) => i.type === "message")?.service) || detail.data.last_service} />
        )}
      </div>
      {wide && infoOpen && detail.data && (
        <aside className="w-[360px] shrink-0 overflow-y-auto border-l border-line bg-panel">
          <ChatInfo chat={detail.data} onClose={() => setInfoOpen(false)} />
        </aside>
      )}
      {!wide && detail.data && (
        <Dialog open={infoOpen} onOpenChange={setInfoOpen} title={title}>
          <ChatInfo chat={detail.data} />
        </Dialog>
      )}
      <Lightbox items={media} index={lightbox} onIndex={setLightbox} onClose={() => setLightbox(null)} />
      <MessageInfo m={infoOf} onClose={() => setInfoOf(null)} />
      <Dialog open={!!deleting} onOpenChange={(o) => { if (!o) setDeleting(null); }} title={t("chat.deleteForAll")}
        description={t("chat.deleteConfirm", { service: deleting ? service(deleting.service).name : "" })}>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={() => setDeleting(null)}>{t("common.cancel")}</Button>
          <Button data-confirm-delete variant="danger" onClick={() => { const m = deleting; setDeleting(null); if (m) remove(m); }}>{t("chat.deleteConfirmButton")}</Button>
        </div>
      </Dialog>
    </div>
  );
}

/** hidden: the services the user turned off in this chat (all on at first); each of its services toggles. */
function ChatHeader({ chat, wide, onInfo, onJumpDate, hidden, onHide }: {
  chat?: ChatDetail; wide: boolean; onInfo: () => void; onJumpDate: (ms: number) => void; hidden: string[]; onHide: (services: string[]) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [dateOpen, setDateOpen] = useState(false);
  useEffect(() => {
    const f = () => setDateOpen(true);
    document.addEventListener("everysaid:jumpdate", f);
    return () => document.removeEventListener("everysaid:jumpdate", f);
  }, []);
  const set = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.patch(`/api/chats/${chat!.id}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["chats"] });
      qc.invalidateQueries({ queryKey: ["chat", chat!.id] });
    },
  });
  const back = useBack("/");
  const title = chat?.title ?? "";
  const subtitle = chat?.type === "group" ? `${chat.members?.length ?? 0} ${t("chat.members")}` : null;
  return (
    <header className="flex items-center gap-2 border-b border-line bg-panel/95 px-2 pb-2 pt-[max(0.5rem,env(safe-area-inset-top))] backdrop-blur md:px-4">
      {(!wide || (back.from && back.from !== "/" && !back.from.startsWith("/chat/"))) && (
        <button type="button" onClick={back.go} className="grid size-10 shrink-0 place-items-center rounded-full hover:bg-panel-2"
          aria-label={t("common.back")} title={t("common.back")} data-back>
          <ArrowLeft className="size-5" />
        </button>
      )}
      <div className="flex min-w-0 flex-1 items-center gap-3 px-1">
        <button onClick={onInfo} className="shrink-0 rounded-full" tabIndex={-1} aria-hidden>       {/* the name beside it is the same, for keyboards */}
          {chat && <Avatar name={title} src={avatarUrl({ type: chat.type, person_id: chat.person_id, avatar: chat.person?.avatar })} group={chat.type === "group"} size={40} />}
        </button>
        <div className="min-w-0">
          <button onClick={onInfo} className="block max-w-full truncate rounded-lg text-left font-semibold hover:underline">{title}</button>
          <div className="flex items-center gap-1 overflow-x-auto text-xs text-muted">
            {subtitle ?? (chat && chat.services.length > 1
              ? chat.services.map((s) => {
                  const off = hidden.includes(s);
                  const last = !off && hidden.length === chat.services.length - 1;     // one stays on
                  return (
                    <button key={s} data-service-toggle={s} aria-pressed={!off} disabled={last}
                      title={t(off ? "chat.showService" : "chat.hideService", { service: service(s).name })}
                      onClick={() => onHide(off ? hidden.filter((x) => x !== s) : [...hidden, s])}
                      className={cn("shrink-0 rounded-full transition disabled:cursor-default", off && "opacity-40 grayscale hover:opacity-70")}>
                      <ServiceBadge id={s} className="px-1.5 py-0" />
                    </button>
                  );
                })
              : chat?.services.map((s) => <ServiceBadge key={s} id={s} className="px-1.5 py-0" />))}
          </div>
        </div>
      </div>
      <IconButton label={t("chat.searchIn")} onClick={() => navigate({ to: "/search", search: { chat: chat?.id } })}><Search className="size-5" /></IconButton>
      <Popover.Root open={dateOpen} onOpenChange={setDateOpen}>
        <Popover.Trigger asChild>
          <Button variant="ghost" size="icon" className="rounded-full" aria-label={t("chat.jumpDate")}><CalendarDays className="size-5" /></Button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content align="end" sideOffset={6} className="z-50 rounded-2xl border border-line bg-panel p-3 shadow-xl">
            <div className="mb-2 text-sm font-medium">{t("chat.jumpDate")}</div>
            <DateField
              value=""
              autoFocus
              label={t("chat.jumpDate")}
              max={isoDay(Date.now())}
              onChange={(v) => {
                if (!v) return;
                const [y, mo, d] = v.split("-").map(Number);
                setDateOpen(false);
                onJumpDate(new Date(y, mo - 1, d).getTime());
              }}
            />
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <IconButton label={t("chat.info")} onClick={onInfo} data-chat-info><Info className="size-5" /></IconButton>
      {chat && (
        <Menu>
          <MenuTrigger asChild><Button variant="ghost" size="icon" className="rounded-full" aria-label={t("common.more")}><MoreVertical className="size-5" /></Button></MenuTrigger>
          <MenuContent>
            <MenuItem icon={chat.pinned ? <PinOff /> : <Pin />} onSelect={() => set.mutate({ pinned: !chat.pinned })}>{chat.pinned ? t("chats.unpin") : t("chats.pin")}</MenuItem>
            <MenuItem icon={<BellOff />} onSelect={() => set.mutate({ muted: !chat.muted })}>{chat.muted ? t("chats.unmute") : t("chats.mute")}</MenuItem>
            <MenuItem icon={chat.archived ? <ArchiveRestore /> : <Archive />} onSelect={() => set.mutate({ archived: !chat.archived })}>{chat.archived ? t("chats.unarchive") : t("chats.archive")}</MenuItem>
          </MenuContent>
        </Menu>
      )}
    </header>
  );
}

/** A person of a group the text names: where, in characters (code points, as the server counts them). */
export interface Mentioned { start: number; length: number; address_id: number }

/** Letters as they compare when searching names: lower case, without accents. */
const plain = (s: string) => s.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();

/** The people the text names: every "@Name" of a member in it, as a whole (not a short name inside a longer one:
 * the longer names claim their places first), each place once. Chosen from the list or typed, kept
 * in a draft or not: the text says it. */
export function placeMentions(text: string, members: { label: string; address_id: number }[]): Mentioned[] {
  const out: Mentioned[] = [];
  const taken: [number, number][] = [];
  const word = /[\p{L}\p{N}_]/u;
  for (const c of [...members].sort((a, b) => b.label.length - a.label.length)) {
    for (let i = text.indexOf(c.label); i >= 0; i = text.indexOf(c.label, i + 1)) {
      const end = i + c.label.length;
      if (word.test(text[end] ?? "") || (i > 0 && word.test(text[i - 1]))) continue;
      if (taken.some(([a, b]) => i < b && end > a)) continue;
      taken.push([i, end]);
      out.push({ start: Array.from(text.slice(0, i)).length, length: Array.from(c.label).length, address_id: c.address_id });
    }
  }
  return out.sort((a, b) => a.start - b.start);
}

/** services: those of the chat with messages; sendable / replyable: those something can send to now
 * (or answer a given message in); mentionable / fileable: those where people of the group can be named
 * with @ / a file sent; missing: why a source cannot send to one now. preferred: where the chat was last
 * active, the way to answer until the user picks another. One it cannot send through still shows,
 * closed, with a lock for Send. */
function Composer({ chatId, services, sendable, replyable, mentionable, fileable, members, missing, preferred, onSend, replyTo, onCancelReply, editing, onEdit, onCancelEdit }: {
  chatId: string; services: string[]; sendable: string[]; replyable: string[]; mentionable: string[]; fileable: string[];
  members: Member[]; missing: Record<string, string>;
  preferred?: string | null;
  onSend: (text: string, service: string | null, replyTo: MessageItem | null, mentions: Mentioned[], file: File | null) => void;
  replyTo: MessageItem | null; onCancelReply: () => void;
  editing: MessageItem | null; onEdit: (m: MessageItem, text: string) => void; onCancelEdit: () => void;
}) {
  const { t } = useTranslation();
  const settings = useSettings();
  const wide = useWide();
  const navigate = useNavigate();
  const [text, setText] = useState(() => draft(chatId));       // what was left unsent here, kept
  useEffect(() => { if (!editing) keepDraft(chatId, text); }, [chatId, text, editing]);
  const [svc, setSvc] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [caret, setCaret] = useState(0);
  const [pick, setPick] = useState(0);              // the person marked in the @ list
  const [noList, setNoList] = useState(false);       // the @ list closed with Esc, until the text changes
  const enterSends = (settings.data?.send_enter as boolean | undefined) ?? wide;
  const picked = useRef(false);
  // Esc lets go of the message being answered or edited, wherever the focus is (unless it closed the @ list)
  useEffect(() => {
    if (!replyTo && !editing) return;
    const off = (e: KeyboardEvent) => { if (e.key === "Escape" && !e.defaultPrevented) { onCancelReply(); onCancelEdit(); } };
    window.addEventListener("keydown", off);
    return () => window.removeEventListener("keydown", off);
  }, [replyTo, editing, onCancelReply, onCancelEdit]);
  // an edit takes the field with the message's text; what was being written comes back after it
  const before = useRef<string | null>(null);
  useEffect(() => {
    if (editing) {
      if (before.current === null) before.current = text;
      setText(editing.text ?? "");
      setFile(null);
    } else if (before.current !== null) {
      setText(before.current);
      before.current = null;
    }
  }, [editing?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setSvc((cur) => (picked.current && cur && services.includes(cur) ? cur
      : preferred && services.includes(preferred) ? preferred : services[0] ?? null));
  }, [services.join(), preferred]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`;
  }, [text]);
  const via = editing?.service ?? replyTo?.service ?? svc ?? "";          // an answer goes where the message came from
  // "@" and what follows it, up to the caret: whom to name
  const asked = useMemo(() => {
    if (!mentionable.includes(via) || noList) return null;
    const m = /(?:^|\s)@([^\s@]{0,30})$/u.exec(text.slice(0, caret));
    return m ? { query: m[1], at: caret - m[1].length - 1 } : null;
  }, [text, caret, via, mentionable.join(), noList]); // eslint-disable-line react-hooks/exhaustive-deps
  const options = useMemo(() => {
    if (!asked) return [];
    const q = plain(asked.query);
    return members.filter((m) => m.services.includes(via) && m.name && plain(m.name).split(/\s+/).some((w) => w.startsWith(q)))
      .sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "")).slice(0, 8);
  }, [asked, members, via]);
  useEffect(() => setPick(0), [asked?.query]);
  if (!services.length || !svc) return null;          // calls only: nothing to write
  const can = !!editing || (replyTo ? replyable : sendable).includes(via);
  const canFile = can && fileable.includes(via);
  const why = () => toast(t("chat.cannotSend", { service: service(via).name }), {
    description: missing[via] ?? t("chat.cannotSendHint"), action: { label: t("nav.sources"), onClick: () => navigate({ to: "/sources" }) },
  });
  const choose = (m: Member) => {
    if (!asked) return;
    const label = `@${m.name}`;
    const next = text.slice(0, asked.at) + label + " " + text.slice(caret);
    const at = asked.at + label.length + 1;
    setText(next);
    setCaret(at);
    requestAnimationFrame(() => { ref.current?.focus(); ref.current?.setSelectionRange(at, at); });
  };
  const go = () => {
    const body = text.trim();
    if (editing) {
      if (body && body !== (editing.text ?? "").trim()) onEdit(editing, body);
      onCancelEdit();
      ref.current?.focus();
      return;
    }
    if ((!body && !file) || !can || (file && !canFile)) return;
    setText("");                        // free for the next one at once
    setFile(null);
    const named = members.filter((m) => m.name && m.services.includes(via)).map((m) => ({ label: `@${m.name}`, address_id: m.address_id }));
    onSend(body, svc, replyTo, mentionable.includes(via) ? placeMentions(body, named) : [], file);
    onCancelReply();
    ref.current?.focus();
  };
  const listOpen = options.length > 0;
  return (
    <div data-composer className="relative border-t border-line bg-panel px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
      {listOpen && (
        <ul data-mention-list role="listbox" aria-label={t("chat.mention")}
          className="absolute bottom-full left-3 right-3 z-20 mb-1 max-h-72 overflow-y-auto rounded-2xl border border-line bg-panel p-1 shadow-xl md:right-auto md:w-80">
          {options.map((m, i) => (
            <li key={m.address_id} role="option" aria-selected={i === pick}>
              <button data-mention-option onMouseDown={(e) => e.preventDefault()} onClick={() => choose(m)}
                className={cn("flex w-full items-center gap-3 rounded-xl px-2 py-1.5 text-left text-sm", i === pick ? "bg-panel-2" : "hover:bg-panel-2")}>
                <Avatar name={m.name || "?"} size={28} />
                <span className="truncate">{m.name}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {replyTo && (
        <div data-replying className="mb-2 flex items-center gap-2 rounded-xl border-l-[3px] border-accent bg-accent/8 py-1.5 pl-3 pr-1 text-sm">
          <CornerUpLeft className="size-4 shrink-0 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold text-accent">{t("chat.replyTo", { name: replyTo.outgoing ? t("common.me") : replyTo.sender || "…" })}</div>
            <div className="truncate text-muted">{replyTo.text || t(`kind.${replyTo.kind}`, { defaultValue: replyTo.kind })}</div>
          </div>
          <Button variant="ghost" size="iconSm" className="rounded-full" onClick={onCancelReply} aria-label={t("chat.cancelReply")}><X className="size-4" /></Button>
        </div>
      )}
      {editing && (
        <div data-editing className="mb-2 flex items-center gap-2 rounded-xl border-l-[3px] border-accent bg-accent/8 py-1.5 pl-3 pr-1 text-sm">
          <Pencil className="size-4 shrink-0 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold text-accent">{t("chat.editing")}</div>
            <div className="truncate text-muted">{editing.text}</div>
          </div>
          <Button variant="ghost" size="iconSm" className="rounded-full" onClick={onCancelEdit} aria-label={t("chat.cancelEdit")}><X className="size-4" /></Button>
        </div>
      )}
      {file && (
        <div data-file className="mb-2 flex items-center gap-2 rounded-xl bg-panel-2 py-1.5 pl-3 pr-1 text-sm">
          <FileText className="size-4 shrink-0 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium">{file.name}</div>
            <div className="text-xs text-muted">{bytes(file.size)}{!canFile && ` · ${t("chat.noFilesVia", { service: service(via).name })}`}</div>
          </div>
          <Button variant="ghost" size="iconSm" className="rounded-full" onClick={() => setFile(null)} aria-label={t("chat.removeFile")}><X className="size-4" /></Button>
        </div>
      )}
      <div className="flex items-end gap-2">
        <div data-composer-body className="flex min-w-0 flex-1 items-end rounded-3xl bg-panel-2 pr-1">
          <Menu>
            <MenuTrigger asChild disabled={!!replyTo || !!editing}>
              <button data-via={via} aria-label={t("chat.sendVia")} title={`${t("chat.sendVia")} ${service(via).name}`}
                className="mb-1 ml-1 flex h-9 shrink-0 items-center gap-0.5 rounded-full pl-2 pr-1 hover:bg-panel disabled:hover:bg-transparent data-[state=open]:bg-panel">
                <ServiceIcon id={via} className="size-5" />
                {!replyTo && !editing && services.length > 1 && <ChevronDown className="size-3.5 text-muted" />}
              </button>
            </MenuTrigger>
            <MenuContent align="start" className="w-60">
              <div className="px-3 pb-1 pt-1.5 text-xs font-medium text-muted">{t("chat.sendVia")}</div>
              {services.map((s) => (
                <MenuItem key={s} onSelect={() => { picked.current = true; setSvc(s); ref.current?.focus(); }}>
                  <ServiceIcon id={s} className="size-5" />
                  <span className="flex-1">{service(s).name}</span>
                  {/* each in its own place, there or not, so neither moves */}
                  <span className="grid size-4 place-items-center">{!sendable.includes(s) && <Lock className="size-3.5 text-muted" aria-label={t("chat.locked")} />}</span>
                  <span className="grid size-4 place-items-center">{s === svc && <Check className="size-4 text-accent" />}</span>
                </MenuItem>
              ))}
            </MenuContent>
          </Menu>
          <Textarea
            ref={ref}
            rows={1}
            value={text}
            disabled={!can}                      // nothing to write where it cannot go
            role={listOpen ? "combobox" : undefined}
            aria-expanded={listOpen || undefined}
            onChange={(e) => { setText(e.target.value); setCaret(e.target.selectionStart ?? e.target.value.length); setNoList(false); }}
            onSelect={(e) => setCaret((e.target as HTMLTextAreaElement).selectionStart ?? 0)}
            onKeyDown={(e) => {
              if (listOpen) {
                if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  setPick((p) => (p + (e.key === "ArrowDown" ? 1 : options.length - 1)) % options.length);
                  return;
                }
                if ((e.key === "Enter" || e.key === "Tab") && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  choose(options[pick] ?? options[0]);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setNoList(true);
                  return;
                }
              }
              if (e.key === "Enter" && !e.shiftKey && enterSends && !e.nativeEvent.isComposing) {
                e.preventDefault();
                go();
              }
            }}
            placeholder={!can ? t("chat.cannotSend", { service: service(via).name }) : file ? t("chat.caption") : t("chat.messageVia", { service: service(via).name })}
            className="max-h-44 border-0 bg-transparent py-2.5 pl-2 focus:ring-0 focus-visible:outline-none disabled:cursor-not-allowed"
          />
          {canFile && !editing && (
            <>
              <input ref={fileInput} type="file" hidden data-file-input
                onChange={(e) => { setFile(e.target.files?.[0] ?? null); e.target.value = ""; ref.current?.focus(); }} />
              <button data-attach onClick={() => fileInput.current?.click()} aria-label={t("chat.attach")} title={t("chat.attach")}
                className="mb-1 grid size-9 shrink-0 place-items-center rounded-full text-muted hover:bg-panel hover:text-fg">
                <Paperclip className="size-5" />
              </button>
            </>
          )}
        </div>
        {can ? (
          <Button data-send variant="primary" size="icon" className="shrink-0 rounded-full" onClick={go}
            disabled={(!text.trim() && !file) || (!!file && !canFile)} aria-label={t("chat.send")}>
            <SendHorizontal className="size-5" />
          </Button>
        ) : (
          <Button data-locked variant="outline" size="icon" className="shrink-0 rounded-full text-muted" onClick={why}
            aria-label={t("chat.cannotSend", { service: service(via).name })} title={t("chat.cannotSend", { service: service(via).name })}>
            <Lock className="size-5" />
          </Button>
        )}
      </div>
    </div>
  );
}
