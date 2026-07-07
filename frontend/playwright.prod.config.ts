import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e-prod",
  timeout: 150_000,
  expect: {
    timeout: 15_000
  },
  fullyParallel: false,
  reporter: [["list"]],
  use: {
    baseURL: process.env.PROD_BASE_URL ?? "https://chat.fuck-clau.de",
    trace: "retain-on-failure"
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] }
    }
  ]
});
