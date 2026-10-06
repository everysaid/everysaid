import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Check, CheckCheck, Clock, Play } from "lucide-react";
import { api, type MessageItem, type Receipt } from "@/lib/api";
import { dateOnly, time } from "@/lib/format";
import { Avatar, Center, Dialog, Spinner } from "./ui";

/** Who got, read and played one of the user's messages, and when, as the service told. */
export function MessageInfo({ m, onClose }: { m: MessageItem | null; onClose: () => void }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["receipts", m?.id],
    queryFn: () => api.get<{ items: Receipt[] }>(`/api/messages/${m!.id}/receipts`),
    enabled: !!m,
  });
  const items = q.data?.items ?? [];
  const read = items.filter((r) => r.read_at != null);
  const delivered = items.filter((r) => r.read_at == null && r.delivered_at != null);
  const waiting = items.filter((r) => r.read_at == null && r.delivered_at == null);
  return (
    <Dialog open={!!m} onOpenChange={(o) => !o && onClose()} title={t("chat.messageInfo")}>
      {m && (
        <div data-message-info className="space-y-4">
          <div className="line-clamp-3 rounded-xl bg-panel-2 px-3 py-2 text-sm">{m.text || t(`kind.${m.kind}`, { defaultValue: m.kind })}</div>
          {q.isLoading ? (
            <Center className="py-6"><Spinner /></Center>
          ) : !items.length ? (
            <div className="text-sm text-muted">{t("chat.noReceipts")}</div>
          ) : (
            <>
              <Part title={t("chat.readBy")} icon={<CheckCheck className="size-4 text-accent" />} items={read} when={(r) => r.read_at}
                extra={(r) => r.played_at != null && <span className="flex items-center gap-1"><Play className="size-3" />{stamp(r.played_at, t)}</span>} />
              <Part title={t("chat.deliveredTo")} icon={<CheckCheck className="size-4 text-muted" />} items={delivered} when={(r) => r.delivered_at} />
              <Part title={t("chat.notYet")} icon={<Clock className="size-4 text-muted" />} items={waiting} when={() => null} />
            </>
          )}
        </div>
      )}
    </Dialog>
  );
}

function stamp(ms: number | null, t: (k: string) => string) {
  if (ms == null) return "";
  return ms === 0 ? t("chat.whenUnknown") : `${dateOnly(ms)} ${time(ms)}`;
}

function Part({ title, icon, items, when, extra }: {
  title: string; icon: React.ReactNode; items: Receipt[]; when: (r: Receipt) => number | null; extra?: (r: Receipt) => React.ReactNode;
}) {
  const { t } = useTranslation();
  if (!items.length) return null;
  return (
    <section>
      <h3 className="mb-1 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted">{icon}{title} · {items.length}</h3>
      <ul className="divide-y divide-line">
        {items.map((r, i) => (
          <li key={`${r.person_id}:${i}`} className="flex items-center gap-3 py-2">
            <Avatar name={r.name || "?"} size={32} />
            <span className="min-w-0 flex-1 truncate text-sm">{r.name || t("common.unknown")}</span>
            <span className="flex flex-col items-end text-xs text-muted">
              {when(r) != null && <span className="flex items-center gap-1"><Check className="size-3" />{stamp(when(r), t)}</span>}
              {extra?.(r)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
