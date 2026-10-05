import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { api, type StreamItem } from "@/lib/api";
import { dayLabel, isoDay, time } from "@/lib/format";
import { service } from "@/lib/services";
import { dayRoute } from "@/router";
import { Button, Center, Empty, Spinner } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";

export function DayPage() {
  const { t } = useTranslation();
  const { day } = dayRoute.useParams();
  const navigate = useNavigate();
  const res = useQuery({ queryKey: ["day", day], queryFn: () => api.get<{ items: StreamItem[]; truncated: boolean }>(`/api/timeline?day=${day}`) });
  const d = new Date(day + "T12:00:00");
  const shift = (n: number) => navigate({ to: "/day/$day", params: { day: isoDay(d.getTime() + n * 86400_000) } });
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={dayLabel(d.getTime())} subtitle={t("day.title")} actions={
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="icon" className="rounded-full" onClick={() => shift(-1)} aria-label={t("common.previous")}><ChevronLeft /></Button>
          <input type="date" value={day} onChange={(e) => e.target.value && navigate({ to: "/day/$day", params: { day: e.target.value } })}
            className="h-9 rounded-xl border border-line bg-panel px-2 text-sm" />
          <Button variant="ghost" size="icon" className="rounded-full" onClick={() => shift(1)} aria-label={t("common.next")}><ChevronRight /></Button>
        </div>
      } />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {res.isLoading ? <Center><Spinner /></Center> : !res.data?.items.length ? <Empty title={t("day.empty")} /> : (
          <ol className="mx-auto max-w-3xl space-y-1 p-4 md:p-6">
            {res.data.items.map((i) => (
              <li key={i.cursor}>
                <Link to="/chat/$chatId" params={{ chatId: i.chat_id ?? "" }} search={i.type === "message" ? { m: i.id } : { ts: i.ts + 1 }}
                  className="flex gap-3 rounded-xl px-3 py-2 text-sm hover:bg-panel-2">
                  <span className="w-12 shrink-0 tabular-nums text-muted">{time(i.ts)}</span>
                  <span className="mt-1.5 size-2 shrink-0 rounded-full" style={{ background: service(i.service).color }} />
                  <span className="min-w-0 flex-1">
                    <span className="font-medium">{i.type === "call" ? `📞 ${i.with ?? ""}` : (i.outgoing ? t("common.me") : i.sender || i.chat_title || "")}</span>{" "}
                    <span className="text-muted">{i.type === "message" ? (i.text ?? `[${t(`kind.${i.kind}`, { defaultValue: i.kind })}]`).slice(0, 200) : ""}</span>
                  </span>
                </Link>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}
