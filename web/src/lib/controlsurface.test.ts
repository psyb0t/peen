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
import { AGENT_SPACE_DEFAULT_OPTION } from "./common/constants";

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
	agentSpace: "control",
	id: "22222222-2222-4222-8222-222222222222",
	messageCount: 0,
	model: "aigate/default",
	updatedAt: "2026-09-23T00:00:00Z",
	workspace: "/workspace/catalog",
};

// A turn.started frame's data must fit TurnStartedData: sessionId, model,
// and workspace are all required by the generated contract.
function turnStartedData(): Record<string, unknown> {
	return { model: session.model, sessionId: session.id, workspace: session.workspace };
}

const agentSpaces = [
	{
		capabilityWarnings: [],
		hostRootEquivalent: false,
		kind: "control",
		name: "control",
		revision: 0,
	},
	{
		capabilityWarnings: [],
		docker: "none",
		hostRootEquivalent: false,
		kind: "docker",
		name: "docker:default",
		network: "bridge",
		profile: "default",
		revision: 0,
		sudo: false,
	},
	{
		capabilityWarnings: ["docker: full mounts the host Docker socket"],
		docker: "full",
		hostRootEquivalent: true,
		kind: "docker",
		name: "docker:ops",
		network: "host",
		profile: "ops",
		revision: 1,
		sudo: true,
	},
];

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
		content:
			"<skill>\n<name>planning</name>\n<description>Plan work.</description>\n" +
			"<directory>/skills/planning</directory>\n\nPlan steps.\n</skill>",
		createdAt: "2026-09-24T00:00:00Z",
		id: "11111111-1111-4111-8111-111111111112",
		injected: true,
		injectedKind: "skill",
		role: "user",
		sequence: 2,
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
				name: "readFile",
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
		content: "<events>\nA background job finished.\n</events>",
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
	await waitFor(() => {
		expect(
			screen.getByRole<HTMLButtonElement>("button", { name: "Send" }).disabled,
		).toBe(false);
	});
	// There is no per-session presence socket: work keeps running unattended,
	// and a reconnect catches up from the session feed instead.
	expect(TestWebSocket.instances).toHaveLength(1);

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

const waitingAgent = {
	agentRunId: "44444444-4444-4444-8444-444444444444",
	allowedTools: [],
	cancelRequested: false,
	completionTokenCount: 0,
	definition: "stored",
	depth: 1,
	eventCount: 3,
	failureClassification: "",
	failureDetail: "",
	finishReason: "",
	instructions: "Review the change.",
	model: "catalog/model",
	modelReference: "aigate/catalog/model",
	name: "reviewer",
	parentTurnId: "55555555-5555-4555-8555-555555555555",
	promptTokenCount: 0,
	question: "Which file should I review?",
	requestId: "66666666-6666-4666-8666-666666666666",
	responseMessages: [],
	responseText: "",
	responseThinking: "",
	sessionId: "22222222-2222-4222-8222-222222222222",
	startedAt: "2026-10-11T00:00:00Z",
	state: "running",
	systemPrompt: "",
	task: "review the change",
	waiting: true,
	workspace: "/workspace/catalog",
};

const AGENT_CANCEL_PATH_PATTERN = /^\/api\/v1\/session\/agents\/([^/]+)\/cancel$/u;

interface FeedFrameFixture {
	createdAt: string;
	data: unknown;
	id: string;
	requestId: string | null;
	sequence: number;
	sessionId: string;
	type: string;
}

function feedFrame(
	sequence: number,
	type: string,
	data: unknown,
	requestID: string,
): FeedFrameFixture {
	return {
		createdAt: "2026-10-15T00:00:00Z",
		data,
		id: `66666666-6666-4666-8666-${String(sequence).padStart(12, "0")}`,
		requestId: requestID,
		sequence,
		sessionId: session.id,
		type,
	};
}

