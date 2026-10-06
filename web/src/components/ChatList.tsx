import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { keepListPlace, listPlace } from "@/lib/memory";
import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Archive, ArchiveRestore, BellOff, Check, Image, MessagesSquare, Mic, MoreVertical, Phone, PhoneMissed, Pin, PinOff, Search, Video, X } from "lucide-react";
import { api, type ChatSummary } from "@/lib/api";
import { useChats, useDebounced } from "@/lib/hooks";
import { shortWhen } from "@/lib/format";
import { cn } from "@/lib/utils";
import { service } from "@/lib/services";
import { Avatar, Button, Empty, Menu, MenuContent, MenuItem, MenuTrigger, Segmented, ServiceDot, Spinner } from "./ui";
import { Logo } from "./Logo";
import { GroupSuggestions } from "./GroupMerge";

type Filter = "all" | "person" | "group" | "unread";

export function avatarUrl(c: { type: string; person_id?: number | null; avatar?: boolean }) {
  return c.type === "person" && c.avatar && c.person_id ? `/api/avatar/${c.person_id}` : null;
}

function Preview({ c }: { c: ChatSummary }) {
  const { t } = useTranslation();
  const l = c.last;
  if (!l) return <span className="text-muted">—</span>;
  if (l.type === "call") {
    const missed = !l.outgoing && !l.answered;
    const Icon = missed ? PhoneMissed : l.video ? Video : Phone;
    return (
      <span className={cn("flex items-center gap-1", missed && "text-danger")}>
        <Icon className="size-3.5 shrink-0" />
        {missed ? t("call.missed") : l.outgoing ? t("call.outgoing") : t("call.incoming")}
      </span>
    );
  }
  const icon = l.kind === "image" ? <Image className="size-3.5 shrink-0" /> : l.kind === "voice" ? <Mic className="size-3.5 shrink-0" /> : l.kind === "video" ? <Video className="size-3.5 shrink-0" /> : null;
  const body = l.deleted ? t("chat.deleted") : l.text || (l.kind && l.kind !== "text" ? t(`kind.${l.kind}`, { defaultValue: l.kind }) : "");
  return (
    <span className="flex min-w-0 items-center gap-1">
      {l.outgoing && <Check className="size-3.5 shrink-0 text-muted" />}
      {l.sender && <span className="shrink-0 font-medium text-fg/80">{/\p{L}/u.test(l.sender) ? l.sender.split(" ")[0] : l.sender}:</span>}
      {icon}
      <span className="truncate">{body}</span>
    </span>
  );
}

