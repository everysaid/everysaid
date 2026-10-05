import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { CalendarDays, MessageSquare, Phone, Users, UsersRound } from "lucide-react";
import { api, type Stats } from "@/lib/api";
import { dateOnly, isoDay, number } from "@/lib/format";
import { service } from "@/lib/services";
import { Avatar, Card, Center, Spinner } from "@/components/ui";
import { Logo } from "@/components/Logo";
import { PageHeader } from "@/components/PageHeader";

export function OverviewPage({ compact }: { compact?: boolean }) {
  const { t } = useTranslation();
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => api.get<Stats>("/api/stats"), staleTime: 300_000 });
  if (stats.isLoading) return <Center><Spinner /></Center>;
  const s = stats.data;
  if (!s) return null;
  const years = Object.entries(s.by_year);
  const maxYear = Math.max(...years.map(([, n]) => n), 1);
  const services = Object.entries(s.by_service).sort((a, b) => b[1] - a[1]);
  return (
    <div className="flex h-full flex-col">
      {!compact && <PageHeader title={t("overview.title")} />}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-6 p-4 md:p-8">
          {compact && (
            <div className="flex items-center gap-4 pb-2">
              <Logo className="size-14" />
              <div>
                <h1 className="text-2xl font-semibold tracking-tight">{t("app.name")}</h1>
                <p className="text-sm text-muted">{s.first ? t("overview.since", { date: dateOnly(s.first) }) : t("app.tagline")}</p>
              </div>
            </div>
          )}
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Tile icon={<MessageSquare />} label={t("overview.messages")} value={number(s.messages)} />
            <Tile icon={<Phone />} label={t("overview.calls")} value={number(s.calls)} />
            <Tile icon={<Users />} label={t("overview.people")} value={number(s.people)} />
            <Tile icon={<UsersRound />} label={t("overview.groups")} value={number(s.groups)} />
          </div>
          <div className="grid gap-4 lg:grid-cols-2">
            <Card className="p-5">
              <div className="mb-4 text-sm font-semibold">{t("overview.byService")}</div>
              <div className="space-y-2.5">
                {services.map(([k, n]) => (
                  <div key={k} className="flex items-center gap-3 text-sm">
                    <span className="w-24 shrink-0">{service(k).name}</span>
                    <div className="h-2.5 flex-1 overflow-hidden rounded-full bg-panel-2">
                      <div className="h-full rounded-full" style={{ width: `${(n / services[0][1]) * 100}%`, background: service(k).color }} />
                    </div>
                    <span className="w-20 text-right tabular-nums text-muted">{number(n)}</span>
                  </div>
                ))}
              </div>
            </Card>
            <Card className="p-5">
              <div className="mb-4 text-sm font-semibold">{t("overview.byYear")}</div>
              <div className="flex h-44 items-end gap-1">
                {years.map(([y, n]) => (
                  <div key={y} className="group flex flex-1 flex-col items-center gap-1" title={`${y}: ${number(n)}`}>
                    <div className="w-full rounded-t-md bg-accent/70 transition-colors group-hover:bg-accent" style={{ height: `${Math.max(2, (n / maxYear) * 150)}px` }} />
                    <span className="text-[10px] text-muted">{years.length > 12 ? y.slice(2) : y}</span>
                  </div>
                ))}
              </div>
            </Card>
          </div>
          <Card className="p-5">
            <div className="mb-3 flex items-center justify-between">
              <span className="text-sm font-semibold">{t("overview.top")}</span>
              <Link to="/day/$day" params={{ day: isoDay(Date.now()) }} className="flex items-center gap-1 text-sm text-accent"><CalendarDays className="size-4" />{t("day.title")}</Link>
            </div>
            <div className="grid gap-1 sm:grid-cols-2">
              {s.top_people.slice(0, 12).map((p, i) => (
                <Link key={p.chat_id} to="/chat/$chatId" params={{ chatId: p.chat_id }} className="flex items-center gap-3 rounded-xl px-2 py-1.5 hover:bg-panel-2">
                  <span className="w-5 text-right text-xs text-muted">{i + 1}</span>
                  <Avatar name={p.title} size={32} />
                  <span className="min-w-0 flex-1 truncate text-sm">{p.title}</span>
                  <span className="text-xs tabular-nums text-muted">{number(p.messages)}</span>
                </Link>
              ))}
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}

function Tile({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <Card className="p-4">
      <div className="mb-2 grid size-9 place-items-center rounded-xl bg-accent/12 text-accent [&_svg]:size-5">{icon}</div>
      <div className="text-2xl font-semibold tabular-nums tracking-tight">{value}</div>
      <div className="text-xs text-muted">{label}</div>
    </Card>
  );
}
