import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import * as Popover from "@radix-ui/react-popover";
import { ArrowDown, ArrowLeft, BellOff, CalendarDays, CornerUpLeft, Archive, ArchiveRestore, Info, Lock, MoreVertical, Pin, PinOff, Search, SendHorizontal, X } from "lucide-react";
import { toast } from "sonner";
import { api, qs, type Attachment, type ChatDetail, type MessageItem, type StreamItem, type StreamPage } from "@/lib/api";
import { dayLabel, isoDay, sameDay } from "@/lib/format";
import { onEvent } from "@/lib/events";
import { useSettings, useWide } from "@/lib/hooks";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { chatRoute } from "@/router";
import { Avatar, Button, Center, Dialog, IconButton, Menu, MenuContent, MenuItem, MenuTrigger, ServiceBadge, Spinner, Textarea } from "@/components/ui";
import { Bubble, CallLine, SystemLine } from "@/components/Message";
import { Lightbox, type LightboxItem } from "@/components/Lightbox";
import { ChatInfo } from "@/components/ChatInfo";
import { avatarUrl } from "@/components/ChatList";

const START = 1_000_000_000;
const PAGE = 80;

export function ChatPage() {
  const { chatId } = chatRoute.useParams();
  const { m, ts } = chatRoute.useSearch();
  return <ChatView key={`${chatId}:${m ?? ""}:${ts ?? ""}`} chatId={chatId} jumpTo={m} around={ts} />;
}

