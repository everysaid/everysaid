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
