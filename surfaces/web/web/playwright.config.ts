import { defineConfig, devices } from "@playwright/test";

// The e2e checks drive the page the Go binary serves, bundle included, over
// internal/e2eserver's stub connections. They run with `make test-web-e2e`.
const port = 8099;

export default defineConfig({
  testDir: "e2e",
  testMatch: "*.e2e.ts",
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? "github" : "list",
  use: { baseURL: `http://127.0.0.1:${port}`, trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: `go run ./internal/e2eserver -addr 127.0.0.1:${port}`,
    cwd: "..",
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
