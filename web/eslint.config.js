import { defineConfig } from "eslint/config";
import js from "@eslint/js";
import noUnsanitized from "eslint-plugin-no-unsanitized";
import security from "eslint-plugin-security";
import svelte from "eslint-plugin-svelte";
import svelteConfig from "./svelte.config.js";
import tseslint from "typescript-eslint";

const browserGlobals = {
	btoa: "readonly",
	crypto: "readonly",
	SubmitEvent: "readonly",
	TextEncoder: "readonly",
	URL: "readonly",
	WebSocket: "readonly",
	window: "readonly",
};

const nodeGlobals = { process: "readonly" };

export default defineConfig(
	{ ignores: [".svelte-kit/", "node_modules/"] },
	js.configs.recommended,
	...tseslint.configs.recommended,
	...svelte.configs.recommended,
	security.configs.recommended,
	noUnsanitized.configs.recommended,
	{
		files: ["**/*.{svelte,ts}"],
		languageOptions: { globals: browserGlobals },
	},
	{
		files: ["**/*.svelte"],
		languageOptions: {
			parserOptions: {
				parser: tseslint.parser,
				svelteConfig,
			},
		},
	},
	{
		files: ["*.config.js", "*.config.ts"],
		languageOptions: { globals: nodeGlobals },
	},
);
