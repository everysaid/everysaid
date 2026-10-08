import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Search, Sparkles, UserRoundX, Users, X } from "lucide-react";
import { api, qs } from "@/lib/api";
import { useDebounced } from "@/lib/hooks";
import { keepPeoplePlace, peoplePlace, type PeoplePlace } from "@/lib/memory";
import { Avatar, Empty, LoadingBar, Spinner } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";
import { MergeSuggestion, WHY, type Suggestion } from "@/components/MergeSuggestion";
import { SpamSuggestions } from "@/components/Spam";
import { labelName, useLabels } from "@/components/Labels";
import { cn } from "@/lib/utils";
import type { LabelKind } from "@/lib/api";

type Row = { id: number; name: string; handles: number;
  labels: { id: number; kind: LabelKind; key: string | null; name: string | null; state: "yes" | "suggested" }[] };

export function PeoplePage() {
  const { t } = useTranslation();
  const [was] = useState(peoplePlace);       // as it was left (this tab), coming back to it
  const [q, setQ] = useState(was?.q ?? "");
  const dq = useDebounced(q, 200);
  const [label, setLabel] = useState<number | undefined>(was?.label);
  const labels = useLabels();
  const used = (labels.data?.items ?? []).filter((l) => l.uses.yes + l.uses.suggested > 0 || l.id === label);
  const list = useInfiniteQuery({
    queryKey: ["people", dq, label],
    initialPageParam: 0,
    // coming back: the first page as long as what was loaded then, so the row left in view is there
    queryFn: ({ pageParam }) => api.get<{ items: Row[]; total: number }>(`/api/people${qs({
      q: dq, label, limit: pageParam === 0 && was && dq === was.q ? Math.max(200, was.loaded) : 200, offset: pageParam })}`),
    getNextPageParam: (last, pages) => {
      const n = pages.reduce((a, p) => a + p.items.length, 0);
      return n < last.total ? n : undefined;
    },
  });
  const people = list.data?.pages.flatMap((p) => p.items) ?? [];
  const scroller = useRef<HTMLElement | null>(null);
  const remember = useCallback(() => {
    const place: PeoplePlace = { q, label, loaded: people.length };
    const el = scroller.current;
    if (el) {
      const edge = el.getBoundingClientRect().top;
      const first = [...el.querySelectorAll<HTMLElement>("[data-person]")].find((r) => r.getBoundingClientRect().bottom > edge + 1);
      if (first) place.top = { id: Number(first.dataset.person), index: Number(first.dataset.index),
                               offset: Math.round(edge - first.getBoundingClientRect().top) };
    } else if (was?.q === q) place.top = was.top;
    keepPeoplePlace(place);
  }, [q, label, people.length]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(remember, [remember]);
  const start = useMemo(() => {
    if (!was?.top || dq !== was.q || label !== was.label) return 0;
    const found = people.findIndex((p) => p.id === was.top!.id);
    const i = found >= 0 ? found : Math.min(was.top.index, people.length - 1);     // gone (merged): about there
    return i >= 0 ? { index: i, align: "start" as const, offset: was!.top!.offset } : 0;
  }, [list.isSuccess]); // eslint-disable-line react-hooks/exhaustive-deps
  const qc = useQueryClient();
  const sugg = useQuery({
    queryKey: ["people-suggestions"],
    queryFn: () => api.get<{ items: Suggestion[] }>("/api/people/suggestions"),
  });
  const dismiss = useMutation({
    mutationFn: (people: number[]) => api.post("/api/people/suggestions/dismiss", { people }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["people-suggestions"] }),
  });
  const [open, setOpen] = useState<Suggestion | null>(null);
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("people.title")} subtitle={list.data ? `${list.data.pages[0].total}` : undefined} actions={
        <Link to="/people/unnamed" className="flex items-center gap-1.5 rounded-full px-3 py-1.5 text-sm text-accent hover:bg-panel-2" data-unnamed-link>
          <UserRoundX className="size-4" />{t("people.unnamedTitle")}
        </Link>
      } />
      <div className="bg-panel px-4 pb-3 md:px-6">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("people.search")}
            className="h-10 w-full rounded-full bg-panel-2 pl-9 pr-4 text-sm outline-none placeholder:text-muted focus:ring-2 focus:ring-accent/30" />
        </div>
        {used.length > 0 && (
          <div className="mt-2 flex gap-1.5 overflow-x-auto pb-0.5" data-label-filter>
            {used.map((l) => (
              <button key={l.id} type="button" onClick={() => setLabel(label === l.id ? undefined : l.id)} aria-pressed={label === l.id}
                data-filter={l.key ?? l.name}
                className={cn("shrink-0 rounded-full border px-3 py-1 text-xs", label === l.id ? "border-accent bg-accent text-accent-fg" : "border-line hover:bg-panel-2")}>
                {labelName(t, l)}
              </button>
            ))}
          </div>
        )}
      </div>
      {!dq && !label && (sugg.data?.items.length ?? 0) > 0 && (
        <div className="border-b border-line bg-panel px-4 pb-3 md:px-6">
          <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted"><Sparkles className="size-3.5" />{t("people.suggestions")}
            <Link to="/people/merge" className="ml-auto normal-case tracking-normal text-accent hover:underline" data-alike-all>{t("people.alikeAll")}</Link>
          </div>
          <div className="flex gap-2 overflow-x-auto pb-1">
            {sugg.data!.items.map((s) => (
              <div key={s.people.map((p) => p.id).join()} className="flex shrink-0 items-center rounded-2xl border border-line text-sm">
                <button onClick={() => setOpen(s)} data-suggestion className="flex items-center gap-2 rounded-l-2xl py-2 pl-3 pr-2 text-left hover:bg-panel-2">
                  <Avatar name={s.name} size={28} />
                  <span>
                    {s.name} <span className="text-xs text-muted">×{s.people.length}</span>
                    <span className="block text-[11px] text-muted">{s.why.map((w) => t(WHY[w] ?? w)).join(" · ")}</span>
                  </span>
                </button>
                <button className="self-stretch rounded-r-2xl px-2 text-muted hover:bg-panel-2 hover:text-fg" title={t("people.notSame")}
                  aria-label={t("people.notSame")} onClick={() => dismiss.mutate(s.people.map((p) => p.id))}><X className="size-4" /></button>
              </div>
            ))}
          </div>
        </div>
      )}
      {!dq && !label && <SpamSuggestions />}
      <MergeSuggestion s={open} onClose={() => setOpen(null)} />
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={list.isFetching} />
        {list.isLoading ? <div className="grid h-40 place-items-center"><Spinner /></div> : !people.length ? (
          <Empty icon={<Users />} title={t("common.none")} />
        ) : (
          <Virtuoso data={people} scrollerRef={(el) => { scroller.current = el as HTMLElement | null; }}
            initialTopMostItemIndex={start} isScrolling={(on) => { if (!on) remember(); }}
            computeItemKey={(_, p) => p.id} endReached={() => list.hasNextPage && !list.isFetchingNextPage && list.fetchNextPage()}
            components={{ Footer: () => (list.isFetchingNextPage ? <div className="flex justify-center py-4"><Spinner /></div> : null) }}
            itemContent={(i, p) => (
            <Link to="/people/$personId" params={{ personId: String(p.id) }} data-person={p.id} data-index={i} className="mx-auto flex max-w-3xl items-center gap-3 px-4 py-2 hover:bg-panel-2 md:px-6">
              <Avatar name={p.name} size={40} />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {p.labels.slice(0, 3).map((l) => (
                <span key={l.id} className={cn("hidden shrink-0 rounded-full px-2 py-0.5 text-[11px] sm:inline",
                  l.state === "yes" ? "bg-accent/12 text-fg" : "border border-dashed border-line text-muted")}>{labelName(t, l)}</span>
              ))}
              {p.handles > 1 && <span className="text-xs text-muted">{p.handles}</span>}
            </Link>
          )} />
        )}
      </div>
    </div>
  );
}
