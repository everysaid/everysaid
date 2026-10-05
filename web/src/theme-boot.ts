// Runs before the app: the theme from the last visit, so the page does not flash.
const t = localStorage.getItem("theme") || "system";
const dark = t === "dark" || (t === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
document.documentElement.classList.toggle("dark", dark);
document.documentElement.lang = localStorage.getItem("lang") || (navigator.language.startsWith("el") ? "el" : "en");
