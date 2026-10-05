import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against a demo archive of invented people (never the user's):
//   uv run chronika demo --dir /tmp/chr-demo && CHRONIKA_DATA=... chronika serve   (see e2e/README)
export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  workers: 1,
  use: { baseURL: process.env.BASE_URL ?? "http://localhost:8530", trace: "retain-on-failure", locale: process.env.LOCALE ?? "el-GR" },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1400, height: 900 } } },
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],
});