function ChatRow({ c, active }: { c: ChatSummary; active: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.patch(`/api/chats/${c.id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["chats"] }),
  });
  const read = useMutation({
    mutationFn: () => api.post(`/api/chats/${c.id}/read`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["chats"] }),
  });
  return (
    <div data-chat={c.id} className={cn("group relative mx-2 my-0.5 rounded-2xl", active ? "bg-accent/12" : "hover:bg-panel-2")}>
      <Link to="/chat/$chatId" params={{ chatId: c.id }} className="flex items-center gap-3 px-3 py-2.5">
        <Avatar name={c.title} src={avatarUrl(c)} group={c.type === "group"} size={50} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className={cn("truncate font-medium", c.unread > 0 && "font-semibold")}>{c.title}</span>
            <span className={cn("ml-auto shrink-0 text-xs text-muted", c.unread > 0 && !c.muted && "font-semibold text-accent")}>
              {shortWhen(c.last_ts)}
            </span>
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-sm text-muted">
            <span className="min-w-0 flex-1"><Preview c={c} /></span>
            <span className="flex shrink-0 items-center gap-1.5">
              {c.archived && <Archive data-archived className="size-3.5" aria-label={t("chats.archivedOne")} />}
              {c.muted && <BellOff className="size-3.5" />}
              {c.pinned && <Pin className="size-3.5 rotate-45" />}
              <span className="flex -space-x-0.5">{c.services.filter((s) => service(s).messages).slice(0, 4).map((s) => <ServiceDot key={s} id={s} className="ring-2 ring-panel" />)}</span>
              {c.unread > 0 && (
                <span className={cn("min-w-5 rounded-full px-1.5 text-center text-[11px] font-bold leading-5", c.muted ? "bg-muted/30 text-fg" : "bg-accent text-accent-fg")}>
                  {c.unread > 999 ? "999+" : c.unread}
                </span>
              )}
            </span>
          </div>
        </div>
      </Link>
      <Menu>
        <MenuTrigger
          aria-label={t("common.more")}
          className="absolute right-2 top-2 grid size-7 place-items-center rounded-full bg-panel text-muted opacity-0 shadow-sm outline-none transition-opacity group-hover:opacity-100 focus:opacity-100 data-[state=open]:opacity-100"
        >
          <MoreVertical className="size-4" />
        </MenuTrigger>
        <MenuContent>
          <MenuItem icon={c.pinned ? <PinOff /> : <Pin />} onSelect={() => set.mutate({ pinned: !c.pinned })}>
            {c.pinned ? t("chats.unpin") : t("chats.pin")}
          </MenuItem>
          <MenuItem icon={<BellOff />} onSelect={() => set.mutate({ muted: !c.muted })}>
            {c.muted ? t("chats.unmute") : t("chats.mute")}
          </MenuItem>
          {c.unread > 0 && <MenuItem icon={<Check />} onSelect={() => read.mutate()}>{t("chats.markRead")}</MenuItem>}
          <MenuItem icon={c.archived ? <ArchiveRestore /> : <Archive />} onSelect={() => set.mutate({ archived: !c.archived })}>
            {c.archived ? t("chats.unarchive") : t("chats.archive")}
          </MenuItem>
        </MenuContent>
      </Menu>
    </div>
  );
}

export function ChatList() {
  const { t } = useTranslation();
  const [was] = useState(listPlace);          // as it was left (this tab), coming back to it
  const [filter, setFilter] = useState<Filter>((was?.filter as Filter) ?? "all");
  const [q, setQ] = useState(was?.q ?? "");
  const [archived, setArchived] = useState(was?.archived ?? false);
  const dq = useDebounced(q, 150);
  const chats = useChats({ q: dq || undefined, archived: archived || undefined });
  const params = useParams({ strict: false }) as { chatId?: string };
  const items = useMemo(() => {
    let list = chats.data?.items ?? [];
    if (archived) list = list.filter((c) => c.archived);
    if (filter === "unread") return list.filter((c) => c.unread > 0);
    if (filter === "person") return list.filter((c) => c.type === "person");
    if (filter === "group") return list.filter((c) => c.type !== "person");
    return list;
  }, [chats.data, filter, archived]);

  // where the list was: the open chat at the same height (wherever new messages moved it), else the
  // first row that was in view
  const scroller = useRef<HTMLElement | null>(null);
  const remember = useCallback(() => {
    const el = scroller.current;
    const place: NonNullable<ReturnType<typeof listPlace>> = { ...listPlace(), filter, q, archived };
    if (el) {                                   // (not drawn yet: where it was stays)
      delete place.active;
      const edge = el.getBoundingClientRect().top;
      const rows = [...el.querySelectorAll<HTMLElement>("[data-chat]")];
      const first = rows.find((r) => r.getBoundingClientRect().bottom > edge + 1);
      if (first) place.top = { id: first.dataset.chat!, offset: Math.round(edge - first.getBoundingClientRect().top) };
      const open = rows.find((r) => r.dataset.chat === params.chatId);
      if (open) place.active = { id: open.dataset.chat!, at: Math.round(open.getBoundingClientRect().top - edge) };
    }
    keepListPlace(place);
  }, [filter, q, archived, params.chatId]);
  useEffect(remember, [remember]);              // a filter changed, or another chat opened
  const start = useMemo(() => {
    if (!was) return 0;
    const open = was.active && items.findIndex((c) => c.id === was.active!.id && c.id === params.chatId);
    if (open !== undefined && open >= 0) return { index: open, align: "start" as const, offset: -was.active!.at };
    const top = was.top ? items.findIndex((c) => c.id === was.top!.id) : -1;
    return top >= 0 ? { index: top, align: "start" as const, offset: was.top!.offset } : 0;
  }, [chats.isSuccess]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="flex h-full flex-col bg-panel">
      <div className="space-y-3 px-4 pb-3 pt-[max(1rem,env(safe-area-inset-top))]">
        <div className="flex items-center gap-2">
          <Logo className="size-8 md:hidden" />
          <h1 className="text-xl font-semibold tracking-tight">{archived ? t("chats.showArchived") : t("chats.title")}</h1>
          <button
            onClick={() => setArchived(!archived)}
            data-show-archived
            aria-pressed={archived}
            className={cn("ml-auto grid size-9 place-items-center rounded-full text-muted hover:bg-panel-2", archived && "bg-accent/12 text-accent")}
            aria-label={t("chats.showArchived")}
            title={t("chats.showArchived")}
          >
            <Archive className="size-4" />
          </button>
        </div>
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t("chats.search")}
            className="h-10 w-full rounded-full bg-panel-2 pl-9 pr-9 text-sm outline-none placeholder:text-muted focus:ring-2 focus:ring-accent/30"
          />
          {q && (
            <button onClick={() => setQ("")} className="absolute right-3 top-1/2 -translate-y-1/2 text-muted" aria-label={t("common.close")}>
              <X className="size-4" />
            </button>
          )}
        </div>
        <Segmented
          value={filter}
          onChange={setFilter}
          className="w-full [&>button]:flex-1"
          options={[
            { value: "all", label: t("chats.all") },
            { value: "unread", label: t("chats.unread") },
            { value: "person", label: t("chats.people") },
            { value: "group", label: t("chats.groups") },
          ]}
        />
      </div>
      {filter === "group" && !dq && <GroupSuggestions />}
      <div className="min-h-0 flex-1">
        {chats.isLoading ? (
          <div className="grid h-40 place-items-center"><Spinner /></div>
        ) : chats.isError ? (
          <Empty icon={<MessagesSquare />} title={t("common.error")} hint={(chats.error as Error).message}
            action={<Button size="sm" onClick={() => chats.refetch()}>{t("common.retry")}</Button>} />
        ) : items.length === 0 ? (
          <Empty icon={<MessagesSquare />} title={t("chats.empty")} hint={dq ? undefined : t("chats.emptyHint")} />
        ) : (
          <Virtuoso
            data={items}
            scrollerRef={(el) => { scroller.current = el as HTMLElement | null; }}
            initialTopMostItemIndex={start}
            isScrolling={(on) => { if (!on) remember(); }}
            computeItemKey={(_, c) => c.id}
            itemContent={(_, c) => <ChatRow c={c} active={params.chatId === c.id} />}
            increaseViewportBy={400}
            className="pb-[env(safe-area-inset-bottom)]"
          />
        )}
      </div>
    </div>
  );
}
