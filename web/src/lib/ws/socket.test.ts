import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	vi,
	type MockInstance,
} from "vitest";

import { BROWSER_LOG_PREFIX } from "$lib/browser/log";
import { RECONNECT_BASE_DELAY_MS, RECONNECT_MAX_DELAY_MS } from "$lib/common/constants";

import {
	PeenSocket,
	parseServerEvent,
	webSocketProtocols,
	webSocketURL,
	type IncomingFrame,
} from "./socket";

const eventID = "11111111-1111-4111-8111-111111111111";
const sessionID = "22222222-2222-4222-8222-222222222222";
const childAgentRunID = "33333333-3333-4333-8333-333333333333";
const requestID = "33333333-3333-4333-8333-333333333333";
const secretToken = "EXAMPLE-DO-NOT-USE-socket-token";
const secretMessage = "private message text for the model";
const secretPayload = "durable payload that must stay private";

function browserRecords(spy: MockInstance<(...data: unknown[]) => void>): unknown[] {
	return spy.mock.calls.map((call) => {
		const [line] = call;
		if (typeof line !== "string" || !line.startsWith(`${BROWSER_LOG_PREFIX} `)) {
			throw new Error("A browser log record must be one prefixed string.");
		}

		return JSON.parse(line.slice(BROWSER_LOG_PREFIX.length + 1));
	});
}

function browserLogText(spy: MockInstance<(...data: unknown[]) => void>): string {
	return spy.mock.calls.map((call) => String(call[0])).join("\n");
}

class TestWebSocket extends EventTarget {
	public static readonly OPEN = 1;
	public static instances: TestWebSocket[] = [];

	public readonly sent: string[] = [];
	public readyState = 0;

	public constructor(
		public readonly url: string,
		public readonly requestedProtocols: string | string[],
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

	public send(data: string): void {
		this.sent.push(data);
	}

	public fail(): void {
		this.dispatchEvent(new Event("error"));
	}

	public close(): void {
		this.readyState = 3;
		this.dispatchEvent(new Event("close"));
	}
}

describe("PeenSocket", () => {
	beforeEach(() => {
		TestWebSocket.instances = [];
		vi.stubGlobal("WebSocket", TestWebSocket);
		vi.stubGlobal("crypto", { randomUUID: () => eventID });
	});

	afterEach(() => {
		vi.restoreAllMocks();
		vi.unstubAllGlobals();
		vi.useRealTimers();
	});

	it("uses one global route and the bearer subprotocol", () => {
		window.history.replaceState({}, "", "/client?ignored=true#ignored");

		expect(webSocketURL()).toBe(
			`${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}/ws/v1`,
		);
		expect(webSocketProtocols(" abc ")).toEqual(["peen.v1", "peen.bearer.YWJj"]);
		expect(webSocketProtocols("  ")).toEqual(["peen.v1"]);
	});

	it("opens exactly one socket and never a session-filtered one", () => {
		const socket = new PeenSocket({
			onError: vi.fn(),
			onEvent: vi.fn(),
			onState: vi.fn(),
			token: secretToken,
		});
		socket.connect();
		const transport = TestWebSocket.instances[0];
		transport?.open();

		expect(TestWebSocket.instances).toHaveLength(1);
		expect(transport?.url).not.toContain("sessionId=");
		socket.close();
	});

	it("gates send on the global socket being open", () => {
		const socket = new PeenSocket({
			onError: vi.fn(),
			onEvent: vi.fn(),
			onState: vi.fn(),
			token: "",
		});
		socket.connect();

		expect(() => socket.send(sessionID, "too early")).toThrow(
			"The WebSocket is not connected.",
		);

		const transport = TestWebSocket.instances[0];
		transport?.open();
		expect(() => socket.send(sessionID, "now it works")).not.toThrow();
	});

	it("reconnects an unexpected close with a doubling, jittered backoff", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0);
		const states: string[] = [];
		const socket = new PeenSocket({
			onError: vi.fn(),
			onEvent: vi.fn(),
			onState: (state) => states.push(state),
			token: "",
		});
		socket.connect();
		TestWebSocket.instances[0]?.open();
		expect(states.at(-1)).toBe("open");

		TestWebSocket.instances[0]?.close();
		expect(states.at(-1)).toBe("reconnecting");
		expect(TestWebSocket.instances).toHaveLength(1);

		vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS);
		expect(TestWebSocket.instances).toHaveLength(2);
		expect(states.at(-1)).toBe("connecting");

		// A second unexpected close doubles the wait before trying again.
		TestWebSocket.instances[1]?.close();
		vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS * 2 - 1);
		expect(TestWebSocket.instances).toHaveLength(2);
		vi.advanceTimersByTime(1);
		expect(TestWebSocket.instances).toHaveLength(3);

