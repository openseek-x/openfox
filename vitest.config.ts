import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["packages/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./tests/frontend/setup.ts"],
    restoreMocks: true,
  },
});
