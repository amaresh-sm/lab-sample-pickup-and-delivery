import { defineConfig } from "@playwright/test";

const externalBaseURL = process.env.GOLDEN_BASE_URL;
const baseURL = externalBaseURL ?? "http://127.0.0.1:8080";

export default defineConfig({
  testDir: "./tests",
  // Keep Playwright's mutable run metadata under a verifier-ignored directory
  // so candidate tests never alter the submitted source fingerprint.
  outputDir: "./coverage/playwright",
  timeout: 30_000,
  expect: { timeout: 8_000 },
  fullyParallel: false,
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: externalBaseURL
    ? undefined
    : {
        command: "npm run dev -- --port 8080",
        url: baseURL,
        reuseExistingServer: true,
        timeout: 30_000,
      },
  reporter: [["list"]],
});
