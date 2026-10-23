import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTypescript from "eslint-config-next/typescript";

const layers = ["shared", "entities", "features", "widgets", "_pages", "_app"];

export default defineConfig([
  ...nextVitals,
  ...nextTypescript,
  { rules: { "@typescript-eslint/no-explicit-any": "error" } },
  ...layers.map((layer, index) => ({
    files: [`src/${layer}/**/*.{ts,tsx}`],
    rules: {
      "no-restricted-imports": ["error", {
        patterns: [{
          group: ["@/../app/**", ...layers.slice(index + 1).flatMap((upper) => [`@/${upper}`, `@/${upper}/**`])],
          message: "FSD: import only from lower layers. Use the slice public API.",
        }],
      }],
    },
  })),
  globalIgnores([".next/**", "out/**", "next-env.d.ts"]),
]);
