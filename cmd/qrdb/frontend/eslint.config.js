// ESLint flat config for the qrdb GUI (Svelte 5 + TypeScript).
import js from "@eslint/js";
import svelte from "eslint-plugin-svelte";
import globals from "globals";
import ts from "typescript-eslint";
import svelteConfig from "./svelte.config.js";

export default ts.config(
  {
    ignores: ["dist/", "node_modules/", "e2e/test-results/", "playwright-report/", "test-results/"],
  },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
  },
  {
    files: ["**/*.svelte", "**/*.svelte.ts", "**/*.svelte.js"],
    languageOptions: {
      parserOptions: {
        projectService: true,
        extraFileExtensions: [".svelte"],
        parser: ts.parser,
        svelteConfig,
      },
    },
  },
  {
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
      // A mustache around a literal with an escape (placeholder={"a\nb"}) is
      // not useless: as a plain attribute the "\n" would render verbatim.
      "svelte/no-useless-mustaches": ["error", { ignoreStringEscape: true }],
      "svelte/require-each-key": "error",
      "svelte/no-useless-children-snippet": "error",
      // Off by ruling 109: the rule asks for SvelteMap/SvelteSet in place of the
      // plain Map/Set state the app reassigns on change; following it would
      // rewrite that state across the app for no change in behaviour.
      "svelte/prefer-svelte-reactivity": "off",
    },
  },
  {
    // Playwright reads a fixture's dependencies from its first parameter's
    // destructuring pattern, so a fixture with none must be written ({}, use).
    files: ["e2e/**/*.ts"],
    rules: {
      "no-empty-pattern": ["error", { allowObjectPatternsAsParameters: true }],
    },
  },
);
