import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { BookUser, ChevronDown, Images, ListChecks, Play, Plus, Radio, Settings2, Smartphone, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, type PluginChat, type PluginInstance, type PluginManifest, type SettingField } from "@/lib/api";
import { dateOnly, isoDay, number, relative } from "@/lib/format";
import { onEvent } from "@/lib/events";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Button, Card, Dialog, Field, Input, Section, Spinner, Switch } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";

const KINDS = [
  { kind: "source", icon: Smartphone },
  { kind: "library", icon: Images },
  { kind: "contacts", icon: BookUser },
] as const;

export function SourcesPage() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const instances = useQuery({ queryKey: ["plugins", lang], queryFn: () => api.get<{ items: PluginInstance[] }>(`/api/plugins?lang=${lang}`), refetchInterval: 15000 });
  const catalog = useQuery({ queryKey: ["catalog", lang], queryFn: () => api.get<{ items: PluginManifest[] }>(`/api/plugins/catalog?lang=${lang}`), staleTime: Infinity });
  const [adding, setAdding] = useState(false);
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("sources.title")} subtitle={t("sources.subtitle")}
        actions={<Button variant="primary" size="sm" onClick={() => setAdding(true)}><Plus className="size-4" />{t("sources.add")}</Button>} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-8 p-4 md:p-6">
          {instances.isLoading ? <Spinner /> : KINDS.map(({ kind, icon: Icon }) => {
            const list = instances.data?.items.filter((i) => i.kind === kind) ?? [];
            return (
              <Section key={kind} title={<span className="flex items-center gap-2"><Icon className="size-4" />{t(`sources.${kind}`)}</span>}>
                {list.length === 0 ? (
                  <Card className="p-5 text-sm text-muted">{t("common.none")}</Card>
                ) : list.map((i) => <InstanceCard key={i.id} i={i} manifest={catalog.data?.items.find((m) => m.id === i.plugin)} />)}
              </Section>
            );
          })}
          <Devices />
        </div>
      </div>
      {catalog.data && <AddDialog open={adding} onOpenChange={setAdding} catalog={catalog.data.items} />}
    </div>
  );
}

