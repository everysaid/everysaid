import { useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { VirtuosoGrid } from "react-virtuoso";
import { Archive, Check, CheckSquare, FileText, Images, Mic, Play, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { api, qs, type MediaItem } from "@/lib/api";
import { bytes, dateOnly } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button, Empty, Segmented, Spinner, Switch } from "@/components/ui";
import { PageHeader } from "@/components/PageHeader";
import { Lightbox } from "@/components/Lightbox";

type Kind = "image" | "video" | "voice" | "file";

export function MediaPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [kind, setKind] = useState<Kind>("image");
  const [available, setAvailable] = useState(true);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const res = useInfiniteQuery({
    queryKey: ["media", kind, available],
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) => api.get<{ items: MediaItem[]; has_more: boolean }>(`/api/media${qs({ kind, before: pageParam, limit: 90, available: available || undefined })}`),
    getNextPageParam: (last) => (last.has_more ? last.items[last.items.length - 1]?.ts : undefined),
  });
  const items = useMemo(() => {
    const seen = new Set<string>();
    return (res.data?.pages.flatMap((p) => p.items) ?? []).filter((m) => !seen.has(m.sha256) && seen.add(m.sha256));
  }, [res.data]);
  const visual = kind === "image" || kind === "video";

  const toggle = (sha: string) => setSelected((s) => {
    const n = new Set(s);
    n.has(sha) ? n.delete(sha) : n.add(sha);
    return n;
  });

  const bulk = async (action: "keep" | "remove" | "library") => {
    setBusy(true);
    let ok = 0;
    for (const sha of selected) {
      try {
        if (action === "library") await api.post(`/api/media/${sha}/library`, {});
        else await api.post(`/api/media/${sha}/decision`, { decision: action });
        ok++;
      } catch (e: any) {
        toast.error(e.message);
        break;
      }
    }
    setBusy(false);
    toast.success(`${ok} ✓`);
    setSelected(new Set());
    setSelecting(false);
    qc.invalidateQueries({ queryKey: ["media"] });
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t("media.title")}
        actions={
          <Button variant={selecting ? "primary" : "ghost"} size="sm" onClick={() => { setSelecting(!selecting); setSelected(new Set()); }}>
            {selecting ? <X className="size-4" /> : <CheckSquare className="size-4" />} {t("media.select")}
          </Button>
        }
      />
      <div className="flex flex-wrap items-center gap-3 bg-panel px-4 pb-3 md:px-6">
        <Segmented value={kind} onChange={(k) => { setKind(k); setSelected(new Set()); }} options={[
          { value: "image", label: t("media.images") }, { value: "video", label: t("media.videos") },
          { value: "voice", label: t("media.voice") }, { value: "file", label: t("media.files") },
        ]} />
        <label className="flex items-center gap-2 text-sm text-muted">
          <Switch checked={available} onChange={setAvailable} /> {t("media.onlyAvailable")}
        </label>
      </div>
      <div className="relative min-h-0 flex-1">
        {res.isLoading ? <div className="grid h-40 place-items-center"><Spinner /></div> : items.length === 0 ? (
          <Empty icon={<Images />} title={t("common.none")} />
        ) : visual ? (
          <VirtuosoGrid
            data={items}
            endReached={() => res.hasNextPage && res.fetchNextPage()}
            listClassName="grid grid-cols-3 gap-1 p-1 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-8"
            itemContent={(i, m) => (
              <button
                onClick={() => (selecting ? toggle(m.sha256) : setOpen(i))}
                className={cn("relative block aspect-square w-full overflow-hidden rounded-md bg-panel-2", selected.has(m.sha256) && "ring-4 ring-accent ring-inset")}
              >
                {m.available !== "gone" && <img src={`/api/media/${m.sha256}/thumb`} alt="" loading="lazy" className="size-full object-cover" />}
                {m.mime?.startsWith("video/") && <Play className="absolute bottom-1.5 left-1.5 size-4 text-white drop-shadow" />}
                {m.decision && (
                  <span className={cn("absolute right-1 top-1 grid size-5 place-items-center rounded-full text-white", m.decision === "remove" ? "bg-danger" : m.decision === "library" ? "bg-accent" : "bg-ok")}>
                    {m.decision === "remove" ? <Trash2 className="size-3" /> : m.decision === "library" ? <Archive className="size-3" /> : <Check className="size-3" />}
                  </span>
                )}
                {selecting && (
                  <span className={cn("absolute left-1.5 top-1.5 size-5 rounded-full border-2 border-white", selected.has(m.sha256) && "bg-accent")} />
                )}
              </button>
            )}
          />
        ) : (
          <div className="h-full overflow-y-auto">
            <div className="mx-auto max-w-3xl divide-y divide-line">
              {items.map((m) => (
                <div key={m.sha256} className="flex items-center gap-3 px-4 py-3">
                  {kind === "voice" ? <Mic className="size-5 text-muted" /> : <FileText className="size-5 text-muted" />}
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm">{m.mime}</div>
                    <div className="text-xs text-muted">{dateOnly(m.ts)} · {bytes(m.size)}</div>
                  </div>
                  {kind === "voice" && m.available !== "gone" && <audio controls preload="none" src={`/api/media/${m.sha256}/original`} className="h-9 w-56" />}
                  {kind === "file" && m.available !== "gone" && <a className="text-sm text-accent" href={`/api/media/${m.sha256}/original`} download>{t("common.download")}</a>}
                  {m.chat_id && <Button size="sm" variant="ghost" onClick={() => navigate({ to: "/chat/$chatId", params: { chatId: m.chat_id! }, search: { m: m.message_id } })}>{t("common.open")}</Button>}
                </div>
              ))}
              {res.hasNextPage && <div className="flex justify-center p-4"><Button onClick={() => res.fetchNextPage()}>{t("common.more")}</Button></div>}
            </div>
          </div>
        )}
        {selecting && selected.size > 0 && (
          <div className="absolute inset-x-0 bottom-4 flex justify-center px-4">
            <div className="flex items-center gap-2 rounded-2xl border border-line bg-panel p-2 shadow-2xl">
              <span className="px-2 text-sm font-medium">{t("media.selected", { count: selected.size })}</span>
              <Button size="sm" onClick={() => bulk("keep")} loading={busy}><Check className="size-4" />{t("media.keep")}</Button>
              <Button size="sm" variant="primary" onClick={() => bulk("library")} loading={busy}><Archive className="size-4" />{t("media.toLibrary")}</Button>
              <Button size="sm" variant="danger" onClick={() => bulk("remove")} loading={busy}><Trash2 className="size-4" />{t("media.remove")}</Button>
            </div>
          </div>
        )}
      </div>
      <Lightbox
        items={items}
        index={open}
        onIndex={setOpen}
        onClose={() => setOpen(null)}
        onOpenChat={(m) => m.chat_id && navigate({ to: "/chat/$chatId", params: { chatId: m.chat_id }, search: { m: m.message_id } })}
      />
    </div>
  );
}
