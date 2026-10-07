import { ArrowLeft } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useBack } from "@/lib/back";

/** A page's title, with a way back to where it was reached from (on every screen size); `back`: where
 * back goes when the page was not reached from inside the app (a page of its section). */
export function PageHeader({ title, subtitle, back, actions }: { title: React.ReactNode; subtitle?: React.ReactNode; back?: string; actions?: React.ReactNode }) {
  const { t } = useTranslation();
  const way = useBack(back);
  return (
    <header className="flex items-center gap-2 bg-panel px-4 pb-3 pt-[max(1rem,env(safe-area-inset-top))] md:px-6">
      {way.show && (
        <button type="button" onClick={way.go} className="-ml-2 grid size-10 shrink-0 place-items-center rounded-full hover:bg-panel-2"
          aria-label={t("common.back")} title={t("common.back")} data-back>
          <ArrowLeft className="size-5" />
        </button>
      )}
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <div className="text-sm text-muted">{subtitle}</div>}
      </div>
      {actions}
    </header>
  );
}
