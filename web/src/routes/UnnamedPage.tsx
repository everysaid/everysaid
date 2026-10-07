import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Check, Merge, UserRoundX } from "lucide-react";
import { toast } from "sonner";
import { api, qs, type Person } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { Avatar, Button, Dialog, Empty, Input, LoadingBar, ServiceBadge, Spinner, Switch } from "@/components/ui";
import { GuessLine, labelName, Votes } from "@/components/Labels";
import { PageHeader } from "@/components/PageHeader";

type Recent = { ts: number; outgoing: boolean; text: string };
type Unnamed = Person & { recent: Recent[] };
type Done = { name: string; merged: boolean };

/** The people no source names, those with the most messages first: a little of each one's history,
 * and a name for them, or the person they are. What is done stays in place, marked, so the list
 * does not move under the hand; the next visit shows only who is left. */
export function UnnamedPage() {
  const { t } = useTranslation();
  const [guessed, setGuessed] = useState(false);
  const list = useInfiniteQuery({
    queryKey: ["people-unnamed", guessed],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<{ items: Unnamed[]; total: number }>(`/api/people/unnamed${qs({ limit: 50, offset: pageParam, guessed: guessed || undefined })}`),
    getNextPageParam: (last, pages) => {
      const n = pages.reduce((a, p) => a + p.items.length, 0);
      return n < last.total ? n : undefined;
    },
    staleTime: Infinity,          // not refetched while naming: rows stay where they are
  });
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const [done, setDone] = useState<Record<number, Done>>({});
  const [merging, setMerging] = useState<Unnamed | null>(null);
  const total = list.data?.pages[0].total;
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("people.unnamedTitle")} back="/people"
        subtitle={total !== undefined ? t("people.unnamedLeft", { count: total - Object.keys(done).length }) : undefined} />
      <div className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-x-4 gap-y-2 bg-panel px-4 pb-2 md:px-6">
        <p className="min-w-0 flex-1 text-xs text-muted">{t("people.unnamedHint")}</p>
        <label className="flex items-center gap-2 text-sm"><Switch checked={guessed} onChange={setGuessed} label={t("people.guessedFirst")} />{t("people.guessedFirst")}</label>
      </div>
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={list.isFetching} />
        {list.isLoading ? <div className="grid h-40 place-items-center"><Spinner /></div> : !items.length ? (
          <Empty icon={<UserRoundX />} title={t("people.unnamedNone")} />
        ) : (
          <Virtuoso data={items} computeItemKey={(_, p) => p.id} increaseViewportBy={600}
            endReached={() => list.hasNextPage && !list.isFetchingNextPage && list.fetchNextPage()}
            components={{ Footer: () => (list.isFetchingNextPage ? <div className="flex justify-center py-4"><Spinner /></div> : null) }}
            itemContent={(_, p) => <Row p={p} done={done[p.id]} onDone={(d) => setDone((all) => ({ ...all, [p.id]: d }))} onMerge={() => setMerging(p)} />} />
        )}
      </div>
      <SameAs p={merging} onClose={() => setMerging(null)} onDone={(name) => merging && setDone((all) => ({ ...all, [merging.id]: { name, merged: true } }))} />
    </div>
  );
}

/** Everything changed but this list: names show at once elsewhere, this one keeps its rows. */
function useRefreshOthers() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "people-unnamed" });
}

