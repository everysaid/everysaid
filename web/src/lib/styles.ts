/** A text's styles as the composer keeps them, whatever service it goes to: each a part of the text
 * (in its UTF-16 units, as the field counts), of one kind. Only as it is sent are they written as
 * the service has them (the server does that). */

export const KINDS = ["bold", "italic", "underline", "strike", "code", "spoiler", "link"] as const;
export type Kind = (typeof KINDS)[number];
/** url: a link's address (words that open it). */
export type Style = { start: number; length: number; kind: Kind; url?: string };

/** How each kind shows. */
export const LOOK: Record<Kind, string> = {
  bold: "font-bold",
  italic: "italic",
  underline: "underline underline-offset-2",
  strike: "line-through",
  code: "rounded bg-panel px-0.5 font-mono text-[0.9em]",
  spoiler: "rounded bg-muted/30",
  link: "underline decoration-current/40 underline-offset-2",
};

/** The styles in order, those of a kind over or beside each other as one (not links: each its own
 * address), none empty or past the text, nothing else over code. */
export function tidy(styles: Style[], size: number): Style[] {
  const on = new Map<Kind, boolean[]>();
  const links: Style[] = [];
  for (const s of styles) {
    const a = Math.max(0, s.start), b = Math.min(size, s.start + s.length);
    if (b <= a) continue;
    if (s.kind === "link") { links.push({ ...s, start: a, length: b - a }); continue; }
    if (!on.has(s.kind)) on.set(s.kind, new Array(size).fill(false));
    on.get(s.kind)!.fill(true, a, b);
  }
  const code = on.get("code");
  if (code) for (const [k, marks] of on) if (k !== "code") marks.forEach((m, i) => (marks[i] = m && !code[i]));
  const out: Style[] = [];
  for (const kind of KINDS) {
    const marks = on.get(kind);
    if (!marks) continue;
    for (let i = 0; i < size; i++) {
      if (!marks[i]) continue;
      let j = i;
      while (j < size && marks[j]) j++;
      out.push({ start: i, length: j - i, kind });
      i = j;
    }
  }
  out.push(...links);
  return out.sort((a, b) => a.start - b.start || KINDS.indexOf(a.kind) - KINDS.indexOf(b.kind));
}

/** The styles once the text was changed from before to after (one change: what differs between
 * what both begin and end with). What was put in within a style takes it; not what was put at its
 * start or its end: a style is over the part chosen for it, and ends there. */
export function follow(before: string, after: string, styles: Style[]): Style[] {
  if (before === after || !styles.length) return styles;
  let p = 0;
  while (p < before.length && p < after.length && before[p] === after[p]) p++;
  let e = 0;
  while (e < before.length - p && e < after.length - p && before[before.length - 1 - e] === after[after.length - 1 - e]) e++;
  const gone = before.length - e;            // [p, gone) of before became [p, put) of after
  const put = after.length - e;
  const shift = put - gone;
  const at = (i: number, end: boolean) => {
    if (i === p && gone === p) return end ? p : put;   // where text was only put in: before it an end, after it a start
    if (i <= p) return i;
    if (i >= gone) return i + shift;
    return end ? put : p;                                          // within what was taken: its edge
  };
  return tidy(styles.map((s) => {
    const start = at(s.start, false), end = at(s.start + s.length, true);
    return { ...s, start, length: end - start };
  }), after.length);
}

/** Whether all of [from, to) has the kind. */
export function has(styles: Style[], from: number, to: number, kind: Kind): boolean {
  if (to <= from) return false;
  let at = from;
  for (const s of styles) {
    if (s.kind !== kind || s.start > at || s.start + s.length <= at) continue;
    at = s.start + s.length;
    if (at >= to) return true;
  }
  return false;
}

/** Whether text put in at `at` takes the kind: within a style of it (not at its start or its end). */
export function within(styles: Style[], at: number, kind: Kind): boolean {
  return styles.some((s) => s.kind === kind && s.start < at && at < s.start + s.length);
}

/** The kind on over [from, to), or off it. */
export function turn(styles: Style[], from: number, to: number, kind: Kind, on: boolean, size: number): Style[] {
  if (to <= from) return styles;
  return on ? toggleOn(styles, from, to, kind, size) : cut(styles, from, to, (k) => k === kind, size);
}

/** The kind over [from, to), or off it where all of it has it already. */
export function toggle(styles: Style[], from: number, to: number, kind: Kind, size: number): Style[] {
  if (to <= from) return styles;
  if (has(styles, from, to, kind)) return cut(styles, from, to, (k) => k === kind, size);
  const rest = kind === "code" ? cut(styles, from, to, () => true, size) : styles;   // code: nothing else in it
  return tidy([...rest, { start: from, length: to - from, kind }], size);
}

