import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef } from "react";
import { isFileLink } from "@/lib/clip";
import { LOOK, parts, type Kind, type Style } from "@/lib/styles";
import { cn } from "@/lib/utils";

/** The field as the composer uses it, as it used a textarea's: where the selection is, and setting it. */
export type FieldHandle = {
  el: HTMLDivElement | null;
  focus: () => void;
  readonly selectionStart: number;
  readonly selectionEnd: number;
  setSelectionRange: (start: number, end: number) => void;
  /** Where a place of the text is on the screen (its line's box), null where it cannot say. */
  rectAt: (at: number) => DOMRect | null;
};

/** A text and its styles, with where the caret is: what undo brings back. */
export type Doc = { text: string; styles: Style[] };

/** The text written as its styles show it: plain text where it has none (as the field's own text
 * nodes, "\n" in them), a span for each part with some; a closing break where it ends with a line
 * break (or the last line would not show). off: kinds the service it goes to does not show, marked so. */
function html(text: string, styles: Style[], off: Kind[], offTitle: string): string {
  const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/\u00a0/g, "&nbsp;");
  // an attribute as innerHTML says it (only & and " written otherwise), to compare
  const attr = (name: string, v: string) => `${name}="${v.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/\u00a0/g, "&nbsp;")}"`;
  let out = "";
  for (const p of parts(text, styles)) {
    if (!p.kinds.length) { out += esc(p.text); continue; }
    const missing = p.kinds.some((k) => off.includes(k));
    const cls = cn(p.kinds.map((k) => LOOK[k]), missing && "border-b border-dashed border-muted");
    const hint = missing ? ` ${attr("title", offTitle)}` : "";
    out += `<span ${attr("data-k", p.kinds.join(" "))} ${attr("class", cls)}${hint}>${esc(p.text)}</span>`;
  }
  return text.endsWith("\n") ? out + '<br data-end="">' : out;   // as innerHTML says it, to compare
}

/** The text the field holds, as the browser left it (text it put in, a line it broke as a block or a break). */
function textOf(root: Node): string {
  let out = "";
  const walk = (n: Node) => {
    for (const c of Array.from(n.childNodes)) {
      if (c.nodeType === Node.TEXT_NODE) out += (c as Text).data;
      else if (c.nodeName === "BR") { if (!(c as HTMLElement).hasAttribute("data-end") && c !== root.lastChild) out += "\n"; }
      else {
        if ((c.nodeName === "DIV" || c.nodeName === "P") && out && !out.endsWith("\n")) out += "\n";
        walk(c);
      }
    }
  };
  walk(root);
  return out;
}

/** Where a place of the DOM (a node and an offset in it) is in the text. */
function offsetOf(root: Node, node: Node, offset: number): number {
  let count = 0;
  let found = -1;
  const walk = (n: Node): boolean => {
    if (n === node && n.nodeType !== Node.TEXT_NODE) {
      for (let i = 0; i < offset && i < n.childNodes.length; i++) count += textOf(wrap(n.childNodes[i])).length;
      found = count;
      return true;
    }
    for (const c of Array.from(n.childNodes)) {
      if (c === node && c.nodeType === Node.TEXT_NODE) { found = count + offset; return true; }
      if (c.nodeType === Node.TEXT_NODE) count += (c as Text).data.length;
      else if (c.nodeName === "BR") { if (!(c as HTMLElement).hasAttribute("data-end") && c !== root.lastChild) count++; }
      else if (walk(c)) return true;
    }
    return false;
  };
  walk(root);
  return found < 0 ? count : found;
}

/** A node alone, to count its text as textOf does. */
function wrap(n: Node): Node {
  const d = document.createElement("div");
  d.appendChild(n.cloneNode(true));
  return d;
}

/** The DOM place of a place in the text (the field as html wrote it: text nodes, spans of them). */
function placeOf(root: Node, at: number): [Node, number] {
  let left = at;
  let last: [Node, number] = [root, 0];
  const walk = (n: Node): [Node, number] | null => {
    for (const c of Array.from(n.childNodes)) {
      if (c.nodeType === Node.TEXT_NODE) {
        const len = (c as Text).data.length;
        if (left <= len) return [c, left];
        left -= len;
        last = [c, len];
      } else if (c.nodeName !== "BR") {
        const r = walk(c);
        if (r) return r;
      }
    }
    return null;
  };
  return walk(root) ?? last;
}

