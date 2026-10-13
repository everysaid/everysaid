import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { MoreOnScroll, Segmented, Spinner } from "@/components/ui";
import { api, qs } from "@/lib/api";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";

// The GIFs to send: those already in the archive (sent or received before), and what each plugin
// that finds them finds (Telegram's @gif), each in a tab of its own; one that cannot now is there,
// greyed, saying why. A tap sends the GIF, as the video it is (the services each send it as theirs).

type Searcher = { instance: number; name: string; why: string | null };
type Found = { id: string; width: number; height: number };
type Mine = { sha256: string; ts: number };

export function GifPicker({ onPick }: { onPick: (file: File) => void }) {
  const { t } = useTranslation();
  const searchers = useQuery({ queryKey: ["gifs"], queryFn: () => api.get<{ items: Searcher[] }>("/api/gifs") });
  const list = searchers.data?.items ?? [];
  const [tab, setTab] = useState<string | null>(null);
  // at first: the first that can search, else those of the archive
  const shown = tab ?? (searchers.isPending ? null : String(list.find((s) => !s.why)?.instance ?? "mine"));
  const [busy, setBusy] = useState<string | null>(null);
  const pick = async (url: string, key: string) => {
    setBusy(key);
    try {
      const r = await fetch(url, { credentials: "same-origin" });
      if (!r.ok) throw new Error(String(r.status));
      onPick(new File([await r.blob()], "gif.mp4", { type: "video/mp4" }));
    } catch {
      toast.error(t("gif.failed"));
    } finally {
      setBusy(null);
    }
  };
  const searcher = list.find((s) => String(s.instance) === shown);
  return (
    <div data-gif-picker className="flex h-96 w-80 flex-col">
      <Segmented className="m-2 self-start" value={shown ?? ""} onChange={setTab} options={[
        { value: "mine", label: t("gif.mine") },
        ...list.map((s) => ({ value: String(s.instance), label: <span data-gif-tab={s.instance} className={cn(s.why && "opacity-50")}>{s.name}</span> })),
      ]} />
      {shown === null ? <div className="grid flex-1 place-items-center"><Spinner /></div>
        : shown === "mine" ? <MineGrid pick={pick} busy={busy} />
        : searcher?.why ? (
          <div data-gif-unavailable className="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center text-sm text-muted">
            <p>{searcher.why}</p>
            <Link to="/sources" className="text-accent hover:underline">{t("gif.setup")}</Link>
          </div>
        ) : searcher ? <SearchGrid key={searcher.instance} instance={searcher.instance} pick={pick} busy={busy} /> : null}
    </div>
  );
}

function SearchGrid({ instance, pick, busy }: { instance: number; pick: (url: string, key: string) => void; busy: string | null }) {
  const { t } = useTranslation();
  const [q, setQ] = useState("");
  const dq = useDebounced(q.trim(), 400);
  const res = useInfiniteQuery({
    queryKey: ["gif-search", instance, dq],
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.get<{ items: Found[]; next: string }>(`/api/gifs/${instance}/search${qs({ q: dq, offset: pageParam })}`),
    getNextPageParam: (last) => last.next || undefined,
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      <input data-gif-search value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("gif.search")} autoFocus
        onKeyDown={(e) => e.stopPropagation()}
        className="mx-2 mb-2 h-8 rounded-lg border border-line bg-panel-2 px-2 text-sm outline-none focus:border-accent" />
      <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.none")} more={!!res.hasNextPage}
        moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
        {items.map((g) => {
          const url = `/api/gifs/${instance}/${encodeURIComponent(g.id)}`;
          return <Clip key={g.id} src={url} w={g.width} h={g.height} busy={busy === url} onClick={() => pick(url, url)} />;
        })}
      </Grid>
    </>
  );
}

function MineGrid({ pick, busy }: { pick: (url: string, key: string) => void; busy: string | null }) {
  const { t } = useTranslation();
  const res = useInfiniteQuery({
    queryKey: ["gif-mine"],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<{ items: Mine[]; has_more: boolean }>(`/api/media${qs({ gif: true, available: true, limit: 30, before: pageParam || undefined })}`),
    getNextPageParam: (last) => (last.has_more && last.items.length ? last.items[last.items.length - 1].ts : undefined),
  });
  const items = res.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Grid loading={res.isPending} error={res.error?.message} empty={t("gif.mineNone")} more={!!res.hasNextPage}
      moreLoading={res.isFetchingNextPage} onMore={() => res.fetchNextPage()}>
      {items.map((m) => {
        const url = `/api/media/${m.sha256}/original`;
        return <Clip key={m.sha256} src={url} busy={busy === url} onClick={() => pick(url, url)} />;
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
    <button ref={ref} type="button" data-gif-option onClick={onClick} disabled={busy}
      style={{ aspectRatio: w && h ? `${w} / ${h}` : "4 / 3" }}
      className="relative mb-1.5 block w-full overflow-hidden rounded-lg bg-panel-2 hover:ring-2 hover:ring-accent">
      {seen && <video src={src} autoPlay muted loop playsInline className="size-full object-cover" />}
      {busy && <div className="absolute inset-0 grid place-items-center bg-black/30"><Spinner /></div>}
    </button>
  );
}
