import { useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ShieldBan, Undo2, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { service } from "@/lib/services";
import { Avatar, Button, Card, Dialog, ServiceBadge, Switch } from "@/components/ui";

/** What removing someone as spam takes (GET /api/people/{id}/spam). refused: why they may not be
 * removed ("" when they may): someone the user named, an address book lists, or the user. */
export interface SpamCheck {
  person_id: number; name: string; refused: "" | "me" | "named" | "contact";
  messages: number; calls: number; chats: number; files: number; groups: number;
  services: string[]; reportable: string[];
}

interface SpamResult { messages: number; calls: number; chats: number; reported: string[]; failed: { service: string; error: string }[] }

interface Blocked { person_id: number; name: string; where: string[] }

interface Removed { address_id: number; label: string; kind: string; service: string | null; name: string | null; at: number }

const REFUSED: Record<string, string> = { me: "spam.refused.me", named: "spam.refused.named", contact: "spam.refused.contact" };

const services = (ids: string[]) => ids.map((s) => service(s).name).join(", ");

/** The removal of someone as spam: what goes and what stays, said before; the services told too
 * where a source can, unless the user turns it off. */
export function SpamDialog({ personId, onClose, onDone }: { personId: number | null; onClose: () => void; onDone?: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const check = useQuery({
    queryKey: ["spam-check", personId],
    enabled: personId != null,
    queryFn: () => api.get<SpamCheck>(`/api/people/${personId}/spam`),
  });
  const [report, setReport] = useState(true);
  useEffect(() => setReport(true), [personId]);
  const c = check.data;
  const remove = useMutation({
    mutationFn: () => api.post<SpamResult>(`/api/people/${personId}/spam`, { report: report && (c?.reportable.length ?? 0) > 0 }),
    onSuccess: (r) => {
      qc.invalidateQueries();
      toast.success(t("spam.removed", { name: c?.name }));
      for (const f of r.failed) toast.error(t("spam.reportFailed", { service: service(f.service).name, error: f.error }));
      onClose();
      onDone?.();
    },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={personId != null} onOpenChange={(o) => !o && onClose()} title={t("spam.title", { name: c?.name ?? "" })}
      description={t("spam.hint")}>
      {c && (c.refused ? (
        <p className="text-sm" data-spam-refused>{t(REFUSED[c.refused])}</p>
      ) : (
        <div className="space-y-4 text-sm" data-spam-check>
          <div>
            <div className="font-medium">{t("spam.goes")}</div>
            <ul className="mt-1 list-disc space-y-0.5 pl-5 text-muted">
              <li>{t("settings.nMessages", { count: c.messages, n: number(c.messages) })} · {t("settings.nCalls", { count: c.calls, n: number(c.calls) })} · {t("spam.nFiles", { count: c.files, n: number(c.files) })}</li>
              {c.chats > 0 && <li>{t("settings.nChats", { count: c.chats, n: number(c.chats) })}: {services(c.services)}</li>}
              <li>{t("spam.names")}</li>
            </ul>
          </div>
          {c.groups > 0 && <p className="text-muted">{t("spam.groupsStay", { count: c.groups, n: number(c.groups) })}</p>}
          <p className="text-muted">{t("spam.notBack")}</p>
          {c.reportable.length > 0 && (
            <label className="flex items-center gap-3 rounded-2xl border border-line p-3">
              <span className="min-w-0 flex-1">
                <span className="block font-medium">{t("spam.report", { services: services(c.reportable) })}</span>
                <span className="block text-xs text-muted">{t("spam.reportHint")}</span>
              </span>
              <Switch checked={report} onChange={setReport} label={t("spam.report", { services: services(c.reportable) })} />
            </label>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => remove.mutate()} data-spam-remove>
              <ShieldBan className="size-4" />{t("spam.remove")}
            </Button>
          </div>
        </div>
      ))}
    </Dialog>
  );
}

/** The people blocked on a phone or a service, offered for removal as spam; one the user says is
 * not spam is not offered again. */
export function SpamSuggestions() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["spam"], queryFn: () => api.get<{ suggestions: Blocked[]; removed: Removed[] }>("/api/spam") });
  const keep = useMutation({
    mutationFn: (pid: number) => api.post(`/api/people/${pid}/not-spam`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["spam"] }),
    onError: (e: Error) => toast.error(e.message),
  });
  const [open, setOpen] = useState<number | null>(null);
  const items = list.data?.suggestions ?? [];
  if (!items.length) return null;
  return (
    <div className="border-b border-line bg-panel px-4 pb-3 md:px-6" data-spam-suggestions>
      <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted">
        <ShieldBan className="size-3.5" />{t("spam.blocked")}
      </div>
      <div className="flex gap-2 overflow-x-auto pb-1">
        {items.map((b) => (
          <div key={b.person_id} className="flex shrink-0 items-center rounded-2xl border border-line text-sm">
            <button onClick={() => setOpen(b.person_id)} data-spam-suggestion className="flex items-center gap-2 rounded-l-2xl py-2 pl-3 pr-2 text-left hover:bg-panel-2">
              <Avatar name={b.name} size={28} />
              <span>
                {b.name}
                <span className="block text-[11px] text-muted">{t("spam.blockedOn", { where: b.where.map((w) => service(w).name).join(", ") })}</span>
              </span>
            </button>
            <button className="self-stretch rounded-r-2xl px-2 text-muted hover:bg-panel-2 hover:text-fg" title={t("spam.notSpam")}
              aria-label={t("spam.notSpam")} onClick={() => keep.mutate(b.person_id)}><X className="size-4" /></button>
          </div>
        ))}
      </div>
      <SpamDialog personId={open} onClose={() => setOpen(null)} />
    </div>
  );
}

