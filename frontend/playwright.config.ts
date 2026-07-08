import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 60_000,
  expect: {
    timeout: 10_000
  },
  fullyParallel: false,
  workers: 1,
  // The suite shares one backend process (no per-test isolation of the global
  // matcher), so a leftover queued request from one test can be grabbed by the
  // next test's responder — a known, tracked isolation weakness that flakes
  // under CI's slower timing. Retry to absorb it until per-test isolation lands.
  retries: process.env.CI ? 2 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: "http://127.0.0.1:5173",
    trace: "retain-on-failure"
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] }
    }
  ],
  webServer: [
    {
      command: "cd ../backend && go run ./cmd/server",
      url: "http://127.0.0.1:8080/api/health",
      reuseExistingServer: true,
      timeout: 20_000
    },
    {
      command: "npm run dev -- --host 127.0.0.1",
      url: "http://127.0.0.1:5173",
      reuseExistingServer: true,
      timeout: 20_000
    }
  ]
});

