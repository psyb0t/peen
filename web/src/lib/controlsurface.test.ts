import { fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Page from "../routes/+page.svelte";

class TestWebSocket extends EventTarget {
	public static readonly OPEN = 1;
	public readyState = 0;

	public constructor(
		public readonly _url: string,
		public readonly _protocols: string | string[],
	) {
		super();
	}

	public close(): void {
		this.readyState = 3;
		this.dispatchEvent(new Event("close"));
	}

	public send(): void {}
}

const session = {
	activeTurn: false,
	createdAt: "2026-09-23T00:00:00Z",
	executionProfile: "native",
	id: "22222222-2222-4222-8222-222222222222",
	messageCount: 0,
	model: "aigate/default",
	updatedAt: "2026-09-23T00:00:00Z",
	workspace: "/workspace/catalog",
};

function response(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
	});
}

describe("control surface", () => {
	beforeEach(() => {
		vi.stubGlobal("WebSocket", TestWebSocket);
		vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
			const url = new URL(
				input instanceof Request ? input.url : input.toString(),
				window.location.href,
			);

			switch (url.pathname) {
				case "/v1/sessions":
					return response({ hasMore: false, items: [session], limit: 200, offset: 0 });
				case "/v1/execution-profiles":
					return response({ items: [] });
				case "/v1/models":
					return response({
						models: [
							{
								connectionName: "aigate",
								contextWindowTokens: 128000,
								modelId: "catalog/model",
								name: "aigate/catalog/model",
							},
							{
								connectionName: "zai",
								contextWindowTokens: 64000,
								modelId: "glm-5.3",
								name: "zai/glm-5.3",
							},
						],
					});
				case "/v1/session":
					return response(session);
				case "/v1/messages":
					return response({ hasMore: false, items: [], limit: 100, offset: 0 });
				case "/v1/session/events":
					return response({ events: [], hasMore: false, limit: 100, offset: 0 });
				case "/v1/session/agents":
					return response({ agents: [], hasMore: false, limit: 100, offset: 0 });
				case "/v1/session/compactions":
					return response({ compactions: [], hasMore: false, limit: 100, offset: 0 });
				case "/v1/session/model-runs":
					return response({ hasMore: false, limit: 100, modelRuns: [], offset: 0 });
				case "/v1/session/notices":
					return response({ hasMore: false, limit: 100, notices: [], offset: 0 });
				case "/v1/session/workers":
					return response({ hasMore: false, items: [], limit: 100, offset: 0 });
				case "/v1/session/jobs":
					return response({ hasMore: false, jobs: [], limit: 100, offset: 0 });
				case "/v1/session/turns":
					return response({ hasMore: false, limit: 100, offset: 0, turns: [] });
				case "/v1/session/profile-decisions":
					return response({ hasMore: false, items: [], limit: 100, offset: 0 });
				default:
					throw new Error(`unexpected request ${url.pathname}`);
			}
		});
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("offers every discovered provider-qualified model without rebuilding names", async () => {
		render(Page);

		const connectionForm = screen
			.getByRole("button", { name: "Connect" })
			.closest("form");
		expect(connectionForm).not.toBeNull();
		await fireEvent.submit(connectionForm as HTMLFormElement);

		const sessionButton = await screen.findByRole("button", {
			name: /\/workspace\/catalog/,
		});
		await fireEvent.click(sessionButton);

		const modelSelector = await screen.findByLabelText<HTMLSelectElement>(
			"One-turn model override",
		);
		const names = Array.from(modelSelector.options, (option) => option.value);
		expect(names).toEqual(["", "aigate/catalog/model", "zai/glm-5.3"]);
	});
});