/** No style over [from, to): what was on both sides of it stays there. */
export function clear(styles: Style[], from: number, to: number, size: number): Style[] {
  return cut(styles, from, to, () => true, size);
}

function cut(styles: Style[], from: number, to: number, which: (k: Kind) => boolean, size: number): Style[] {
  const out: Style[] = [];
  for (const s of styles) {
    const end = s.start + s.length;
    if (!which(s.kind) || end <= from || s.start >= to) { out.push(s); continue; }
    if (s.start < from) out.push({ ...s, length: from - s.start });
    if (end > to) out.push({ ...s, start: to, length: end - to });
  }
  return tidy(out, size);
}

/** The marks typed for a style, as the services read them ("*bold*"), and the kinds they give. */
export const MARKS: [string, Kind][] = [["```", "code"], ["||", "spoiler"], ["*", "bold"], ["_", "italic"], ["~", "strike"], ["`", "code"]];

const WORD = /[\p{L}\p{N}]/u;
const SPACE = /\s/u;

/** A pair of marks just closed at the caret, at the edges of words ("*γεια*"; not "2*3*4", nor
 * within a word, which only the menu styles): the text without them and its part styled so.
 * from: where the opening mark was. None within code, or where the user turned this one back. */
export function marked(text: string, styles: Style[], caret: number, keptAt: number | null):
  { text: string; styles: Style[]; caret: number; from: number; to: number; mark: string; kind: Kind } | null {
  for (const [mark, kind] of MARKS) {
    const close = caret - mark.length;
    if (close < 2 || text.slice(close, caret) !== mark) continue;
    const next = text[caret];
    if (next !== undefined && (WORD.test(next) || next === mark[0])) continue;  // the end of a word
    const last = text[close - 1];
    if (SPACE.test(last) || last === mark[0]) continue;
    const line = text.lastIndexOf("\n", close - 1) + 1;
    const open = text.lastIndexOf(mark, close - 1);
    if (open < (mark === "```" ? 0 : line) || open + mark.length >= close || open === keptAt) continue;
    const before = text[open - 1], first = text[open + mark.length];
    if (before !== undefined && (WORD.test(before) || before === mark[0])) continue;   // the start of a word
    if (SPACE.test(first) || first === mark[0]) continue;
    if (styles.some((s) => s.kind === "code" && s.start < caret && s.start + s.length > open)) continue;
    const inner = text.slice(open + mark.length, close);
    if (!/[\p{L}\p{N}]/u.test(inner)) continue;                                  // "*_*": an emoticon, not a style
    const out = text.slice(0, open) + inner + text.slice(caret);
    const moved = follow(text.slice(0, close) + text.slice(caret), out, follow(text, text.slice(0, close) + text.slice(caret), styles));
    const to = open + inner.length;
    return { text: out, styles: toggleOn(moved, open, to, kind, out.length), caret: caret - 2 * mark.length, from: open, to, mark, kind };
  }
  return null;
}

/** The kind over [from, to), whatever it had. */
function toggleOn(styles: Style[], from: number, to: number, kind: Kind, size: number): Style[] {
  return has(styles, from, to, kind) ? styles : toggle(styles, from, to, kind, size);
}

/** The styles as the server takes them: in characters (as it counts, not UTF-16 units), of the text
 * from `from` (where what is sent starts, the spaces before it left out) on. */
export function inCharacters(text: string, styles: Style[], from = 0): Style[] {
  const chars = (i: number) => Array.from(text.slice(from, Math.max(from, i))).length;
  return styles.map((s) => {
    const start = chars(s.start);
    return { ...s, start, length: chars(s.start + s.length) - start };
  }).filter((s) => s.length > 0);
}

/** The text in parts of the same styles, for showing: each part's text and kinds (and the address
 * of a link it is in). */
export function parts(text: string, styles: Style[]): { text: string; kinds: Kind[]; url?: string }[] {
  const cuts = new Set([0, text.length]);
  for (const s of styles) { cuts.add(Math.min(s.start, text.length)); cuts.add(Math.min(s.start + s.length, text.length)); }
  const at = [...cuts].sort((a, b) => a - b);
  const out: { text: string; kinds: Kind[]; url?: string }[] = [];
  for (let i = 0; i + 1 < at.length; i++) {
    const a = at[i], b = at[i + 1];
    if (b <= a) continue;
    const over = styles.filter((s) => s.start <= a && s.start + s.length >= b);
    out.push({ text: text.slice(a, b), kinds: KINDS.filter((k) => over.some((s) => s.kind === k)), url: over.find((s) => s.kind === "link")?.url });
  }
  return out;
}