function Row({ p, done, onDone, onMerge }: { p: Unnamed; done?: Done; onDone: (d: Done) => void; onMerge: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [guessGone, setGuessGone] = useState(false);      // turned down: gone from the row
  const refresh = useRefreshOthers();
  const save = useMutation({
    mutationFn: () => api.patch(`/api/people/${p.id}`, { name: name.trim() }),
    onSuccess: () => { onDone({ name: name.trim(), merged: false }); refresh(); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <div className="mx-auto max-w-3xl border-b border-line px-4 py-3 md:px-6" data-unnamed={p.id}>
      <div className="flex items-start gap-3">
        <Avatar name={done?.name ?? p.name} size={40} />
        <div className="min-w-0 flex-1">
          <Link to="/people/$personId" params={{ personId: String(p.id) }} className="text-sm font-medium hover:underline">{p.name}</Link>
          <div className="mt-0.5 text-xs text-muted">
            {t("people.statsLine", { messages: number(p.stats.messages), calls: number(p.stats.calls) })}
            {p.stats.first && ` · ${dateOnly(p.stats.first)} – ${dateOnly(p.stats.last)}`}
            {p.groups.length > 0 && ` · ${t("people.inGroups", { count: p.groups.length })}`}
          </div>
          <div className="mt-1 flex flex-wrap gap-1">
            {p.handles.slice(0, 4).map((h) => (
              <span key={h.address_id} className="flex max-w-full items-center gap-1 truncate rounded-full bg-panel-2 px-2 py-0.5 text-[11px]">
                {h.label}{h.service && <ServiceBadge id={h.service} />}
              </span>
            ))}
            {p.handles.length > 4 && <span className="text-[11px] text-muted">+{p.handles.length - 4}</span>}
          </div>
          {p.recent.length > 0 && (
            <div className="mt-1.5 space-y-0.5 border-l-2 border-line pl-2 text-xs text-muted">
              {p.recent.map((m) => <div key={m.ts} className="truncate">{dateOnly(m.ts)} {m.outgoing ? "→" : "←"} {m.text}</div>)}
            </div>
          )}
          {!!p.labels?.length && (
            <div className="mt-1.5 flex flex-wrap gap-1.5 text-[11px]" data-unnamed-labels>
              {p.labels.map((l) => (
                <span key={l.id} className={l.state === "yes" ? "rounded-full bg-accent/15 px-2 py-0.5" : "rounded-full border border-dashed border-line px-2 py-0.5 text-muted"}>
                  {labelName(t, l)}{l.state !== "yes" && <> <Votes votes={l.votes} models={l.models} /></>}
                </span>
              ))}
            </div>
          )}
          {!done && p.guess && !guessGone && <GuessLine personId={p.id} guess={p.guess} onPick={setName}
            onDone={(n) => { if (n) { onDone({ name: n, merged: false }); refresh(); } else setGuessGone(true); }} />}
          {done ? (
            <div className="mt-2 flex items-center gap-1.5 text-sm text-accent" data-unnamed-done>
              <Check className="size-4" />{done.merged ? t("people.mergedInto", { name: done.name }) : done.name}
            </div>
          ) : (
            <form className="mt-2 flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (name.trim()) save.mutate(); }}>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("people.giveName")} className="h-9 min-w-40 flex-1" data-unnamed-name />
              <Button size="sm" variant="primary" type="submit" disabled={!name.trim()} loading={save.isPending}>{t("common.save")}</Button>
              <Button size="sm" variant="ghost" type="button" onClick={onMerge} data-unnamed-same><Merge className="size-4" />{t("people.sameAs")}</Button>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

/** "The same as…": a person found by name, who takes this one's handles and history. */
function SameAs({ p, onClose, onDone }: { p: Unnamed | null; onClose: () => void; onDone: (name: string) => void }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const refresh = useRefreshOthers();
  const res = useQuery({
    queryKey: ["people", "same-as", dq],
    enabled: !!p && dq.length > 1,
    queryFn: () => api.get<{ items: { id: number; name: string; handles: number }[] }>(`/api/people${qs({ q: dq, limit: 20 })}`),
  });
  const merge = useMutation({
    mutationFn: (into: { id: number; name: string }) => api.post(`/api/people/${into.id}/merge`, { other: p!.id }).then(() => into),
    onSuccess: (into) => { onDone(into.name); refresh(); setQ(""); onClose(); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={!!p} onOpenChange={(o) => { if (!o) { setQ(""); onClose(); } }} title={t("people.sameAs")}
      description={p ? t("people.sameAsHint", { name: p.name }) : undefined}>
      <div className="p-4">
        <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("people.search")} data-same-search />
        <div className="mt-3 divide-y divide-line">
          {res.data?.items.filter((x) => x.id !== p?.id).map((x) => (
            <div key={x.id} className="flex items-center gap-3 py-2">
              <Avatar name={x.name} size={36} />
              <span className="min-w-0 flex-1 truncate">{x.name}</span>
              <Button size="sm" variant="primary" onClick={() => merge.mutate(x)} loading={merge.isPending} data-same-pick>{t("people.mergeConfirm")}</Button>
            </div>
          ))}
        </div>
      </div>
    </Dialog>
  );
}
