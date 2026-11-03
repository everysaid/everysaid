import { defineConfig } from "vitest/config";

// The unit tests, beside the code they test; e2e/ is Playwright's (pnpm exec playwright test).
export default defineConfig({ test: { include: ["src/**/*.test.ts"] } });
