import type { Attachment } from "./api";

// A file of the archive copied to the clipboard: its address as text (the name after #, where the
// server never sees it), and a picture as PNG too, for other apps. Pasted in a chat here, the address
// brings the file itself back, as it is, whatever service it came from or goes to.
const LINK = /^(https?:\/\/[^/\s]+)\/api\/media\/([0-9a-f]{64})\/original(?:#name=([^\s]*))?$/;

export function copyText(text: string) {
  return navigator.clipboard.writeText(text);
}

export async function copyFile(a: Attachment) {
  const link = `${location.origin}/api/media/${a.sha256}/original${a.name ? `#name=${encodeURIComponent(a.name)}` : ""}`;
  if (a.mime?.startsWith("image/") && typeof ClipboardItem !== "undefined") {
    try {
      await navigator.clipboard.write([new ClipboardItem({
        "text/plain": new Blob([link], { type: "text/plain" }),
        "image/png": png(`/api/media/${a.sha256}/original`),
      })]);
      return;
    } catch { /* one the browser cannot draw (HEIC), or no pictures on the clipboard: the address alone */ }
  }
  await navigator.clipboard.writeText(link);
}

async function png(url: string): Promise<Blob> {
  const r = await fetch(url);
  if (!r.ok) throw new Error(String(r.status));
  const img = await createImageBitmap(await r.blob());
  const c = document.createElement("canvas");
  c.width = img.width;
  c.height = img.height;
  c.getContext("2d")!.drawImage(img, 0, 0);
  return new Promise((ok, no) => c.toBlob((b) => (b ? ok(b) : no(new Error("png"))), "image/png"));
}

/** Whether a pasted text is the address of a file of this archive (one copied here). */
export function isFileLink(text: string) {
  const m = LINK.exec(text.trim());
  return !!m && m[1] === location.origin;
}

/** The archive's file a pasted address (isFileLink) is of. */
export async function pastedFile(text: string): Promise<File> {
  const m = LINK.exec(text.trim())!;
  const r = await fetch(`/api/media/${m[2]}/original`);
  if (!r.ok) throw new Error(String(r.status));
  const b = await r.blob();
  let name = "";
  try { name = decodeURIComponent(m[3] ?? ""); } catch { /* not one copied here */ }
  if (!name) name = `${m[2].slice(0, 12)}.${b.type.split("/")[1]?.split(/[;+]/)[0] || "bin"}`;
  return new File([b], name, { type: b.type });
}