function InstanceCard({ i, manifest }: { i: PluginInstance; manifest?: PluginManifest }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [log, setLog] = useState<string[]>(i.log);
  const [showLog, setShowLog] = useState(false);
  const [editing, setEditing] = useState(false);
  const [choosing, setChoosing] = useState(false);
  useEffect(() => setLog(i.log), [i.log]);
  useEffect(() => onEvent((e) => {
    if (e.type === "plugin_log" && e.instance === i.id) setLog((l) => [...l.slice(-199), e.line]);
  }), [i.id]);
  const refresh = () => qc.invalidateQueries({ queryKey: ["plugins"] });
  const run = useMutation({
    mutationFn: (action?: string) => api.post(`/api/plugins/${i.id}/run`, { action }),
    onSuccess: () => { setShowLog(true); refresh(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const live = useMutation({ mutationFn: (on: boolean) => api.post(`/api/plugins/${i.id}/live`, { on }), onSuccess: refresh, onError: (e: Error) => toast.error(e.message) });
  const patch = useMutation({ mutationFn: (b: Record<string, unknown>) => api.patch(`/api/plugins/${i.id}`, b), onSuccess: refresh });
  const remove = useMutation({ mutationFn: () => api.del(`/api/plugins/${i.id}`), onSuccess: refresh });

  return (
    <Card className={cn("overflow-hidden", !i.enabled && "opacity-60")}>
      <div className="flex items-start gap-3 p-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold">{i.label}</span>
            <span className="text-xs text-muted">{i.name}</span>
            {i.is_default && <span className="rounded-full bg-accent/15 px-2 py-0.5 text-[11px] font-medium text-accent">{t("sources.default")}</span>}
            {i.live && <span className="flex items-center gap-1 rounded-full bg-ok/15 px-2 py-0.5 text-[11px] font-medium text-ok"><Radio className="size-3" />{t("sources.liveBadge")}</span>}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
            <span className={cn("flex items-center gap-1", i.ready ? "text-ok" : "text-danger")}>
              <span className={cn("size-1.5 rounded-full", i.ready ? "bg-ok" : "bg-danger")} />{i.ready ? t("sources.ready") : i.ready_text}
            </span>
            <span>{t("sources.lastRun")}: {i.last_run ? relative(i.last_run) : t("sources.never")}</span>
            {i.last_status && i.last_status !== "ok" && <span className="text-danger">{i.last_status}</span>}
            {manifest?.services.map((s) => <span key={s} style={{ color: service(s).color }}>● {service(s).name}</span>)}
          </div>
        </div>
        <Switch checked={i.enabled} onChange={(v) => patch.mutate({ enabled: v })} label={t("sources.enabled")} />
      </div>
      <div className="flex flex-wrap items-center gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
        {i.kind !== "library" && (
          <Button size="sm" variant="primary" onClick={() => run.mutate(undefined)} loading={!!i.running || run.isPending} disabled={!i.enabled}>
            {!i.running && <Play className="size-3.5" />}{i.running ? t("sources.running") : t("sources.run")}
          </Button>
        )}
        {manifest?.actions.map((a) => (
          <Button key={a.id} size="sm" variant="outline" onClick={() => run.mutate(a.id)} disabled={!!i.running || !i.enabled}>{a.label}</Button>
        ))}
        {i.live_capable && (
          <label className="flex items-center gap-2 px-2 text-sm">
            <Switch checked={i.live} onChange={(v) => live.mutate(v)} disabled={!i.enabled} /> {t("sources.live")}
          </label>
        )}
        {i.kind === "library" && !i.is_default && (
          <Button size="sm" variant="outline" onClick={() => patch.mutate({ is_default: true })}>{t("sources.makeDefault")}</Button>
        )}
        <div className="ml-auto flex gap-1">
          {manifest?.has_chats && (
            <Button size="sm" variant="ghost" onClick={() => setChoosing(true)}><ListChecks className="size-4" />{t("sources.chats")}</Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setShowLog(!showLog)}>
            {t("sources.log")}<ChevronDown className={cn("size-3.5 transition-transform", showLog && "rotate-180")} />
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setEditing(true)} aria-label={t("common.edit")}><Settings2 className="size-4" /></Button>
          <Button size="sm" variant="ghost" onClick={() => confirm(t("sources.removeConfirm")) && remove.mutate()} aria-label={t("sources.remove")}><Trash2 className="size-4" /></Button>
        </div>
      </div>
      {showLog && (
        <pre className="max-h-72 overflow-auto whitespace-pre-wrap border-t border-line bg-bg p-3 font-mono text-[11px] leading-relaxed text-muted">
          {log.length ? log.join("\n") : "—"}
        </pre>
      )}
      {manifest && <SettingsDialog open={editing} onOpenChange={setEditing} manifest={manifest} instance={i} />}
      {choosing && <ChatsDialog instance={i} onClose={() => setChoosing(false)} />}
    </Card>
  );
}

function SettingsForm({ fields, values, secrets, onChange, onSecret, existing }: {
  fields: SettingField[]; values: Record<string, unknown>; secrets: Record<string, string>;
  onChange: (k: string, v: unknown) => void; onSecret: (k: string, v: string) => void; existing?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className="space-y-4">
      {fields.map((f) => (
        <Field key={f.key} label={f.label + (f.required ? " *" : "")} help={f.help}>
          {f.type === "bool" ? (
            <div><Switch checked={!!values[f.key]} onChange={(v) => onChange(f.key, v)} /></div>
          ) : f.type === "secret" ? (
            <Input type="password" autoComplete="new-password" value={secrets[f.key] ?? ""} placeholder={existing ? t("sources.secretSet") : ""}
              onChange={(e) => onSecret(f.key, e.target.value)} />
          ) : f.type === "select" ? (
            <select value={String(values[f.key] ?? "")} onChange={(e) => onChange(f.key, e.target.value)} className="h-10 w-full rounded-xl border border-line bg-panel px-3 text-sm">
              {f.options.map((o) => <option key={o}>{o}</option>)}
            </select>
          ) : (
            <Input type={f.type === "number" ? "number" : f.type === "url" ? "url" : "text"} value={String(values[f.key] ?? "")}
              onChange={(e) => onChange(f.key, f.type === "number" ? Number(e.target.value) : e.target.value)} />
          )}
        </Field>
      ))}
    </div>
  );
}

function SettingsDialog({ open, onOpenChange, manifest, instance }: { open: boolean; onOpenChange: (o: boolean) => void; manifest: PluginManifest; instance: PluginInstance }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [label, setLabel] = useState(instance.label);
  const [values, setValues] = useState<Record<string, unknown>>(instance.settings);
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  useEffect(() => { if (open) { setLabel(instance.label); setValues(instance.settings); setSecrets({}); } }, [open, instance]);
  const save = useMutation({
    mutationFn: () => api.patch(`/api/plugins/${instance.id}`, { label, settings: values, secrets }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["plugins"] }); onOpenChange(false); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={manifest.name} description={manifest.description}>
      <div className="space-y-4">
        <Field label={t("sources.label")}><Input value={label} onChange={(e) => setLabel(e.target.value)} /></Field>
        <SettingsForm fields={manifest.settings} values={values} secrets={secrets} existing
          onChange={(k, v) => setValues((s) => ({ ...s, [k]: v }))} onSecret={(k, v) => setSecrets((s) => ({ ...s, [k]: v }))} />
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t("common.cancel")}</Button>
          <Button variant="primary" onClick={() => save.mutate()} loading={save.isPending}>{t("common.save")}</Button>
        </div>
      </div>
    </Dialog>
  );
}

function AddDialog({ open, onOpenChange, catalog }: { open: boolean; onOpenChange: (o: boolean) => void; catalog: PluginManifest[] }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [chosen, setChosen] = useState<PluginManifest | null>(null);
  const [label, setLabel] = useState("");
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!open) setChosen(null);
  }, [open]);
  const pick = (m: PluginManifest) => {
    setChosen(m);
    setLabel(m.name);
    setValues(Object.fromEntries(m.settings.filter((s) => s.default != null && s.type !== "secret").map((s) => [s.key, s.default])));
    setSecrets({});
  };
  const add = useMutation({
    mutationFn: () => api.post("/api/plugins", { plugin: chosen!.id, label, settings: values, secrets }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["plugins"] }); onOpenChange(false); toast.success(t("common.done")); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={chosen ? chosen.name : t("sources.addTitle")} description={chosen?.description} wide>
      {!chosen ? (
        <div className="space-y-5">
          {KINDS.map(({ kind, icon: Icon }) => (
            <div key={kind} className="space-y-2">
              <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted"><Icon className="size-3.5" />{t(`sources.${kind}`)}</div>
              <div className="grid gap-2 sm:grid-cols-2">
                {catalog.filter((m) => m.kind === kind).map((m) => (
                  <button key={m.id} disabled={!m.available} onClick={() => pick(m)}
                    className="rounded-2xl border border-line p-3 text-left transition-colors hover:border-accent/50 hover:bg-panel-2 disabled:opacity-50">
                    <div className="font-medium">{m.name}</div>
                    <div className="mt-0.5 line-clamp-2 text-xs text-muted">{m.available ? m.description : t("sources.unavailable")}</div>
                    <div className="mt-1.5 flex flex-wrap gap-1">
                      {m.modes.includes("live") && <span className="rounded-full bg-ok/15 px-1.5 text-[10px] font-medium text-ok">{t("sources.liveBadge")}</span>}
                      {m.can_send && <span className="rounded-full bg-accent/15 px-1.5 text-[10px] font-medium text-accent">{t("sources.canSend")}</span>}
                    </div>
                  </button>
                ))}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="space-y-4">
          {chosen.needs.length > 0 && (
            <div className="rounded-2xl bg-panel-2 p-3 text-sm">
              <div className="mb-1 font-medium">{t("sources.needs")}</div>
              <ul className="list-inside list-disc text-muted">{chosen.needs.map((n) => <li key={n}>{n}</li>)}</ul>
            </div>
          )}
          <Field label={t("sources.label")}><Input value={label} onChange={(e) => setLabel(e.target.value)} /></Field>
          <SettingsForm fields={chosen.settings} values={values} secrets={secrets}
            onChange={(k, v) => setValues((s) => ({ ...s, [k]: v }))} onSecret={(k, v) => setSecrets((s) => ({ ...s, [k]: v }))} />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={() => setChosen(null)}>{t("common.back")}</Button>
            <Button variant="primary" onClick={() => add.mutate()} loading={add.isPending} disabled={!label.trim()}>{t("common.add")}</Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function Devices() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const devices = useQuery({ queryKey: ["devices"], queryFn: () => api.get<{ items: { id: number; name: string; kind: string | null; used_from: number | null; used_until: number | null }[] }>("/api/devices") });
  const set = useMutation({
    mutationFn: ({ id, ...b }: { id: number; used_from?: number | null; used_until?: number | null }) => api.patch(`/api/devices/${id}`, b),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices"] }),
  });
  const parse = (v: string) => (v ? new Date(v + "T00:00:00").getTime() : null);
  if (!devices.data?.items.length) return null;
  return (
    <Section title={<span className="flex items-center gap-2"><Smartphone className="size-4" />{t("sources.devices")}</span>}>
      <Card className="divide-y divide-line">
        <p className="p-4 text-xs text-muted">{t("sources.devicesHint")}</p>
        {devices.data.items.map((d) => (
          <div key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
            <span className="min-w-28 flex-1 font-medium">{d.name} <span className="text-xs font-normal text-muted">{d.kind}</span></span>
            <label className="flex items-center gap-1.5 text-muted">{t("sources.from")}
              <input type="date" defaultValue={d.used_from ? isoDay(d.used_from) : ""} onChange={(e) => set.mutate({ id: d.id, used_from: parse(e.target.value) })}
                className="h-9 rounded-xl border border-line bg-panel px-2 text-fg" /></label>
            <label className="flex items-center gap-1.5 text-muted">{t("sources.until")}
              <input type="date" defaultValue={d.used_until ? isoDay(d.used_until) : ""} onChange={(e) => set.mutate({ id: d.id, used_until: parse(e.target.value) })}
                className="h-9 rounded-xl border border-line bg-panel px-2 text-fg" /></label>
          </div>
        ))}
      </Card>
    </Section>
  );
}


function ChatsDialog({ instance, onClose }: { instance: PluginInstance; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const chats = useQuery({ queryKey: ["plugin-chats", instance.id], queryFn: () => api.get<{ items: PluginChat[] }>(`/api/plugins/${instance.id}/chats`) });
  const [imp, setImp] = useState<Set<number>>(new Set());
  const [med, setMed] = useState<Set<number>>(new Set());
  const [q, setQ] = useState("");
  const [kind, setKind] = useState("");
  useEffect(() => {
    if (!chats.data) return;
    setImp(new Set(chats.data.items.filter((c) => c.import).map((c) => c.id)));
    setMed(new Set(chats.data.items.filter((c) => c.media).map((c) => c.id)));
  }, [chats.data]);
  const all = chats.data?.items ?? [];
  const shown = all.filter((c) => (!kind || c.kind === kind) && (!q || (c.title ?? "").toLowerCase().includes(q.toLowerCase())));
  const kinds = [...new Set(all.map((c) => c.kind))];
  const setMany = (which: "imp" | "med", on: boolean) => {
    const f = which === "imp" ? setImp : setMed;
    f((s) => {
      const n = new Set(s);
      shown.forEach((c) => (on ? n.add(c.id) : n.delete(c.id)));
      return n;
    });
  };
  const toggle = (which: "imp" | "med", id: number) => (which === "imp" ? setImp : setMed)((s) => {
    const n = new Set(s);
    n.has(id) ? n.delete(id) : n.add(id);
    return n;
  });
  const save = useMutation({
    mutationFn: () => api.put(`/api/plugins/${instance.id}/chats`, { skip: all.filter((c) => !imp.has(c.id)).map((c) => c.id), media: [...med] }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["plugins"] }); toast.success(t("common.done")); onClose(); },
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`${instance.label}: ${t("sources.chats")}`} description={t("sources.chatsHint")} wide>
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("sources.filterChats")} className="h-9 max-w-56" />
          <select value={kind} onChange={(e) => setKind(e.target.value)} className="h-9 rounded-xl border border-line bg-panel px-2 text-sm">
            <option value="">{t("common.all")}</option>
            {kinds.map((k) => <option key={k}>{k}</option>)}
          </select>
        </div>
        <div className="grid grid-cols-[1fr_auto_auto] items-center gap-x-4 gap-y-1 text-sm">
          <span className="text-xs text-muted">{shown.length}</span>
          <span className="flex gap-1 text-xs"><b>{t("sources.importCol")}</b>
            <button className="text-accent" onClick={() => setMany("imp", true)}>{t("sources.selectAll")}</button>/
            <button className="text-accent" onClick={() => setMany("imp", false)}>{t("sources.selectNone")}</button></span>
          <span className="flex gap-1 text-xs"><b>{t("sources.mediaCol")}</b>
            <button className="text-accent" onClick={() => setMany("med", true)}>{t("sources.selectAll")}</button>/
            <button className="text-accent" onClick={() => setMany("med", false)}>{t("sources.selectNone")}</button></span>
          {chats.isLoading && <Spinner />}
          {shown.map((c) => (
            <div key={c.id} className="contents">
              <span className="min-w-0 truncate py-1.5">
                {c.title || c.id} <span className="text-xs text-muted">· {c.kind} · {number(c.messages)}{c.last ? ` · ${dateOnly(c.last)}` : ""}{c.archived ? ` · ${t("sources.archivedChat")}` : ""}</span>
              </span>
              <span className="justify-self-center"><Switch checked={imp.has(c.id)} onChange={() => toggle("imp", c.id)} /></span>
              <span className="justify-self-center"><Switch checked={med.has(c.id)} onChange={() => toggle("med", c.id)} /></span>
            </div>
          ))}
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="primary" onClick={() => save.mutate()} loading={save.isPending}>{t("common.save")}</Button>
        </div>
      </div>
    </Dialog>
  );
}
