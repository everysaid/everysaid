import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useWide } from "@/lib/hooks";

export function PageHeader({ title, subtitle, back, actions }: { title: React.ReactNode; subtitle?: React.ReactNode; back?: string; actions?: React.ReactNode }) {
  const wide = useWide();
  const { t } = useTranslation();
  return (
    <header className="flex items-center gap-2 bg-panel px-4 pb-3 pt-[max(1rem,env(safe-area-inset-top))] md:px-6">
      {back && !wide && (
        <Link to={back} className="-ml-2 grid size-10 place-items-center rounded-full hover:bg-panel-2" aria-label={t("common.back")}>
          <ArrowLeft className="size-5" />
        </Link>
      )}
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <div className="text-sm text-muted">{subtitle}</div>}
      </div>
      {actions}
    </header>
  );
}
