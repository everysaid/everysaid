// A text file shown as it is, a Markdown one as its formatting says (its HTML left out, never run).
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Failed } from "./DocumentViewer";

/** The most of a file shown; the rest only in the file downloaded. */
const shownBytes = 2 << 20;

/** Old text files are rarely UTF-8: those that are not are read as the language's own old
 *  encoding. */
const legacy: Record<string, string> = { el: "windows-1253", en: "windows-1252" };

/** The text of b; cut: b is the beginning of a file, so its last character may be cut in two (left
 *  out, not taken for a file that is not UTF-8). */
function decode(b: ArrayBuffer, lang: string, cut: boolean) {
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(b, { stream: cut });
  } catch {
    return new TextDecoder(legacy[lang] ?? "windows-1252").decode(b);
  }
}

export default function TextPages({ sha256, name, markdown }: { sha256: string; name: string; markdown: boolean }) {
  const { t, i18n } = useTranslation();
  const [text, setText] = useState<{ body: string; cut: boolean } | null | "failed">(null);
  useEffect(() => {
    let gone = false;
    setText(null);
    fetch(`/api/media/${sha256}/original`)
      .then((r) => (r.ok ? r.arrayBuffer() : Promise.reject(r.status)))
      .then((b) => {
        if (gone) return;
        const cut = b.byteLength > shownBytes;
        setText({ body: decode(cut ? b.slice(0, shownBytes) : b, i18n.language, cut), cut });
      })
      .catch(() => !gone && setText("failed"));
    return () => { gone = true; };
  }, [sha256, i18n.language]);

  if (text === null) return <div className="absolute inset-0 grid place-items-center"><Loader2 className="size-8 animate-spin opacity-70" /></div>;
  if (text === "failed") return <Failed sha256={sha256} name={name} />;
  return (
    <div className="absolute inset-0 overflow-auto bg-bg text-fg" data-document-pages>
      <div className="mx-auto max-w-4xl px-4 py-6">
        {markdown ? (
          <div className="markdown" data-document-markdown><Markdown remarkPlugins={[remarkGfm]} components={{ a: ({ node: _, ...p }) => <a {...p} target="_blank" rel="noopener noreferrer nofollow" /> }}>{text.body}</Markdown></div>
        ) : (
          <pre className="whitespace-pre-wrap break-words font-mono text-sm" data-document-text>{text.body}</pre>
        )}
        {text.cut && <p className="mt-6 border-t border-line pt-4 text-sm text-muted">{t("document.cut")}</p>}
      </div>
    </div>
  );
}