/** Those removed as spam, each to be let back (what the sources still have returns with their next
 * import). */
export function SpamRemoved() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["spam"], queryFn: () => api.get<{ suggestions: Blocked[]; removed: Removed[] }>("/api/spam") });
  const restore = useMutation({
    mutationFn: (aid: number) => api.post(`/api/spam/${aid}/restore`),
    onSuccess: () => { qc.invalidateQueries(); toast.success(t("spam.restored")); },
    onError: (e: Error) => toast.error(e.message),
  });
  const items = list.data?.removed ?? [];
  return (
    <>
      <div className="px-1 pt-2 text-sm font-semibold">{t("spam.removedTitle")}</div>
      <p className="-mt-1 px-1 text-xs text-muted">{t("spam.removedHint")}</p>
      <div data-spam-removed>
      <Card className="divide-y divide-line">
        {!items.length && <div className="px-4 py-3.5 text-sm text-muted">{t("common.none")}</div>}
        {items.map((r) => (
          <div key={r.address_id} className="flex items-center gap-3 px-4 py-3 text-sm">
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium">{r.name ?? r.label}</span>
              <span className="block text-xs text-muted">{r.name ? `${r.label} · ` : ""}{dateOnly(r.at * 1000)}</span>
            </span>
            {r.service ? <ServiceBadge id={r.service} /> : <span className="text-xs text-muted">{r.kind}</span>}
            <Button size="sm" variant="ghost" onClick={() => restore.mutate(r.address_id)}
              loading={restore.isPending && restore.variables === r.address_id} disabled={restore.isPending}>
              <Undo2 className="size-4" />{t("spam.restore")}
            </Button>
          </div>
        ))}
      </Card>
      </div>
    </>
  );
}

/** On a person's page: the way to remove them as spam, for someone the user has not named and no
 * contact lists. */
export function SpamCard({ personId }: { personId: number }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const check = useQuery({ queryKey: ["spam-check", personId], queryFn: () => api.get<SpamCheck>(`/api/people/${personId}/spam`) });
  const [open, setOpen] = useState(false);
  if (!check.data || check.data.refused) return null;
  return (
    <Card className="flex items-center gap-3 p-5">
      <div className="min-w-0 flex-1 text-sm">
        <div className="font-semibold">{t("spam.cardTitle")}</div>
        <div className="mt-0.5 text-xs text-muted">{t("spam.cardHint")}</div>
      </div>
      <Button size="sm" variant="outline" className="text-danger" onClick={() => setOpen(true)} data-spam-open>
        <ShieldBan className="size-4" />{t("spam.remove")}
      </Button>
      <SpamDialog personId={open ? personId : null} onClose={() => setOpen(false)} onDone={() => navigate({ to: "/people" })} />
    </Card>
  );
}
