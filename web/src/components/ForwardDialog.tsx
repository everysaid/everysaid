import { useEffect, useState } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, FileText, Forward } from "lucide-react";
import { toast } from "sonner";
import { api, thumbUrl, type ChatDetail, type ChatSummary, type MessageItem } from "@/lib/api";
import { avatarUrl } from "@/lib/avatar";
import { useChats, useDebounced } from "@/lib/hooks";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Avatar, Button, Dialog, Input, ServiceBadge } from "./ui";

/** What became of the message in one chat (POST /api/messages/{id}/forward). */
interface Forwarded {
  chat: string;
  service: string;
  forwarded: boolean;        // by its service's own forward; else sent as a message of its own
  queued: boolean;           // kept, to go when it can
  error?: { code: string; text?: string; params?: Record<string, unknown> };
}

/** The service a chat is forwarded to by default: the message's own where the chat has it and it
 *  can be sent to, else the way the chat is written to. */
function byDefault(m: MessageItem, c: ChatSummary, d: ChatDetail | undefined): string | null {
  if (!d) return null;
  if (c.services.includes(m.service) && d.sendable.includes(m.service)) return m.service;
  if (d.last_service && d.sendable.includes(d.last_service)) return d.last_service;
  return d.sendable[0] ?? null;
}

/** A message sent on to other chats, chosen here, each through a service (its own preferred, by its
 *  forward; any other the chat can be written to, as a message of its own). */
export function ForwardDialog({ m, onClose }: { m: MessageItem | null; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const [chosen, setChosen] = useState<ChatSummary[]>([]);
  const [picked, setPicked] = useState<Record<string, string>>({});    // a service the user chose for a chat
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (m) { setQ(""); setChosen([]); setPicked({}); } }, [m]);
  const found = useChats({ q: dq || undefined });
  const list = (found.data?.items ?? []).slice(0, 40);
  const details = useQueries({ queries: chosen.map((c) => ({ queryKey: ["chat", c.id], queryFn: () => api.get<ChatDetail>(`/api/chats/${c.id}`) })) });
  const detailOf = (id: string) => details[chosen.findIndex((c) => c.id === id)]?.data;
  const via = (c: ChatSummary) => picked[c.id] ?? (m ? byDefault(m, c, detailOf(c.id)) : null);
  const toggle = (c: ChatSummary) => setChosen((was) => (was.some((x) => x.id === c.id) ? was.filter((x) => x.id !== c.id) : [...was, c]));
  const send = async () => {
    if (!m || !chosen.length) return;
    setBusy(true);
    try {
      const r = await api.post<{ items: Forwarded[] }>(`/api/messages/${m.id}/forward`,
        { chats: chosen.map((c) => (via(c) ? { chat: c.id, service: via(c) } : c.id)) });
      const title = (id: string) => chosen.find((c) => c.id === id)?.title ?? id;
      const failed = r.items.filter((x) => x.error);
      const went = r.items.length - failed.length;
      if (went) toast.success(t("chat.forwardedTo", { count: went }));
      if (r.items.some((x) => !x.error && x.queued)) toast(t("chat.forwardQueued"));
      for (const x of failed) toast.error(`${title(x.chat)}: ${x.error!.text ?? t(`errors.${x.error!.code}`, { ...x.error!.params, defaultValue: x.error!.code })}`);
      qc.invalidateQueries({ queryKey: ["chats"] });
      qc.invalidateQueries({ queryKey: ["outbox"] });
      onClose();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const file = m?.attachments.find((a) => a.available !== "gone");
  const picture = file && (file.mime?.startsWith("image/") || file.mime?.startsWith("video/"));
  return (
    <Dialog open={!!m} onOpenChange={(o) => { if (!o) onClose(); }} title={t("chat.forward")} description={t("chat.forwardHint")}>
      {m && (
        // what is forwarded
        <div data-forward-preview className="mb-3 flex items-center gap-3 rounded-2xl border border-line bg-panel-2/50 p-2">
          {picture ? <img src={thumbUrl(file.sha256)} alt="" className="size-12 shrink-0 rounded-lg object-cover" />
            : file ? <FileText className="size-8 shrink-0 text-muted" /> : null}
          <div className="min-w-0 flex-1 text-sm">
            {m.text ? <p className="line-clamp-2 whitespace-pre-wrap break-words">{m.text}</p>
              : <p className="truncate text-muted">{file?.name ?? t(`kind.${m.kind}`, { defaultValue: m.kind })}</p>}
            <div className="mt-0.5 text-[11px] text-muted">{m.sender ?? (m.outgoing ? t("chat.fromMe") : "")}{m.sender || m.outgoing ? " · " : ""}{service(m.service).name}</div>
          </div>
        </div>
      )}
      <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("chat.searchChats")} />
      <div data-forward-list className="mt-3 max-h-80 divide-y divide-line overflow-y-auto overflow-x-hidden">
        {list.map((c) => {
          const on = chosen.some((x) => x.id === c.id);
          const d = on ? detailOf(c.id) : undefined;
          const now = on ? via(c) : null;
          return (
            <div key={c.id} className="py-1">
              <button data-forward-chat={c.id} aria-pressed={on} onClick={() => toggle(c)}
                className="flex w-full min-w-0 items-center gap-3 rounded-xl px-1 py-1 text-left hover:bg-panel-2">
                <Avatar name={c.title} src={avatarUrl(c)} group={c.type === "group"} size={36} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{c.title}</span>
                  {!on && <span className="block truncate text-[11px] text-muted">{c.services.map((s) => service(s).name).join(" · ")}</span>}
                </span>
                <span className={cn("grid size-5 shrink-0 place-items-center rounded-full border", on ? "border-accent bg-accent text-white" : "border-line")}>
                  {on && <Check className="size-3.5" />}
                </span>
              </button>
              {on && (
                // through which service: the message's own first (a forward), any other the chat can be written to
                <div data-forward-via={c.id} className="flex flex-wrap items-center gap-1.5 pb-1 pl-12">
                  {d && !d.sendable.length && <span className="text-xs text-danger">{t("errors.chat.no_sender")}</span>}
                  {d?.sendable.map((s) => (
                    <button key={s} data-service={s} aria-pressed={now === s} onClick={() => setPicked((p) => ({ ...p, [c.id]: s }))}
                      className={cn("rounded-full ring-offset-1 ring-offset-panel", now === s ? "ring-2 ring-accent" : "opacity-60 hover:opacity-100")}>
                      <ServiceBadge id={s} />
                    </button>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
      <div className="flex items-center justify-between gap-2 pt-3">
        <span className="text-sm text-muted">{chosen.length ? t("chat.chosenChats", { count: chosen.length }) : ""}</span>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
          <Button data-forward-send variant="primary" disabled={!chosen.length} loading={busy} onClick={send}>
            <Forward className="size-4" />{t("chat.forwardSend")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