function ChatView({ chatId, jumpTo, around }: { chatId: string; jumpTo?: number; around?: number }) {
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
  const answer = useCallback((m: MessageItem) => {
    setReplyTo(m);
    requestAnimationFrame(() => (document.querySelector("[data-composer-body] textarea") as HTMLElement | null)?.focus());
  }, []);

  // the first page: the latest, or around a message or a time
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        let at = around;
        if (jumpTo && !at) at = (await api.get<MessageItem>(`/api/messages/${jumpTo}`)).ts + 1;
        const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ around: at, limit: PAGE })}`);
        if (cancelled) return;
        setItems(page.items);
        setFirst(START - page.items.length);
        setHasOlder(page.has_older);
        setHasNewer(at ? page.has_newer : false);
        setReady(true);
      } catch (e: any) {
        if (!cancelled) setError(e.message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [chatId, jumpTo, around]);

  const loadOlder = useCallback(async () => {
    const cur = itemsRef.current;
    if (busy.current.older || !hasOlder || !cur.length) return;
    busy.current.older = true;
    try {
      const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ before: cur[0].cursor, limit: PAGE })}`);
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
      const page = await api.get<StreamPage>(`/api/chats/${chatId}/stream${qs({ after: last.cursor, limit: PAGE })}`);
      setItems((prev) => {
        const seen = new Set(prev.map((i) => i.cursor));
        const fresh = page.items.filter((i) => !seen.has(i.cursor));
        // a placeholder of a sent message goes once the message itself is here
        const arrived = new Set(fresh.filter((i) => i.type === "message" && i.outgoing).map((i) => (i as MessageItem).text));
        return [...prev.filter((i) => !(i.type === "message" && i.id < 0 && i.status === "sent" && arrived.has(i.text))), ...fresh];
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
  const send = useCallback(async (body: string, service: string | null, answered: MessageItem | null) => {
    if (hasNewer) navigate({ to: "/chat/$chatId", params: { chatId }, search: {} });
    const id = -Date.now() - Math.random();
    const mark = (status: string) => setItems((prev) => prev.map((i) => (i.type === "message" && i.id === id ? { ...i, status } : i)));
    toEnd.current = true;
    setItems((prev) => [...prev, {
      type: "message", id, ts: Date.now(), cursor: `tmp:${id}`, service: service ?? "", conversation_id: 0, outgoing: true,
      kind: "text", subtype: null, text: body, sender_id: null, sender: null, reply_to: null, reply_text: null, edited: false,
      deleted: false, forwarded: false, starred: false, status: "sending", location: null, reactions: [], attachments: [],
      reply: answered ? { id: answered.id, text: answered.text ?? "", outgoing: answered.outgoing, sender: answered.sender, kind: answered.kind } : undefined,
    }]);
    try {
      await api.post(`/api/chats/${chatId}/send`, { text: body, service: answered ? answered.service : service, reply_to: answered?.id });
      mark("sent");
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

  // read: when the latest are in view
  const read = useMutation({
    mutationFn: () => api.post(`/api/chats/${chatId}/read`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["chats"] }),
  });
  const lastTs = items.length ? items[items.length - 1].ts : 0;
  useEffect(() => {
    if (!ready || !atBottom || hasNewer) return;
    const timer = setTimeout(() => read.mutate(), 800);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, atBottom, hasNewer, lastTs, chatId]);

  // highlight fades
  useEffect(() => {
    if (!highlight) return;
    const timer = setTimeout(() => setHighlight(null), 2500);
    return () => clearTimeout(timer);
  }, [highlight]);

  const media = useMemo(() => {
    const out: (LightboxItem & { key: string })[] = [];
    for (const i of items) {
      if (i.type !== "message") continue;
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
      navigate({ to: "/chat/$chatId", params: { chatId }, search: { m: id } });
    }
  }, [chatId, navigate]);

  const isGroup = detail.data?.type === "group";
  const title = detail.data?.title ?? "";
  const initialIndex = useMemo(() => {
    if (jumpTo) {
      const i = items.findIndex((x) => x.type === "message" && x.id === jumpTo);
      if (i >= 0) return { index: i, align: "center" as const };
    }
    if (around) return { index: Math.max(0, Math.floor(items.length / 2)), align: "center" as const };
    return items.length - 1;
  }, [ready]); // eslint-disable-line react-hooks/exhaustive-deps

  const render = useCallback((index: number, item: StreamItem) => {
    const i = index - first;
    const prev = i > 0 ? items[i - 1] : null;
    const next = i < items.length - 1 ? items[i + 1] : null;
    const newDay = !prev || !sameDay(prev.ts, item.ts);
    const sep = newDay ? (
      <div className="flex justify-center py-2">
        <button
          onClick={() => document.dispatchEvent(new CustomEvent("chronika:jumpdate"))}
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
      const same = (a: StreamItem | null) => a && a.type === "message" && a.kind !== "system" && a.outgoing === item.outgoing &&
        a.sender_id === item.sender_id && Math.abs(a.ts - item.ts) < 5 * 60_000 && sameDay(a.ts, item.ts);
      const firstOfRun = !same(prev);
      const lastOfRun = !same(next);
      const showService = !prev || prev.type !== "message" || prev.service !== item.service || newDay;
      body = <Bubble m={item} group={isGroup} first={firstOfRun} last={lastOfRun} showService={showService}
        highlight={highlight === item.id} onOpen={openMedia} onJump={jump}
        onReply={item.keyed && replyable.includes(item.service) ? answer : undefined} />;
    }
    return <div className={cn(next ? "" : "pb-3")}>{sep}{body}</div>;
  }, [first, items, isGroup, highlight, openMedia, jump, replyable.join(), answer]); // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <Center><div className="space-y-2"><div className="font-semibold">{t("common.error")}</div><div className="text-sm text-muted">{error}</div></div></Center>;

  return (
    <div className="flex h-full">
      <div className="flex min-w-0 flex-1 flex-col">
        <ChatHeader chat={detail.data} wide={wide} onInfo={() => setInfoOpen((o) => !o)} onJumpDate={(d) => navigate({ to: "/chat/$chatId", params: { chatId }, search: { ts: d } })} />
        <div data-stream className="chat-bg relative min-h-0 flex-1">
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
          {ready && (!atBottom || hasNewer) && (
            <button
              onClick={() => (hasNewer ? navigate({ to: "/chat/$chatId", params: { chatId }, search: {} }) : virt.current?.scrollToIndex({ index: items.length - 1, behavior: "smooth" }))}
              className="absolute bottom-4 right-4 grid size-11 place-items-center rounded-full border border-line bg-panel shadow-lg hover:bg-panel-2"
              aria-label={t("chat.latest")}
            >
              <ArrowDown className="size-5" />
            </button>
          )}
        </div>
        {detail.data && (
          <Composer services={sendable} onSend={send} replyTo={replyTo} onCancelReply={() => setReplyTo(null)}
            preferred={[...items].reverse().find((i) => sendable.includes(i.service ?? ""))?.service ?? undefined} />
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
    </div>
  );
}

function ChatHeader({ chat, wide, onInfo, onJumpDate }: { chat?: ChatDetail; wide: boolean; onInfo: () => void; onJumpDate: (ms: number) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [dateOpen, setDateOpen] = useState(false);
  useEffect(() => {
    const f = () => setDateOpen(true);
    document.addEventListener("chronika:jumpdate", f);
    return () => document.removeEventListener("chronika:jumpdate", f);
  }, []);
  const set = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.patch(`/api/chats/${chat!.id}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["chats"] });
      qc.invalidateQueries({ queryKey: ["chat", chat!.id] });
    },
  });
  const title = chat?.title ?? "";
  const subtitle = chat?.type === "group" ? `${chat.members?.length ?? 0} ${t("chat.members")}` : null;
  return (
    <header className="flex items-center gap-2 border-b border-line bg-panel/95 px-2 pb-2 pt-[max(0.5rem,env(safe-area-inset-top))] backdrop-blur md:px-4">
      {!wide && (
        <Link to="/" className="grid size-10 place-items-center rounded-full hover:bg-panel-2" aria-label={t("common.back")}>
          <ArrowLeft className="size-5" />
        </Link>
      )}
      <button onClick={onInfo} className="flex min-w-0 flex-1 items-center gap-3 rounded-2xl px-1 py-0.5 text-left hover:bg-panel-2">
        {chat && <Avatar name={title} src={avatarUrl({ type: chat.type, person_id: chat.person_id, avatar: chat.person?.avatar })} group={chat.type === "group"} size={40} />}
        <div className="min-w-0">
          <div className="truncate font-semibold">{title}</div>
          <div className="flex items-center gap-1 overflow-hidden text-xs text-muted">
            {subtitle ?? chat?.services.filter((s) => service(s).messages).map((s) => <ServiceBadge key={s} id={s} className="px-1.5 py-0" />)}
          </div>
        </div>
      </button>
      <IconButton label={t("chat.searchIn")} onClick={() => navigate({ to: "/search", search: { chat: chat?.id } })}><Search className="size-5" /></IconButton>
      <Popover.Root open={dateOpen} onOpenChange={setDateOpen}>
        <Popover.Trigger asChild>
          <Button variant="ghost" size="icon" className="rounded-full" aria-label={t("chat.jumpDate")}><CalendarDays className="size-5" /></Button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content align="end" sideOffset={6} className="z-50 rounded-2xl border border-line bg-panel p-3 shadow-xl">
            <div className="mb-2 text-sm font-medium">{t("chat.jumpDate")}</div>
            <input
              type="date"
              className="h-10 rounded-xl border border-line bg-panel px-3 text-sm"
              max={isoDay(Date.now())}
              onChange={(e) => {
                if (!e.target.value) return;
                const [y, mo, d] = e.target.value.split("-").map(Number);
                setDateOpen(false);
                onJumpDate(new Date(y, mo - 1, d).getTime());
              }}
            />
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <IconButton label={t("chat.info")} onClick={onInfo}><Info className="size-5" /></IconButton>
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

/** preferred: where the chat was last active (the default way to send, until the user picks another). */
function Composer({ services, preferred, onSend, replyTo, onCancelReply }: {
  services: string[]; preferred?: string; onSend: (text: string, service: string | null, replyTo: MessageItem | null) => void;
  replyTo: MessageItem | null; onCancelReply: () => void;
}) {
  const { t } = useTranslation();
  const settings = useSettings();
  const wide = useWide();
  const [text, setText] = useState("");
  const [svc, setSvc] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const enterSends = (settings.data?.send_enter as boolean | undefined) ?? wide;
  const picked = useRef(false);
  useEffect(() => {
    setSvc((cur) => (picked.current && cur && services.includes(cur) ? cur : preferred ?? services[0] ?? null));
  }, [services.join(), preferred]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`;
  }, [text]);
  if (!services.length) {
    return (
      <div data-composer className="flex items-center gap-2 border-t border-line bg-panel px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] text-xs text-muted">
        <Lock className="size-4 shrink-0" />
        <span data-composer-body className="flex-1"><span className="font-medium text-fg/80">{t("chat.readOnly")}.</span> {t("chat.readOnlyHint")}</span>
        <Link to="/sources" className="shrink-0 font-medium text-accent hover:underline">{t("nav.sources")}</Link>
      </div>
    );
  }
  const go = () => {
    const body = text.trim();
    if (!body) return;
    setText("");                        // free for the next one at once
    onSend(body, svc, replyTo);
    onCancelReply();
    ref.current?.focus();
  };
  return (
    <div data-composer className="border-t border-line bg-panel px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]">
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
      <div className="flex items-end gap-2">
        <div data-composer-body className="flex min-w-0 flex-1 items-end rounded-3xl bg-panel-2 pr-1">
          <Textarea
            ref={ref}
            rows={1}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && enterSends && !e.nativeEvent.isComposing) {
                e.preventDefault();
                go();
              } else if (e.key === "Escape" && replyTo) onCancelReply();
            }}
            placeholder={`${t("chat.placeholder")} · ${t("chat.via")} ${service(replyTo?.service ?? svc ?? services[0]).name}`}
            className="max-h-44 border-0 bg-transparent py-2.5 pl-4 focus:ring-0 focus-visible:outline-none"
          />
          {services.length > 1 && (
            <select value={svc ?? ""} onChange={(e) => { picked.current = true; setSvc(e.target.value); }} className="mb-1.5 rounded-full bg-panel px-2 py-1 text-xs" aria-label={t("chat.via")}>
              {services.map((s) => <option key={s} value={s}>{service(s).name}</option>)}
            </select>
          )}
        </div>
        <Button data-send variant="primary" size="icon" className="shrink-0 rounded-full" onClick={go} disabled={!text.trim()} aria-label={t("chat.send")}>
          <SendHorizontal className="size-5" />
        </Button>
      </div>
    </div>
  );
}
