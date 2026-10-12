export type Theme = "light" | "dark" | "system";

export function getTheme(): Theme {
  return (localStorage.getItem("theme") as Theme) || "system";
}

export function applyTheme(t: Theme) {
  localStorage.setItem("theme", t);
  const dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  document.querySelectorAll('meta[name="theme-color"]').forEach((m) => m.setAttribute("content", dark ? "#0b0d12" : "#f7f7f9"));
}

matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (getTheme() === "system") applyTheme("system");
});

/** The main colour by default: the icon's dark brown. The dark theme takes a lighter shade of it
 * (index.css). */
export const ACCENT = "#5e4428";

/** The main colour as the user chose it (null: the default), kept on this device too, so that the
 * next visit starts with it (theme-boot). */
export function applyAccent(c: string | null) {
  if (c) localStorage.setItem("accent", c);
  else localStorage.removeItem("accent");
  paintAccent();
}

/** The colour kept on this device put on the page, with the text on it (white, or dark on a light
 * colour). */
export function paintAccent() {
  const c = localStorage.getItem("accent");
  const style = document.documentElement.style;
  if (c && /^#[0-9a-f]{6}$/i.test(c)) {
    style.setProperty("--accent-user", c);
    style.setProperty("--accent-user-fg", light(c) ? "#12141a" : "#ffffff");
  } else {
    style.removeProperty("--accent-user");
    style.removeProperty("--accent-user-fg");
  }
}

// whether dark text reads better on it than white (relative luminance, WCAG)
function light(hex: string) {
  const ch = (i: number) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  const l = 0.2126 * ch(1) + 0.7152 * ch(3) + 0.0722 * ch(5);
  return (l + 0.05) / 0.05 > 1.05 / (l + 0.05);
}
