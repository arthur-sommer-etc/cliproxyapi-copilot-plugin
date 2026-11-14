import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

import capturedJsonFixtures from "./tools/captured-json-fixtures.json" with { type: "json" };

export default defineConfig({
  ...ultracite,
  // Captured bytes are validated by packaging/test_captured_json_fixtures.py.
  ignorePatterns: [
    ...(ultracite.ignorePatterns ?? []),
    "**/*.md",
    ...capturedJsonFixtures.map(({ path }) => path)
  ],
  trailingComma: "none"
});
