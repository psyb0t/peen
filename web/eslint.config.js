import { defineConfig } from "eslint/config";
import js from "@eslint/js";
import noUnsanitized from "eslint-plugin-no-unsanitized";
import security from "eslint-plugin-security";
import svelte from "eslint-plugin-svelte";
import tseslint from "typescript-eslint";

const browserGlobals = {
	btoa: "readonly",
	ClipboardEvent: "readonly",
	crypto: "readonly",
	DragEvent: "readonly",
	Event: "readonly",
	File: "readonly",
	HTMLDivElement: "readonly",
	HTMLInputElement: "readonly",
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
	{ ignores: [".svelte-kit/", "node_modules/", "src/lib/ws/generated.ts"] },
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
