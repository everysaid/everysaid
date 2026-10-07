import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ArrowDown, ArrowUp, Check, Pencil, Plus, RotateCcw, Sparkles, X } from "lucide-react";
import { onEvent } from "@/lib/events";
import { toast } from "sonner";
import { api, type Guess, type Label, type LabelKind, type Person, type PersonLabel } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button, Card, Dialog, Field, Input, Switch, Textarea } from "@/components/ui";

export function useLabels() {
  return useQuery({ queryKey: ["labels"], queryFn: () => api.get<{ items: Label[]; stale: number }>("/api/labels") });
}

/** A label in words: the user's name for it, else the app's words for one it brings. */
export function labelName(t: TFunction, l: { key: string | null; name: string | null }) {
  return l.name ?? t(`labels.builtin.${l.key}`);
}

/** How many of the models said so: ●●○, with the words for it. */
export function Votes({ votes, models }: { votes: number | null; models: number | null }) {
  const { t } = useTranslation();
  if (!votes || !models) return null;
  return (
    <span className="whitespace-nowrap text-[11px] text-muted" title={t("labels.votes", { votes, models })}>
      <span className="tracking-tighter text-accent">{"●".repeat(votes)}</span>{"○".repeat(Math.max(0, models - votes))}
      {" "}{votes >= 2 ? t("labels.sure") : t("labels.unsure")}
    </span>
  );
}

/** A name found for someone without one: taken with a click, or turned down for good. onPick: the
 * name into the field, to change before saving. */
