import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

/** The service worker: registered here; a new version is offered with a toast. */
export function UpdatePrompt() {
  const { t } = useTranslation();
  useEffect(() => {
    if (!("serviceWorker" in navigator) || import.meta.env.DEV) return;
    import("virtual:pwa-register").then(({ registerSW }) => {
      const update = registerSW({
        onNeedRefresh() {
          toast(t("settings.update"), { duration: Infinity, action: { label: t("settings.reload"), onClick: () => update(true) } });
        },
      });
    });
  }, [t]);
  return null;
}
