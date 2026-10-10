import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/svelte";
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
	{
		content: "# Catalog readme",
		createdAt: "2026-09-24T00:00:02Z",
		id: "44444444-4444-4444-8444-444444444441",
		role: "tool",
		sequence: 3,
		toolCallId: "call-read-file",
		workspace: "/workspace/catalog",
	},
	{
		content:
			'<session-events count="1">\nA background job finished.\n</session-events>',
		createdAt: "2026-09-24T00:00:03Z",
		id: "99999999-9999-4999-8999-999999999999",
		injected: true,
		role: "user",
		sequence: 4,
		workspace: "/workspace/catalog",
	},
];

async function connectAndOpenChat(): Promise<TestWebSocket | undefined> {
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

	return transport;
}

function sentMessageData(transport: TestWebSocket | undefined): unknown[] {
	return (transport?.sent ?? []).map(
		(frame) => (JSON.parse(frame) as { data: unknown }).data,
	);
}

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
				case "/v1/messages": {
					const isNewestFirst = url.searchParams.get("order") === "desc";
					const items = isNewestFirst ? [...messages].reverse() : messages;
					return response({ hasMore: false, items, limit: 100, offset: 0 });
				}
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

		const thinking = (await screen.findByText("Thinking")).closest("details");
		expect(thinking?.open).toBe(false);
		await fireEvent.click(screen.getByText("Thinking"));
		expect(thinking?.open).toBe(true);
		expect(thinking?.textContent).toContain(
			"Inspect the workspace before changing it.",
		);

		const toolCard = (await screen.findByText("read_file:")).closest("details");
		const summary = toolCard?.querySelector("summary")?.textContent;
		expect(summary).toContain("Done");
		expect(summary).toContain("README.md");
		expect(screen.queryByText("Tool result")).toBeNull();
		await fireEvent.click(screen.getByText("read_file:"));
		expect(toolCard?.open).toBe(true);
		expect(toolCard?.textContent).toContain("Arguments");
		expect(toolCard?.textContent).toContain('"path": "README.md"');
		expect(toolCard?.textContent).toContain("Result");
		expect(toolCard?.textContent).toContain("# Catalog readme");

		const prompt = screen.getByText("inspect the workspace");
		const reply = screen.getByText("I found the first thing to fix.");
		expect(
			prompt.compareDocumentPosition(reply) & Node.DOCUMENT_POSITION_FOLLOWING,
		).toBe(Node.DOCUMENT_POSITION_FOLLOWING);

		// The user's message is a bare bubble and the agent writes straight onto
		// the page, so neither carries a speaker label.
		const promptMessage = prompt.closest("article");
		const replyMessage = reply.closest("article");
		expect(promptMessage).not.toBe(replyMessage);
		expect(promptMessage?.classList.contains("user")).toBe(true);
		expect(within(promptMessage as HTMLElement).queryByText("You")).toBeNull();
		expect(within(replyMessage as HTMLElement).queryByText("Peen")).toBeNull();

		const injectedLabel = await screen.findByText("Background update");
		const injectedMessage = injectedLabel.closest("article");
		expect(injectedMessage?.classList.contains("user")).toBe(false);
		expect(document.querySelectorAll("article.message.user")).toHaveLength(1);

		const modelSelector = await screen.findByLabelText<HTMLSelectElement>("Model");
		const names = Array.from(modelSelector.options, (option) => option.value);
		expect(names).toEqual(["aigate/default", "aigate/catalog/model", "zai/glm-5.3"]);
		expect(modelSelector.value).toBe("aigate/default");
	});

	it("sends the shown model and reasoning level with each new turn", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const reasoningSelector = screen.getByLabelText<HTMLSelectElement>("Reasoning");
		expect(Array.from(reasoningSelector.options, (option) => option.value)).toEqual([
			"minimal",
			"low",
			"medium",
			"high",
			"xhigh",
			"max",
		]);
		expect(reasoningSelector.value).toBe("medium");

		const messageInput = screen.getByLabelText<HTMLTextAreaElement>("Message");
		const composer = messageInput.closest("form") as HTMLFormElement;
		await fireEvent.input(messageInput, { target: { value: "first" } });
		await fireEvent.submit(composer);

		await fireEvent.change(screen.getByLabelText("Model"), {
			target: { value: "zai/glm-5.3" },
		});
		await fireEvent.change(reasoningSelector, { target: { value: "xhigh" } });
		await fireEvent.input(messageInput, { target: { value: "second" } });
		await fireEvent.submit(composer);

		expect(sentMessageData(transport)).toEqual([
			{ message: "first", model: "aigate/default", reasoningEffort: "medium" },
			{ message: "second", model: "zai/glm-5.3", reasoningEffort: "xhigh" },
		]);
	});

	it("leaves the model out of a message sent while a turn is streaming", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		transport?.receive(
			JSON.stringify({
				data: { message: "run the build" },
				id: "99999999-9999-4999-8999-000000000101",
				metadata: {
					requestId: "77777777-7777-4777-8777-777777777781",
					sessionId: session.id,
				},
				timestamp: 1,
				triggeredBy: null,
				type: "user_message.created",
			}),
		);
		await screen.findByText("run the build");

		const messageInput = screen.getByLabelText<HTMLTextAreaElement>("Message");
		await fireEvent.input(messageInput, { target: { value: "then run the tests" } });
		await fireEvent.keyDown(messageInput, { key: "Enter" });

		expect(sentMessageData(transport)).toEqual([{ message: "then run the tests" }]);
	});

	it("sends on Enter and keeps Shift+Enter for a new line", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const messageInput = screen.getByLabelText<HTMLTextAreaElement>("Message");
		await fireEvent.input(messageInput, { target: { value: "first line" } });
		await fireEvent.keyDown(messageInput, { key: "Enter", shiftKey: true });
		expect(sentMessageData(transport)).toEqual([]);

		await fireEvent.keyDown(messageInput, { key: "Enter", isComposing: true });
		expect(sentMessageData(transport)).toEqual([]);

		await fireEvent.keyDown(messageInput, { key: "Enter" });
		expect(sentMessageData(transport)).toEqual([
			{ message: "first line", model: "aigate/default", reasoningEffort: "medium" },
		]);
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

	it("streams content blocks into one live reply rendered as markdown", async () => {
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

		const requestID = "77777777-7777-4777-8777-777777777777";
		let sequence = 0;
		const send = (type: string, data: unknown): void => {
			sequence += 1;
			transport?.receive(
				JSON.stringify({
					data,
					id: `88888888-8888-4888-8888-${String(sequence).padStart(12, "0")}`,
					metadata: { requestId: requestID, sessionId: session.id },
					timestamp: 1,
					triggeredBy: null,
					type,
				}),
			);
		};

		send("user_message.created", { message: "make a script" });
		send("content_block_start", {
			content_block: { type: "thinking" },
			index: 0,
			type: "content_block_start",
		});
		send("content_block_delta", {
			delta: { text: "plan the ", type: "thinking_delta" },
			index: 0,
			type: "content_block_delta",
		});
		send("content_block_delta", {
			delta: { text: "script", type: "thinking_delta" },
			index: 0,
			type: "content_block_delta",
		});
		send("content_block_stop", { index: 0, type: "content_block_stop" });
		send("content_block_start", {
			content_block: {
				id: "call-write",
				input: {},
				name: "write_file",
				type: "tool_use",
			},
			index: 1,
			type: "content_block_start",
		});
		send("content_block_delta", {
			delta: { partial_json: '{"path":"run.sh"}', type: "input_json_delta" },
			index: 1,
			type: "content_block_delta",
		});
		send("content_block_stop", { index: 1, type: "content_block_stop" });
		send("content_block_start", {
			content_block: { tool_use_id: "call-write", type: "tool_result" },
			index: 2,
			type: "content_block_start",
		});
		send("content_block_delta", {
			delta: { text: '{"created":true}', type: "json_partial" },
			index: 2,
			type: "content_block_delta",
		});
		send("content_block_stop", { index: 2, type: "content_block_stop" });
		send("content_block_start", {
			content_block: { type: "text" },
			index: 3,
			type: "content_block_start",
		});
		send("content_block_delta", {
			delta: { text: "Created **run", type: "text_delta" },
			index: 3,
			type: "content_block_delta",
		});
		send("content_block_delta", {
			delta: { text: ".sh** for you.", type: "text_delta" },
			index: 3,
			type: "content_block_delta",
		});

		await screen.findByText("make a script");
		await screen.findByText("plan the script");
		const writeCard = (await screen.findByText("write_file:")).closest("details");
		expect(writeCard?.querySelector(".tool-subject")?.textContent).toBe("run.sh");
		expect(writeCard?.querySelector("summary")?.textContent).toContain("Done");
		expect(writeCard?.textContent).toContain('"path": "run.sh"');
		expect(writeCard?.textContent).toContain('{"created":true}');
		const emphasized = await screen.findByText("run.sh", { selector: "strong" });
		expect(emphasized.tagName).toBe("STRONG");
		expect(screen.queryByText("content_block_delta")).toBeNull();
		expect(screen.queryByText("content_block_start")).toBeNull();
	});

	it("shows a message sent mid-turn as queued until it lands in the running turn", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const runningRequestID = "77777777-7777-4777-8777-777777777771";
		const queuedRequestID = "77777777-7777-4777-8777-777777777772";
		let sequence = 0;
		const send = (type: string, data: unknown, requestID: string): void => {
			sequence += 1;
			transport?.receive(
				JSON.stringify({
					data,
					id: `99999999-9999-4999-8999-${String(sequence).padStart(12, "0")}`,
					metadata: { requestId: requestID, sessionId: session.id },
					timestamp: 1,
					triggeredBy: null,
					type,
				}),
			);
		};

		send("user_message.created", { message: "run the build" }, runningRequestID);
		send("turn.started", {}, runningRequestID);
		send(
			"content_block_start",
			{
				content_block: { id: "call-build", name: "run_command", type: "tool_use" },
				index: 0,
				type: "content_block_start",
			},
			runningRequestID,
		);
		send("user_message.created", { message: "then run the tests" }, queuedRequestID);
		send("user_message.queued", { message: "then run the tests" }, queuedRequestID);

		const queuedNote = await screen.findByText("Queued. Lands after the current step.");
		expect(queuedNote.closest("article")?.textContent).toContain("then run the tests");

		send(
			"content_block_start",
			{
				content_block: { tool_use_id: "call-build", type: "tool_result" },
				index: 1,
				type: "content_block_start",
			},
			runningRequestID,
		);
		send("user_message.delivered", { message: "then run the tests" }, queuedRequestID);
		send(
			"content_block_start",
			{ content_block: { type: "text" }, index: 2, type: "content_block_start" },
			runningRequestID,
		);
		send(
			"content_block_delta",
			{
				delta: { text: "Tests pass.", type: "text_delta" },
				index: 2,
				type: "content_block_delta",
			},
			runningRequestID,
		);

		const reply = await screen.findByText("Tests pass.");
		await waitFor(() => {
			expect(screen.queryByText("Queued. Lands after the current step.")).toBeNull();
		});
		const delivered = screen.getAllByText("then run the tests");
		expect(delivered).toHaveLength(1);
		const deliveredMessage = delivered[0] as HTMLElement;
		const buildCard = screen.getByText("run_command").closest("details") as HTMLElement;
		expect(
			buildCard.compareDocumentPosition(deliveredMessage) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
		expect(
			deliveredMessage.compareDocumentPosition(reply) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
	});
});
