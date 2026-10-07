import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against a demo archive of invented people (never the user's):
//   everysaid demo --dir /tmp/chr-demo --serve        (port 8530; docs/go.md, "Developing")
// with EVERYSAID_CMD naming the same everysaid binary (else e2e/demo.ts uses `go run ./cmd/everysaid`).
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
