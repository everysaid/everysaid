// A document shown, not downloaded: a PDF (or the PDF the server made of an office document) in
// pdf.js, a text or Markdown file as text. Each kind's code is loaded only when one is opened.
import { lazy, Suspense } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useTranslation } from "react-i18next";
import { Download, X } from "lucide-react";

const PdfPages = lazy(() => import("./PdfPages"));
const TextPages = lazy(() => import("./TextPages"));

/** How a document is shown: "pdf" (also an office document, as the server's PDF), "text" or
 *  "markdown". */
export type DocumentKind = "pdf" | "text" | "markdown";

export default function DocumentViewer({ sha256, name, kind, onClose }: { sha256: string; name: string; kind: DocumentKind; onClose: () => void }) {
  const { t } = useTranslation();
  const button = "grid size-11 place-items-center rounded-full hover:bg-white/10";
  return (
    <DialogPrimitive.Root open onOpenChange={(o) => !o && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/95" />
        <DialogPrimitive.Content className="fixed inset-0 z-50 flex flex-col text-white outline-none" aria-describedby={undefined} data-document-viewer>
          <div className="flex items-center gap-1 p-2 pt-[max(0.5rem,env(safe-area-inset-top))]">
            <button onClick={onClose} className={button} aria-label={t("common.close")}><X /></button>
            <DialogPrimitive.Title className="min-w-0 flex-1 truncate px-2 text-sm opacity-80">{name || t("kind.file")}</DialogPrimitive.Title>
            <a href={`/api/media/${sha256}/original`} download={name} className={button} title={t("common.download")} aria-label={t("common.download")}><Download className="size-5" /></a>
          </div>
          <div className="relative min-h-0 flex-1">
            <Suspense>
              {kind === "pdf" ? <PdfPages sha256={sha256} name={name} /> : <TextPages sha256={sha256} name={name} markdown={kind === "markdown"} />}
            </Suspense>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

/** What is shown when a document cannot be: that, and its download. */
export function Failed({ sha256, name }: { sha256: string; name: string }) {
  const { t } = useTranslation();
  return (
    <div className="absolute inset-0 grid place-items-center p-6 text-center">
      <div className="space-y-4">
        <p className="opacity-80">{t("errors.no_preview")}</p>
        <a href={`/api/media/${sha256}/original`} download={name} className="inline-flex items-center gap-2 rounded-full bg-white px-4 py-2 text-sm text-black">
          <Download className="size-4" />{t("common.download")}
        </a>
      </div>
    </div>
  );
}
