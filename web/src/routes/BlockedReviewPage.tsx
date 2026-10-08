import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Check, ShieldBan, ShieldCheck, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Avatar, Button, Dialog, Empty, LoadingBar, Segmented, ServiceBadge, Spinner, Switch } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";
import type { Blocked, SpamCheck, SpamList } from "@/components/Spam";

type Choice = "spam" | "keep";
type Applied = { removed: number[]; kept: number; failed: { name: string; service?: string; error: string }[] };

const services = (ids: string[]) => ids.map((s) => service(s).name).join(", ");

/** Those blocked on a phone or a service, to decide many at once: each marked spam or not spam (none
 * marked at first: a removal deletes), then applied together after a dialog that says what goes.
 * Those said not to be spam are listed apart, each can be suggested again. */
export function BlockedReviewPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"suggestions" | "kept">("suggestions");
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("spam.pageTitle")} back="/people" actions={
        <Segmented value={tab} onChange={setTab} options={[
          { value: "suggestions", label: t("people.tabSuggestions") },
          { value: "kept", label: t("spam.notSpam") },
        ]} />
      } />
      {tab === "suggestions" ? <Suggestions /> : <KeptList />}
    </div>
  );
}

function Suggestions() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const res = useQuery({ queryKey: ["spam"], queryFn: () => api.get<SpamList>("/api/spam") });
  const items = useMemo(() => res.data?.suggestions ?? [], [res.data]);
  const [choice, setChoice] = useState<Record<number, Choice>>({});
  const remove = items.filter((b) => choice[b.person_id] === "spam").map((b) => b.person_id);
  const keep = items.filter((b) => choice[b.person_id] === "keep").map((b) => b.person_id);
  const allSpam = items.length > 0 && remove.length === items.length;
  const set = (pid: number, c: Choice) => setChoice((all) => {
    const next = { ...all };
    if (next[pid] === c) delete next[pid]; else next[pid] = c;
    return next;
  });

  // what the removal takes, said before: each one's own check, added up
  const [confirm, setConfirm] = useState(false);
  const checks = useQueries({
    queries: remove.map((pid) => ({
      queryKey: ["spam-check", pid], enabled: confirm,
      queryFn: () => api.get<SpamCheck>(`/api/people/${pid}/spam`),
    })),
  });
  const counted = checks.every((c) => c.data);
  const totals = useMemo(() => {
    const out = { messages: 0, calls: 0, files: 0, reportable: [] as string[] };
    for (const c of checks) {
      if (!c.data) continue;
      out.messages += c.data.messages;
      out.calls += c.data.calls;
      out.files += c.data.files;
      for (const s of c.data.reportable) if (!out.reportable.includes(s)) out.reportable.push(s);
    }
    return out;
  }, [checks]);
  const [report, setReport] = useState(true);

  const apply = useMutation({
    mutationFn: () => api.post<Applied>("/api/spam/apply", { remove, keep, report: report && totals.reportable.length > 0 }),
    onSuccess: (r) => {
      setConfirm(false);
      setChoice({});
      qc.invalidateQueries();
      toast.success(t("spam.applied", { removed: r.removed.length, kept: r.kept }));
      for (const f of r.failed) {
        toast.error(f.service ? `${f.name}: ${t("spam.reportFailed", { service: service(f.service).name, error: f.error })}`
          : t("spam.failed", { name: f.name, error: f.error }));
      }
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (res.isLoading) return <div className="grid h-40 place-items-center"><Spinner /></div>;
  if (!items.length) return <Empty icon={<ShieldCheck />} title={t("spam.noBlocked")} />;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2 bg-panel px-4 pb-3 md:px-6">
        <Button size="sm" variant={allSpam ? "primary" : "ghost"} data-all-spam
          onClick={() => setChoice(allSpam ? {} : Object.fromEntries(items.map((b) => [b.person_id, "spam" as Choice])))}>
          <ShieldBan className="size-4" />{t("spam.allSpam")}
        </Button>
        <Button size="sm" variant="primary" className="ml-auto" disabled={!remove.length && !keep.length} onClick={() => setConfirm(true)} data-apply>
          <Check className="size-4" />{t("people.apply")}
        </Button>
      </div>
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={res.isFetching} />
        <Virtuoso data={items} computeItemKey={(_, b) => b.person_id} increaseViewportBy={600}
          itemContent={(_, b) => <Row b={b} c={choice[b.person_id]} set={(c) => set(b.person_id, c)} />} />
      </div>
      <Dialog open={confirm} onOpenChange={setConfirm} title={t("people.apply")}>
        <div className="space-y-2 p-5 text-sm" data-spam-plan>
          {remove.length > 0 && (
            <>
              <p className="font-medium">{t("spam.planRemove", { count: remove.length })}</p>
              {counted ? (
                <p className="text-muted">{t("spam.goes")} {t("settings.nMessages", { count: totals.messages, n: number(totals.messages) })} · {t("settings.nCalls", { count: totals.calls, n: number(totals.calls) })} · {t("spam.nFiles", { count: totals.files, n: number(totals.files) })}</p>
              ) : <Spinner />}
              <p className="text-muted">{t("spam.planStays")}</p>
            </>
          )}
          {keep.length > 0 && <p>{t("spam.planKeep", { count: keep.length })}</p>}
          {totals.reportable.length > 0 && (
            <label className="flex items-center gap-3 rounded-2xl border border-line p-3">
              <span className="min-w-0 flex-1">
                <span className="block font-medium">{t("spam.report", { services: services(totals.reportable) })}</span>
                <span className="block text-xs text-muted">{t("spam.reportHint")}</span>
              </span>
              <Switch checked={report} onChange={setReport} label={t("spam.report", { services: services(totals.reportable) })} />
            </label>
          )}
          <p className="text-xs text-muted">{t("spam.planHint")}</p>
        </div>
        <div className="flex justify-end gap-2 border-t border-line px-4 py-3">
          <Button size="sm" variant="ghost" onClick={() => setConfirm(false)}>{t("common.cancel")}</Button>
          <Button size="sm" variant={remove.length ? "danger" : "primary"} disabled={!counted} loading={apply.isPending}
            onClick={() => apply.mutate()} data-apply-confirm>{t("people.apply")}</Button>
        </div>
      </Dialog>
    </>
  );
}

function Row({ b, c, set }: { b: Blocked; c: Choice | undefined; set: (c: Choice) => void }) {
  const { t } = useTranslation();
  const p = b.person;
  return (
    <div className={cn("mx-auto max-w-5xl border-b border-line px-4 py-3 md:px-6", c === "keep" && "opacity-60")} data-blocked>
      <div className="flex flex-wrap items-center gap-2">
        <Avatar name={p.name} src={p.avatar ? `/api/avatar/${p.id}` : null} size={36} />
        <div className="min-w-0 flex-1">
          <Link to="/people/$personId" params={{ personId: String(p.id) }} className="block truncate font-medium hover:underline">{p.name}</Link>
          <div className="text-xs text-muted">
            {t("spam.blockedOn", { where: b.where.map((w) => service(w).name).join(", ") })}
            {" · "}{t("people.statsLine", { messages: number(p.stats.messages), calls: number(p.stats.calls) })}
            {p.stats.first && ` · ${dateOnly(p.stats.first)} – ${dateOnly(p.stats.last)}`}
            {p.groups.length > 0 && ` · ${t("people.inGroups", { count: p.groups.length })}`}
          </div>
        </div>
        <span className="flex w-full justify-end gap-1 sm:w-auto">
          <Button size="sm" variant={c === "spam" ? "danger" : "ghost"} aria-pressed={c === "spam"} onClick={() => set("spam")} data-mark-spam>
            <ShieldBan className="size-4" />{t("spam.isSpam")}
          </Button>
          <Button size="sm" variant={c === "keep" ? "primary" : "ghost"} aria-pressed={c === "keep"} onClick={() => set("keep")} data-mark-keep>
            <ShieldCheck className="size-4" />{t("spam.notSpam")}
          </Button>
        </span>
      </div>
      <div className="mt-1.5 flex flex-wrap gap-1 pl-11">
        {p.handles.slice(0, 3).map((h) => (
          <span key={h.address_id} className="flex max-w-full items-center gap-1 truncate rounded-full bg-panel-2 px-2 py-0.5 text-[11px]">
            {h.label}{h.service && <ServiceBadge id={h.service} />}
          </span>
        ))}
        {p.handles.length > 3 && <span className="text-[11px] text-muted">+{p.handles.length - 3}</span>}
      </div>
      {p.recent.length > 0 && (
        <div className="ml-11 mt-1.5 space-y-0.5 border-l-2 border-line pl-2 text-[11px] text-muted">
          {p.recent.map((m) => (
            <div key={m.ts} className="truncate">{dateOnly(m.ts)} {m.outgoing ? "→" : "←"} {m.text}</div>
          ))}
        </div>
      )}
    </div>
  );
}

function KeptList() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const res = useQuery({ queryKey: ["spam"], queryFn: () => api.get<SpamList>("/api/spam") });
  const undo = useMutation({
    mutationFn: (pid: number) => api.post(`/api/people/${pid}/not-spam/undo`),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["spam"] }); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  const items = res.data?.kept ?? [];
  if (res.isLoading) return <div className="grid h-40 place-items-center"><Spinner /></div>;
  if (!items.length) return <Empty icon={<ShieldCheck />} title={t("spam.keptEmpty")} />;
  return (
    <div className="relative min-h-0 flex-1 bg-panel">
      <p className="mx-auto max-w-3xl px-4 pb-2 text-xs text-muted md:px-6">{t("spam.keptHint")}</p>
      <Virtuoso data={items} computeItemKey={(_, x) => x.person_id} itemContent={(_, x) => (
        <div className="mx-auto flex max-w-3xl items-center gap-2 px-4 py-2 text-sm md:px-6" data-kept>
          <Link to="/people/$personId" params={{ personId: String(x.person_id) }} className="min-w-0 truncate hover:underline">{x.name}</Link>
          <span className="ml-auto shrink-0 text-xs text-muted">{dateOnly(x.at * 1000)}</span>
          <Button size="sm" variant="ghost" onClick={() => undo.mutate(x.person_id)} data-undo-kept><Undo2 className="size-4" />{t("people.undoApart")}</Button>
        </div>
      )} />
    </div>
  );
}
