import { existsSync } from "node:fs";
import { defineConfig } from "@playwright/test";

const systemChromium = "/usr/bin/chromium";

export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:5173",
    colorScheme: "dark",
    launchOptions: existsSync(systemChromium) ? { executablePath: systemChromium } : undefined,
  },
  webServer: {
    command: "pnpm run dev --host 127.0.0.1",
    url: "http://127.0.0.1:5173",
    reuseExistingServer: true,
    timeout: 30_000,
  },
});
