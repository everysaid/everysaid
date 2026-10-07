import { useState } from "react";
import * as Popover from "@radix-ui/react-popover";
import { useTranslation } from "react-i18next";
import { Check, ListFilter, Minus } from "lucide-react";
import { number } from "@/lib/format";
import { useSettings } from "@/lib/hooks";
import { service } from "@/lib/services";
import { cn } from "@/lib/utils";
import { labelName, useLabels } from "./Labels";
import { Button, Segmented, ServiceDot, Switch } from "./ui";

/** What the chat list is narrowed to, kept on this device. unnamed: undefined follows the setting. */
export interface ChatFilters {
  archived: boolean;
  unnamed?: boolean;
  minMessages: number;          // the number of messages the slider is at
  fewer?: boolean;              // at most that many, not at least
  services: Record<string, "with" | "without">;
  label?: number;
}

export const NO_FILTERS: ChatFilters = { archived: false, minMessages: 0, services: {} };
const KEY = "chat-filters";

export function savedFilters(): ChatFilters {
  try {
    return { ...NO_FILTERS, ...JSON.parse(localStorage.getItem(KEY) ?? "{}") };
  } catch {
    return NO_FILTERS;
  }
}

export function keepFilters(f: ChatFilters) {
  localStorage.setItem(KEY, JSON.stringify(f));
}

/** How many filters narrow the list (the archived view is a view, not counted). */
export function activeFilters(f: ChatFilters) {
  return (f.unnamed !== undefined ? 1 : 0) + (f.minMessages > 0 || f.fewer ? 1 : 0) + Object.keys(f.services).length + (f.label ? 1 : 0);
}

/** The server's words for them. */
export function filterParams(f: ChatFilters) {
  const pick = (w: "with" | "without") => Object.entries(f.services).filter(([, v]) => v === w).map(([s]) => s).join(",") || undefined;
  return { archived: f.archived || undefined, unnamed: f.unnamed, min_messages: f.minMessages || undefined,
    max_messages: f.fewer ? f.minMessages : undefined, ...(f.fewer ? { min_messages: undefined } : {}),
    services: pick("with"), no_services: pick("without"), label: f.label };
}

// the least messages, in steps that grow as the numbers do: a few, tens, hundreds, thousands
const STEPS = [0, 1, 2, 3, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000];

export function ChatFiltersButton({ value, onChange, seen }: { value: ChatFilters; onChange: (f: ChatFilters) => void; seen: string[] }) {
  const { t } = useTranslation();
  const settings = useSettings();
  const labels = useLabels();
  const [open, setOpen] = useState(false);
  const n = activeFilters(value);
  const set = (part: Partial<ChatFilters>) => onChange({ ...value, ...part });
  const step = Math.max(0, STEPS.findIndex((s) => s >= value.minMessages));
  const unnamed = value.unnamed ?? ((settings.data?.show_unnamed as boolean | undefined) ?? true);
  const services = [...new Set([...seen, ...Object.keys(value.services)])].filter((s) => service(s).messages !== false || value.services[s]).sort();
  const used = (labels.data?.items ?? []).filter((l) => l.uses.yes + l.uses.suggested > 0 || l.id === value.label);
  const cycle = (s: string) => {
    const next = { ...value.services };
    const now = next[s];
    if (!now) next[s] = "with";
    else if (now === "with") next[s] = "without";
    else delete next[s];
    set({ services: next });
  };
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button data-chat-filters aria-label={t("chats.filters")} title={t("chats.filters")}
          className={cn("relative ml-auto grid size-9 place-items-center rounded-full text-muted hover:bg-panel-2",
            (n > 0 || value.archived) && "bg-accent/12 text-accent")}>
          <ListFilter className="size-4" />
          {n > 0 && <span className="absolute -right-0.5 -top-0.5 min-w-4 rounded-full bg-accent px-1 text-[10px] font-bold leading-4 text-accent-fg">{n}</span>}
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} collisionPadding={8}
          className="z-50 max-h-[80dvh] w-[min(22rem,calc(100vw-1rem))] space-y-4 overflow-y-auto rounded-2xl border border-line bg-panel p-4 shadow-xl" data-filters-panel>
          <label className="flex items-center justify-between gap-3 text-sm">
            {t("chats.showArchived")}
            <Switch checked={value.archived} onChange={(v) => set({ archived: v })} label={t("chats.showArchived")} />
          </label>
          <label className="flex items-center justify-between gap-3 text-sm">
            <span>{t("chats.withUnnamed")}{value.unnamed === undefined && <span className="block text-xs text-muted">{t("chats.asSettings")}</span>}</span>
            <Switch checked={unnamed} onChange={(v) => set({ unnamed: v })} label={t("chats.withUnnamed")} />
          </label>
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2 text-sm">
              {t("chats.messages")}
              <Segmented value={value.fewer ? "fewer" : "more"} onChange={(v) => set({ fewer: v === "fewer" })}
                options={[{ value: "more", label: t("chats.atLeastWord") }, { value: "fewer", label: t("chats.atMostWord") }]} />
              <span className="min-w-12 text-right font-medium tabular-nums" data-min-shown>
                {value.fewer ? t("chats.atMost", { n: number(value.minMessages) }) : value.minMessages ? t("chats.atLeast", { n: number(value.minMessages) }) : t("chats.any")}
              </span>
            </div>
            <input type="range" min={0} max={STEPS.length - 1} step={1} value={step} data-min-messages
              aria-label={t("chats.messages")} className="w-full accent-[var(--accent)]"
              onChange={(e) => set({ minMessages: STEPS[Number(e.target.value)] })} />
          </div>
          {services.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-sm">{t("chats.services")}<span className="block text-xs text-muted">{t("chats.servicesHint")}</span></div>
              <div className="flex flex-wrap gap-1.5">
                {services.map((s) => {
                  const v = value.services[s];
                  return (
                    <button key={s} type="button" onClick={() => cycle(s)} data-service-filter={s} data-state={v ?? "any"}
                      title={v === "with" ? t("chats.withService") : v === "without" ? t("chats.withoutService") : undefined}
                      className={cn("flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs",
                        v === "with" ? "border-ok bg-ok/12" : v === "without" ? "border-danger bg-danger/10 text-muted line-through" : "border-line hover:bg-panel-2")}>
                      {v === "with" ? <Check className="size-3 text-ok" /> : v === "without" ? <Minus className="size-3 text-danger" /> : <ServiceDot id={s} />}
                      {service(s).name}
                    </button>
                  );
                })}
              </div>
            </div>
          )}
          {used.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-sm">{t("labels.title")}</div>
              <div className="flex flex-wrap gap-1.5">
                {used.map((l) => (
                  <button key={l.id} type="button" onClick={() => set({ label: value.label === l.id ? undefined : l.id })} aria-pressed={value.label === l.id}
                    className={cn("rounded-full border px-2.5 py-1 text-xs", value.label === l.id ? "border-accent bg-accent text-accent-fg" : "border-line hover:bg-panel-2")}>
                    {labelName(t, l)}
                  </button>
                ))}
              </div>
            </div>
          )}
          {(n > 0 || value.archived) && (
            <Button size="sm" variant="ghost" className="w-full" onClick={() => onChange(NO_FILTERS)} data-filters-clear>{t("chats.clearFilters")}</Button>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
