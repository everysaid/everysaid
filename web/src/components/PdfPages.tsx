// A PDF's pages as Firefox shows them, with pdf.js: the PDF itself, or the one the server made of an
// office document. Loaded only when such a document is opened (pdf.js is large).
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Minus, MoveHorizontal, Plus } from "lucide-react";
import * as pdfjsLib from "pdfjs-dist";
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import "pdfjs-dist/web/pdf_viewer.css";
import { Failed } from "./DocumentViewer";

declare const __PDFJS_DATA__: string; // vite.config.ts: where pdf.js's data is served

pdfjsLib.GlobalWorkerOptions.workerSrc = workerUrl;
// pdf.js's viewer finds the library here
(globalThis as unknown as { pdfjsLib: typeof pdfjsLib }).pdfjsLib = pdfjsLib;
const viewerModule = import("pdfjs-dist/web/pdf_viewer.mjs");

export default function PdfPages({ sha256, name }: { sha256: string; name: string }) {
  const { t } = useTranslation();
  const container = useRef<HTMLDivElement>(null);
  const viewer = useRef<import("pdfjs-dist/web/pdf_viewer.mjs").PDFViewer | null>(null);
  const [state, setState] = useState<"loading" | "shown" | "failed">("loading");

  useEffect(() => {
    let task: ReturnType<typeof pdfjsLib.getDocument> | null = null;
    let gone = false;
    (async () => {
      const { EventBus, PDFLinkService, PDFViewer, LinkTarget } = await viewerModule;
      if (gone || !container.current) return;
      const eventBus = new EventBus();
      const links = new PDFLinkService({ eventBus, externalLinkTarget: LinkTarget.BLANK, externalLinkRel: "noopener noreferrer nofollow" });
      const v = new PDFViewer({ container: container.current, eventBus, linkService: links });
      links.setViewer(v);
      viewer.current = v;
      eventBus.on("pagesinit", () => { v.currentScaleValue = "page-width"; });
      task = pdfjsLib.getDocument({
        url: `/api/media/${sha256}/document`,
        cMapUrl: `${__PDFJS_DATA__}cmaps/`,
        standardFontDataUrl: `${__PDFJS_DATA__}standard_fonts/`,
        wasmUrl: `${__PDFJS_DATA__}wasm/`,
        enableXfa: false,
      });
      const doc = await task.promise;
      if (gone) return;
      v.setDocument(doc);
      links.setDocument(doc, null);
      setState("shown");
    })().catch(() => !gone && setState("failed"));
    return () => {
      gone = true;
      viewer.current = null;
      task?.destroy();
    };
  }, [sha256]);

  const zoom = (by: number) => {
    const v = viewer.current;
    if (v) v.currentScale = Math.min(8, Math.max(0.25, v.currentScale * by));
  };
  const fit = () => {
    if (viewer.current) viewer.current.currentScaleValue = "page-width";
  };
  const button = "grid size-11 place-items-center rounded-full hover:bg-white/10";
  return (
    <>
      {/* pdf.js wants its container absolutely placed, the pages in a .pdfViewer inside it */}
      <div ref={container} className="absolute inset-0 overflow-auto bg-neutral-700" data-document-pages>
        <div className="pdfViewer" />
      </div>
      {state === "loading" && <div className="absolute inset-0 grid place-items-center"><Loader2 className="size-8 animate-spin opacity-70" /></div>}
      {state === "failed" && <Failed sha256={sha256} name={name} />}
      {state === "shown" && (
        <div className="absolute bottom-[max(1rem,env(safe-area-inset-bottom))] right-4 flex rounded-full bg-black/70 backdrop-blur">
          <button onClick={() => zoom(1 / 1.25)} className={button} title={t("document.zoomOut")} aria-label={t("document.zoomOut")}><Minus className="size-5" /></button>
          <button onClick={fit} className={button} title={t("document.fitWidth")} aria-label={t("document.fitWidth")}><MoveHorizontal className="size-5" /></button>
          <button onClick={() => zoom(1.25)} className={button} title={t("document.zoomIn")} aria-label={t("document.zoomIn")}><Plus className="size-5" /></button>
        </div>
      )}
    </>
  );
}
