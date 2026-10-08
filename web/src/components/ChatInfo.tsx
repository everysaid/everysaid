import { useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronRight, Images, Phone, Users, X } from "lucide-react";
import { toast } from "sonner";
import { api, qs, thumbUrl, type ChatDetail, type MediaItem, type NameSources, type StateField } from "@/lib/api";
import { dateOnly, number } from "@/lib/format";
import { service } from "@/lib/services";
import { Avatar, Button, Input, Segmented, ServiceBadge, Textarea } from "./ui";
import { avatarUrl } from "./ChatList";
import { GroupParts } from "./GroupMerge";
import { ChatLabels } from "./Labels";

export function ChatInfo({ chat, onClose }: { chat: ChatDetail; onClose?: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const p = chat.person;
  const [name, setName] = useState(p?.given_name ?? "");
  const [note, setNote] = useState(p?.note ?? "");
  useEffect(() => {
    setName(p?.given_name ?? "");
    setNote(p?.note ?? "");
  }, [p?.id, p?.given_name, p?.note]);
  const media = useQuery({
    queryKey: ["media", { chat: chat.id, limit: 9 }],
    queryFn: () => api.get<{ items: MediaItem[] }>(`/api/media${qs({ chat: chat.id, kind: "image", limit: 9, available: true })}`),
  });
  const save = useMutation({
    mutationFn: (body: Record<string, string>) => api.patch(`/api/people/${p!.id}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["chat", chat.id] });
      qc.invalidateQueries({ queryKey: ["chats"] });
      toast.success(t("common.done"));
    },
  });

  return (
    <div className="space-y-6 p-5">
      {onClose && (
        <div className="flex justify-end">
          <Button variant="ghost" size="iconSm" className="rounded-full" onClick={onClose} aria-label={t("common.close")}><X className="size-4" /></Button>
        </div>
      )}
      <div className="flex flex-col items-center gap-3 text-center">
        <Avatar name={chat.title} src={avatarUrl({ type: chat.type, person_id: chat.person_id, avatar: p?.avatar })} group={chat.type === "group"} size={96} />
        <div>
          <div className="text-lg font-semibold">{chat.title}</div>
          {p?.contact && <div className="text-xs text-muted">{t("people.contact")}{p.contact.organization ? ` · ${p.contact.organization}` : ""}</div>}
        </div>
        <div className="flex flex-wrap justify-center gap-1">{chat.services.map((s) => <ServiceBadge key={s} id={s} />)}</div>
      </div>

      {p && (
        <>
          <div className="grid grid-cols-2 gap-2 text-center">
            <Stat label={t("people.messages")} value={number(p.stats.messages)} />
            <Stat label={t("people.calls")} value={number(p.stats.calls)} />
            <Stat label={t("people.firstContact")} value={dateOnly(p.stats.first)} small />
            <Stat label={t("people.lastContact")} value={dateOnly(p.stats.last)} small />
          </div>
          <div className="space-y-2">
            <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("people.name")}</div>
            <div className="flex gap-2">
              <Input value={name} placeholder={chat.title} onChange={(e) => setName(e.target.value)} />
              <Button onClick={() => save.mutate({ name })} disabled={name === (p.given_name ?? "")}>{t("common.save")}</Button>
            </div>
          </div>
          <NameFrom chat={chat} />
          <div className="space-y-2">
            <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("people.handles")}</div>
            <div className="divide-y divide-line rounded-2xl border border-line">
              {p.handles.map((h) => (
                <div key={h.address_id} className="flex items-center gap-2 px-3 py-2 text-sm">
                  <span className="min-w-0 flex-1 truncate">{h.label}</span>
                  {h.service ? <ServiceBadge id={h.service} /> : <span className="text-xs text-muted">{h.kind}</span>}
                </div>
              ))}
            </div>
          </div>
          <div className="space-y-2">
            <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("people.note")}</div>
            <Textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} onBlur={() => note !== (p.note ?? "") && save.mutate({ note })} />
          </div>
          {p.stats.by_service && Object.keys(p.stats.by_service).length > 1 && (
            <div className="space-y-1.5">
              {Object.entries(p.stats.by_service).sort((a, b) => b[1] - a[1]).map(([s, n]) => (
                <div key={s} className="flex items-center gap-2 text-sm">
                  <span className="w-24 shrink-0 text-muted">{service(s).name}</span>
                  <div className="h-2 flex-1 overflow-hidden rounded-full bg-panel-2">
                    <div className="h-full rounded-full" style={{ width: `${(n / p.stats.messages) * 100}%`, background: service(s).color }} />
                  </div>
                  <span className="w-14 text-right tabular-nums text-muted">{number(n)}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}

      <ChatStates chat={chat} />

      {chat.type === "group" && <GroupParts chat={chat} />}

      {chat.members && (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted"><Users className="size-3.5" />{chat.members.length} {t("chat.members")}</div>
          <div className="divide-y divide-line rounded-2xl border border-line">
            {chat.members.map((m, i) => (
              <Link key={i} to={m.person_id ? "/chat/$chatId" : "."} params={{ chatId: `p${m.person_id}` }} className="flex items-center gap-3 px-3 py-2 text-sm hover:bg-panel-2">
                <Avatar name={m.name ?? "?"} size={32} />
                <span className="truncate">{m.name ?? "?"}</span>
              </Link>
            ))}
          </div>
        </div>
      )}

      {(media.data?.items.length ?? 0) > 0 && (
        <div className="space-y-2">
          <Link to="/media" search={{ chat: chat.id }} className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted"><Images className="size-3.5" />{t("nav.media")}</Link>
          <div className="grid grid-cols-3 gap-1">
            {media.data!.items.map((m) => (
              <Link key={m.sha256} to="/chat/$chatId" params={{ chatId: chat.id }} search={{ m: m.message_id }} className="aspect-square overflow-hidden rounded-lg bg-panel-2">
                <img src={thumbUrl(m.sha256)} alt="" loading="lazy" className="size-full object-cover" />
              </Link>
            ))}
          </div>
        </div>
      )}

      {p && p.groups.length > 0 && (
        <div className="space-y-2">
          <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("people.groups")}</div>
          <div className="divide-y divide-line rounded-2xl border border-line">
            {p.groups.map((g) => (
              <Link key={g.chat_id} to="/chat/$chatId" params={{ chatId: g.chat_id }} className="flex items-center gap-3 px-3 py-2 text-sm hover:bg-panel-2">
                <Avatar name={g.title ?? "?"} size={32} group />
                <span className="min-w-0 flex-1 truncate">{g.title}</span>
                <ChevronRight className="size-4 text-muted" />
              </Link>
            ))}
          </div>
        </div>
      )}

      {p && <ChatLabels personId={p.id} />}

      {p && (
        <div className="flex flex-col gap-2">
          <Link to="/people/$personId" params={{ personId: String(p.id) }}><Button variant="outline" className="w-full">{t("people.title")}: {t("common.edit")}</Button></Link>
          <Link to="/calls" search={{ chat: chat.id }}><Button variant="ghost" className="w-full"><Phone className="size-4" />{t("nav.calls")}</Button></Link>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value, small }: { label: string; value: string; small?: boolean }) {
  return (
    <div className="rounded-2xl bg-panel-2 px-3 py-2.5">
      <div className={small ? "text-sm font-semibold" : "text-xl font-semibold tabular-nums"}>{value}</div>
      <div className="text-xs text-muted">{label}</div>
    </div>
  );
}

/** Where the person's name comes from, what else they have been called, and the choice of source. */
function NameFrom({ chat }: { chat: ChatDetail }) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const p = chat.person!;
  const names = useQuery({ queryKey: ["names", i18n.language], queryFn: () => api.get<NameSources>(`/api/names?lang=${i18n.language}`) });
  const label = (id: string) =>
    id === "user" ? t("people.nameGiven") : id === "handle" ? t("people.nameHandle")
      : names.data?.order.find((x) => x.id === id)?.label ?? id;
  const pin = useMutation({
    mutationFn: (v: string) => api.patch(`/api/people/${p.id}`, { name_source: v || null }),
    onSuccess: () => qc.invalidateQueries(),
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <div className="space-y-2 text-sm">
      <label className="block space-y-1">
        <span className="text-xs text-muted">{t("people.nameFrom")}</span>
        <select value={p.name_source ?? ""} onChange={(e) => pin.mutate(e.target.value)}
          className="w-full rounded-xl border border-line bg-panel px-2 py-1.5 text-sm">
          <option value="">{t("people.nameAuto")}{p.name_source ? "" : ` · ${label(p.name_from)}`}</option>
          {names.data?.order.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
          {p.handles.map((h) => <option key={h.address_id} value={`address:${h.address_id}`}>{h.label} ({t("people.thisHandle")})</option>)}
        </select>
      </label>
      {p.contacts.length > 1 && (
        <p className="rounded-xl bg-accent/10 px-3 py-2 text-xs">{t("people.severalContacts", { names: p.contacts.map((c) => c.name).join(", ") })}</p>
      )}
      {p.aka.length > 0 && (
        <div>
          <div className="text-xs text-muted">{t("people.aka")}</div>
          <ul className="mt-1 space-y-0.5">
            {p.aka.slice(0, 8).map((a) => (
              <li key={`${a.source}:${a.name}`} className={a.current ? "" : "text-muted line-through decoration-muted/40"}>
                {a.name} <span className="text-xs text-muted">· {label(a.source)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const STATE_FIELDS: { field: StateField; label: string }[] = [
  { field: "archived", label: "chat.stateArchived" }, { field: "muted", label: "chat.stateMuted" }, { field: "pinned", label: "chat.statePinned" },
];

/** The chat's state (archived: the app's own; muted, pinned: what the services say, and the user's choice). */
function ChatStates({ chat }: { chat: ChatDetail }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.patch(`/api/chats/${chat.id}`, body),
    onSuccess: () => qc.invalidateQueries(),
    onError: (e: Error) => toast.error(e.message),
  });
  return (
    <div className="space-y-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted">{t("chat.state")}</div>
      {STATE_FIELDS.map(({ field, label }) => {
        const mine = chat.state_user[field];
        const now = Boolean(chat[field as "archived" | "muted" | "pinned"]);
        const by = chat.state_from[field];
        const reports = chat.state_reports[field] ?? [];
        return (
          <div key={field} className="space-y-1.5" data-state-field={field}>
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 text-sm">{t(label)}</span>
              {field === "archived" ? (       // the app's own: whatever a service does later
                <Segmented value={now ? "yes" : "no"} onChange={(v) => set.mutate({ archived: v === "yes" })}
                  options={[{ value: "yes", label: t("chat.stateYes") }, { value: "no", label: t("chat.stateNo") }]} />
              ) : (
                <Segmented value={mine ? (mine.value ? "yes" : "no") : "auto"}
                  onChange={(v) => set.mutate({ [field]: v === "auto" ? null : v === "yes", always: !!mine?.always })}
                  options={[{ value: "auto", label: t("chat.stateAuto") }, { value: "yes", label: t("chat.stateYes") }, { value: "no", label: t("chat.stateNo") }]} />
              )}
            </div>
            {field === "archived" ? (reports.length > 0 && (
              // where it started from: what each service said when the chat first came (then it is ours alone)
              <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
                <span>{t("chat.archivedFrom")}</span>
                {reports.map((r) => <span key={r.service}>· {service(r.service).name}: {r.value ? t("chat.yes") : t("chat.no")}</span>)}
              </div>
            )) : <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
              <span>{t("chat.stateNow", { value: now ? t("chat.yes") : t("chat.no"), by: by === "user" ? t("chat.stateByYou") : by ? service(by).name : t("chat.stateAuto") })}</span>
              {reports.map((r) => <span key={r.service}>· {service(r.service).name}: {r.value ? t("chat.yes") : t("chat.no")}</span>)}
            </div>}
            {mine && field !== "archived" && (
              <label className="flex items-center gap-2 text-xs text-muted">
                <input type="checkbox" checked={!!mine.always} onChange={(e) => set.mutate({ [field]: !!mine.value, always: e.target.checked })} />
                {t("chat.stateAlways")}
              </label>
            )}
          </div>
        );
      })}
      <p className="text-xs text-muted">{t("chat.stateHint")}</p>
    </div>
  );
}
