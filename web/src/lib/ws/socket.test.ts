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

import {
	PeenSocket,
	parseServerEvent,
	webSocketProtocols,
	webSocketURL,
} from "./socket";

const eventID = "11111111-1111-4111-8111-111111111111";
const sessionID = "22222222-2222-4222-8222-222222222222";
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
	});

	it("uses one global route and the bearer subprotocol", () => {
		window.history.replaceState({}, "", "/client?ignored=true#ignored");

		expect(webSocketURL()).toBe(
			`${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}/v1/ws`,
		);
		expect(webSocketProtocols(" abc ")).toEqual(["peen.v1", "peen.bearer.YWJj"]);
		expect(webSocketProtocols("  ")).toEqual(["peen.v1"]);
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
		socket.send(sessionID, "inspect this", "aigate/model/name");

		expect(states).toEqual(["connecting", "open"]);
		expect(errors).toEqual([]);
		expect(transport?.sent).toEqual([
			JSON.stringify({
				data: { message: "inspect this", model: "aigate/model/name" },
				id: eventID,
				metadata: { sessionId: sessionID },
				timestamp: 1_750_000_000,
				triggeredBy: null,
				type: "message.send",
			}),
		]);

		transport?.receive(
			JSON.stringify({
				data: { result: ["kept", 1] },
				id: eventID,
				metadata: { requestId: eventID, sessionId: sessionID },
				timestamp: 1_750_000_001,
				triggeredBy: null,
				type: "message.completed",
			}),
		);

		expect(events).toEqual([
			{
				data: { result: ["kept", 1] },
				id: eventID,
				metadata: { requestId: eventID, sessionId: sessionID },
				timestamp: 1_750_000_001,
				triggeredBy: null,
				type: "message.completed",
			},
		]);
	});

	it.each([
		["invalid JSON", "{"],
		["an array", "[]"],
		["a null value", "null"],
		[
			"numeric session metadata",
			JSON.stringify({
				data: {},
				id: eventID,
				metadata: { sessionId: 1 },
				timestamp: 1,
				triggeredBy: null,
				type: "message.completed",
			}),
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
		],
	])("rejects %s", (_name, raw) => {
		expect(parseServerEvent(raw)).toBeUndefined();
	});

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
		socket.send(sessionID, secretMessage, "aigate/catalog/model");
		transport?.receive(
			JSON.stringify({
				data: { text: secretPayload },
				id: eventID,
				metadata: { requestId: requestID, sessionId: sessionID },
				timestamp: 1_750_000_001,
				triggeredBy: null,
				type: "message.completed",
			}),
		);
		transport?.receive(`{"broken": "${secretPayload}"`);
		transport?.receive(new Uint8Array([1]));
		transport?.fail();
		transport?.close();

		expect(browserRecords(debug)).toEqual([
			{ event: "socket.connect.start", socket_state: "connecting" },
			{ event: "socket.open", socket_state: "open" },
			{
				event: "socket.message.sent",
				event_id: eventID,
				model: "aigate/catalog/model",
				session_id: sessionID,
			},
			{
				event: "socket.event.received",
				event_id: eventID,
				event_type: "message.completed",
				request_id: requestID,
				session_id: sessionID,
			},
			{ event: "socket.frame.rejected", reason: "invalid_event" },
			{ event: "socket.frame.rejected", reason: "non_text" },
			{ event: "socket.error" },
			{ event: "socket.close", socket_state: "closed" },
		]);

		const logged = browserLogText(debug);
		expect(logged).not.toContain(secretToken);
		const bearerProtocol = webSocketProtocols(secretToken)[1];
		expect(bearerProtocol).toBeDefined();
		expect(logged).not.toContain(bearerProtocol ?? secretToken);
		expect(logged).not.toContain(secretMessage);
		expect(logged).not.toContain(secretPayload);
	});

	it("leaves the model out of the sent record when none was chosen", () => {
		const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
		const socket = new PeenSocket({
			onError: () => undefined,
			onEvent: () => undefined,
			onState: () => undefined,
			token: "",
		});
		socket.connect();
		TestWebSocket.instances[0]?.open();
		socket.send(sessionID, secretMessage, "");

		expect(browserRecords(debug).at(-1)).toEqual({
			event: "socket.message.sent",
			event_id: eventID,
			session_id: sessionID,
		});
		expect(browserLogText(debug)).not.toContain(secretMessage);
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

		expect(() => socket.send(sessionID, secretMessage, "")).toThrow(
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
