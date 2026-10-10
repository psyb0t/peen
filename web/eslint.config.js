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
	HTMLDivElement: "readonly",
	HTMLTextAreaElement: "readonly",
	KeyboardEvent: "readonly",
	SubmitEvent: "readonly",
	TextEncoder: "readonly",
	URL: "readonly",
	WebSocket: "readonly",
	WheelEvent: "readonly",
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
		// Markdown links point at content outside the app, which resolve() does
		// not apply to. Their targets are protocol-filtered by safeLinkHref.
		files: ["src/lib/chat/Markdown.svelte"],
		rules: {
			"svelte/no-navigation-without-resolve": ["error", { ignoreLinks: true }],
		},
	},
	{
		files: ["*.config.js", "*.config.ts"],
		languageOptions: { globals: nodeGlobals },
	},
);
