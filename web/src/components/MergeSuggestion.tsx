import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, Merge } from "lucide-react";
import { toast } from "sonner";
import { api, type Person, type StreamPage } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Avatar, Button, Dialog, ServiceBadge } from "@/components/ui";

export interface Suggestion { name: string; why: string[]; people: Person[] }

export const WHY: Record<string, string> = { contact: "people.whyContact", book: "people.whyBook", name: "people.whyName", similar: "people.whySimilar" };

/** One suggestion: its people side by side, a little of each one's history, and the choice of who is
 * one. Those left out are recorded as not the same as the ones merged, so they are not suggested again. */
export function MergeSuggestion({ s, onClose }: { s: Suggestion | null; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [chosen, setChosen] = useState<number[]>([]);
  useEffect(() => setChosen(s ? s.people.map((p) => p.id) : []), [s]);
  const done = () => { qc.invalidateQueries(); toast.success(t("common.done")); onClose(); };
  const merge = useMutation({
    mutationFn: async () => {
      const [into, ...others] = chosen;
      for (const other of others) await api.post(`/api/people/${into}/merge`, { other });
      for (const left of s!.people.filter((p) => !chosen.includes(p.id)))
        await api.post("/api/people/suggestions/dismiss", { people: [into, left.id] });
    },
    onSuccess: done,
    onError: (e: Error) => toast.error(e.message),
  });
  const none = useMutation({
    mutationFn: () => api.post("/api/people/suggestions/dismiss", { people: s!.people.map((p) => p.id) }),
    onSuccess: done,
    onError: (e: Error) => toast.error(e.message),
  });
  const toggle = (id: number) => setChosen((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]));
  return (
    <Dialog open={!!s} onOpenChange={(o) => !o && onClose()} wide title={s?.name}
      description={s && `${s.why.map((w) => t(WHY[w] ?? w)).join(" · ")}. ${t("people.chooseSame")}`}>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {s?.people.map((p) => <Candidate key={p.id} p={p} on={chosen.includes(p.id)} toggle={() => toggle(p.id)} />)}
      </div>
      <div className="flex items-center gap-2 border-t border-line px-4 py-3">
        <Button size="sm" variant="ghost" onClick={() => none.mutate()} loading={none.isPending} data-none-same>{t("people.noneSame")}</Button>
        <Button size="sm" variant="primary" className="ml-auto" disabled={chosen.length < 2} loading={merge.isPending}
          onClick={() => merge.mutate()} data-merge-chosen>
          <Merge className="size-4" />{t("people.mergeChosen", { count: chosen.length })}
        </Button>
      </div>
    </Dialog>
  );
}

function Candidate({ p, on, toggle }: { p: Person; on: boolean; toggle: () => void }) {
  const { t } = useTranslation();
  const recent = useQuery({     // a little of their history: the latest messages of their chat
    queryKey: ["chat", `p${p.id}`, "recent"],
    queryFn: () => api.get<StreamPage>(`/api/chats/p${p.id}/stream?limit=4`),
    retry: false,
  });
  const lines = (recent.data?.items ?? []).filter((i) => i.type === "message" && i.text);
  return (
    <div className={cn("rounded-2xl border p-3 transition-colors", on ? "border-accent/60 bg-accent/5" : "border-line")} data-candidate>
      <div className="flex items-start gap-3">
        <button onClick={toggle} aria-pressed={on} aria-label={t("people.sameOne")} title={t("people.sameOne")}
          className={cn("mt-1 grid size-6 shrink-0 place-items-center rounded-md border", on ? "border-accent bg-accent text-white" : "border-line")}>
          {on && <Check className="size-4" />}
        </button>
        <Avatar name={p.name} src={p.avatar ? `/api/avatar/${p.id}` : null} size={40} />
        <div className="min-w-0 flex-1">
          <Link to="/people/$personId" params={{ personId: String(p.id) }} className="font-medium hover:underline">{p.name}</Link>
          <div className="mt-0.5 text-xs text-muted">
            {t("people.statsLine", { messages: number(p.stats.messages), calls: number(p.stats.calls) })}
            {p.stats.first && ` · ${dateOnly(p.stats.first)} – ${dateOnly(p.stats.last)}`}
            {p.groups.length > 0 && ` · ${t("people.inGroups", { count: p.groups.length })}`}
          </div>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {p.handles.map((h) => (
              <span key={h.address_id} className="flex items-center gap-1 rounded-full bg-panel-2 px-2 py-0.5 text-xs">
                {h.label}{h.service && <ServiceBadge id={h.service} />}
              </span>
            ))}
          </div>
          {lines.length > 0 && (
            <div className="mt-2 space-y-0.5 border-l-2 border-line pl-2 text-xs text-muted">
              {lines.map((m) => m.type === "message" && (
                <div key={m.id} className="truncate">
                  <span className="tabular-nums">{dateOnly(m.ts)}</span> {m.outgoing ? "→" : "←"} {m.text}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
