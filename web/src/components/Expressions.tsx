import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { EmojiPicker, recentEmoji } from "@/components/EmojiPicker";
import { MoreOnScroll, Segmented, Spinner } from "@/components/ui";
import { api, qs } from "@/lib/api";
import { findEmoji, shortcodes } from "@/lib/emoji";
import { cn } from "@/lib/utils";

// What goes into a message besides words, behind one button, a tab each: the emoji first, then the
// GIFs plugins in the order added (Sources, GIFs): the GIFs already in the archive (History: sent or
// received before, each once however many copies the services made of it), what each plugin that
// finds GIFs finds (Telegram's @gif, Klipy), one that cannot now there, greyed, saying why. An emoji
// goes into the text; a GIF is sent at once, as the video it is (the services each send it as theirs).

type Searcher = { instance: number; name: string; why: string | null; history?: boolean; wait_ms?: number };
type Found = { id: string; width: number; height: number };
type Kept = { sha256: string; ts: number; look?: string };

const searchersQuery = { queryKey: ["gifs"], queryFn: () => api.get<{ items: Searcher[] }>("/api/gifs") };

/** A GIF picked: its file, fetched; null when it could not be (said). */
function useFetchGif(onPick: (file: File) => void) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<string | null>(null);
  const pick = async (url: string) => {
    setBusy(url);
    try {
      const r = await fetch(url, { credentials: "same-origin" });
      if (!r.ok) throw new Error(String(r.status));
      onPick(new File([await r.blob()], "gif.mp4", { type: "video/mp4" }));
      // one a plugin found: it is told it went, where its terms ask (nothing of who, nor where)
      if (url.startsWith("/api/gifs/")) api.post(`${url}/sent`).catch(() => {});
    } catch {
      toast.error(t("gif.failed"));
    } finally {
      setBusy(null);
    }
  };
  return { busy, pick };
}

export function Expressions({ onEmoji, onGif, gifs }: { onEmoji: (emoji: string) => void; onGif: (file: File) => void; gifs: boolean }) {
  const { t } = useTranslation();
  const searchers = useQuery({ ...searchersQuery, enabled: gifs });
  const list = gifs ? searchers.data?.items ?? [] : [];
  const [tab, setTab] = useState("emoji");
  const { busy, pick } = useFetchGif(onGif);
  const searcher = list.find((s) => String(s.instance) === tab);
  return (
    <div data-expressions className="flex h-[26rem] w-80 flex-col">
      {gifs && (
        <Segmented className="mx-2 mt-2 self-start" value={tab} onChange={setTab} options={[
          { value: "emoji", label: t("emoji.choose") },
          ...list.map((s) => ({ value: String(s.instance), label: (
            <span data-gif-tab={s.instance} data-gif-history={s.history || undefined} className={cn(s.why && "opacity-50")}>{s.name}</span>) })),
        ]} />
      )}
      {tab === "emoji" ? <div className="flex min-h-0 flex-1 justify-center"><EmojiPicker onPick={onEmoji} /></div>
        : searcher?.history ? <div data-gif-picker className="flex min-h-0 flex-1 flex-col pt-2"><HistoryGrid pick={pick} busy={busy} /></div>
        : searcher?.why ? (
          <div data-gif-unavailable className="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center text-sm text-muted">
            <p>{searcher.why}</p>
            <Link to="/sources" className="text-accent hover:underline">{t("gif.setup")}</Link>
          </div>
        ) : searcher ? (
          <div data-gif-picker className="flex min-h-0 flex-1 flex-col pt-2">
            <SearchGrid key={searcher.instance} instance={searcher.instance} wait={searcher.wait_ms} pick={pick} busy={busy} />
          </div>
        ) : null}
    </div>
  );
}

/** Keys the window above the text takes while "@gif" and words are written there (true: taken). */
export type ResultsKeys = { key: (key: string, shift: boolean) => boolean };

const RESULTS_TAB = "gif-results-tab";

/** What "@gif <words>" finds, above the text being written, as behind the button: the emoji first (all
 * of them, by category, the ones used lately first; with words, those whose names have them), then a
 * tab for each GIFs plugin in the order added (the History of the archive's GIFs too, which has no
 * words; one that cannot now there, greyed, saying why), opened at the one chosen last. All by
 * keyboard, the caret staying in the text: Tab and Shift+Tab the tabs, ↓ and ↑ (and, among them, →
 * and ←) the results (the emoji a row at a time), Enter the one marked (or, none marked, the words
 * asked for now). */
