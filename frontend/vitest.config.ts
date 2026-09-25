import { defineConfig } from "vitest/config";
import path from "path";

// Unit tests for the pure logic under src/ — label resolution, relay
// selection, formatting. Deliberately node-environment and no jsdom: nothing
// here renders a component, and adding a DOM would buy setup cost for tests
// that do not need one. A component-rendering setup can be added later
// alongside it if that changes.
export default defineConfig({
  resolve: {
    // Mirrors the "src/..." absolute imports the app uses (see tsconfig paths).
    alias: { src: path.resolve(__dirname, "./src") },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
