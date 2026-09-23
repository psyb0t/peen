import adapter from "@sveltejs/adapter-static";

const staticOutput =
	process.env.PEEN_WEB_OUTPUT ?? "../internal/pkg/http/server/web/dist";

export default {
	kit: {
		adapter: adapter({
			assets: staticOutput,
			pages: staticOutput,
			strict: true,
		}),
	},
};
