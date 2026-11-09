import { defineConfig, devices } from "@playwright/test";

// The e2e checks drive the page the Go binary serves, bundle included, over
// internal/e2eserver's stub connections. They run with `make test-web-e2e`.
// The <chakra-chat> checks load host pages from pagesPort, a second origin,
// so the element calls the API across origins as it would from another app.
const port = 8099;
const pagesPort = 8098;

export default defineConfig({
  testDir: "e2e",
  testMatch: "*.e2e.ts",
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? "github" : "list",
  use: { baseURL: `http://127.0.0.1:${port}`, trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [
    {
      command: `node web/e2e/pages/build.mjs && go run ./internal/e2eserver -addr 127.0.0.1:${port} -pages-addr 127.0.0.1:${pagesPort}`,
      cwd: "..",
      url: `http://127.0.0.1:${port}/`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    },
    {
      // The <chakra-chat> room checks (#324): an app origin with a relay, a
      // rooms bridge and a page on 8097, and agentweb on 8096. It is its own
      // Go module, so massrelay stays out of this one.
      command: "go run . -app-addr 127.0.0.1:8097 -agent-addr 127.0.0.1:8096",
      cwd: "../../../tests/chat_element_rooms_e2e",
      url: "http://127.0.0.1:8097/",
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
    },
  ],
});
