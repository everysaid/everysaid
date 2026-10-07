import { useEffect, useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Check, Merge, Sparkles, Undo2, X } from "lucide-react";
import { toast } from "sonner";
import { api, type Person } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Avatar, Button, Dialog, Empty, LoadingBar, Segmented, ServiceBadge, Spinner } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";
import { WHY, type Suggestion } from "@/components/MergeSuggestion";

type Recent = { ts: number; outgoing: boolean; text: string };
type Candidate = Person & { recent: Recent[] };
type Item = Omit<Suggestion, "people"> & { people: Candidate[]; key: string };
type Choice = { chosen: number[]; apart: boolean };     // who is one; or the whole suggestion not one
type Apart = { a: { id: number; name: string }; b: { id: number; name: string }; at: number };

const activity = (p: Person) => p.stats.messages + p.stats.calls;

/** Every merge suggestion on one page, to decide many at once: ticked unless only their names sound
 * alike; untick who is not the same, or a whole suggestion; then apply them all together. Pairs said
 * not to be one are listed apart, each can be suggested again. */
export function MergeReviewPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"suggestions" | "apart">("suggestions");
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("people.alikeTitle")} back="/people" actions={
        <Segmented value={tab} onChange={setTab} options={[
          { value: "suggestions", label: t("people.tabSuggestions") },
          { value: "apart", label: t("people.tabApart") },
        ]} />
      } />
      {tab === "suggestions" ? <Suggestions /> : <ApartList />}
    </div>
  );
}

