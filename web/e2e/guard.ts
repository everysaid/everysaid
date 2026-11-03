import type { FullConfig } from "@playwright/test";

// Before any test: the server is the demo (`everysaid demo --serve`), never one with the user's own
// accounts behind it, where the tests' sending would go out for real.
export default async function guard(config: FullConfig) {
  const base = config.projects[0].use.baseURL!;
  const health = await fetch(new URL("/api/health", base)).then((r) => r.json()).catch(() => null);
  if (health?.demo !== true) throw new Error(`${base} is not the demo: the tests run only against \`everysaid demo --serve\``);
}
