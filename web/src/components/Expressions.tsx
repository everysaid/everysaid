import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { EmojiPicker } from "@/components/EmojiPicker";
import { MoreOnScroll, Segmented, Spinner } from "@/components/ui";
import { api, qs } from "@/lib/api";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";

// What goes into a message besides words, behind one button, a tab each: the emoji; the GIFs already
// in the archive (History: sent or received before, each once however many copies the services
// made of it); and what each plugin that finds GIFs finds (Telegram's @gif, Klipy), one that cannot
// now there, greyed, saying why. An emoji goes into the text; a GIF is sent at once, as the video it
// is (the services each send it as theirs).

type Searcher = { instance: number; name: string; why: string | null };
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
          { value: "history", label: <span data-gif-tab="history">{t("gif.history")}</span> },
          ...list.map((s) => ({ value: String(s.instance), label: <span data-gif-tab={s.instance} className={cn(s.why && "opacity-50")}>{s.name}</span> })),
        ]} />
      )}
      {tab === "emoji" ? <div className="flex min-h-0 flex-1 justify-center"><EmojiPicker onPick={onEmoji} /></div>
        : tab === "history" ? <div data-gif-picker className="flex min-h-0 flex-1 flex-col pt-2"><HistoryGrid pick={pick} busy={busy} /></div>
        : searcher?.why ? (
          <div data-gif-unavailable className="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center text-sm text-muted">
            <p>{searcher.why}</p>
            <Link to="/sources" className="text-accent hover:underline">{t("gif.setup")}</Link>
          </div>
        ) : searcher ? (
          <div data-gif-picker className="flex min-h-0 flex-1 flex-col pt-2">
            <SearchGrid key={searcher.instance} instance={searcher.instance} pick={pick} busy={busy} />
          </div>
        ) : null}
    </div>
  );
}

/** What "@gif <words>" finds, above the text being written: the first plugin that can, the others a tap away. */
export function GifResults({ query, onPick }: { query: string; onPick: (file: File) => void }) {
  const { t } = useTranslation();
  const searchers = useQuery(searchersQuery);
  const able = (searchers.data?.items ?? []).filter((s) => !s.why);
  const [chosen, setChosen] = useState<number | null>(null);
  const searcher = able.find((s) => s.instance === chosen) ?? able[0];
  const { busy, pick } = useFetchGif(onPick);
  return (
    <div data-gif-results className="flex h-72 flex-col">
      {able.length > 1 && (
        <Segmented className="mx-2 mt-2 self-start" value={String(searcher?.instance)} onChange={(v) => setChosen(Number(v))}
          options={able.map((s) => ({ value: String(s.instance), label: s.name }))} />
      )}
      {searchers.isPending ? <div className="grid flex-1 place-items-center"><Spinner /></div>
        : !searcher ? <div className="grid flex-1 place-items-center px-6 text-center text-sm text-muted">{t("gif.noSearcher")}</div>
        : <div className="flex min-h-0 flex-1 flex-col pt-2"><SearchGrid key={searcher.instance} instance={searcher.instance} pick={pick} busy={busy} query={query} /></div>}
    </div>
  );
}

function SearchGrid({ instance, pick, busy, query }: { instance: number; pick: (url: string) => void; busy: string | null; query?: string }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced((query ?? q).trim(), 400);
  const res = useInfiniteQuery({
    queryKey: ["gif-search", instance, dq],
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.get<{ items: Found[]; next: string }>(`/api/gifs/${instance}/search${qs({ q: dq, offset: pageParam })}`),
    getNextPageParam: (last) => last.next || undefined,
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      {query === undefined && (
        <input data-gif-search value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("gif.search")} autoFocus
          onKeyDown={(e) => e.stopPropagation()}
          className="mx-2 mb-2 h-8 rounded-lg border border-line bg-panel-2 px-2 text-sm outline-none focus:border-accent" />
      )}
      <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.none")} more={!!res.hasNextPage}
        moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
        {items.map((g) => {
          const url = `/api/gifs/${instance}/${encodeURIComponent(g.id)}`;
          return <Clip key={g.id} src={url} w={g.width} h={g.height} busy={busy === url} onClick={() => pick(url)} />;
        })}
      </Grid>
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

function HistoryGrid({ pick, busy }: { pick: (url: string) => void; busy: string | null }) {
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
  return (
    <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.historyNone")} more={!!res.hasNextPage}
      moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
      {items.map((m) => {
        const url = `/api/media/${m.sha256}/original`;
        return <Clip key={m.sha256} src={url} busy={busy === url} onClick={() => pick(url)} />;
      })}
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
function Clip({ src, w, h, busy, onClick }: { src: string; w?: number; h?: number; busy: boolean; onClick: () => void }) {
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
    <button ref={ref} type="button" data-gif-option onMouseDown={(e) => e.preventDefault()} onClick={onClick} disabled={busy}
      style={{ aspectRatio: w && h ? `${w} / ${h}` : "4 / 3" }}
      className="relative mb-1.5 block w-full overflow-hidden rounded-lg bg-panel-2 hover:ring-2 hover:ring-accent">
      {seen && <video src={src} autoPlay muted loop playsInline className="size-full object-cover" />}
      {busy && <div className="absolute inset-0 grid place-items-center bg-black/30"><Spinner /></div>}
    </button>
  );
}