function Suggestions() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const res = useQuery({
    queryKey: ["people-suggestions", "all"],
    queryFn: () => api.get<{ items: Suggestion[] }>("/api/people/suggestions?limit=2000&recent=true"),
  });
  const items: Item[] = useMemo(() => (res.data?.items ?? []).map((s) => ({
    ...s, people: s.people as Candidate[], key: s.people.map((p) => p.id).join("-"),
  })), [res.data]);
  const [choice, setChoice] = useState<Record<string, Choice>>({});
  useEffect(() => {          // as they come: ticked, unless only their names sound alike
    setChoice(Object.fromEntries(items.map((s) => [s.key, {
      chosen: s.why.every((w) => w === "similar") ? [] : s.people.map((p) => p.id), apart: false,
    }])));
  }, [items]);
  const [why, setWhy] = useState("all");
  const shown = why === "all" ? items : items.filter((s) => s.why.includes(why));
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const s of items) for (const w of s.why) c[w] = (c[w] ?? 0) + 1;
    return c;
  }, [items]);

  // what applying does: the ticked of each suggestion become one (the busiest keeps its page), the
  // unticked are not one with them; a suggestion turned down: none of its people are one
  const plan = useMemo(() => {
    const merge: number[][] = [];
    const apart: number[][] = [];
    for (const s of items) {
      const c = choice[s.key];
      if (!c) continue;
      if (c.apart) {
        s.people.forEach((a, i) => s.people.slice(i + 1).forEach((b) => apart.push([a.id, b.id])));
      } else if (c.chosen.length >= 2) {
        const one = s.people.filter((p) => c.chosen.includes(p.id)).sort((a, b) => activity(b) - activity(a)).map((p) => p.id);
        merge.push(one);
        for (const p of s.people) if (!c.chosen.includes(p.id)) apart.push([one[0], p.id]);
      }
    }
    return { merge, apart };
  }, [items, choice]);
  const [confirm, setConfirm] = useState(false);
  const apply = useMutation({
    mutationFn: () => api.post<{ merged: number; apart: number }>("/api/people/suggestions/apply", plan),
    onSuccess: (r) => {
      setConfirm(false);
      qc.invalidateQueries();
      toast.success(t("people.applied", { merged: r.merged, apart: r.apart }));
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const set = (key: string, c: Choice) => setChoice((all) => ({ ...all, [key]: c }));
  const nothing = !plan.merge.length && !plan.apart.length;

  if (res.isLoading) return <div className="grid h-40 place-items-center"><Spinner /></div>;
  if (!items.length) return <Empty icon={<Sparkles />} title={t("people.noSuggestions")} />;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2 bg-panel px-4 pb-3 md:px-6">
        {["all", "contact", "book", "name", "similar"].filter((w) => w === "all" || counts[w]).map((w) => (
          <button key={w} onClick={() => setWhy(w)} aria-pressed={why === w}
            className={cn("rounded-full border px-3 py-1 text-xs", why === w ? "border-accent bg-accent/10 text-accent" : "border-line text-muted hover:bg-panel-2")}>
            {w === "all" ? t("people.whyAll") : t(WHY[w])} <span className="tabular-nums">{w === "all" ? items.length : counts[w]}</span>
          </button>
        ))}
        <Button size="sm" variant="primary" className="ml-auto" disabled={nothing} onClick={() => setConfirm(true)} data-apply>
          <Check className="size-4" />{t("people.apply")}
        </Button>
      </div>
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={res.isFetching} />
        <Virtuoso data={shown} computeItemKey={(_, s) => s.key} increaseViewportBy={600}
          itemContent={(_, s) => <Row s={s} c={choice[s.key] ?? { chosen: [], apart: false }} set={(c) => set(s.key, c)} />} />
      </div>
      <Dialog open={confirm} onOpenChange={setConfirm} title={t("people.apply")}>
        <div className="space-y-2 p-5 text-sm">
          <p>{t("people.planMerge", { count: plan.merge.length, people: plan.merge.reduce((n, g) => n + g.length, 0) })}</p>
          <p>{t("people.planApart", { count: plan.apart.length })}</p>
          <p className="text-xs text-muted">{t("people.planHint")}</p>
        </div>
        <div className="flex justify-end gap-2 border-t border-line px-4 py-3">
          <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>{t("common.cancel")}</Button>
          <Button size="sm" variant="primary" loading={apply.isPending} onClick={() => apply.mutate()} data-apply-confirm>{t("people.apply")}</Button>
        </div>
      </Dialog>
    </>
  );
}

function Row({ s, c, set }: { s: Item; c: Choice; set: (c: Choice) => void }) {
  const { t } = useTranslation();
  const all = s.people.map((p) => p.id);
  const whole = !c.apart && c.chosen.length === all.length;
  const toggle = (id: number) => set({ apart: false, chosen: c.chosen.includes(id) ? c.chosen.filter((x) => x !== id) : [...c.chosen, id] });
  return (
    <div className={cn("mx-auto max-w-5xl border-b border-line px-4 py-3 md:px-6", c.apart && "opacity-60")} data-alike>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="font-medium">{s.name}</span>
        <span className="text-xs text-muted">{s.why.map((w) => t(WHY[w] ?? w)).join(" · ")}</span>
        <span className="ml-auto flex gap-1">
          <Button size="sm" variant={whole ? "primary" : "ghost"} onClick={() => set({ apart: false, chosen: all })} data-same>
            <Merge className="size-4" />{t("people.sameAll")}
          </Button>
          <Button size="sm" variant={c.apart ? "primary" : "ghost"} onClick={() => set({ apart: !c.apart, chosen: [] })} data-apart>
            <X className="size-4" />{t("people.apartAll")}
          </Button>
        </span>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {s.people.map((p) => <Card key={p.id} p={p} on={!c.apart && c.chosen.includes(p.id)} toggle={() => toggle(p.id)} />)}
      </div>
    </div>
  );
}

function Card({ p, on, toggle }: { p: Candidate; on: boolean; toggle: () => void }) {
  const { t } = useTranslation();
  return (
    <div role="button" tabIndex={0} aria-pressed={on} onClick={toggle} onKeyDown={(e) => (e.key === " " || e.key === "Enter") && (e.preventDefault(), toggle())}
      className={cn("min-w-0 cursor-pointer rounded-2xl border p-3 text-left transition-colors", on ? "border-accent/60 bg-accent/5" : "border-line hover:bg-panel-2")}
      data-candidate>
      <div className="flex items-center gap-2">
        <span className={cn("grid size-5 shrink-0 place-items-center rounded-md border", on ? "border-accent bg-accent text-white" : "border-line")}>
          {on && <Check className="size-3.5" />}
        </span>
        <Avatar name={p.name} src={p.avatar ? `/api/avatar/${p.id}` : null} size={32} />
        <Link to="/people/$personId" params={{ personId: String(p.id) }} onClick={(e) => e.stopPropagation()}
          className="min-w-0 truncate text-sm font-medium hover:underline">{p.name}</Link>
      </div>
      <div className="mt-1 text-xs text-muted">
        {t("people.statsLine", { messages: number(p.stats.messages), calls: number(p.stats.calls) })}
        {p.stats.first && ` · ${dateOnly(p.stats.first)} – ${dateOnly(p.stats.last)}`}
        {p.groups.length > 0 && ` · ${t("people.inGroups", { count: p.groups.length })}`}
      </div>
      <div className="mt-1 flex flex-wrap gap-1">
        {p.handles.slice(0, 3).map((h) => (
          <span key={h.address_id} className="flex max-w-full items-center gap-1 truncate rounded-full bg-panel-2 px-2 py-0.5 text-[11px]">
            {h.label}{h.service && <ServiceBadge id={h.service} />}
          </span>
        ))}
        {p.handles.length > 3 && <span className="text-[11px] text-muted">+{p.handles.length - 3}</span>}
      </div>
      {p.recent.length > 0 && (
        <div className="mt-1.5 space-y-0.5 border-l-2 border-line pl-2 text-[11px] text-muted">
          {p.recent.map((m) => (
            <div key={m.ts} className="truncate">{dateOnly(m.ts)} {m.outgoing ? "→" : "←"} {m.text}</div>
          ))}
        </div>
      )}
    </div>
  );
}

function ApartList() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const res = useQuery({ queryKey: ["people-apart"], queryFn: () => api.get<{ items: Apart[] }>("/api/people/apart") });
  const undo = useMutation({
    mutationFn: (x: Apart) => api.post("/api/people/apart/undo", { a: x.a.id, b: x.b.id }),
    onSuccess: () => { qc.invalidateQueries(); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  const items = res.data?.items ?? [];
  if (res.isLoading) return <div className="grid h-40 place-items-center"><Spinner /></div>;
  if (!items.length) return <Empty icon={<X />} title={t("people.apartEmpty")} />;
  return (
    <div className="relative min-h-0 flex-1 bg-panel">
      <p className="mx-auto max-w-3xl px-4 pb-2 text-xs text-muted md:px-6">{t("people.apartHint")}</p>
      <Virtuoso data={items} computeItemKey={(_, x) => `${x.a.id}-${x.b.id}`} itemContent={(_, x) => (
        <div className="mx-auto flex max-w-3xl items-center gap-2 px-4 py-2 text-sm md:px-6" data-apart-pair>
          <Link to="/people/$personId" params={{ personId: String(x.a.id) }} className="min-w-0 truncate hover:underline">{x.a.name}</Link>
          <span className="text-muted">≠</span>
          <Link to="/people/$personId" params={{ personId: String(x.b.id) }} className="min-w-0 truncate hover:underline">{x.b.name}</Link>
          <span className="ml-auto shrink-0 text-xs text-muted">{dateOnly(x.at * 1000)}</span>
          <Button size="sm" variant="ghost" onClick={() => undo.mutate(x)} data-undo-apart><Undo2 className="size-4" />{t("people.undoApart")}</Button>
        </div>
      )} />
    </div>
  );
}
