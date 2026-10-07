import { useEffect, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Archive, Check, ChevronDown, Search, Users } from "lucide-react";
import { cn } from "@/lib/utils";
import { api, qs, type ChatDetail, type StreamItem } from "@/lib/api";
import { fullDate } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { SERVICES } from "@/lib/services";
import { searchRoute } from "@/router";
import { Avatar, Empty, LoadingBar, Menu, MenuContent, MenuItem, MenuTrigger, MoreOnScroll, ServiceBadge, Spinner } from "@/components/ui";
import { ChatFilter } from "@/components/ChatFilter";
import { DateField } from "@/components/DateField";
import { CallLine, RichText } from "@/components/Message";
import { PageHeader } from "@/components/PageHeader";

interface Found { chat_id: string; title: string; type: string; count: number }

export function SearchPage() {
  const { t } = useTranslation();
  const search = searchRoute.useSearch();
  const navigate = useNavigate();
  const [q, setQ] = useState(search.q ?? "");
  const [service, setService] = useState(search.service ?? "");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [who, setWho] = useState<"" | "true" | "false">("");
  const [archived, setArchived] = useState(false);     // as in the chat list: the archived chats, or the others
  // as in an editor's search: match case (and accents), whole words; remembered on this device
  const [mode, setMode] = useState<{ case: boolean; whole: boolean }>(() => JSON.parse(localStorage.getItem("search-mode") ?? '{"case":false,"whole":false}'));
  const toggle = (k: "case" | "whole") => setMode((m) => {
    const next = { ...m, [k]: !m[k] };
    localStorage.setItem("search-mode", JSON.stringify(next));
    return next;
  });
  const dq = useDebounced(q.trim(), 300);

  useEffect(() => {
    navigate({ to: "/search", search: { q: dq || undefined, service: service || undefined, chat: search.chat }, replace: true });
  }, [dq, service]); // eslint-disable-line react-hooks/exhaustive-deps

  const toMs = (d: string) => (d ? new Date(d + "T00:00:00").getTime() : undefined);
  // no words, but dates: everything of those days, calls too, oldest first
  const browsing = !dq && !!(since || until);
  const res = useInfiniteQuery({
    queryKey: ["search", dq, service, search.chat, since, until, who, mode.case, mode.whole, archived],
    enabled: dq.length > 0 || browsing,
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<{ items: StreamItem[]; total: number; chats: Found[] | null }>(`/api/search${qs({
      q: dq, service, chat: search.chat, since: toMs(since), until: until ? toMs(until)! + 86400_000 : undefined,
      outgoing: who || undefined, limit: 40, offset: pageParam, case: mode.case || undefined, whole: mode.whole || undefined, archived,
    })}`),
    getNextPageParam: (last, pages) => {
      const n = pages.reduce((a, p) => a + p.items.length, 0);
      return n < last.total ? n : undefined;
    },
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  const total = res.data?.pages[0]?.total ?? 0;
  // where it was found: each person or group, with how many; one tap keeps only that one
  const [found, setFound] = useState<Found[]>([]);
  const firstChats = res.data?.pages[0]?.chats;
  useEffect(() => { if (firstChats) setFound(firstChats); }, [firstChats]);
  useEffect(() => { if (!dq && !browsing) setFound([]); }, [dq, browsing]);
  const picked = found.find((c) => c.chat_id === search.chat);
  // the chat narrowed to, even when nothing is found in it (the others are still offered)
  const narrowed = useQuery({ queryKey: ["chat", search.chat], queryFn: () => api.get<ChatDetail>(`/api/chats/${search.chat}`), enabled: !!search.chat && !picked });
  const shown = picked ?? (search.chat && narrowed.data ? { chat_id: search.chat, title: narrowed.data.title, type: narrowed.data.type, count: 0 } : undefined);
  const only = (id?: string) => navigate({ to: "/search", search: { q: dq || undefined, chat: id } });

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("nav.search")} back={search.chat ? `/chat/${search.chat}` : undefined} />
      <div className="space-y-3 border-b border-line bg-panel px-4 pb-3 md:px-6">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3.5 top-1/2 size-5 -translate-y-1/2 text-muted" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t("search.placeholder")}
            className="h-12 w-full rounded-2xl bg-panel-2 pl-11 pr-24 text-base outline-none placeholder:text-muted focus:ring-2 focus:ring-accent/30"
          />
          <div className="absolute right-2 top-1/2 flex -translate-y-1/2 gap-1">
            {([["case", "Aa", "search.matchCase"], ["whole", "W", "search.wholeWords"]] as const).map(([k, label, tip]) => (
              <button key={k} type="button" data-mode={k} aria-pressed={mode[k]} title={t(tip)} aria-label={t(tip)} onClick={() => toggle(k)}
                className={cn("grid h-8 min-w-8 place-items-center rounded-lg px-1.5 font-mono text-sm font-semibold transition",
                  mode[k] ? "bg-accent text-accent-fg" : "text-muted hover:bg-panel hover:text-fg", k === "whole" && "underline decoration-2 underline-offset-2")}>
                {label}
              </button>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          {/* where it was found, to narrow to one: a choice among them, or the one chat it is in */}
          {found.length > 1 ? (
            <Menu>
              <MenuTrigger asChild>
                <button data-found className="flex max-w-full items-center gap-2 rounded-xl border border-line bg-panel py-1.5 pl-1.5 pr-3 text-sm hover:border-accent/40 data-[state=open]:border-accent">
                  {shown ? <Avatar name={shown.title} size={24} group={shown.type === "group"} /> : <span className="grid size-6 place-items-center rounded-full bg-panel-2"><Users className="size-3.5 text-muted" /></span>}
                  <span className="truncate font-medium">{shown ? shown.title : t("search.everywhere")}</span>
                  <span className="text-xs text-muted">{shown ? shown.count : found.reduce((a, c) => a + c.count, 0)}</span>
                  <ChevronDown className="size-4 shrink-0 text-muted" />
                </button>
              </MenuTrigger>
              <MenuContent align="start" className="max-h-80 w-72 overflow-y-auto">
                <MenuItem onSelect={() => only(undefined)} icon={<Users />}>
                  <span className="flex-1">{t("search.everywhere")}</span>
                  {!search.chat && <Check className="size-4 text-accent" />}
                </MenuItem>
                {found.map((c) => (
                  <MenuItem key={c.chat_id} onSelect={() => only(c.chat_id)}>
                    <Avatar name={c.title} size={24} group={c.type === "group"} />
                    <span className="min-w-0 flex-1 truncate">{c.title}</span>
                    <span className="text-xs tabular-nums text-muted">{c.count}</span>
                    {search.chat === c.chat_id && <Check className="size-4 text-accent" />}
                  </MenuItem>
                ))}
              </MenuContent>
            </Menu>
          ) : <ChatFilter chat={search.chat} onClear={() => only(undefined)} />}
          <select value={service} onChange={(e) => setService(e.target.value)} className="h-9 rounded-xl border border-line bg-panel px-2">
            <option value="">{t("search.anyService")}</option>
            {Object.entries(SERVICES).filter(([, s]) => s.messages || browsing).map(([k, s]) => <option key={k} value={k}>{s.name}</option>)}
          </select>
          <select value={who} onChange={(e) => setWho(e.target.value as "" | "true" | "false")} className="h-9 rounded-xl border border-line bg-panel px-2">
            <option value="">{t("common.all")}</option>
            <option value="true">{t("search.mine")}</option>
            <option value="false">{t("search.theirs")}</option>
          </select>
          <label className="flex items-center gap-1 text-muted">{t("search.from")}<DateField value={since} onChange={setSince} max={until || undefined} /></label>
          <label className="flex items-center gap-1 text-muted">{t("search.to")}<DateField value={until} onChange={setUntil} min={since || undefined} /></label>
          {!search.chat && (      // (one chat asked for is searched whatever it is)
            <button data-search-archived aria-pressed={archived} onClick={() => setArchived(!archived)}
              aria-label={t("chats.showArchived")} title={t("chats.showArchived")}
              className={cn("ml-auto grid size-9 place-items-center rounded-full text-muted transition hover:bg-panel-2", archived && "bg-accent/12 text-accent")}>
              <Archive className="size-4" />
            </button>
          )}
        </div>
      </div>
      <div className="relative min-h-0 flex-1 overflow-y-auto">
        <LoadingBar active={res.isFetching} />
        {!dq && !browsing ? (
          <Empty icon={<Search />} title={t("search.placeholder")} hint={t("search.hint")} />
        ) : res.isLoading ? (
          <div className="grid h-40 place-items-center"><Spinner /></div>
        ) : items.length === 0 ? (
          <Empty icon={<Search />} title={t("search.none")} hint={t("search.hint")} />
        ) : (
          <div data-results className="mx-auto max-w-3xl space-y-2 p-4 md:p-6">
            <div className="px-1 text-sm text-muted">{t("search.results", { count: total })}</div>
            {items.map((m) => (
              <Link
                key={m.cursor}
                to="/chat/$chatId"
                params={{ chatId: m.chat_id ?? "" }}
                search={m.type === "message" ? { m: m.id } : { ts: m.ts + 1 }}
                className="block rounded-2xl border border-line bg-panel p-4 transition-colors hover:border-accent/40"
              >
                <div className="mb-1.5 flex items-center gap-2 text-sm">
                  <span className="truncate font-semibold">{m.chat_title}</span>
                  <ServiceBadge id={m.service} />
                  <span className="ml-auto shrink-0 text-xs text-muted">{fullDate(m.ts)}</span>
                </div>
                {m.type === "call" ? <div data-call className="-mx-4 -mb-1.5"><CallLine c={m} /></div> : (
                  <div className="text-sm">
                    <span className="mr-1 font-medium text-muted">{m.outgoing ? t("common.me") : m.sender || ""}{(m.outgoing || m.sender) && ":"}</span>
                    <RichText text={m.text ?? (m.kind !== "text" ? `[${t(`kind.${m.kind}`, { defaultValue: m.kind })}]` : "")} marks={m.highlight} />
                  </div>
                )}
              </Link>
            ))}
            <MoreOnScroll hasMore={!!res.hasNextPage} loading={res.isFetchingNextPage} onMore={res.fetchNextPage} />
          </div>
        )}
      </div>
    </div>
  );
}
