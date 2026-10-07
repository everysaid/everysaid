import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Info } from "lucide-react";
import { api, type Person } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { cn } from "@/lib/utils";
import { ServiceBadge, Spinner } from "./ui";

type Recent = { ts: number; outgoing: boolean; text: string };

/** "Who is this?", where people are chosen by name (merging): a button that opens, under the row,
 * their handles, how much they have and of when, and their latest messages. */
export function usePeek() {
  const [open, setOpen] = useState<number | null>(null);
  return {
    open,
    button: (id: number) => <PeekButton open={open === id} onClick={() => setOpen(open === id ? null : id)} />,
    panel: (id: number) => (open === id ? <PersonPeek id={id} /> : null),
  };
}

function PeekButton({ open, onClick }: { open: boolean; onClick: () => void }) {
  const { t } = useTranslation();
  return (
    <button type="button" onClick={onClick} aria-expanded={open} aria-label={t("people.peek")} title={t("people.peek")} data-peek
      className={cn("grid size-8 shrink-0 place-items-center rounded-full text-muted hover:bg-panel-2", open && "bg-accent/12 text-accent")}>
      <Info className="size-4" />
    </button>
  );
}

export function PersonPeek({ id }: { id: number }) {
  const { t } = useTranslation();
  const p = useQuery({ queryKey: ["person", id, "peek"], queryFn: () => api.get<Person & { recent: Recent[] }>(`/api/people/${id}?recent=3`) });
  if (!p.data) return <div className="flex justify-center py-3"><Spinner /></div>;
  const s = p.data.stats;
  return (
    <div className="mb-2 ml-12 space-y-2 rounded-xl bg-panel-2 px-3 py-2 text-xs" data-peek-panel>
      <div className="flex flex-wrap gap-1">
        {p.data.handles.map((h) => (
          <span key={h.address_id} className="flex max-w-full items-center gap-1 truncate rounded-full bg-panel px-2 py-0.5">
            {h.label}{h.service && <ServiceBadge id={h.service} />}
          </span>
        ))}
      </div>
      <div className="text-muted">
        {t("people.statsLine", { messages: number(s.messages), calls: number(s.calls) })}
        {s.first && ` · ${dateOnly(s.first)} – ${dateOnly(s.last)}`}
        {p.data.groups.length > 0 && ` · ${t("people.inGroups", { count: p.data.groups.length })}`}
        {p.data.contact && ` · ${t("people.contact")}: ${p.data.contact.name}`}
      </div>
      {p.data.recent.length > 0 && (
        <div className="space-y-0.5 border-l-2 border-line pl-2 text-muted">
          {p.data.recent.map((m) => <div key={m.ts} className="truncate">{dateOnly(m.ts)} {m.outgoing ? "→" : "←"} {m.text}</div>)}
        </div>
      )}
    </div>
  );
}
