import adapter from "@sveltejs/adapter-static";
import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vitest/config";

const staticOutput =
	process.env.PEEN_WEB_OUTPUT ?? "../internal/pkg/http/server/web/dist";

export default defineConfig({
	plugins: [
		sveltekit({
			// SvelteKit 3 replaced $lib with #lib. The control surface keeps
			// importing shared code through $lib.
			alias: { $lib: "src/lib" },
			adapter: adapter({
				assets: staticOutput,
				pages: staticOutput,
				strict: true,
			}),
		}),
	],
	resolve: {
		conditions: ["browser"],
	},
	test: {
		environment: "jsdom",
	},
});
