import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { api, thumbUrl, type Person } from "@/lib/api";
import { pinnedAvatarUrl } from "@/lib/avatar";
import { dateOnly } from "@/lib/format";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { Avatar, ServiceBadge } from "./ui";

/** Where a person's photo comes from: by the order of names (automatic), a source, one handle, or none. */
export function PhotoFrom({ p }: { p: Person }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const pin = useMutation({
    mutationFn: (v: string) => api.patch(`/api/people/${p.id}`, { photo_source: v || null }),
    onSuccess: () => qc.invalidateQueries(),
    onError: (e: Error) => toast.error(e.message),
  });
  if (p.photo_choices.length === 0) return null;
  const key = p.photo_history[0]?.sha.slice(0, 12) ?? p.avatar;
  const sourceName = (s: string) => (s === "contacts" ? t("people.photoContacts") : service(s).name);
  const options = [
    { pin: "", label: t("people.photoAuto"), src: pinnedAvatarUrl(p.id, "auto", key) },
    ...p.photo_choices.map((c) => ({
      pin: c.pin,
      label: c.address_id ? `${sourceName(c.source)} · ${c.label ?? ""}` : sourceName(c.source),
      src: pinnedAvatarUrl(p.id, c.pin, key),
    })),
    { pin: "none", label: t("people.photoNone"), src: null },
  ];
  const chosen = p.photo_source ?? "";
  return (
    <div className="space-y-1.5" data-photo-from>
      <div className="text-xs text-muted">
        {t("people.photoFrom")}
        {!p.photo_source && p.photo_from ? ` · ${sourceName(p.photo_from)}` : ""}
      </div>
      <div className="flex flex-wrap gap-2">
        {options.map((o) => (
          <button key={o.pin} type="button" title={o.label} aria-pressed={chosen === o.pin} onClick={() => chosen !== o.pin && pin.mutate(o.pin)}
            className={cn("flex w-16 flex-col items-center gap-1 rounded-xl p-1 text-[11px] leading-tight hover:bg-panel-2",
              chosen === o.pin && "bg-accent/15 ring-1 ring-accent")}>
            <Avatar name={p.name} src={o.src} size={44} />
            <span className="line-clamp-2 w-full break-words text-center">{o.label}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

/** Every photo the services have shown for the person, the latest first, old ones too. */
export function PhotoHistory({ p }: { p: Person }) {
  const { t } = useTranslation();
  if (p.photo_history.length === 0) return null;
  return (
    <div className="space-y-2" data-photo-history>
      <div className="text-sm font-semibold">{t("people.photoHistory")}</div>
      <div className="grid grid-cols-3 gap-3 sm:grid-cols-4">
        {p.photo_history.map((h) => (
          <div key={`${h.address_id}:${h.source}:${h.sha}:${h.first_seen}`} className="space-y-1 text-xs">
            <div className={cn("aspect-square overflow-hidden rounded-xl bg-panel-2", !h.current && "opacity-80")}>
              {h.available ? (
                <img src={thumbUrl(h.sha)} alt="" loading="lazy" className="size-full object-cover" />
              ) : (
                <div className="grid size-full place-items-center p-2 text-center text-muted">{t("people.photoGone")}</div>
              )}
            </div>
            <div className="flex items-center gap-1">
              <ServiceBadge id={h.source} />
              {h.label && <span className="min-w-0 truncate text-muted">{h.label}</span>}
            </div>
            <div className="text-muted">
              {h.first_seen === h.last_seen ? dateOnly(h.first_seen * 1000) : `${dateOnly(h.first_seen * 1000)} – ${dateOnly(h.last_seen * 1000)}`}
            </div>
            {h.removed_at && <div className="text-muted">{t("people.photoRemoved", { date: dateOnly(h.removed_at * 1000) })}</div>}
          </div>
        ))}
      </div>
    </div>
  );
}