export const GifResults = forwardRef<ResultsKeys, { query: string; onPick: (file: File) => void; onEmoji: (emoji: string) => void }>(
  function GifResults({ query, onPick, onEmoji }, ref) {
    const { t } = useTranslation();
    const searchers = useQuery(searchersQuery);
    const all = searchers.data?.items ?? [];
    const tabs = [{ id: "emoji", name: t("emoji.choose"), history: false, why: null as string | null },
      ...all.map((s) => ({ id: String(s.instance), name: s.name, history: !!s.history, why: s.why }))];
    const [chosen, setChosen] = useState(() => localStorage.getItem(RESULTS_TAB));
    const tab = tabs.find((x) => x.id === chosen) ?? tabs[0];
    const searcher = all.find((s) => String(s.instance) === tab.id);
    const choose = (id: string) => { setChosen(id); localStorage.setItem(RESULTS_TAB, id); setCursor(-1); };
    const [cursor, setCursor] = useState(-1);
    const [go, setGo] = useState(0);
    const list = useRef<string[]>([]);              // what the tab shows, in order: GIFs' addresses, or emoji
    const rows = useRef<number[][] | null>(null);   // the emoji's rows (their places in list), as shown
    const { busy, pick } = useFetchGif(onPick);
    const take = (i: number) => {
      const v = list.current[i];
      if (v === undefined) return;
      if (tab.id === "emoji") onEmoji(v);
      else pick(v);
    };
    // the place a row down (or up) from the one marked, in the same column or the row's last; -1 above the first
    const row = (c: number, by: number) => {
      const r = rows.current!;
      const at = r.findIndex((x) => x.includes(c));
      const next = r[at + by];
      if (!next) return by < 0 ? -1 : c;
      return next[Math.min(r[at].indexOf(c), next.length - 1)];
    };
    useEffect(() => setCursor(-1), [query]);
    useImperativeHandle(ref, () => ({
      key: (key, shift) => {
        const n = list.current.length;
        const emoji = tab.id === "emoji" && rows.current;
        switch (key) {
          case "Tab": {
            const i = tabs.findIndex((x) => x.id === tab.id);
            choose(tabs[(i + (shift ? tabs.length - 1 : 1)) % tabs.length].id);
            return true;
          }
          case "ArrowDown":
            setCursor((c) => (c < 0 ? Math.min(0, n - 1) : emoji ? row(c, 1) : Math.min(c + 1, n - 1)));
            return n > 0;
          case "ArrowUp":
            if (cursor < 0) return false;
            setCursor((c) => (emoji ? row(c, -1) : c - 1));
            return true;
          case "ArrowRight":
          case "ArrowLeft":
            if (cursor < 0) return false;
            setCursor((c) => Math.max(0, Math.min(n - 1, c + (key === "ArrowRight" ? 1 : -1))));
            return true;
          case "Enter":
            if (cursor >= 0) take(cursor);
            else if (tab.id === "emoji") take(0);
            else setGo((g) => g + 1);
            return true;
        }
        return false;
      },
    }));
    if (tab.id !== "emoji") rows.current = null;
    if (searcher?.why) list.current = [];
    return (
      // a tap here leaves the caret in the text, where the keys are
      <div data-gif-results className="flex h-80 flex-col" onMouseDown={(e) => e.preventDefault()}>
        <Segmented className="mx-2 mt-2 self-start" value={tab.id} onChange={choose}
          options={tabs.map((x) => ({ value: x.id, label: (
            <span data-results-tab={x.id} data-results-history={x.history || undefined} className={cn(x.why && "opacity-50")}>{x.name}</span>) }))} />
        {tab.id === "emoji" ? <EmojiResults query={query} cursor={cursor} list={list} rows={rows} onPick={onEmoji} />
          : searchers.isPending ? <div className="grid flex-1 place-items-center"><Spinner /></div>
          : searcher?.why ? (
            <div data-gif-unavailable className="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center text-sm text-muted">
              <p>{searcher.why}</p>
              <Link to="/sources" className="text-accent hover:underline">{t("gif.setup")}</Link>
            </div>
          ) : searcher?.history
            ? <div className="flex min-h-0 flex-1 flex-col pt-2"><HistoryGrid pick={pick} busy={busy} cursor={cursor} list={list} /></div>
          : searcher ? <div className="flex min-h-0 flex-1 flex-col pt-2">
              <SearchGrid key={tab.id} instance={searcher.instance} wait={searcher.wait_ms}
                pick={pick} busy={busy} query={query} go={go} cursor={cursor} list={list} />
            </div> : null}
      </div>
    );
  });

const COLUMNS = 8;

/** All the emoji, as the button's picker shows them: the ones used lately, then each category; with
 * words, those whose names (English, as the picker's) have them. A row of the grid at a time by the keys. */
