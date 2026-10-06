import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { CalendarDays } from "lucide-react";
import { loc } from "@/lib/format";
import { cn } from "@/lib/utils";

type Part = "day" | "month" | "year";
const LEN: Record<Part, number> = { day: 2, month: 2, year: 4 };

/** The order of day, month and year in the app's language, and what goes between them. */
function layout() {
  const parts = new Intl.DateTimeFormat(loc(), { year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date(2001, 10, 22));
  const order = parts.filter((p) => p.type === "day" || p.type === "month" || p.type === "year").map((p) => p.type as Part);
  const sep = parts.find((p) => p.type === "literal")?.value.trim() || "/";
  return { order, sep };
}

/** The typed text as the parts it holds: digits fill one part, then the next; a separator typed
 * closes the part (a single digit padded: 1/2 is 01/02). */
function read(raw: string, order: Part[]) {
  const got: string[] = ["", "", ""];
  let i = 0;
  for (const ch of raw) {
    if (/\d/.test(ch)) {
      if (got[i].length === LEN[order[i]] && i < 2) i++;
      if (got[i].length < LEN[order[i]]) got[i] += ch;
    } else if (got[i] && i < 2) {
      got[i] = got[i].padStart(LEN[order[i]], "0");
      i++;
    }
  }
  return { got, i };
}

function iso(got: string[], order: Part[]) {
  const v = Object.fromEntries(order.map((p, k) => [p, got[k]])) as Record<Part, string>;
  if (v.year.length !== 4 || v.month.length !== 2 || v.day.length !== 2) return null;
  const d = new Date(Number(v.year), Number(v.month) - 1, Number(v.day));
  if (d.getFullYear() !== Number(v.year) || d.getMonth() !== Number(v.month) - 1 || d.getDate() !== Number(v.day)) return null;
  return `${v.year}-${v.month}-${v.day}`;
}

/** A date written in the app's language's order (ηη/μμ/εεεε, mm/dd/yyyy…), or picked from the
 * calendar. It counts only once whole (a year of four digits) and real: onChange("yyyy-mm-dd"), or
 * "" once emptied. */
export function DateField({ value, onChange, min, max, autoFocus, className, label }: {
  value: string; onChange: (iso: string) => void; min?: string; max?: string; autoFocus?: boolean; className?: string; label?: string;
}) {
  const { t, i18n } = useTranslation();
  const { order, sep } = useMemo(layout, [i18n.language]);
  const show = (v: string) => {
    if (!v) return "";
    const [y, m, d] = v.split("-");
    const of: Record<Part, string> = { year: y, month: m, day: d };
    return order.map((p) => of[p]).join(sep);
  };
  const [text, setText] = useState(() => show(value));
  const [bad, setBad] = useState(false);
  useEffect(() => { setText(show(value)); setBad(false); }, [value, sep]); // eslint-disable-line react-hooks/exhaustive-deps
  const picker = useRef<HTMLInputElement>(null);
  const letters: Record<Part, string> = { day: t("date.day"), month: t("date.month"), year: t("date.year") };
  const hint = order.map((p) => letters[p]).join(sep);

  const typed = (raw: string) => {
    const { got, i } = read(raw, order);
    setText(got.slice(0, i + 1).join(sep));
    if (!raw.trim()) { setBad(false); if (value) onChange(""); return; }
    const whole = got.every((g, k) => g.length === LEN[order[k]]);
    const v = whole ? iso(got, order) : null;
    const out = !!v && ((min && v < min) || (max && v > max));
    setBad(whole && (!v || !!out));
    if (v && !out && v !== value) onChange(v);
  };

  return (
    <span className={cn("relative inline-flex h-9 items-center rounded-xl border bg-panel pl-2 text-fg focus-within:ring-2 focus-within:ring-accent/30",
      bad ? "border-danger" : "border-line", className)}>
      <input
        data-date-field
        inputMode="numeric"
        autoFocus={autoFocus}
        aria-label={label}
        aria-invalid={bad || undefined}
        value={text}
        placeholder={hint}
        onChange={(e) => typed(e.target.value)}
        className="w-[7.5rem] bg-transparent text-sm tabular-nums outline-none placeholder:text-muted"
      />
      <button type="button" className="grid h-full place-items-center px-2 text-muted hover:text-fg" aria-label={t("date.pick")} title={t("date.pick")}
        onClick={() => { try { picker.current?.showPicker(); } catch { picker.current?.focus(); } }}>
        <CalendarDays className="size-4" />
      </button>
      {/* the browser's calendar, for picking with a click: what it gives is whole */}
      <input ref={picker} type="date" tabIndex={-1} aria-hidden value={value} min={min} max={max}
        onChange={(e) => e.target.value && e.target.value !== value && onChange(e.target.value)}
        className="pointer-events-none absolute bottom-0 right-0 size-px opacity-0" />
    </span>
  );
}
