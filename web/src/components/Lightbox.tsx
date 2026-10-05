import { useEffect, useState } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Archive, Check, ChevronLeft, ChevronRight, Download, ExternalLink, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { fullDate } from "@/lib/format";
import { cn } from "@/lib/utils";

export interface LightboxItem {
  sha256: string;
  mime: string | null;
  ts?: number;
  available?: string;
  decision?: string | null;
  chat_id?: string | null;
  message_id?: number;
}

export function Lightbox({ items, index, onClose, onIndex, onOpenChat }: {
  items: LightboxItem[];
  index: number | null;
  onClose: () => void;
  onIndex: (i: number) => void;
  onOpenChat?: (item: LightboxItem) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const item = index != null ? items[index] : null;
  const [decision, setDecision] = useState<string | null | undefined>(item?.decision);
  useEffect(() => setDecision(item?.decision), [item]);

  useEffect(() => {
    if (index == null) return;
    const key = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft" && index > 0) onIndex(index - 1);
      if (e.key === "ArrowRight" && index < items.length - 1) onIndex(index + 1);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [index, items.length, onIndex]);

  const decide = useMutation({
    mutationFn: (d: string | null) => api.post(`/api/media/${item!.sha256}/decision`, { decision: d }),
    onSuccess: (_, d) => {
      setDecision(d);
      qc.invalidateQueries({ queryKey: ["media"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const library = useMutation({
    mutationFn: () => api.post<{ already: boolean; library: string }>(`/api/media/${item!.sha256}/library`, {}),
    onSuccess: (r) => {
      setDecision("library");
      toast.success(`${r.already ? t("media.already") : t("media.sent")}: ${r.library}`);
      qc.invalidateQueries({ queryKey: ["media"] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (!item) return null;
  const video = item.mime?.startsWith("video/");
  return (
    <DialogPrimitive.Root open onOpenChange={(o) => !o && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/95" />
        <DialogPrimitive.Content className="fixed inset-0 z-50 flex flex-col text-white outline-none" aria-describedby={undefined}>
          <DialogPrimitive.Title className="sr-only">{t("kind.image")}</DialogPrimitive.Title>
          <div className="flex items-center gap-1 p-2 pt-[max(0.5rem,env(safe-area-inset-top))]">
            <button onClick={onClose} className="grid size-11 place-items-center rounded-full hover:bg-white/10" aria-label={t("common.close")}><X /></button>
            <div className="min-w-0 flex-1 px-2 text-sm opacity-80">{item.ts ? fullDate(item.ts) : ""}</div>
            {onOpenChat && item.chat_id && (
              <button onClick={() => onOpenChat(item)} className="grid size-11 place-items-center rounded-full hover:bg-white/10" title={t("media.openChat")} aria-label={t("media.openChat")}><ExternalLink className="size-5" /></button>
            )}
            {item.available !== "gone" && (
              <a href={`/api/media/${item.sha256}/original`} download className="grid size-11 place-items-center rounded-full hover:bg-white/10" title={t("common.download")} aria-label={t("common.download")}><Download className="size-5" /></a>
            )}
          </div>
          <div className="relative flex min-h-0 flex-1 items-center justify-center px-2">
            {video ? (
              <video key={item.sha256} src={`/api/media/${item.sha256}/original`} controls autoPlay playsInline className="max-h-full max-w-full rounded-lg" />
            ) : (
              <img key={item.sha256} src={`/api/media/${item.sha256}/preview`} alt="" className="max-h-full max-w-full rounded-lg object-contain" />
            )}
            {index! > 0 && (
              <button onClick={() => onIndex(index! - 1)} className="absolute left-2 grid size-12 place-items-center rounded-full bg-white/10 hover:bg-white/20" aria-label={t("common.previous")}><ChevronLeft /></button>
            )}
            {index! < items.length - 1 && (
              <button onClick={() => onIndex(index! + 1)} className="absolute right-2 grid size-12 place-items-center rounded-full bg-white/10 hover:bg-white/20" aria-label={t("common.next")}><ChevronRight /></button>
            )}
          </div>
          <div className="flex justify-center gap-2 p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
            <Choice active={decision === "keep"} onClick={() => decide.mutate(decision === "keep" ? null : "keep")} icon={<Check className="size-4" />}>{t("media.keep")}</Choice>
            <Choice active={decision === "library"} onClick={() => library.mutate()} icon={<Archive className="size-4" />} busy={library.isPending}>{t("media.toLibrary")}</Choice>
            <Choice active={decision === "remove"} onClick={() => decide.mutate(decision === "remove" ? null : "remove")} icon={<Trash2 className="size-4" />} danger>{t("media.remove")}</Choice>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

function Choice({ active, onClick, icon, children, danger, busy }: {
  active: boolean; onClick: () => void; icon: React.ReactNode; children: React.ReactNode; danger?: boolean; busy?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      disabled={busy}
      className={cn("flex items-center gap-2 rounded-full border border-white/20 px-4 py-2 text-sm transition-colors hover:bg-white/10",
        active && (danger ? "border-transparent bg-red-500/80" : "border-transparent bg-white text-black hover:bg-white/90"))}
    >
      {icon}
      {children}
    </button>
  );
}