type Props = {
  value: string;
  styles: Style[];
  off?: Kind[];
  offTitle?: string;
  disabled?: boolean;
  placeholder?: string;
  className?: string;
  role?: string;
  "aria-expanded"?: boolean;
  /** The user changed the text: it, and where the caret is. */
  onText: (text: string, caret: number) => void;
  /** Where the selection is now. */
  onSelect?: (start: number, end: number) => void;
  /** Undo or redo: the text and styles to come back, the caret at the end of what changed. */
  onRestore: (doc: Doc, caret: number) => void;
  onKeyDown?: (e: React.KeyboardEvent<HTMLDivElement>) => void;
};

/** The composer's field: a text with styles shown as they are (bold as bold, no marks), whatever
 * service it goes to. The text and styles are the composer's; the field shows them, says what the
 * user typed, and keeps the undo of both. */
export const RichField = forwardRef<FieldHandle, Props>(function RichField(
  { value, styles, off = [], offTitle = "", disabled, placeholder, className, role, onText, onSelect, onRestore, onKeyDown, ...aria }, ref) {
  const div = useRef<HTMLDivElement>(null);
  const composing = useRef(false);
  const pointed = useRef(false);               // a click or touch on it: it places the caret itself
  const sel = useRef<[number, number]>([value.length, value.length]);   // kept while the focus is elsewhere (the emoji picker)
  const shown = useRef("");
  const props = useRef({ onText, onSelect, onRestore, value });
  props.current = { onText, onSelect, onRestore, value };

  const selection = (): [number, number] => {
    const el = div.current, s = window.getSelection();
    if (!el || !s || !s.rangeCount || !el.contains(s.anchorNode) || !el.contains(s.focusNode)) return sel.current;
    const a = offsetOf(el, s.anchorNode!, s.anchorOffset), b = offsetOf(el, s.focusNode!, s.focusOffset);
    return [Math.min(a, b), Math.max(a, b)];
  };
  const select = (start: number, end: number) => {
    const el = div.current;
    if (!el) return;
    sel.current = [start, end];
    if (document.activeElement !== el) return;
    const [an, ao] = placeOf(el, start), [fn, fo] = placeOf(el, end);
    const s = window.getSelection();
    s?.setBaseAndExtent(an, ao, fn, fo);
  };
  useImperativeHandle(ref, () => ({
    get el() { return div.current; },
    focus: () => {
      const el = div.current;
      if (!el || document.activeElement === el) return;
      el.focus();
      select(...sel.current);
    },
    get selectionStart() { return selection()[0]; },
    get selectionEnd() { return selection()[1]; },
    setSelectionRange: (start: number, end: number) => select(start, end),
    rectAt: (at: number) => {
      const el = div.current;
      if (!el) return null;
      const [node, offset] = placeOf(el, at);
      const r = document.createRange();
      r.setStart(node, offset);
      r.setEnd(node, offset);
      const box = r.getClientRects()[0] ?? (node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as Element))?.getBoundingClientRect();
      return box ?? null;
    },
  }));

  // the text as its styles show it, written anew where it is not so already (not while a word is
  // being composed: the keyboard's own; once it is, as it should be)
  const want = html(value, styles, off, offTitle);
  const sync = () => {
    const el = div.current;
    if (!el || composing.current || (shown.current === want && el.innerHTML === want)) return;
    const focused = document.activeElement === el;
    if (!focused && el.textContent !== value) sel.current = [value.length, value.length];   // a text given while away: the caret at its end, as a textarea's
    const [a, b] = focused ? selection() : sel.current;
    el.innerHTML = want;
    shown.current = want;
    if (focused) select(Math.min(a, value.length), Math.min(b, value.length));
  };
  useLayoutEffect(sync);

  // undo and redo of the text with its styles: a step for each pause in typing, or each other change
  const history = useRef<{ past: Doc[]; future: Doc[]; last: Doc; at: number; restoring: boolean }>(
    { past: [], future: [], last: { text: value, styles }, at: 0, restoring: false });
  useEffect(() => {
    const h = history.current;
    if (h.last.text === value && h.last.styles === styles) return;
    if (h.restoring) { h.restoring = false; h.last = { text: value, styles }; return; }
    const now = Date.now();
    const typing = h.last.styles === styles && Math.abs(value.length - h.last.text.length) === 1 && now - h.at < 1000;
    if (!typing) h.past.push(h.last);
    if (h.past.length > 200) h.past.shift();
    h.future = [];
    h.last = { text: value, styles };
    h.at = now;
  }, [value, styles]);
  const restore = (back: boolean) => {
    const h = history.current;
    const doc = (back ? h.past : h.future).pop();
    if (!doc) return;
    (back ? h.future : h.past).push(h.last);
    h.restoring = true;
    h.at = 0;
    let p = 0;                                  // the caret where the two differ
    while (p < doc.text.length && doc.text[p] === h.last.text[p]) p++;
    let e = 0;
    while (e < doc.text.length - p && e < h.last.text.length - p && doc.text[doc.text.length - 1 - e] === h.last.text[h.last.text.length - 1 - e]) e++;
    props.current.onRestore(doc, doc.text.length - e);
  };

  const put = (s: string) => {                    // text put in place of the selection
    const [a, b] = selection();
    const v = props.current.value;
    props.current.onText(v.slice(0, a) + s + v.slice(b), a + s.length);
    sel.current = [a + s.length, a + s.length];
  };

  useEffect(() => {
    const el = div.current;
    if (!el) return;
    const before = (e: InputEvent) => {
      switch (e.inputType) {
        case "insertParagraph":
        case "insertLineBreak":
          e.preventDefault();
          put("\n");
          return;
        case "historyUndo":
        case "historyRedo":
          e.preventDefault();
          restore(e.inputType === "historyUndo");
          return;
        case "insertFromDrop":
          e.preventDefault();
          put(e.dataTransfer?.getData("text/plain") ?? "");
          return;
      }
      if (e.inputType.startsWith("format")) e.preventDefault();       // the composer's own, by its keys
    };
    const moved = () => {
      const s = window.getSelection();
      if (!s || !el.contains(s.anchorNode)) return;
      sel.current = selection();
      props.current.onSelect?.(...sel.current);
    };
    el.addEventListener("beforeinput", before);
    document.addEventListener("selectionchange", moved);
    return () => { el.removeEventListener("beforeinput", before); document.removeEventListener("selectionchange", moved); };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div
      ref={div}
      contentEditable={!disabled}
      suppressContentEditableWarning
      role={role ?? "textbox"}
      aria-multiline
      aria-disabled={disabled || undefined}
      aria-placeholder={placeholder}
      aria-expanded={aria["aria-expanded"]}
      data-placeholder={placeholder}
      data-composer-field
      className={cn("min-h-10 w-full overflow-y-auto whitespace-pre-wrap break-words px-3 py-2 text-sm outline-none",
        "empty:before:pointer-events-none empty:before:text-muted empty:before:content-[attr(data-placeholder)]",
        disabled && "cursor-not-allowed", className)}
      onInput={() => {
        const el = div.current!;
        const text = textOf(el);
        const [a, b] = selection();
        sel.current = [a, b];
        props.current.onText(text, a);
      }}
      onPointerDown={() => { pointed.current = true; }}
      onFocus={() => {
        // focused by the app or the keyboard (not a click or a touch, which puts the caret where it
        // is): the caret where it was
        if (!pointed.current) select(...sel.current);
        pointed.current = false;
      }}
      onBlur={() => { pointed.current = false; }}
      onCompositionStart={() => { composing.current = true; }}
      onCompositionEnd={() => {
        composing.current = false;
        const el = div.current!;
        const text = textOf(el);
        if (text !== props.current.value) props.current.onText(text, selection()[0]);
        else sync();
      }}
      onPaste={(e) => {
        const text = e.clipboardData.getData("text/plain");
        if ((e.clipboardData.files.length && !text.trim()) || isFileLink(text)) return;   // a file: the composer's
        e.preventDefault();
        put(text.replace(/\r\n?/g, "\n"));
      }}
      onKeyDown={(e) => {
        onKeyDown?.(e);
        if (e.defaultPrevented || !(e.ctrlKey || e.metaKey) || e.altKey) return;
        const k = e.key.toLowerCase();
        if (k === "z" || k === "y") {
          e.preventDefault();
          restore(k === "z" && !e.shiftKey);
        }
      }}
    />
  );
});
