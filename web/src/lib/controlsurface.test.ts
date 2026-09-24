import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Page from "../routes/+page.svelte";

class TestWebSocket extends EventTarget {
	public static readonly OPEN = 1;
	public static instances: TestWebSocket[] = [];

	public readonly sent: string[] = [];
	public readyState = 0;

	public constructor(
		public readonly _url: string,
		public readonly _protocols: string | string[],
	) {
		super();
		TestWebSocket.instances.push(this);
	}

	public open(): void {
		this.readyState = TestWebSocket.OPEN;
		this.dispatchEvent(new Event("open"));
	}

	public receive(data: unknown): void {
		this.dispatchEvent(new MessageEvent("message", { data }));
	}

	public close(): void {
		this.readyState = 3;
		this.dispatchEvent(new Event("close"));
	}

	public send(data: string): void {
		this.sent.push(data);
	}
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

const messages = [
	{
		content: "inspect the workspace",
		createdAt: "2026-09-24T00:00:00Z",
		id: "11111111-1111-4111-8111-111111111111",
		role: "user",
		sequence: 1,
		workspace: "/workspace/catalog",
	},
	{
		content: "I found the first thing to fix.",
		createdAt: "2026-09-24T00:00:01Z",
		id: "33333333-3333-4333-8333-333333333333",
		role: "assistant",
		sequence: 2,
		thinking: "Inspect the workspace before changing it.",
		toolCalls: [
			{
				arguments: { path: "README.md" },
				id: "call-read-file",
				name: "read_file",
			},
		],
		workspace: "/workspace/catalog",
	},
];

function response(body: unknown): Response {
	return new Response(JSON.stringify(body), {
		headers: { "Content-Type": "application/json" },
	});
}

describe("control surface", () => {
	let sessionOpened = false;
	let openedWorkspace = "";

	beforeEach(() => {
		sessionOpened = false;
		openedWorkspace = "";
		TestWebSocket.instances = [];
		vi.stubGlobal("WebSocket", TestWebSocket);
		vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
			const request = input instanceof Request ? input : new Request(input, init);
			const url = new URL(request.url, window.location.href);

			switch (url.pathname) {
				case "/v1/sessions":
					return response({
						hasMore: false,
						items: sessionOpened ? [session] : [],
						limit: 200,
						offset: 0,
					});
				case "/v1/workspace-roots":
					return response({ roots: ["/workspace/catalog"] });
				case "/v1/sessions/open": {
					const body = (await request.clone().json()) as { workspace: string };
					openedWorkspace = body.workspace;
					sessionOpened = true;
					return response({ created: true, session });
				}
				case "/v1/execution-profiles":
					return response({ default: "native", items: [] });
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
					return response({ hasMore: false, items: messages, limit: 100, offset: 0 });
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
		cleanup();
		vi.unstubAllGlobals();
	});

	it("opens an allowed workspace as a focused chat with durable agent activity", async () => {
		render(Page);

		const connectionForm = screen
			.getByRole("button", { name: "Connect" })
			.closest("form");
		expect(connectionForm).not.toBeNull();
		await fireEvent.submit(connectionForm as HTMLFormElement);

		const workspaceInput =
			await screen.findByDisplayValue<HTMLInputElement>("/workspace/catalog");
		await fireEvent.input(workspaceInput, {
			target: { value: "/workspace/catalog/child-project" },
		});
		await fireEvent.click(screen.getByRole("button", { name: "Open chat" }));
		await waitFor(() => {
			expect(openedWorkspace).toBe("/workspace/catalog/child-project");
		});

		await screen.findByRole("heading", { name: "catalog" });
		await screen.findByText("inspect the workspace");
		await screen.findByText("Thinking");
		await screen.findByText("read_file");

		const modelSelector = await screen.findByLabelText<HTMLSelectElement>("Model");
		const names = Array.from(modelSelector.options, (option) => option.value);
		expect(names).toEqual(["", "aigate/catalog/model", "zai/glm-5.3"]);
	});

	it("shows an ignored harness source in the active workspace chat", async () => {
		render(Page);

		const connectionForm = screen
			.getByRole("button", { name: "Connect" })
			.closest("form");
		expect(connectionForm).not.toBeNull();
		await fireEvent.submit(connectionForm as HTMLFormElement);
		await screen.findByDisplayValue("/workspace/catalog");

		const transport = TestWebSocket.instances[0];
		expect(transport).toBeDefined();
		transport?.open();
		await fireEvent.click(screen.getByRole("button", { name: "Open chat" }));
		await screen.findByRole("heading", { name: "catalog" });

		transport?.receive(
			JSON.stringify({
				data: {
					warnings: [
						{
							kind: "skill",
							reason: "skill directory has no SKILL.md",
							source: "/workspace/catalog/.agents/skills/broken-skill",
						},
					],
				},
				id: "44444444-4444-4444-8444-444444444444",
				metadata: {
					requestId: "55555555-5555-4555-8555-555555555555",
					sessionId: session.id,
				},
				timestamp: 1,
				triggeredBy: "66666666-6666-4666-8666-666666666666",
				type: "harness.warning",
			}),
		);

		await screen.findByText("Workspace configuration warning");
		await screen.findByText(
			"skill at /workspace/catalog/.agents/skills/broken-skill: skill directory has no SKILL.md",
		);
	});
});
