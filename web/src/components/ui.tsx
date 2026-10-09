import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import * as DropdownPrimitive from "@radix-ui/react-dropdown-menu";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cva, type VariantProps } from "class-variance-authority";
import { Loader2, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { service } from "@/lib/services";
import i18n from "@/lib/i18n";

// ---- Button ------------------------------------------------------------------------------------

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-xl text-sm font-medium transition-[background,color,box-shadow,transform] active:scale-[.98] disabled:pointer-events-none disabled:opacity-50 select-none",
  {
    variants: {
      variant: {
        primary: "bg-accent text-accent-fg hover:brightness-110 shadow-sm",
        secondary: "bg-panel-2 text-fg hover:bg-line",
        ghost: "text-fg hover:bg-panel-2",
        outline: "border border-line bg-panel hover:bg-panel-2",
        danger: "bg-danger text-white hover:brightness-110",
      },
      size: { sm: "h-8 px-3 text-xs", md: "h-10 px-4", lg: "h-12 px-6 text-base", icon: "size-10", iconSm: "size-8" },
    },
    defaultVariants: { variant: "secondary", size: "md" },
  },
);

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  loading?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, loading, children, disabled, ...props }, ref) => (
    <button ref={ref} className={cn(buttonVariants({ variant, size }), className)} disabled={disabled || loading} {...props}>
      {loading && <Loader2 className="size-4 animate-spin" />}
      {children}
    </button>
  ),
);
Button.displayName = "Button";

export function IconButton({ label, children, className, ...props }: ButtonProps & { label: string }) {
  return (
    <Tip label={label}>
      <Button variant="ghost" size="icon" aria-label={label} className={cn("rounded-full", className)} {...props}>
        {children}
      </Button>
    </Tip>
  );
}

// ---- Inputs ------------------------------------------------------------------------------------

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={cn(
        "h-10 w-full rounded-xl border border-line bg-panel px-3 text-sm outline-none placeholder:text-muted focus:border-accent focus:ring-2 focus:ring-accent/25",
        className,
      )}
      {...props}
    />
  ),
);
Input.displayName = "Input";

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn(
        "w-full resize-none rounded-xl border border-line bg-panel px-3 py-2 text-sm outline-none placeholder:text-muted focus:border-accent focus:ring-2 focus:ring-accent/25",
        className,
      )}
      {...props}
    />
  ),
);
Textarea.displayName = "Textarea";

export function Field({ label, help, error, children }: { label: string; help?: string; error?: string | false; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="text-sm font-medium">{label}</span>
      {children}
      {error ? <span role="alert" className="block text-xs text-danger">{error}</span> : help && <span className="block text-xs text-muted">{help}</span>}
    </label>
  );
}

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label?: string; disabled?: boolean }) {
  return (
    <SwitchPrimitive.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      aria-label={label}
      className="relative h-6 w-11 shrink-0 rounded-full bg-line transition-colors data-[state=checked]:bg-accent disabled:opacity-50"
    >
      <SwitchPrimitive.Thumb className="block size-5 translate-x-0.5 rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[22px]" />
    </SwitchPrimitive.Root>
  );
}