		// A successful open resets the backoff back to the base delay.
		TestWebSocket.instances[2]?.open();
		TestWebSocket.instances[2]?.close();
		vi.advanceTimersByTime(RECONNECT_BASE_DELAY_MS - 1);
		expect(TestWebSocket.instances).toHaveLength(3);
		vi.advanceTimersByTime(1);
		expect(TestWebSocket.instances).toHaveLength(4);

		socket.close();
	});

	it("caps the backoff delay and never reconnects after an explicit close", () => {
		vi.useFakeTimers();
		vi.spyOn(Math, "random").mockReturnValue(0);
		const states: string[] = [];
		const socket = new PeenSocket({
			onError: vi.fn(),
			onEvent: vi.fn(),
			onState: (state) => states.push(state),
			token: "",
		});
		socket.connect();
		TestWebSocket.instances[0]?.open();

		for (let round = 0; round < 10; round += 1) {
			const current = TestWebSocket.instances.at(-1);
			current?.close();
			vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS);
		}
		expect(TestWebSocket.instances.length).toBeGreaterThan(1);

		const beforeExplicitClose = TestWebSocket.instances.length;
		socket.close();
		expect(states.at(-1)).toBe("closed");
		vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS * 2);
		expect(TestWebSocket.instances).toHaveLength(beforeExplicitClose);
	});

	it("sends a routed message command and accepts a valid global event", () => {
		const events: unknown[] = [];
		const errors: string[] = [];
		const states: string[] = [];
		vi.spyOn(Date, "now").mockReturnValue(1_750_000_000_000);

		const socket = new PeenSocket({
			onError: (message) => errors.push(message),
			onEvent: (event) => events.push(event),
			onState: (state) => states.push(state),
			token: "token",
		});
		socket.connect();

		const transport = TestWebSocket.instances[0];
		expect(transport).toBeDefined();
		transport?.open();
		expect(
			socket.send(sessionID, "inspect this", {
				model: "aigate/model/name",
				reasoningEffort: "high",
			}),
		).toBe(eventID);

		expect(states).toEqual(["connecting", "open"]);
		expect(errors).toEqual([]);
		expect(transport?.sent).toEqual([
			JSON.stringify({
				data: {
					message: "inspect this",
					model: "aigate/model/name",
					reasoningEffort: "high",
				},
				id: eventID,
				metadata: { sessionId: sessionID },
				timestamp: 1_750_000_000,
				triggeredBy: null,
				type: "message.send",
			}),
		]);

		transport?.receive(
			JSON.stringify({
				data: { model: "aigate/model/name", text: "kept" },
				id: eventID,
				metadata: { requestId: eventID, sequence: 7, sessionId: sessionID },
				timestamp: 1_750_000_001,
				triggeredBy: null,
				type: "turn.completed",
			}),
		);

		expect(events).toEqual([
			{
				event: {
					data: { model: "aigate/model/name", text: "kept" },
					id: eventID,
					metadata: { requestId: eventID, sequence: 7, sessionId: sessionID },
					timestamp: 1_750_000_001,
					triggeredBy: null,
					type: "turn.completed",
				},
				kind: "valid",
			},
		]);
	});

	// There is no envelope to report a type or sequence from: no diagnostic
	// is possible, so the frame is dropped outright.
	it.each([
		["invalid JSON", "{"],
		["an array", "[]"],
		["a null value", "null"],
	])("drops %s with nothing to classify", (_name, raw) => {
		expect(parseServerEvent(raw)).toBeUndefined();
	});

	// Each of these decodes to an object, so there is an envelope to report,
	// but the contract rejects it: a malformed metadata field or a `data`
	// that does not fit its `type`'s schema. The frame classifies as invalid
	// rather than being dropped, so a feed cursor tracking its session can
	// still see whatever sequence number it trusts.
	it.each([
		[
			"metadata.sessionId of the wrong type",
			JSON.stringify({
				data: {},
				id: eventID,
				metadata: { sessionId: 1 },
				timestamp: 1,
				triggeredBy: null,
				type: "message.completed",
			}),
			"message.completed",
		],
		[
			"a non-numeric timestamp",
			JSON.stringify({
				data: {},
				id: eventID,
				metadata: {},
				timestamp: "1",
				triggeredBy: null,
				type: "message.completed",
			}),
			"message.completed",
		],
		[
			"a non-numeric sequence",
			JSON.stringify({
				data: {},
				id: eventID,
				metadata: { sequence: "1" },
				timestamp: 1,
				triggeredBy: null,
				type: "turn.started",
			}),
			"turn.started",
		],
	])(
		"classifies %s as an invalid frame with no trusted sequence",
		(_name, raw, type) => {
			expect(parseServerEvent(raw)).toEqual({
				invalid: { sequence: undefined, sessionId: undefined, type },
				kind: "invalid",
			});
		},
	);

	it("logs each lifecycle step with safe metadata and no payload content", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: () => undefined,
			onState: () => undefined,
			token: secretToken,
		});
		socket.connect();

		const transport = TestWebSocket.instances[0];
		transport?.open();
		socket.send(sessionID, secretMessage, {
			model: "aigate/catalog/model",
			reasoningEffort: "low",
		});
		transport?.receive(
			JSON.stringify({
				data: { model: "aigate/model", text: secretPayload },
				id: eventID,
				metadata: { requestId: requestID, sessionId: sessionID },
				timestamp: 1_750_000_001,
				triggeredBy: null,
				type: "turn.completed",
			}),
		);
		transport?.receive(`{"broken": "${secretPayload}"`);
		transport?.receive(new Uint8Array([1]));
		transport?.fail();
		transport?.close();

		const records = browserRecords(debug);
		expect(records.slice(0, 8)).toEqual([
			{ event: "socket.connect.start", socket_state: "connecting" },
			{ event: "socket.open", socket_state: "open" },
			{
				event: "socket.message.sent",
				event_id: eventID,
				model: "aigate/catalog/model",
				reasoning_effort: "low",
				session_id: sessionID,
			},
			{
				event: "socket.event.received",
				event_id: eventID,
				event_type: "turn.completed",
				request_id: requestID,
				session_id: sessionID,
			},
			{ event: "socket.frame.rejected", reason: "invalid_event" },
			{ event: "socket.frame.rejected", reason: "non_text" },
			{ event: "socket.error" },
			{ event: "socket.close", socket_state: "closed" },
		]);
		// The close was unexpected (no explicit socket.close() call), so a
		// reconnect is scheduled right after.
		expect(records.at(-1)).toMatchObject({ event: "socket.reconnect.scheduled" });

		const logged = browserLogText(debug);
		expect(logged).not.toContain(secretToken);
		const bearerProtocol = webSocketProtocols(secretToken)[1];
		expect(bearerProtocol).toBeDefined();
		expect(logged).not.toContain(bearerProtocol ?? secretToken);
		expect(logged).not.toContain(secretMessage);
		expect(logged).not.toContain(secretPayload);
		socket.close();
	});

	it("classifies an unknown type and a shape the schema forbids as invalid, logging only the type and sequence", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
		const frames: IncomingFrame[] = [];
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: (frame) => frames.push(frame),
			onState: () => undefined,
			token: "",
		});
		socket.connect();
		const transport = TestWebSocket.instances[0];
		transport?.open();

		// Forward compatibility: a type the client has not learned yet.
		transport?.receive(
			JSON.stringify({
				data: { anything: secretPayload },
				id: eventID,
				metadata: { sequence: 9, sessionId: sessionID },
				timestamp: 1,
				triggeredBy: null,
				type: "agent.run.not.a.real.type",
			}),
		);
		// A known type whose data does not fit its schema.
		transport?.receive(
			JSON.stringify({
				data: { text: secretPayload },
				id: eventID,
				metadata: { sequence: 10, sessionId: sessionID },
				timestamp: 1,
				triggeredBy: null,
				type: "message.completed",
			}),
		);

		expect(frames).toEqual([
			{
				invalid: {
					sequence: 9,
					sessionId: sessionID,
					type: "agent.run.not.a.real.type",
				},
				kind: "invalid",
			},
			{
				invalid: { sequence: 10, sessionId: sessionID, type: "message.completed" },
				kind: "invalid",
			},
		]);

		const records = browserRecords(debug);
		expect(records.slice(-2)).toEqual([
			{
				event: "socket.frame.invalid",
				event_type: "agent.run.not.a.real.type",
				sequence: 9,
			},
			{ event: "socket.frame.invalid", event_type: "message.completed", sequence: 10 },
		]);
		expect(browserLogText(debug)).not.toContain(secretPayload);
		socket.close();
	});

	it("sends a queued message without turn settings", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: () => undefined,
			onState: () => undefined,
			token: "",
		});
		socket.connect();
		const transport = TestWebSocket.instances[0];
		transport?.open();
		socket.send(sessionID, secretMessage);

		const sent: unknown = JSON.parse(transport?.sent[0] ?? "null");
		expect(sent).toMatchObject({ data: { message: secretMessage } });
		expect(sent).not.toHaveProperty("data.model");
		expect(sent).not.toHaveProperty("data.reasoningEffort");
		expect(browserRecords(debug).at(-1)).toEqual({
			event: "socket.message.sent",
			event_id: eventID,
			session_id: sessionID,
		});
		expect(browserLogText(debug)).not.toContain(secretMessage);
	});

	it("sends a message to a child agent by its run and without turn settings", () => {
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: () => undefined,
			onState: () => undefined,
			token: "",
		});
		socket.connect();
		const transport = TestWebSocket.instances[0];
		transport?.open();
		socket.send(
			sessionID,
			secretMessage,
			{ model: "aigate/default", reasoningEffort: "high" },
			childAgentRunID,
		);

		const sent: unknown = JSON.parse(transport?.sent[0] ?? "null");
		expect(sent).toMatchObject({
			data: { message: secretMessage, to: childAgentRunID },
		});
		expect(sent).not.toHaveProperty("data.model");
		expect(sent).not.toHaveProperty("data.reasoningEffort");
	});

	it("does not log a send that never reached an open socket", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: () => undefined,
			onState: () => undefined,
			token: "",
		});
		socket.connect();

		expect(() => socket.send(sessionID, secretMessage)).toThrow(
			"The WebSocket is not connected.",
		);
		expect(browserRecords(debug)).toEqual([
			{ event: "socket.connect.start", socket_state: "connecting" },
		]);
	});

	it("reports malformed and non-text frames without delivering them", () => {
		const errors: string[] = [];
		const events: unknown[] = [];
		const socket = new PeenSocket({
			onError: (message) => errors.push(message),
			onEvent: (event) => events.push(event),
			onState: () => undefined,
			token: "",
		});
		socket.connect();

		const transport = TestWebSocket.instances[0];
		transport?.receive("not JSON");
		transport?.receive(new Uint8Array([1]));

		expect(events).toEqual([]);
		expect(errors).toEqual([
			"The controller sent an invalid WebSocket event.",
			"The controller sent a non-text WebSocket frame.",
		]);
	});
});