function EmojiResults({ query, cursor, list, rows, onPick }: {
  query: string; cursor: number; list: React.MutableRefObject<string[]>; rows: React.MutableRefObject<number[][] | null>;
  onPick: (emoji: string) => void;
}) {
  const { t } = useTranslation();
  const [codes, setCodes] = useState<Awaited<ReturnType<typeof shortcodes>> | null>(null);
  useEffect(() => { shortcodes().then(setCodes, () => {}); }, []);
  // emojibase's groups, in the user's words (2, the skin tones alone, is not among them)
  const groups = [t("emoji.smileys"), t("emoji.people"), "", t("emoji.animals"), t("emoji.food"), t("emoji.travel"),
    t("emoji.activities"), t("emoji.objects"), t("emoji.symbols"), t("emoji.flags")];
  const words = query.trim().toLowerCase().replace(/\s+/g, "_");
  const recent = recentEmoji();
  const sections: { name: string; emoji: string[] }[] = [];
  if (words) sections.push({ name: "", emoji: codes ? findEmoji(codes, words, recent, Infinity).map((x) => x.emoji) : [] });
  else {
    if (recent.length) sections.push({ name: t("emoji.recent"), emoji: recent.slice(0, 16) });
    const byGroup = new Map<number, string[]>();
    for (const [emoji, , group] of codes ?? []) {
      if (!byGroup.has(group)) byGroup.set(group, []);
      byGroup.get(group)!.push(emoji);
    }
    for (const [group, emoji] of byGroup) sections.push({ name: groups[group] ?? "", emoji });
  }
  list.current = sections.flatMap((s) => s.emoji);
  let at = 0;
  rows.current = sections.flatMap((s) => {
    const r: number[][] = [];
    for (let i = 0; i < s.emoji.length; i += COLUMNS) r.push(s.emoji.slice(i, i + COLUMNS).map((_, j) => at + i + j));
    at += s.emoji.length;
    return r;
  });
  if (!codes) return <div className="grid flex-1 place-items-center"><Spinner /></div>;
  if (!list.current.length) return <div className="grid flex-1 place-items-center text-sm text-muted">{t("emoji.none")}</div>;
  let i = 0;
  return (
    <div data-emoji-results className="flex-1 overflow-y-auto pb-1.5">
      {sections.map((s, k) => (
        <div key={k}>
          {s.name && <div className="sticky top-0 bg-panel px-3 pb-1 pt-2.5 text-xs font-medium text-muted">{s.name}</div>}
          <div className={cn("grid gap-0.5 px-1.5", !s.name && "pt-2")} style={{ gridTemplateColumns: `repeat(${COLUMNS}, minmax(0, 1fr))` }}>
            {s.emoji.map((e) => {
              const n = i++;
              return (
                <Marked key={n} on={n === cursor}>
                  <button type="button" data-emoji-result={e} data-active={n === cursor || undefined} onClick={() => onPick(e)}
                    className={cn("grid aspect-square w-full scroll-mt-9 place-items-center rounded-md text-xl hover:bg-panel-2",
                      n === cursor && "bg-panel-2 ring-2 ring-inset ring-accent")}>{e}</button>
                </Marked>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

/** The result marked by the keys, kept in view (the result itself: this wrapper has no box of its own). */
function Marked({ on, children }: { on: boolean; children: React.ReactNode }) {
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => { if (on) ref.current?.firstElementChild?.scrollIntoView({ block: "nearest" }); }, [on]);
  return <span ref={ref} className="contents">{children}</span>;
}

// the words are asked for as long after the last typed as the plugin says (wait: Klipy's a second,
// each search counting with its limit an hour; Telegram's a moment), or at once with Enter (go:
// the composer's Enter, for "@gif"); nothing before words are written
function SearchGrid({ instance, wait = 1000, pick, busy, query, go, cursor = -1, list }: {
  instance: number; wait?: number; pick: (url: string) => void; busy: string | null; query?: string; go?: number;
  cursor?: number; list?: React.MutableRefObject<string[]>;
}) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const words = (query ?? q).trim();
  const [dq, setAsked] = useState("");             // nothing asked for until words are written
  useEffect(() => {
    const timer = setTimeout(() => setAsked(words), wait);
    return () => clearTimeout(timer);
  }, [words, wait]);
  useEffect(() => { if (go) setAsked(words); }, [go]); // eslint-disable-line react-hooks/exhaustive-deps
  const res = useInfiniteQuery({
    queryKey: ["gif-search", instance, dq],
    enabled: dq !== "",
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.get<{ items: Found[]; next: string }>(`/api/gifs/${instance}/search${qs({ q: dq, offset: pageParam })}`),
    getNextPageParam: (last) => last.next || undefined,
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  const url = (g: Found) => `/api/gifs/${instance}/${encodeURIComponent(g.id)}`;
  if (list) list.current = items.map(url);
  return (
    <>
      {query === undefined && (
        <input data-gif-search value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("gif.search")} autoFocus
          onKeyDown={(e) => {
            e.stopPropagation();
            if (e.key === "Enter" && !e.nativeEvent.isComposing) setAsked(words);
          }}
          className="mx-2 mb-2 h-8 rounded-lg border border-line bg-panel-2 px-2 text-sm outline-none focus:border-accent" />
      )}
      {!dq ? <div data-gif-waiting className="grid flex-1 place-items-center px-6 text-center text-sm text-muted">{t("gif.typeToSearch")}</div> : (
      <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.none")} more={!!res.hasNextPage}
        moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
        {items.map((g, i) => (
          <Marked key={g.id} on={i === cursor}>
            <Clip src={url(g)} w={g.width} h={g.height} busy={busy === url(g)} active={i === cursor} onClick={() => pick(url(g))} />
          </Marked>
        ))}
      </Grid>)}
    </>
  );
}

// a look's difference from another (the bits that differ), where both have one
const LOOKALIKE = 10;
function apart(a: string, b: string) {
  let x = BigInt(`0x${a}`) ^ BigInt(`0x${b}`), n = 0;
  for (; x; x &= x - 1n) n++;
  return n;
}

function HistoryGrid({ pick, busy, cursor = -1, list }: {
  pick: (url: string) => void; busy: string | null; cursor?: number; list?: React.MutableRefObject<string[]>;
}) {
  const { t } = useTranslation();
  const res = useInfiniteQuery({
    queryKey: ["gif-history"],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<{ items: Kept[]; has_more: boolean }>(`/api/media${qs({ gif: true, available: true, limit: 30, before: pageParam || undefined })}`),
    getNextPageParam: (last) => (last.has_more && last.items.length ? last.items[last.items.length - 1].ts : undefined),
  });
  // each GIF once: a file seen on an earlier page, or one that looks as one already shown (the same
  // GIF, encoded again by a service), is left out
  const seen = new Set<string>();
  const looks: string[] = [];
  const items = (res.data?.pages.flatMap((p) => p.items) ?? []).filter((m) => {
    if (seen.has(m.sha256)) return false;
    seen.add(m.sha256);
    if (m.look) {
      if (looks.some((l) => apart(l, m.look!) <= LOOKALIKE)) return false;
      looks.push(m.look);
    }
    return true;
  });
  const url = (m: Kept) => `/api/media/${m.sha256}/original`;
  if (list) list.current = items.map(url);
  return (
    <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.historyNone")} more={!!res.hasNextPage}
      moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
      {items.map((m, i) => (
        <Marked key={m.sha256} on={i === cursor}>
          <Clip src={url(m)} busy={busy === url(m)} active={i === cursor} onClick={() => pick(url(m))} />
        </Marked>
      ))}
    </Grid>
  );
}

function Grid({ loading, error, empty, more, moreLoading, onMore, children }: {
  loading: boolean; error?: string; empty: string; more: boolean; moreLoading: boolean; onMore: () => void; children: React.ReactNode[];
}) {
  if (loading) return <div className="grid flex-1 place-items-center"><Spinner /></div>;
  if (error) return <div data-gif-error className="grid flex-1 place-items-center px-6 text-center text-sm text-muted">{error}</div>;
  if (!children.length) return <div className="grid flex-1 place-items-center text-sm text-muted">{empty}</div>;
  return (
    <div className="flex-1 overflow-y-auto px-2 pb-2">
      <div className="columns-2 gap-1.5">{children}</div>
      <MoreOnScroll hasMore={more} loading={moreLoading} onMore={onMore} />
    </div>
  );
}

// a GIF, played once it comes into view (a grid of them would otherwise all load at once)
function Clip({ src, w, h, busy, active, onClick }: { src: string; w?: number; h?: number; busy: boolean; active?: boolean; onClick: () => void }) {
  const ref = useRef<HTMLButtonElement>(null);
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver((e) => { if (e[0].isIntersecting) { setSeen(true); io.disconnect(); } }, { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, []);
  return (
    <button ref={ref} type="button" data-gif-option data-active={active || undefined} onMouseDown={(e) => e.preventDefault()} onClick={onClick} disabled={busy}
      style={{ aspectRatio: w && h ? `${w} / ${h}` : "4 / 3" }}
      className={cn("relative mb-1.5 block w-full overflow-hidden rounded-lg bg-panel-2 hover:ring-2 hover:ring-accent", active && "ring-2 ring-accent")}>
      {seen && <video src={src} autoPlay muted loop playsInline className="size-full object-cover" />}
      {busy && <div className="absolute inset-0 grid place-items-center bg-black/30"><Spinner /></div>}
    </button>
  );
}
