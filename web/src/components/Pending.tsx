import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Clock, Paperclip, RotateCw, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { dropUnsent, flushUnsent, useUnsent } from "@/lib/outbox";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Spinner } from "@/components/ui";

// What of a chat waits to be sent, after its messages: this device's, not taken by the server yet
// (it could not be reached), and the server's, which it sends by itself when it can. Each can be sent
// again now, or discarded.

interface Kept {
  id: string;
  text: string;
  service: string;
  file_name: string;
  state: "queued" | "sending" | "failed";
  error: { code?: string; text?: string; params?: Record<string, unknown> } | null;
}

type Row = { id: string; text: string; service: string | null; file: string | null; state: "waiting" | "queued" | "sending" | "failed";
  why: string | null; again: () => void; discard: () => void };

export function Pending({ chatId }: { chatId: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const local = useUnsent(chatId);
  const kept = useQuery({ queryKey: ["outbox", chatId], queryFn: () => api.get<{ items: Kept[] }>(`/api/chats/${chatId}/outbox`) });
  const refresh = () => qc.invalidateQueries({ queryKey: ["outbox", chatId] });
  const fail = (e: unknown) => toast.error((e as Error).message);
  const said = (e: Kept["error"]) => !e ? null : e.text ?? t(`errors.${e.code}`, { ...e.params, defaultValue: e.code });
  const rows: Row[] = [
    ...(kept.data?.items ?? []).map((k): Row => ({
      id: k.id, text: k.text, service: k.service || null, file: k.file_name || null, state: k.state, why: said(k.error),
      again: () => { api.post(`/api/outbox/${k.id}/retry`).then(refresh, fail); },
      discard: () => { api.del(`/api/outbox/${k.id}`).then(refresh, fail); },
    })),
    ...local.filter((u) => !kept.data?.items.some((k) => k.id === u.id)).map((u): Row => ({
      id: u.id, text: u.text, service: u.service, file: u.fileName, state: u.state, why: u.error,
      again: () => { flushUnsent(u.id).then(refresh); },
      discard: () => { dropUnsent(u.id); },
    })),
  ];
  if (!rows.length) return null;
  return (
    <div data-pending className="flex flex-col items-end gap-1.5 px-3 pb-2 md:px-6">
      {rows.map((r) => (
        <div key={r.id} data-pending-item={r.state} className="flex max-w-[85%] flex-col items-end">
          <div className="rounded-bubble bg-bubble-out px-3 py-2 text-[15px] leading-snug text-bubble-out-fg opacity-70 shadow-sm">
            {r.file && <div className="mb-1 flex items-center gap-1.5 text-sm"><Paperclip className="size-3.5" />{r.file}</div>}
            {r.text && <div className="whitespace-pre-wrap break-words">{r.text}</div>}
          </div>
          <div className={cn("mt-1 flex flex-wrap items-center justify-end gap-x-2 gap-y-1 text-xs", r.state === "failed" ? "text-danger" : "text-muted")}>
            {r.state === "sending" ? <Spinner className="size-3.5" /> : r.state !== "failed" && <Clock className="size-3.5" />}
            <span>
              {r.state === "failed" ? t("chat.sendFailed") : r.state === "waiting" ? t("chat.waitingServer") : r.state === "sending" ? t("chat.sending") : t("chat.waitingService")}
              {r.service && ` · ${service(r.service).name}`}
              {r.why && `: ${r.why}`}
            </span>
            {r.state !== "sending" && (
              <>
                <button data-resend onClick={r.again} className="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 font-medium text-accent hover:bg-panel-2">
                  <RotateCw className="size-3.5" />{t("chat.resend")}
                </button>
                <button data-discard onClick={r.discard} className="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-muted hover:bg-panel-2 hover:text-fg">
                  <X className="size-3.5" />{t("chat.discard")}
                </button>
              </>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
