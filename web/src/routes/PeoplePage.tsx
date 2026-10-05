import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Search, Sparkles, Users, X } from "lucide-react";
import { api, qs, type Person } from "@/lib/api";
import { useDebounced } from "@/lib/hooks";
import { Avatar, Empty, LoadingBar, Spinner } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";

export function PeoplePage() {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const list = useInfiniteQuery({
    queryKey: ["people", dq],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<{ items: { id: number; name: string; handles: number }[]; total: number }>(`/api/people${qs({ q: dq, limit: 200, offset: pageParam })}`),
    getNextPageParam: (last, pages) => {
      const n = pages.reduce((a, p) => a + p.items.length, 0);
      return n < last.total ? n : undefined;
    },
  });
  const people = list.data?.pages.flatMap((p) => p.items) ?? [];
  const qc = useQueryClient();
  const sugg = useQuery({
    queryKey: ["people-suggestions"],
    queryFn: () => api.get<{ items: { name: string; why: string[]; people: Person[] }[] }>("/api/people/suggestions"),
  });
  const dismiss = useMutation({
    mutationFn: (people: number[]) => api.post("/api/people/suggestions/dismiss", { people }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["people-suggestions"] }),
  });
  const WHY: Record<string, string> = { contact: "people.whyContact", book: "people.whyBook", name: "people.whyName" };
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("people.title")} subtitle={list.data ? `${list.data.pages[0].total}` : undefined} />
      <div className="bg-panel px-4 pb-3 md:px-6">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted" />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("people.search")}
            className="h-10 w-full rounded-full bg-panel-2 pl-9 pr-4 text-sm outline-none placeholder:text-muted focus:ring-2 focus:ring-accent/30" />
        </div>
      </div>
      {!dq && (sugg.data?.items.length ?? 0) > 0 && (
        <div className="border-b border-line bg-panel px-4 pb-3 md:px-6">
          <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted"><Sparkles className="size-3.5" />{t("people.suggestions")}</div>
          <div className="flex gap-2 overflow-x-auto pb-1">
            {sugg.data!.items.map((s) => (
              <div key={s.people.map((p) => p.id).join()} className="flex shrink-0 items-center rounded-2xl border border-line text-sm">
                <Link to="/people/$personId" params={{ personId: String(s.people[0].id) }} className="flex items-center gap-2 rounded-l-2xl py-2 pl-3 pr-2 hover:bg-panel-2">
                  <Avatar name={s.name} size={28} />
                  <span>
                    {s.name} <span className="text-xs text-muted">×{s.people.length}</span>
                    <span className="block text-[11px] text-muted">{s.why.map((w) => t(WHY[w] ?? w)).join(" · ")}</span>
                  </span>
                </Link>
                <button className="self-stretch rounded-r-2xl px-2 text-muted hover:bg-panel-2 hover:text-fg" title={t("people.notSame")}
                  aria-label={t("people.notSame")} onClick={() => dismiss.mutate(s.people.map((p) => p.id))}><X className="size-4" /></button>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={list.isFetching} />
        {list.isLoading ? <div className="grid h-40 place-items-center"><Spinner /></div> : !people.length ? (
          <Empty icon={<Users />} title={t("common.none")} />
        ) : (
          <Virtuoso data={people} endReached={() => list.hasNextPage && !list.isFetchingNextPage && list.fetchNextPage()}
            components={{ Footer: () => (list.isFetchingNextPage ? <div className="flex justify-center py-4"><Spinner /></div> : null) }}
            itemContent={(_, p) => (
            <Link to="/people/$personId" params={{ personId: String(p.id) }} className="mx-auto flex max-w-3xl items-center gap-3 px-4 py-2 hover:bg-panel-2 md:px-6">
              <Avatar name={p.name} size={40} />
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {p.handles > 1 && <span className="text-xs text-muted">{p.handles}</span>}
            </Link>
          )} />
        )}
      </div>
    </div>
  );
}
