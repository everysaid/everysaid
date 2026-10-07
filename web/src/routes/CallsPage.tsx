import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Virtuoso } from "react-virtuoso";
import { Phone, PhoneIncoming, PhoneMissed, PhoneOutgoing, Video } from "lucide-react";
import { api, qs, type CallItem } from "@/lib/api";
import { duration, fullDate } from "@/lib/format";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Avatar, Empty, LoadingBar, Segmented, Spinner } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";
import { ChatFilter } from "@/components/ChatFilter";
import { callsRoute } from "@/router";

export function CallsPage() {
  const { t } = useTranslation();
  const [missed, setMissed] = useState<"all" | "missed">("all");
  const { chat } = callsRoute.useSearch();
  const navigate = useNavigate();
  const res = useInfiniteQuery({
    queryKey: ["calls", missed, chat],
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) => api.get<{ items: CallItem[]; has_more: boolean }>(`/api/calls${qs({ chat, missed: missed === "missed" || undefined, before: pageParam, limit: 80 })}`),
    getNextPageParam: (last) => (last.has_more ? last.items[last.items.length - 1]?.ts : undefined),
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("nav.calls")} back={chat ? `/chat/${chat}` : undefined} actions={
        <Segmented value={missed} onChange={setMissed} options={[{ value: "all", label: t("call.all") }, { value: "missed", label: t("call.missedOnly") }]} />
      } />
      {chat && <div className="flex bg-panel px-4 pb-3 md:px-6"><ChatFilter chat={chat} onClear={() => navigate({ to: "/calls", search: {} })} /></div>}
      <div className="relative min-h-0 flex-1 bg-panel">
        <LoadingBar active={res.isFetching} />
        {res.isLoading ? <div className="grid h-40 place-items-center"><Spinner /></div> : items.length === 0 ? (
          <Empty icon={<Phone />} title={t("common.none")} />
        ) : (
          <Virtuoso
            data={items}
            endReached={() => res.hasNextPage && !res.isFetchingNextPage && res.fetchNextPage()}
            components={{ Footer: () => (res.isFetchingNextPage ? <div className="flex justify-center py-4"><Spinner /></div> : null) }}
            itemContent={(_, c) => {
              const miss = !c.outgoing && !c.answered;
              const Icon = c.video ? Video : miss ? PhoneMissed : c.outgoing ? PhoneOutgoing : PhoneIncoming;
              const row = (
                <div className="mx-auto flex max-w-3xl items-center gap-3 px-4 py-2.5 hover:bg-panel-2 md:px-6">
                  <Avatar name={c.with || "?"} size={44} />
                  <div className="min-w-0 flex-1">
                    <div className={cn("truncate font-medium", miss && "text-danger")}>{c.with || t("common.unknown")}</div>
                    <div className="flex items-center gap-1.5 text-xs text-muted">
                      <Icon className={cn("size-3.5", miss && "text-danger")} />
                      <span>{service(c.service).name}</span>
                      {c.duration > 0 && <span>· {duration(c.duration)}</span>}
                      {c.attempts > 1 && <span>· {t("call.times", { n: c.attempts })}</span>}
                    </div>
                  </div>
                  <span className="shrink-0 text-xs text-muted">{fullDate(c.ts)}</span>
                </div>
              );
              return c.chat_id ? <Link to="/chat/$chatId" params={{ chatId: c.chat_id }} search={{ ts: c.ts + 1 }}>{row}</Link> : row;
            }}
          />
        )}
      </div>
    </div>
  );
}