export function Segmented<T extends string>({ value, onChange, options, className }: {
  value: T; onChange: (v: T) => void; options: { value: T; label: React.ReactNode }[]; className?: string;
}) {
  return (
    <div role="tablist" className={cn("inline-flex rounded-xl bg-panel-2 p-1", className)}>
      {options.map((o) => (
        <button
          key={o.value}
          role="tab"
          aria-selected={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded-lg px-3 py-1.5 text-xs font-medium text-muted transition-colors",
            value === o.value && "bg-panel text-fg shadow-sm",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ---- Overlays ----------------------------------------------------------------------------------

export function Dialog({ open, onOpenChange, title, description, children, className, wide }: {
  open: boolean; onOpenChange: (o: boolean) => void; title: React.ReactNode; description?: React.ReactNode;
  children: React.ReactNode; className?: string; wide?: boolean;
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm data-[state=open]:animate-in" />
        <DialogPrimitive.Content
          className={cn(
            "fixed z-50 flex max-h-[92dvh] w-full flex-col overflow-hidden border border-line bg-panel shadow-2xl outline-none",
            "inset-x-0 bottom-0 rounded-t-3xl pb-[env(safe-area-inset-bottom)] sm:inset-auto sm:left-1/2 sm:top-1/2 sm:-translate-x-1/2 sm:-translate-y-1/2 sm:rounded-3xl",
            wide ? "sm:max-w-2xl" : "sm:max-w-md",
            className,
          )}
        >
          <div className="flex items-start gap-3 border-b border-line px-5 py-4">
            <div className="min-w-0 flex-1">
              <DialogPrimitive.Title className="text-base font-semibold">{title}</DialogPrimitive.Title>
              {description ? (
                <DialogPrimitive.Description className="mt-0.5 text-sm text-muted">{description}</DialogPrimitive.Description>
              ) : (
                <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
              )}
            </div>
            <DialogPrimitive.Close asChild>
              <Button variant="ghost" size="iconSm" aria-label={i18n.t("common.close")} className="rounded-full">
                <X className="size-4" />
              </Button>
            </DialogPrimitive.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

export const Menu = DropdownPrimitive.Root;
export const MenuTrigger = DropdownPrimitive.Trigger;

export function MenuContent({ children, align = "end", className }: { children: React.ReactNode; align?: "start" | "end" | "center"; className?: string }) {
  return (
    <DropdownPrimitive.Portal>
      <DropdownPrimitive.Content
        align={align}
        sideOffset={6}
        className={cn("z-50 min-w-48 overflow-hidden rounded-2xl border border-line bg-panel p-1 shadow-xl", className)}
      >
        {children}
      </DropdownPrimitive.Content>
    </DropdownPrimitive.Portal>
  );
}

export function MenuItem({ children, onSelect, danger, icon }: {
  children: React.ReactNode; onSelect?: () => void; danger?: boolean; icon?: React.ReactNode;
}) {
  return (
    <DropdownPrimitive.Item
      onSelect={onSelect}
      className={cn(
        "flex cursor-default select-none items-center gap-2.5 rounded-xl px-3 py-2 text-sm outline-none data-[highlighted]:bg-panel-2",
        danger && "text-danger",
      )}
    >
      {icon && <span className="text-muted [&_svg]:size-4">{icon}</span>}
      {children}
    </DropdownPrimitive.Item>
  );
}

export function Tip({ label, children }: { label: string; children: React.ReactElement }) {
  return (
    <TooltipPrimitive.Root delayDuration={400}>
      <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content sideOffset={6} className="z-50 rounded-lg bg-fg px-2 py-1 text-xs text-bg shadow">
          {label}
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}

export const TipProvider = TooltipPrimitive.Provider;

// ---- Small pieces ------------------------------------------------------------------------------

const PALETTE = ["#f97316", "#eab308", "#22c55e", "#14b8a6", "#06b6d4", "#3b82f6", "#6366f1", "#a855f7", "#ec4899", "#f43f5e"];

function hash(s: string) {
  let h = 0;
  for (const c of s) h = (h * 31 + c.charCodeAt(0)) | 0;
  return Math.abs(h);
}

export function initials(name: string) {
  const words = name.replace(/[^\p{L}\p{N} ]/gu, "").trim().split(/\s+/).filter(Boolean);
  if (!words.length) return "#";
  return (words[0][0] + (words.length > 1 ? words[words.length - 1][0] : "")).toUpperCase();
}

export function Avatar({ name, src, size = 44, group, className }: {
  name: string; src?: string | null; size?: number; group?: boolean; className?: string;
}) {
  const [failed, setFailed] = React.useState(false);
  const color = PALETTE[hash(name) % PALETTE.length];
  return (
    <div
      className={cn("relative shrink-0 overflow-hidden rounded-full font-semibold text-white grid place-items-center", className)}
      style={{ width: size, height: size, background: `linear-gradient(135deg, ${color}, color-mix(in oklab, ${color} 60%, #000))`, fontSize: size * 0.38 }}
      aria-hidden
    >
      {src && !failed ? (
        <img src={src} alt="" className="size-full object-cover" loading="lazy" onError={() => setFailed(true)} />
      ) : group || !/\p{L}/u.test(name) ? (
        group ? (
          <svg viewBox="0 0 24 24" className="size-[55%] fill-white/90"><path d="M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Zm7 0a3 3 0 1 0 0-6 3 3 0 0 0 0 6ZM9 13c-3.3 0-7 1.6-7 4.5V20h14v-2.5C16 14.6 12.3 13 9 13Zm7 0c-.5 0-1 0-1.5.1 1.5 1 2.5 2.5 2.5 4.4V20h5v-2.5c0-2.9-3.2-4.5-6-4.5Z"/></svg>
        ) : (
          <svg viewBox="0 0 24 24" className="size-[52%] fill-white/90"><path d="M12 12a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9Zm0 2c-4 0-8 2-8 5.2V21h16v-1.8C20 16 16 14 12 14Z"/></svg>
        )
      ) : (
        initials(name)
      )}
    </div>
  );
}

export function ServiceDot({ id, className }: { id: string; className?: string }) {
  const s = service(id);
  return <span title={s.name} className={cn("inline-block size-2 shrink-0 rounded-full", className)} style={{ background: s.color }} />;
}

/** A service's name in its colour; compact: its short name on a narrow screen. */
export function ServiceBadge({ id, className, compact }: { id: string; className?: string; compact?: boolean }) {
  const s = service(id);
  return (
    <span
      className={cn("inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium", className)}
      style={{ background: `color-mix(in oklab, ${s.color} 16%, transparent)`, color: `color-mix(in oklab, ${s.color} 75%, var(--fg))` }}
    >
      <span className="size-1.5 rounded-full" style={{ background: s.color }} />
      {compact ? <><span className="md:hidden">{s.short}</span><span className="hidden md:inline">{s.name}</span></> : s.name}
    </span>
  );
}

/** A service's own icon, in its colour (its first letters where it has none). */
export function ServiceIcon({ id, className }: { id: string; className?: string }) {
  const s = service(id);
  if (!s.icon) return <span className={cn("grid size-4 place-items-center text-[9px] font-bold", className)} style={{ color: s.color }}>{s.short}</span>;
  return <svg data-icon={id} viewBox="0 0 24 24" aria-hidden className={cn("size-4 shrink-0", className)} fill={s.color}><path d={s.icon} /></svg>;
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn("size-5 animate-spin text-muted", className)} />;
}

/** A thin bar at the top of a list, moving while something loads. */
export function LoadingBar({ active }: { active: boolean }) {
  return (
    <div data-loading={active || undefined} aria-hidden className={cn("pointer-events-none absolute inset-x-0 top-0 z-20 h-0.5 overflow-hidden transition-opacity", active ? "opacity-100" : "opacity-0")}>
      <div className="loading-bar h-full w-1/3 rounded-full bg-accent" />
    </div>
  );
}

/** The end of a list that loads more by itself as it comes near (no button); a spinner while it does. */
export function MoreOnScroll({ hasMore, loading, onMore }: { hasMore: boolean; loading: boolean; onMore: () => void }) {
  const ref = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    const el = ref.current;
    if (!el || !hasMore) return;
    const io = new IntersectionObserver((e) => { if (e[0].isIntersecting && !loading) onMore(); }, { rootMargin: "600px" });
    io.observe(el);
    return () => io.disconnect();
  }, [hasMore, loading, onMore]);
  return <div ref={ref} className="flex justify-center py-4">{hasMore && loading && <Spinner />}</div>;
}

export function Center({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("grid h-full place-items-center p-6 text-center", className)}>{children}</div>;
}

export function Empty({ icon, title, hint, action }: { icon?: React.ReactNode; title: string; hint?: string; action?: React.ReactNode }) {
  return (
    <Center>
      <div className="max-w-sm space-y-3">
        {icon && <div className="mx-auto grid size-16 place-items-center rounded-3xl bg-panel-2 text-muted [&_svg]:size-7">{icon}</div>}
        <div className="font-semibold">{title}</div>
        {hint && <div className="text-sm text-muted">{hint}</div>}
        {action}
      </div>
    </Center>
  );
}

export function Card({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("rounded-2xl border border-line bg-panel", className)}>{children}</div>;
}

export function Section({ title, children, action, className }: { title: React.ReactNode; children: React.ReactNode; action?: React.ReactNode; className?: string }) {
  return (
    <section className={cn("space-y-3", className)}>
      <div className="flex items-center justify-between gap-2 px-1">
        <h2 className="text-sm font-semibold text-muted">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

export function Row({ children, className, onClick }: { children: React.ReactNode; className?: string; onClick?: () => void }) {
  return (
    <div onClick={onClick} className={cn("flex items-center gap-3 px-4 py-3", onClick && "cursor-pointer hover:bg-panel-2", className)}>
      {children}
    </div>
  );
}