export function GuessLine({ personId, guess, onDone, onPick }: { personId: number; guess: Guess; onDone: (name: string | null) => void; onPick?: (name: string) => void }) {
  const { t } = useTranslation();
  const decide = useMutation({
    mutationFn: (accept: boolean) => api.post<Person>(`/api/people/${personId}/guess`, { how: guess.how, accept }),
    onSuccess: (_, accept) => onDone(accept ? guess.name : null),
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <div className="mt-2 rounded-xl border border-accent/30 bg-accent/5 px-3 py-2" data-guess={guess.how}>
      <div className="flex flex-wrap items-center gap-2">
        <Sparkles className="size-4 shrink-0 text-accent" />
        <button type="button" className="font-medium hover:underline" title={t("labels.guessPick")} onClick={() => onPick?.(guess.name)}>{guess.name}</button>
        {guess.how === "models" ? <Votes votes={guess.votes} models={guess.models} /> : <span className="text-[11px] text-muted">{t("labels.fromHandle")}</span>}
        <div className="ml-auto flex gap-1">
          <Button size="sm" variant="primary" onClick={() => decide.mutate(true)} loading={decide.isPending && decide.variables} data-guess-accept>
            <Check className="size-3.5" />{t("labels.accept")}
          </Button>
          <Button size="sm" variant="ghost" aria-label={t("labels.wrong")} title={t("labels.wrong")} onClick={() => decide.mutate(false)} data-guess-reject>
            <X className="size-3.5" />
          </Button>
        </div>
      </div>
      {guess.evidence && <div className="mt-1 truncate text-xs italic text-muted">«{guess.evidence}»</div>}
    </div>
  );
}

/** A person's labels in a chat's info, with "analyse now" where a local analysis is on: their chat
 * read at once, the suggestions there when it is done. */
export function ChatLabels({ personId }: { personId: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const person = useQuery({ queryKey: ["person", personId], queryFn: () => api.get<Person>(`/api/people/${personId}`) });
  const state = useQuery({ queryKey: ["analysis"], queryFn: () => api.get<{ instance: number | null; running: boolean }>("/api/analysis") });
  const [busy, setBusy] = useState(false);
  const now = useMutation({
    mutationFn: () => api.post<{ instance: number }>(`/api/people/${personId}/analyse/now`),
    onMutate: () => setBusy(true),
    onSuccess: () => toast.success(t("labels.analysing")),
    onError: (e: Error) => { setBusy(false); toast.error(e.message); },
  });
  useEffect(() => onEvent((e) => {
    if (busy && e.type === "plugin" && e.instance === state.data?.instance && e.running === null) {
      setBusy(false);
      qc.invalidateQueries({ queryKey: ["person", personId] });
      qc.invalidateQueries({ queryKey: ["chat"] });
      toast.success(t("labels.analysed_now"));
    }
  }), [busy, state.data?.instance, personId]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!person.data) return null;
  return (
    <div className="space-y-2" data-chat-labels>
      <PersonLabels p={person.data} onChanged={() => qc.invalidateQueries({ queryKey: ["person", personId] })} />
      {state.data?.instance && (
        <Button variant="outline" className="w-full" onClick={() => now.mutate()} loading={busy} disabled={busy} data-analyse-now>
          <Sparkles className="size-4" />{busy ? t("labels.analysing") : t("labels.analyseNow")}
        </Button>
      )}
    </div>
  );
}

/** A person's labels: the user's (✓, for good) and the models' (their votes; a click takes one, ✕
 * turns it down), a label of the lists added by hand, and when the models read the chat. */
export function PersonLabels({ p, onChanged }: { p: Person; onChanged: () => void }) {
  const { t } = useTranslation();
  const [adding, setAdding] = useState(false);
  const set = useMutation({
    mutationFn: ({ id, state }: { id: number; state: "yes" | "no" | null }) => api.put(`/api/people/${p.id}/labels/${id}`, { state }),
    onSuccess: onChanged,
    onError: (e: Error) => toast.error(e.message),
  });
  const again = useMutation({ mutationFn: () => api.post(`/api/people/${p.id}/analyse`), onSuccess: () => { onChanged(); toast.success(t("labels.againSoon")); } });
  const list = p.labels ?? [];
  return (
    <div data-labels><Card className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <div className="text-sm font-semibold">{t("labels.title")}</div>
        <Button size="sm" variant="ghost" onClick={() => setAdding(true)} data-label-add><Plus className="size-4" />{t("labels.add")}</Button>
      </div>
      {list.length ? (
        <div className="flex flex-wrap gap-2">
          {list.map((l) => <Chip key={l.id} l={l} onYes={() => set.mutate({ id: l.id, state: "yes" })}
            onNo={() => set.mutate({ id: l.id, state: l.state === "yes" ? null : "no" })} />)}
        </div>
      ) : <div className="text-sm text-muted">{t("labels.none")}</div>}
      {p.analysed && (
        <div className="mt-3 flex flex-wrap items-center gap-x-2 text-xs text-muted">
          {t("labels.analysed", { date: dateOnly(p.analysed.at * 1000), messages: number(p.analysed.messages) })}
          <button type="button" className="text-accent hover:underline" onClick={() => again.mutate()}>{t("labels.again")}</button>
        </div>
      )}
      <LabelPicker open={adding} onOpenChange={setAdding} taken={new Set(list.filter((l) => l.state === "yes").map((l) => l.id))}
        onPick={(id) => { set.mutate({ id, state: "yes" }); setAdding(false); }} />
    </Card></div>
  );
}

function Chip({ l, onYes, onNo }: { l: PersonLabel; onYes: () => void; onNo: () => void }) {
  const { t } = useTranslation();
  const mine = l.state === "yes";
  return (
    <span data-chip={l.key ?? l.name} data-state={l.state} title={l.evidence ? `«${l.evidence}»` : undefined}
      className={cn("flex items-center gap-1.5 rounded-full py-1 pl-3 pr-1 text-sm",
        mine ? "bg-accent/15 text-fg" : "border border-dashed border-line text-muted")}>
      {mine && <Check className="size-3.5 text-accent" />}
      {!mine ? <button type="button" className="hover:text-fg" title={t("labels.confirm")} onClick={onYes}>{labelName(t, l)}</button> : labelName(t, l)}
      {l.kind === "relation" && <span className="text-[10px] uppercase text-muted">{t("labels.relationShort")}</span>}
      {!mine && <Votes votes={l.votes} models={l.models} />}
      <button type="button" className="grid size-5 place-items-center rounded-full hover:bg-panel-2" aria-label={mine ? t("labels.remove") : t("labels.wrong")}
        title={mine ? t("labels.remove") : t("labels.wrong")} onClick={onNo}><X className="size-3" /></button>
    </span>
  );
}

/** A label of the lists, or a new one, for a person. */
function LabelPicker({ open, onOpenChange, taken, onPick }: { open: boolean; onOpenChange: (o: boolean) => void; taken: Set<number>; onPick: (id: number) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const labels = useLabels();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<LabelKind>("tone");
  const add = useMutation({
    mutationFn: () => api.post<{ id: number }>("/api/labels", { kind, name: name.trim() }),
    onSuccess: (r) => { qc.invalidateQueries({ queryKey: ["labels"] }); setName(""); onPick(r.id); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={t("labels.add")}>
      <div className="space-y-4 p-4">
        {(["tone", "relation"] as const).map((k) => (
          <div key={k}>
            <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted">{t(k === "tone" ? "labels.tones" : "labels.relations")}</div>
            <div className="flex flex-wrap gap-2">
              {labels.data?.items.filter((l) => l.kind === k && !taken.has(l.id)).map((l) => (
                <button key={l.id} type="button" onClick={() => onPick(l.id)} data-pick={l.key ?? l.name}
                  className="rounded-full border border-line px-3 py-1 text-sm hover:bg-panel-2">{labelName(t, l)}</button>
              ))}
            </div>
          </div>
        ))}
        <form className="flex flex-wrap gap-2 border-t border-line pt-4" onSubmit={(e) => { e.preventDefault(); if (name.trim()) add.mutate(); }}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t("labels.newName")} className="h-9 min-w-40 flex-1" data-label-new />
          <select value={kind} onChange={(e) => setKind(e.target.value as LabelKind)} className="h-9 rounded-xl border border-line bg-panel px-2 text-sm">
            <option value="tone">{t("labels.tone")}</option>
            <option value="relation">{t("labels.relation")}</option>
          </select>
          <Button size="sm" variant="primary" type="submit" disabled={!name.trim()} loading={add.isPending}>{t("common.add")}</Button>
        </form>
      </div>
    </Dialog>
  );
}

/** The lists themselves (Settings): the tones and the relations, each label with its words, what it
 * means for the models, whether it is sensitive; renamed, moved, merged into another, removed. */
export function LabelLists() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const labels = useLabels();
  const [editing, setEditing] = useState<Label | { kind: LabelKind } | null>(null);
  const refresh = () => qc.invalidateQueries();
  const order = useMutation({ mutationFn: (ids: number[]) => api.put("/api/labels/order", { ids }), onSuccess: refresh });
  const items = labels.data?.items ?? [];
  return (
    <div className="space-y-4">
      {!!labels.data?.stale && <p className="px-1 text-xs text-muted" data-stale>{t("labels.stale", { count: labels.data.stale })}</p>}
      {(["tone", "relation"] as const).map((k) => {
        const list = items.filter((l) => l.kind === k);
        const move = (i: number, by: number) => {
          const ids = list.map((l) => l.id);
          [ids[i], ids[i + by]] = [ids[i + by], ids[i]];
          order.mutate([...ids, ...items.filter((l) => l.kind !== k).map((l) => l.id)]);
        };
        return (
          <div key={k} data-list={k}><Card className="divide-y divide-line">
            <div className="flex items-center justify-between px-4 py-2.5">
              <div className="text-sm font-semibold">{t(k === "tone" ? "labels.tones" : "labels.relations")}</div>
              <Button size="sm" variant="ghost" onClick={() => setEditing({ kind: k })} data-list-add><Plus className="size-4" />{t("common.add")}</Button>
            </div>
            {list.map((l, i) => {
              const meaning = l.meaning ?? l.default_meaning;
              return (
                <div key={l.id} className="flex items-center gap-2 px-4 py-2.5" data-label={l.key ?? l.name}>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-sm font-medium">
                      {labelName(t, l)}
                      {l.sensitive && <span className="rounded-full bg-danger/10 px-1.5 py-0.5 text-[10px] font-medium text-danger">{t("labels.sensitive")}</span>}
                      {(l.uses.yes > 0 || l.uses.suggested > 0) && <span className="text-[11px] font-normal text-muted">{t("labels.uses", { yes: l.uses.yes, suggested: l.uses.suggested })}</span>}
                    </div>
                    <div className="truncate text-xs text-muted">{meaning || t("labels.onlyYours")}</div>
                  </div>
                  <Button size="iconSm" variant="ghost" aria-label={t("settings.up")} disabled={i === 0} onClick={() => move(i, -1)}><ArrowUp className="size-4" /></Button>
                  <Button size="iconSm" variant="ghost" aria-label={t("settings.down")} disabled={i === list.length - 1} onClick={() => move(i, 1)}><ArrowDown className="size-4" /></Button>
                  <Button size="iconSm" variant="ghost" aria-label={t("common.edit")} onClick={() => setEditing(l)} data-label-edit><Pencil className="size-4" /></Button>
                </div>
              );
            })}
          </Card></div>
        );
      })}
      {editing && <LabelEditor label={editing} all={items} onClose={() => setEditing(null)} onDone={refresh} />}
    </div>
  );
}

function LabelEditor({ label, all, onClose, onDone }: { label: Label | { kind: LabelKind }; all: Label[]; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const old = "id" in label ? label : null;
  const [name, setName] = useState(old ? (old.name ?? "") : "");
  const [meaning, setMeaning] = useState(old ? (old.meaning ?? old.default_meaning ?? "") : "");
  const [sensitive, setSensitive] = useState(old?.sensitive ?? false);
  const [into, setInto] = useState("");
  const done = () => { onDone(); onClose(); toast.success(t("common.done")); };
  const fail = (e: Error) => toast.error(e.message);
  const save = useMutation({
    mutationFn: () => old
      ? api.patch(`/api/labels/${old.id}`, {
        name: name.trim() || null, sensitive,
        // the app's own meaning stays the app's (and follows its updates) until it is changed
        meaning: old.key && meaning.trim() === (old.default_meaning ?? "") ? null : meaning.trim(),
      })
      : api.post("/api/labels", { kind: label.kind, name: name.trim(), meaning: meaning.trim(), sensitive }),
    onSuccess: done, onError: fail,
  });
  const merge = useMutation({ mutationFn: () => api.post(`/api/labels/${old!.id}/merge`, { into: Number(into) }), onSuccess: done, onError: fail });
  const remove = useMutation({ mutationFn: () => api.del(`/api/labels/${old!.id}`), onSuccess: done, onError: fail });
  const others = all.filter((l) => l.kind === label.kind && l.id !== old?.id);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={old ? labelName(t, old) : t(label.kind === "tone" ? "labels.newTone" : "labels.newRelation")}>
      <div className="space-y-4 p-4">
        <Field label={t("labels.name")} help={old?.key ? t("labels.nameBuiltin", { name: t(`labels.builtin.${old.key}`) }) : undefined}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={old?.key ? t(`labels.builtin.${old.key}`) : ""} data-label-name />
        </Field>
        <Field label={t("labels.meaning")} help={t("labels.meaningHint")}>
          <div className="space-y-1">
            <Textarea rows={2} value={meaning} onChange={(e) => setMeaning(e.target.value)} data-label-meaning />
            {old?.key && meaning !== (old.default_meaning ?? "") && (
              <button type="button" className="flex items-center gap-1 text-xs text-accent hover:underline" onClick={() => setMeaning(old.default_meaning ?? "")}>
                <RotateCcw className="size-3" />{t("labels.meaningDefault")}
              </button>
            )}
          </div>
        </Field>
        <label className="flex items-start gap-3 text-sm">
          <Switch checked={sensitive} onChange={setSensitive} />
          <span><span className="font-medium">{t("labels.sensitive")}</span><span className="block text-xs text-muted">{t("labels.sensitiveHint")}</span></span>
        </label>
        <Button variant="primary" className="w-full" onClick={() => save.mutate()} loading={save.isPending} disabled={!old?.key && !name.trim()} data-label-save>{t("common.save")}</Button>
        {old && (
          <div className="space-y-3 border-t border-line pt-4">
            <div className="flex gap-2">
              <select value={into} onChange={(e) => setInto(e.target.value)} className="h-9 min-w-0 flex-1 rounded-xl border border-line bg-panel px-2 text-sm" data-label-into>
                <option value="">{t("labels.mergeInto")}</option>
                {others.map((l) => <option key={l.id} value={l.id}>{labelName(t, l)}</option>)}
              </select>
              <Button size="sm" variant="outline" disabled={!into} loading={merge.isPending}
                onClick={() => confirm(t("labels.mergeConfirm", { name: labelName(t, old), into: labelName(t, others.find((l) => String(l.id) === into)!) })) && merge.mutate()}
                data-label-merge>{t("labels.merge")}</Button>
            </div>
            <Button size="sm" variant="ghost" className="text-danger" loading={remove.isPending}
              onClick={() => confirm(old.uses.yes ? t("labels.removeConfirmUsed", { count: old.uses.yes }) : t("labels.removeConfirm")) && remove.mutate()}
              data-label-remove>{t("common.delete")}</Button>
          </div>
        )}
      </div>
    </Dialog>
  );
}