describe("control surface", () => {
	let sessionOpened = false;
	let openedWorkspace = "";
	let openedBody: Record<string, unknown> = {};
	let reconfiguredBody: Record<string, unknown> | undefined;
	let sessionAgents: unknown[] = [];
	let cancelledAgentRunIDs: string[] = [];
	// The session's feed head and running turn, as GET /session reports them,
	// and the stored frames GET /session/feed pages through for catch-up.
	let sessionFeedSequence = 0;
	let sessionActiveTurnFeedSequence: number | null = null;
	let feedFrames: FeedFrameFixture[] = [];

	beforeEach(() => {
		sessionOpened = false;
		openedWorkspace = "";
		openedBody = {};
		reconfiguredBody = undefined;
		sessionAgents = [];
		cancelledAgentRunIDs = [];
		sessionFeedSequence = 0;
		sessionActiveTurnFeedSequence = null;
		feedFrames = [];
		TestWebSocket.instances = [];
		vi.stubGlobal("WebSocket", TestWebSocket);
		vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
			const request = input instanceof Request ? input : new Request(input, init);
			const url = new URL(request.url, window.location.href);

			const cancelledAgent = AGENT_CANCEL_PATH_PATTERN.exec(url.pathname);
			if (cancelledAgent?.[1] !== undefined && request.method === "POST") {
				cancelledAgentRunIDs.push(cancelledAgent[1]);
				sessionAgents = [];

				return response({
					agentRunId: cancelledAgent[1],
					cancelRequested: true,
					state: "running",
				});
			}

			switch (url.pathname) {
				case "/api/v1/sessions":
					return response({
						hasMore: false,
						items: sessionOpened ? [session] : [],
						limit: 200,
						offset: 0,
					});
				case "/api/v1/workspace-roots":
					return response({ roots: ["/workspace/catalog"] });
				case "/api/v1/sessions/open": {
					const body = (await request.clone().json()) as { workspace: string };
					openedBody = body;
					openedWorkspace = body.workspace;
					sessionOpened = true;
					return response({ created: true, session });
				}
				case "/api/v1/session/reconfigure": {
					reconfiguredBody = (await request.clone().json()) as Record<string, unknown>;
					return response({
						decidedAt: "2026-09-24T00:00:00Z",
						id: "33333333-3333-4333-8333-333333333333",
						reason: "needs the ops tools",
						sessionId: session.id,
						toAgentSpace: "docker:ops",
					});
				}
				case "/api/v1/agent-spaces":
					return response({ default: "control", items: agentSpaces });
				case "/api/v1/models":
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
				case "/api/v1/session":
					return response({
						...session,
						activeTurnFeedSequence: sessionActiveTurnFeedSequence,
						feedSequence: sessionFeedSequence,
					});
				case "/api/v1/session/feed": {
					const cursor = Number(url.searchParams.get("cursor") ?? "0");
					const limit = Number(url.searchParams.get("limit") ?? "100");
					const items = feedFrames
						.filter((frame) => frame.sequence > cursor)
						.slice(0, limit);
					const lastItem = items.at(-1);
					const nextCursor = lastItem === undefined ? cursor : lastItem.sequence;
					const hasMore = feedFrames.some((frame) => frame.sequence > nextCursor);
					return response({
						hasMore,
						head: sessionFeedSequence,
						items,
						limit,
						nextCursor,
					});
				}
				case "/api/v1/messages": {
					const isNewestFirst = url.searchParams.get("order") === "desc";
					const items = isNewestFirst ? [...messages].reverse() : messages;
					return response({ hasMore: false, items, limit: 100, offset: 0 });
				}
				case "/api/v1/session/events":
					return response({ events: [], hasMore: false, limit: 100, offset: 0 });
				case "/api/v1/session/agents":
					return response({
						agents: sessionAgents,
						hasMore: false,
						limit: 100,
						offset: 0,
					});
				case "/api/v1/session/compactions":
					return response({ compactions: [], hasMore: false, limit: 100, offset: 0 });
				case "/api/v1/session/model-runs":
					return response({ hasMore: false, limit: 100, modelRuns: [], offset: 0 });
				case "/api/v1/session/notices":
					return response({ hasMore: false, limit: 100, notices: [], offset: 0 });
				case "/api/v1/session/workers":
					return response({ hasMore: false, items: [], limit: 100, offset: 0 });
				case "/api/v1/session/jobs":
					return response({ hasMore: false, jobs: [], limit: 100, offset: 0 });
				case "/api/v1/session/turns":
					return response({ hasMore: false, limit: 100, offset: 0, turns: [] });
				case "/api/v1/session/agent-space-decisions":
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

		const skillMarker = (await screen.findByText("skill: planning")).closest("details");
		expect(skillMarker?.open).toBe(false);
		expect(skillMarker?.closest("article")?.classList.contains("user")).toBe(false);
		await fireEvent.click(screen.getByText("skill: planning"));
		expect(skillMarker?.open).toBe(true);
		expect(skillMarker?.textContent).toContain("Plan steps.");

		const thinking = (await screen.findByText("Thinking")).closest("details");
		expect(thinking?.open).toBe(false);
		await fireEvent.click(screen.getByText("Thinking"));
		expect(thinking?.open).toBe(true);
		expect(thinking?.textContent).toContain(
			"Inspect the workspace before changing it.",
		);

		const toolCard = (await screen.findByText("readFile:")).closest("details");
		const summary = toolCard?.querySelector("summary")?.textContent;
		expect(summary).toContain("Done");
		expect(summary).toContain("README.md");
		expect(screen.queryByText("Tool result")).toBeNull();
		await fireEvent.click(screen.getByText("readFile:"));
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

	it("lists each agent space with its grants when opening a workspace", async () => {
		render(Page);

		const connectionForm = screen
			.getByRole("button", { name: "Connect" })
			.closest("form");
		await fireEvent.submit(connectionForm as HTMLFormElement);
		await screen.findByDisplayValue("/workspace/catalog");

		const selector = screen.getByLabelText<HTMLSelectElement>("Agent space");
		expect(Array.from(selector.options, (option) => option.textContent)).toEqual([
			"Controller default (control)",
			"control (runs where Peen runs)",
			"default (docker: none, network: bridge, sudo: no)",
			"ops (docker: full = host root, network: host, sudo: yes)",
		]);
		expect(Array.from(selector.options, (option) => option.value)).toEqual([
			"",
			"control",
			"docker:default",
			"docker:ops",
		]);
	});

	it("opens the workspace in the chosen agent space", async () => {
		render(Page);

		const connectionForm = screen
			.getByRole("button", { name: "Connect" })
			.closest("form");
		await fireEvent.submit(connectionForm as HTMLFormElement);
		await screen.findByDisplayValue("/workspace/catalog");

		await fireEvent.change(screen.getByLabelText("Agent space"), {
			target: { value: "docker:ops" },
		});
		await fireEvent.click(screen.getByRole("button", { name: "Open chat" }));
		await waitFor(() => {
			expect(openedWorkspace).toBe("/workspace/catalog");
		});

		expect(openedBody).toEqual({
			agentSpace: "docker:ops",
			workspace: "/workspace/catalog",
		});
	});

	it("opens the workspace in the controller default when none is chosen", async () => {
		render(Page);
		await connectAndOpenChat();

		expect(openedBody).toEqual({ workspace: "/workspace/catalog" });
	});

	it("lists the agent spaces with their grants in the change form", async () => {
		render(Page);
		await connectAndOpenChat();

		await fireEvent.click(screen.getByRole("button", { name: "Details" }));
		const form = (await screen.findByText("Change agent space")).closest(
			"form",
		) as HTMLFormElement;

		const selector = within(form).getByLabelText<HTMLSelectElement>("Agent space");
		expect(Array.from(selector.options, (option) => option.textContent)).toEqual([
			"Choose an agent space",
			"control (runs where Peen runs)",
			"default (docker: none, network: bridge, sudo: no)",
			"ops (docker: full = host root, network: host, sudo: yes)",
		]);

		await fireEvent.change(selector, { target: { value: "docker:ops" } });
		await fireEvent.input(within(form).getByLabelText("Reason"), {
			target: { value: "needs the ops tools" },
		});
		await fireEvent.submit(form);

		await waitFor(() => {
			expect(reconfiguredBody).toEqual({
				agentSpace: "docker:ops",
				reason: "needs the ops tools",
			});
		});
	});

	it("shows the session a link names once connected", async () => {
		sessionOpened = true;
		window.history.replaceState({}, "", `/?session=${session.id}`);
		try {
			render(Page);
			const connectionForm = screen
				.getByRole("button", { name: "Connect" })
				.closest("form");
			await fireEvent.submit(connectionForm as HTMLFormElement);

			expect(await screen.findByRole("heading", { name: "catalog" })).not.toBeNull();
		} finally {
			window.history.replaceState({}, "", "/");
		}
	});

	it("opens no session when the link names none", async () => {
		sessionOpened = true;
		window.history.replaceState({}, "", "/?session=not-a-session");
		try {
			render(Page);
			const connectionForm = screen
				.getByRole("button", { name: "Connect" })
				.closest("form");
			await fireEvent.submit(connectionForm as HTMLFormElement);
			await screen.findByDisplayValue("/workspace/catalog");

			expect(screen.queryByRole("heading", { name: "catalog" })).toBeNull();
		} finally {
			window.history.replaceState({}, "", "/");
		}
	});

	it("shows the agent space a session runs in", async () => {
		render(Page);
		await connectAndOpenChat();

		expect(
			screen.getByText("control", { selector: ".session-facts span" }),
		).not.toBeNull();
	});

	it("shows the controller default for a session with no recorded agent space", async () => {
		session.agentSpace = "";
		try {
			render(Page);
			await connectAndOpenChat();

			expect(
				screen.getByText(AGENT_SPACE_DEFAULT_OPTION, {
					selector: ".session-facts span",
				}),
			).not.toBeNull();
		} finally {
			session.agentSpace = "control";
		}
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
				type: "userMessage.created",
			}),
		);
		await screen.findByText("run the build");

		const messageInput = screen.getByLabelText<HTMLTextAreaElement>("Message");
		await fireEvent.input(messageInput, { target: { value: "then run the tests" } });
		await fireEvent.keyDown(messageInput, { key: "Enter" });

		expect(sentMessageData(transport)).toEqual([{ message: "then run the tests" }]);
	});

	it("lists a waiting child, writes to it, and stops it", async () => {
		sessionAgents = [waitingAgent];
		render(Page);
		const transport = await connectAndOpenChat();

		const panel = await screen.findByRole("region", { name: "Running agents" });
		expect(within(panel).getByText("reviewer 44444444")).toBeDefined();
		expect(within(panel).getByText("Asks: Which file should I review?")).toBeDefined();

		await fireEvent.change(screen.getByLabelText("Send to"), {
			target: { value: waitingAgent.agentRunId },
		});
		expect(screen.getByLabelText<HTMLSelectElement>("Model").disabled).toBe(true);

		const messageInput = screen.getByLabelText<HTMLTextAreaElement>("Message");
		await fireEvent.input(messageInput, { target: { value: "review README.md" } });
		await fireEvent.submit(messageInput.closest("form") as HTMLFormElement);

		expect(sentMessageData(transport)).toEqual([
			{ message: "review README.md", to: waitingAgent.agentRunId },
		]);

		await fireEvent.click(
			within(panel).getByRole("button", { name: "Stop reviewer 44444444" }),
		);
		await waitFor(() => {
			expect(cancelledAgentRunIDs).toEqual([waitingAgent.agentRunId]);
		});
		await waitFor(() => {
			expect(screen.queryByRole("region", { name: "Running agents" })).toBeNull();
		});
		expect(screen.queryByLabelText("Send to")).toBeNull();
	});

	it("shows the root's open question above the composer until it ends", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const questionEvent = (type: string, data: unknown) =>
			JSON.stringify({
				data,
				id: crypto.randomUUID(),
				metadata: {
					requestId: "77777777-7777-4777-8777-777777777791",
					sessionId: session.id,
				},
				timestamp: 1,
				triggeredBy: null,
				type,
			});

		transport?.receive(
			questionEvent("question.asked", { question: "Ship it on Friday?" }),
		);

		const question = await screen.findByText("Ship it on Friday?");
		expect(question.closest('[role="status"]')).not.toBeNull();
		expect(screen.getByLabelText<HTMLTextAreaElement>("Message").placeholder).toBe(
			"Answer Peen's question",
		);

		transport?.receive(questionEvent("question.answered", { answered: true }));
		await waitFor(() => {
			expect(screen.queryByText("Ship it on Friday?")).toBeNull();
		});
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

		send("userMessage.created", { message: "make a script" });
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
				name: "writeFile",
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
		const writeCard = (await screen.findByText("writeFile:")).closest("details");
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

		send("userMessage.created", { message: "run the build" }, runningRequestID);
		send("turn.started", turnStartedData(), runningRequestID);
		send(
			"content_block_start",
			{
				content_block: {
					id: "call-build",
					input: {},
					name: "runCommand",
					type: "tool_use",
				},
				index: 0,
				type: "content_block_start",
			},
			runningRequestID,
		);
		send("userMessage.created", { message: "then run the tests" }, queuedRequestID);
		send("userMessage.queued", { message: "then run the tests" }, queuedRequestID);

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
		send("userMessage.delivered", { message: "then run the tests" }, queuedRequestID);
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
		const buildCard = screen.getByText("runCommand").closest("details") as HTMLElement;
		expect(
			buildCard.compareDocumentPosition(deliveredMessage) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
		expect(
			deliveredMessage.compareDocumentPosition(reply) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
	});

	it("reconnects after the socket drops mid-turn and catches up the missed deltas", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const requestID = "77777777-7777-4777-8777-777777777701";
		let sequence = 0;
		const sendSequenced = (type: string, data: unknown): void => {
			sequence += 1;
			transport?.receive(
				JSON.stringify({
					data,
					id: `88888888-8888-4888-8888-${String(sequence).padStart(12, "0")}`,
					metadata: { requestId: requestID, sequence, sessionId: session.id },
					timestamp: 1,
					triggeredBy: null,
					type,
				}),
			);
		};

		sendSequenced("userMessage.created", { message: "stream this" });
		sendSequenced("turn.started", turnStartedData());
		sendSequenced("content_block_start", {
			content_block: { type: "text" },
			index: 0,
			type: "content_block_start",
		});
		sendSequenced("content_block_delta", {
			delta: { text: "Hello ", type: "text_delta" },
			index: 0,
			type: "content_block_delta",
		});
		await screen.findByText("Hello", { exact: false });

		// The feed keeps moving on the server while the socket is down. Catch-up
		// must fill exactly the deltas the client missed, once, in order.
		sessionFeedSequence = 6;
		feedFrames = [
			feedFrame(
				5,
				"content_block_delta",
				{
					delta: { text: "world", type: "text_delta" },
					index: 0,
					type: "content_block_delta",
				},
				requestID,
			),
			feedFrame(
				6,
				"content_block_stop",
				{ index: 0, type: "content_block_stop" },
				requestID,
			),
		];

		transport?.close();
		await waitFor(
			() => {
				expect(TestWebSocket.instances.length).toBeGreaterThan(1);
			},
			{ timeout: 3000 },
		);
		TestWebSocket.instances.at(-1)?.open();

		await screen.findByText("Hello world");
		expect(screen.getAllByText("Hello world")).toHaveLength(1);
	});

	it("marks a live turn finished when a post-catch-up session shows no active turn", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const requestID = "77777777-7777-4777-8777-777777777702";
		let sequence = 0;
		const sendSequenced = (type: string, data: unknown): void => {
			sequence += 1;
			transport?.receive(
				JSON.stringify({
					data,
					id: `99999999-9999-4999-8999-${String(sequence).padStart(12, "0")}`,
					metadata: { requestId: requestID, sequence, sessionId: session.id },
					timestamp: 1,
					triggeredBy: null,
					type,
				}),
			);
		};

		sendSequenced("userMessage.created", { message: "keep going" });
		sendSequenced("turn.started", turnStartedData());
		sendSequenced("content_block_start", {
			content_block: { type: "text" },
			index: 0,
			type: "content_block_start",
		});
		sendSequenced("content_block_delta", {
			delta: { text: "Working on it", type: "text_delta" },
			index: 0,
			type: "content_block_delta",
		});

		await screen.findByText("Working on it");
		expect(screen.getByText("Working", { selector: "p.writing" })).not.toBeNull();

		// A crash writes no turn.completed or turn.failed event. GET /session
		// reporting no active turn is the only signal the turn actually ended.
		sessionFeedSequence = sequence;
		transport?.close();
		await waitFor(
			() => {
				expect(TestWebSocket.instances.length).toBeGreaterThan(1);
			},
			{ timeout: 3000 },
		);
		TestWebSocket.instances.at(-1)?.open();

		await waitFor(() => {
			expect(screen.queryByText("Working", { selector: "p.writing" })).toBeNull();
		});
		expect(screen.getByText("Working on it")).not.toBeNull();
	});

	it("replays a running turn's live view from activeTurnFeedSequence when selecting the session", async () => {
		const requestID = "77777777-7777-4777-8777-777777777703";
		sessionActiveTurnFeedSequence = 1;
		sessionFeedSequence = 4;
		feedFrames = [
			feedFrame(1, "userMessage.created", { message: "resume this" }, requestID),
			feedFrame(2, "turn.started", turnStartedData(), requestID),
			feedFrame(
				3,
				"content_block_start",
				{ content_block: { type: "text" }, index: 0, type: "content_block_start" },
				requestID,
			),
			feedFrame(
				4,
				"content_block_delta",
				{
					delta: { text: "Already underway", type: "text_delta" },
					index: 0,
					type: "content_block_delta",
				},
				requestID,
			),
		];

		render(Page);
		await connectAndOpenChat();

		await screen.findByText("resume this");
		await screen.findByText("Already underway");
	});

	it("shows a live notice as an activity row with its type and summary", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		transport?.receive(
			JSON.stringify({
				data: {
					createdAt: "2026-10-15T00:00:00Z",
					data: {},
					delivery: "queue",
					id: "88888888-8888-4888-8888-888888888888",
					source: "worker",
					summary: "The nightly build finished.",
					type: "build.finished",
				},
				id: "88888888-8888-4888-8888-888888888889",
				metadata: { sequence: 1, sessionId: session.id },
				timestamp: 1,
				triggeredBy: null,
				type: "notice.created",
			}),
		);

		await screen.findByText("build.finished");
		await screen.findByText("The nightly build finished.");
	});

	it("accepts a child agent's camelCase assistant message without breaking the activity list", async () => {
		render(Page);
		const transport = await connectAndOpenChat();

		const agentRunID = "44444444-4444-4444-8444-444444444444";
		const parentTurnID = "55555555-5555-4555-8555-555555555555";

		// Part 2's Peen-owned camelCase shape: role, content[], toolCalls[].
		// The activity list already renders this type elsewhere (the chat
		// does), so it must stay hidden, and it must not stop a later
		// agent.run.* event for the same run from rendering.
		transport?.receive(
			JSON.stringify({
				data: {
					agentRunId: agentRunID,
					event: {
						content: [{ text: "Reviewed the diff.", type: "text" }],
						role: "assistant",
						toolCalls: [],
					},
					parentTurnId: parentTurnID,
				},
				id: "77777777-7777-4777-8777-777777777799",
				metadata: { sessionId: session.id },
				timestamp: 1,
				triggeredBy: null,
				type: "agent.run.assistant.message",
			}),
		);

		transport?.receive(
			JSON.stringify({
				data: {
					agentRunId: agentRunID,
					event: { agentRunId: agentRunID, state: "completed" },
					parentTurnId: parentTurnID,
				},
				id: "77777777-7777-4777-8777-777777777800",
				metadata: { sessionId: session.id },
				timestamp: 1,
				triggeredBy: null,
				type: "agent.run.completed",
			}),
		);

		await screen.findByText("Complete");
		expect(screen.queryByText("agent.run.assistant.message")).toBeNull();
	});
});
