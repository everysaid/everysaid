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

/** Each theme's main colour by default: the icon's dark brown, and its light one on the dark theme. */
export const ACCENT = { light: "#5e4428", dark: "#c29c62" } as const;

/** Each theme's main colour as the user chose it (null: the default), kept on this device too, so
 * that the next visit starts with it (theme-boot). */
export function applyAccent(light: string | null, dark: string | null) {
  localStorage.setItem("accent", JSON.stringify({ light, dark }));
  paintAccent();
}

/** The colours kept on this device put on the page: each theme's main colour, and the text on it
 * (white, or dark on a light colour). */
export function paintAccent() {
  let kept: { light?: string | null; dark?: string | null } = {};
  try { kept = JSON.parse(localStorage.getItem("accent") || "{}"); } catch { /* none */ }
  const style = document.documentElement.style;
  for (const mode of ["light", "dark"] as const) {
    const c = kept[mode];
    if (c) {
      style.setProperty(`--accent-${mode}`, c);
      style.setProperty(`--accent-${mode}-fg`, light(c) ? "#12141a" : "#ffffff");
    } else {
      style.removeProperty(`--accent-${mode}`);
      style.removeProperty(`--accent-${mode}-fg`);
    }
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
