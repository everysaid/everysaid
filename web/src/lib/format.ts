import i18n from "./i18n";

const loc = () => (i18n.language === "el" ? "el-GR" : i18n.language || "en");

function dayStart(d: Date) {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

export function time(ts: number) {
  return new Date(ts).toLocaleTimeString(loc(), { hour: "2-digit", minute: "2-digit" });
}

/** For the chat list: time today, weekday this week, date otherwise. */
export function shortWhen(ts: number) {
  if (!ts) return "";
  const d = new Date(ts);
  const today = dayStart(new Date());
  const day = dayStart(d);
  if (day === today) return time(ts);
  if (today - day < 6 * 86400_000) return d.toLocaleDateString(loc(), { weekday: "short" });
  if (d.getFullYear() === new Date().getFullYear()) return d.toLocaleDateString(loc(), { day: "numeric", month: "short" });
  return d.toLocaleDateString(loc(), { day: "numeric", month: "numeric", year: "2-digit" });
}

/** A day separator in a chat. */
export function dayLabel(ts: number) {
  const d = new Date(ts);
  const today = dayStart(new Date());
  const day = dayStart(d);
  if (day === today) return i18n.t("common.today");
  if (today - day === 86400_000) return i18n.t("common.yesterday");
  return d.toLocaleDateString(loc(), {
    weekday: "long", day: "numeric", month: "long",
    year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
  });
}

export function fullDate(ts: number | null | undefined) {
  if (!ts) return "—";
  return new Date(ts).toLocaleString(loc(), { day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function dateOnly(ts: number | null | undefined) {
  if (!ts) return "—";
  return new Date(ts).toLocaleDateString(loc(), { day: "numeric", month: "long", year: "numeric" });
}

export function sameDay(a: number, b: number) {
  return dayStart(new Date(a)) === dayStart(new Date(b));
}

export function duration(s: number) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  if (h) return `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
  return `${m}:${String(sec).padStart(2, "0")}`;
}

export function bytes(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

export function number(n: number) {
  return n.toLocaleString(loc());
}

export function isoDay(ts: number) {
  const d = new Date(ts);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function relative(sec: number | null | undefined) {
  if (!sec) return "";
  const diff = Date.now() / 1000 - sec;
  const rtf = new Intl.RelativeTimeFormat(loc(), { numeric: "auto" });
  if (diff < 60) return rtf.format(-Math.round(diff), "second");
  if (diff < 3600) return rtf.format(-Math.round(diff / 60), "minute");
  if (diff < 86400) return rtf.format(-Math.round(diff / 3600), "hour");
  return rtf.format(-Math.round(diff / 86400), "day");
}
