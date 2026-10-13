// Writing emoji by keyboard: the usual emoticons ( :) <3 ) turned into theirs as they are typed, and
// emoji found by their shortcodes ( :joy: ) after a ":".

/** The emoticons turned into emoji: the usual ones (emojibase's, with a nose too), not those that
 * stand for something else in plain text ("8)" of a list, "XP", "xo", "D:"...). */
const EMOTICONS: Record<string, string> = {};
for (const [emoji, all] of [
  ["😄", ":D :-D"], ["😆", "xD XD :-))"], ["🤣", ":'D"], ["😂", ":')"], ["🙂", ":) :-)"], ["😉", ";) ;-)"],
  ["😊", ":>"], ["😇", "o:) O:)"], ["😘", ":x :X"], ["😚", ":* :-*"], ["😛", ":p :P :-p :-P"], ["😜", ";p ;P"],
  ["😐️", ":| :-|"], ["😶", ":#"], ["😕", ":/ :-/"], ["☹️", ":( :-("], ["😲", ":o :O :-o :-O"], ["😢", ":'("],
  ["😭", ":'o"], ["😖", "x( X("], ["🤬", ":@"], ["😈", ">:)"], ["👿", ">:("], ["💔", "</3"], ["❤️", "<3"],
  ["😫", ":c :C"],
]) for (const e of all.split(" ")) EMOTICONS[e] = emoji;
const LONGEST = Object.keys(EMOTICONS).sort((a, b) => b.length - a.length);

/** The emoticon that ends text, standing alone (at its start or after a space): what it is and where it
 * starts. */
export function emoticonAt(text: string): { from: number; emoticon: string; emoji: string } | null {
  for (const e of LONGEST) {
    if (!text.endsWith(e)) continue;
    const from = text.length - e.length;
    if (from > 0 && !/\s/.test(text[from - 1])) continue;
    return { from, emoticon: e, emoji: EMOTICONS[e] };
  }
  return null;
}

/** What ends a word, after which an emoticon before it is turned. */
export const ENDS = /^[\s.,!?]$/;

type Coded = [emoji: string, codes: string, group: number];
let index: Promise<Coded[]> | null = null;

/** The emoji with their shortcodes and categories (emojibase's groups), in the picker's order (served by the app with the picker's data;
 * loaded at the first ":"). */
export function shortcodes(): Promise<Coded[]> {
  index ??= fetch("/emojibase/en/shortcodes.json").then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
    .catch((e) => { index = null; throw e; });
  return index;
}

/** The emoji whose shortcodes start with what was typed (or have a word of theirs that does), the
 * ones used lately first, then those whose code starts so, in the picker's order. */
export function findEmoji(all: Coded[], query: string, recent: string[], max = 8): { emoji: string; code: string }[] {
  const q = query.toLowerCase();
  const hits: { emoji: string; code: string; rank: number; i: number }[] = [];
  all.forEach(([emoji, codes], i) => {
    const list = codes.split(" ");
    const whole = list.find((c) => c.startsWith(q));
    const part = whole ?? list.find((c) => c.split("_").some((w) => w.startsWith(q)));
    if (!part) return;
    const used = recent.indexOf(emoji);
    hits.push({ emoji, code: part, rank: used >= 0 ? used - 1000 : whole ? 0 : 1, i });
  });
  return hits.sort((a, b) => a.rank - b.rank || a.i - b.i).slice(0, max).map(({ emoji, code }) => ({ emoji, code }));
}

/** The emoji of a whole shortcode, as typed (":joy:"). */
export function byCode(all: Coded[], code: string): string | null {
  const c = code.toLowerCase();
  return all.find(([, codes]) => codes.split(" ").includes(c))?.[0] ?? null;
}
