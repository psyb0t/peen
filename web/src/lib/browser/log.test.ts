import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	vi,
	type MockInstance,
} from "vitest";

import {
	BROWSER_LOG_PREFIX,
	logBrowserEvent,
	type BrowserLogEvent,
	type BrowserLogMetadata,
} from "./log";

const sessionID = "22222222-2222-4222-8222-222222222222";
const eventID = "11111111-1111-4111-8111-111111111111";
const tokenLike = "EXAMPLE-DO-NOT-USE-browser-token";
const messageLike = "please read my private notes";

type DebugSpy = MockInstance<(...data: unknown[]) => void>;

function capturedLines(spy: DebugSpy): string[] {
	return spy.mock.calls.map((call) => {
		expect(call).toHaveLength(1);
		const [line] = call;
		if (typeof line !== "string") {
			throw new Error("A browser log record must be one string.");
		}

		return line;
	});
}

function parseRecord(line: string): unknown {
	expect(line.startsWith(`${BROWSER_LOG_PREFIX} `)).toBe(true);

	return JSON.parse(line.slice(BROWSER_LOG_PREFIX.length + 1));
}

describe("logBrowserEvent", () => {
	let debug: DebugSpy;

	beforeEach(() => {
		debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("emits one prefixed JSON record with the event and safe metadata", () => {
		logBrowserEvent("session.load.complete", {
			count: 3,
			duration_ms: 12.6,
			session_id: sessionID,
			status: 200,
		});

		const lines = capturedLines(debug);
		expect(lines).toHaveLength(1);
		expect(parseRecord(lines[0] ?? "")).toEqual({
			count: 3,
			duration_ms: 13,
			event: "session.load.complete",
			session_id: sessionID,
			status: 200,
		});
	});

	it("emits only the event name when there is no metadata", () => {
		logBrowserEvent("controller.refresh.start");

		expect(capturedLines(debug).map(parseRecord)).toEqual([
			{ event: "controller.refresh.start" },
		]);
	});

	it("keeps provider-qualified model names that contain slashes", () => {
		logBrowserEvent("socket.message.sent", {
			event_id: eventID,
			model: "aigate/catalog/model",
			session_id: sessionID,
		});

		expect(capturedLines(debug).map(parseRecord)).toEqual([
			{
				event: "socket.message.sent",
				event_id: eventID,
				model: "aigate/catalog/model",
				session_id: sessionID,
			},
		]);
	});

	it("does not accept token or message fields in its public type", () => {
		// @ts-expect-error A token has no field in the metadata type.
		logBrowserEvent("ui.error", { token: tokenLike });
		// @ts-expect-error Message text has no field in the metadata type.
		logBrowserEvent("ui.error", { message: messageLike });
		// @ts-expect-error Operation names are a closed set.
		logBrowserEvent("ui.error", { operation: messageLike });
		// @ts-expect-error Event names are a closed set.
		logBrowserEvent(tokenLike);

		const lines = capturedLines(debug);
		expect(lines.map(parseRecord)).toEqual([
			{ event: "ui.error" },
			{ event: "ui.error" },
			{ event: "ui.error" },
		]);

		for (const line of lines) {
			expect(line).not.toContain(tokenLike);
			expect(line).not.toContain(messageLike);
		}
	});

	it.each<[string, Record<string, unknown>]>([
		["unlisted token keys", { api_token: tokenLike, authorization: tokenLike }],
		["unlisted body keys", { body: messageLike, data: { text: messageLike } }],
		["a token in ID fields", { event_id: tokenLike, session_id: tokenLike }],
		["message text in the event type", { event_type: messageLike }],
		["message text in the model name", { model: messageLike }],
		["a token in closed-set fields", { reason: tokenLike, socket_state: tokenLike }],
		["objects in primitive fields", { count: { value: 1 }, model: [tokenLike] }],
		["out-of-range numbers", { count: -1, duration_ms: Number.NaN, status: 42 }],
		["a fractional count and status", { count: 1.5, status: 200.5 }],
	])("drops %s at runtime", (_name, unsafe) => {
		logBrowserEvent("ui.error", unsafe as BrowserLogMetadata);

		const lines = capturedLines(debug);
		expect(lines.map(parseRecord)).toEqual([{ event: "ui.error" }]);
		expect(lines[0]).not.toContain(tokenLike);
		expect(lines[0]).not.toContain(messageLike);
	});

	it("writes nothing for an event name outside the allowlist", () => {
		logBrowserEvent(tokenLike as BrowserLogEvent, { session_id: sessionID });
		logBrowserEvent(messageLike as BrowserLogEvent);

		expect(debug).not.toHaveBeenCalled();
	});

	it("ignores metadata that is not an object", () => {
		logBrowserEvent("ui.error", tokenLike as unknown as BrowserLogMetadata);

		const lines = capturedLines(debug);
		expect(lines.map(parseRecord)).toEqual([{ event: "ui.error" }]);
		expect(lines[0]).not.toContain(tokenLike);
	});
});
