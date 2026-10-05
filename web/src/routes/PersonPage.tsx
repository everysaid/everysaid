import { useEffect, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Merge, MessageSquare, Scissors } from "lucide-react";
import { toast } from "sonner";
import { api, qs, type Person } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { personRoute } from "@/router";
import { Avatar, Button, Card, Center, Dialog, Field, Input, ServiceBadge, Spinner, Textarea } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";

export function PersonPage() {
  const { t } = useTranslation();
  const { personId } = personRoute.useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const pid = Number(personId);
  const person = useQuery({ queryKey: ["person", pid], queryFn: () => api.get<Person>(`/api/people/${pid}`) });
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [merging, setMerging] = useState(false);
  useEffect(() => {
    setName(person.data?.given_name ?? "");
    setNote(person.data?.note ?? "");
  }, [person.data]);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["person"] });
    qc.invalidateQueries({ queryKey: ["people"] });
    qc.invalidateQueries({ queryKey: ["chats"] });
    qc.invalidateQueries({ queryKey: ["chat"] });
  };
  const save = useMutation({
    mutationFn: (b: Record<string, string>) => api.patch(`/api/people/${pid}`, b),
    onSuccess: () => { refresh(); toast.success(t("common.done")); },
  });
  const split = useMutation({
    mutationFn: (aid: number) => api.post<{ person_id: number }>(`/api/addresses/${aid}/split`),
    onSuccess: () => { refresh(); toast.success(t("common.done")); },
  });
  if (person.isLoading) return <Center><Spinner /></Center>;
  const p = person.data;
  if (!p) return <Center>{t("common.none")}</Center>;
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={p.name} back="/people" actions={
        <Button variant="primary" size="sm" onClick={() => navigate({ to: "/chat/$chatId", params: { chatId: `p${p.id}` } })}>
          <MessageSquare className="size-4" />{t("people.openChat")}
        </Button>
      } />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-2xl space-y-5 p-4 md:p-6">
          <Card className="flex items-center gap-4 p-5">
            <Avatar name={p.name} src={p.avatar ? `/api/avatar/${p.id}` : null} size={72} />
            <div className="grid flex-1 grid-cols-2 gap-3 text-sm">
              <div><div className="text-xl font-semibold tabular-nums">{number(p.stats.messages)}</div><div className="text-muted">{t("people.messages")}</div></div>
              <div><div className="text-xl font-semibold tabular-nums">{number(p.stats.calls)}</div><div className="text-muted">{t("people.calls")}</div></div>
              <div><div className="font-medium">{dateOnly(p.stats.first)}</div><div className="text-muted">{t("people.firstContact")}</div></div>
              <div><div className="font-medium">{dateOnly(p.stats.last)}</div><div className="text-muted">{t("people.lastContact")}</div></div>
            </div>
          </Card>
          <Card className="space-y-4 p-5">
            <Field label={t("people.name")} help={p.contact ? `${t("people.contact")}: ${p.contact.name}` : t("people.nameHint")}>
              <div className="flex gap-2">
                <Input value={name} placeholder={p.name} onChange={(e) => setName(e.target.value)} />
                <Button onClick={() => save.mutate({ name })} disabled={name === (p.given_name ?? "")}>{t("common.save")}</Button>
              </div>
            </Field>
            <Field label={t("people.note")}>
              <Textarea rows={4} value={note} onChange={(e) => setNote(e.target.value)} onBlur={() => note !== (p.note ?? "") && save.mutate({ note })} />
            </Field>
          </Card>
          <Card>
            <div className="flex items-center justify-between px-5 pt-4">
              <div className="text-sm font-semibold">{t("people.handles")}</div>
              <Button size="sm" variant="ghost" onClick={() => setMerging(true)}><Merge className="size-4" />{t("people.merge")}</Button>
            </div>
            <div className="divide-y divide-line">
              {p.handles.map((h) => (
                <div key={h.address_id} className="flex items-center gap-3 px-5 py-3 text-sm">
                  <span className="min-w-0 flex-1 truncate font-medium">{h.label}</span>
                  {h.service ? <ServiceBadge id={h.service} /> : <span className="text-xs text-muted">{h.kind}</span>}
                  {p.handles.length > 1 && (
                    <Button size="sm" variant="ghost" title={t("people.splitHint")} onClick={() => split.mutate(h.address_id)}>
                      <Scissors className="size-3.5" />{t("people.split")}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          </Card>
          {p.groups.length > 0 && (
            <Card className="p-5">
              <div className="mb-2 text-sm font-semibold">{t("people.groups")}</div>
              <div className="flex flex-wrap gap-2">
                {p.groups.map((g) => (
                  <Link key={g.chat_id} to="/chat/$chatId" params={{ chatId: g.chat_id }} className="rounded-full border border-line px-3 py-1 text-sm hover:bg-panel-2">{g.title}</Link>
                ))}
              </div>
            </Card>
          )}
        </div>
      </div>
      <MergeDialog open={merging} onOpenChange={setMerging} person={p} onDone={refresh} />
    </div>
  );
}

function MergeDialog({ open, onOpenChange, person, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; person: Person; onDone: () => void }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const res = useQuery({
    queryKey: ["people", "merge", dq],
    enabled: open && dq.length > 1,
    queryFn: () => api.get<{ items: { id: number; name: string }[] }>(`/api/people${qs({ q: dq, limit: 20 })}`),
  });
  const merge = useMutation({
    mutationFn: (other: number) => api.post(`/api/people/${person.id}/merge`, { other }),
    onSuccess: () => { onDone(); onOpenChange(false); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={t("people.merge")} description={t("people.mergeHint")}>
      <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("people.search")} />
      <div className="mt-3 divide-y divide-line">
        {res.data?.items.filter((x) => x.id !== person.id).map((x) => (
          <div key={x.id} className="flex items-center gap-3 py-2">
            <Avatar name={x.name} size={36} />
            <span className="min-w-0 flex-1 truncate">{x.name}</span>
            <Button size="sm" variant="primary" onClick={() => merge.mutate(x.id)} loading={merge.isPending}>{t("people.mergeConfirm")}</Button>
          </div>
        ))}
      </div>
    </Dialog>
  );
}
