import { describe, expect, it } from "vitest";

import type { PeenSocketEvent } from "$lib/ws/socket";

import { applyStreamEvent, type LiveTurn } from "./stream";

const sessionID = "22222222-2222-4222-8222-222222222222";
const otherSessionID = "33333333-3333-4333-8333-333333333333";
const firstRequestID = "44444444-4444-4444-8444-444444444444";
const secondRequestID = "55555555-5555-4555-8555-555555555555";

function event(
	type: string,
	data: unknown,
	requestID = firstRequestID,
	session: string | null = sessionID,
): PeenSocketEvent {
	return {
		data,
		id: crypto.randomUUID(),
		metadata: {
			requestId: requestID,
			...(session === null ? {} : { sessionId: session }),
		},
		timestamp: 1,
		triggeredBy: null,
		type,
	};
}

function blockStart(index: number, block: Record<string, unknown>, requestID?: string) {
	return event(
		"content_block_start",
		{ content_block: block, index, type: "content_block_start" },
		requestID,
	);
}

function blockDelta(index: number, delta: Record<string, unknown>, requestID?: string) {
	return event(
		"content_block_delta",
		{ delta, index, type: "content_block_delta" },
		requestID,
	);
}

function blockStop(index: number, requestID?: string) {
	return event("content_block_stop", { index, type: "content_block_stop" }, requestID);
}

function fold(events: PeenSocketEvent[]): LiveTurn[] {
	return events.reduce(applyStreamEvent, [] as LiveTurn[]);
}

describe("live stream reducer", () => {
	it("joins text deltas into one text block", () => {
		const turns = fold([
			blockStart(0, { type: "text" }),
			blockDelta(0, { text: "Hel", type: "text_delta" }),
			blockDelta(0, { text: "lo ", type: "text_delta" }),
			blockDelta(0, { text: "there", type: "text_delta" }),
			blockStop(0),
		]);

		expect(turns).toHaveLength(1);
		expect(turns[0]?.blocks).toEqual([
			{ key: `${firstRequestID}:0`, kind: "text", text: "Hello there" },
		]);
	});

	it("keeps thinking and text as separate blocks in arrival order", () => {
		const turns = fold([
			blockStart(0, { type: "thinking" }),
			blockDelta(0, { text: "plan", type: "thinking_delta" }),
			blockStop(0),
			blockStart(1, { type: "text" }),
			blockDelta(1, { text: "answer", type: "text_delta" }),
			blockStop(1),
		]);

		expect(turns[0]?.blocks.map((block) => block.kind)).toEqual(["thinking", "text"]);
		expect(turns[0]?.blocks[0]).toMatchObject({ text: "plan" });
		expect(turns[0]?.blocks[1]).toMatchObject({ text: "answer" });
	});

	it("pairs a tool result with its tool call by tool use id", () => {
		const turns = fold([
			blockStart(0, { id: "call-1", input: {}, name: "write_file", type: "tool_use" }),
			blockDelta(0, { partial_json: '{"path":"a.sh"}', type: "input_json_delta" }),
			blockStop(0),
			blockStart(1, { tool_use_id: "call-1", type: "tool_result" }),
			blockDelta(1, { text: '{"created":', type: "json_partial" }),
			blockDelta(1, { text: "true}", type: "json_partial" }),
			blockStop(1),
		]);

		expect(turns[0]?.blocks).toEqual([
			{
				input: '{"path":"a.sh"}',
				isError: false,
				key: `${firstRequestID}:0`,
				kind: "tool",
				name: "write_file",
				result: '{"created":true}',
				toolUseID: "call-1",
			},
		]);
	});

	it("marks a failed tool result as an error", () => {
		const turns = fold([
			blockStart(0, { id: "call-1", input: {}, name: "run_command", type: "tool_use" }),
			blockStart(1, { is_error: true, tool_use_id: "call-1", type: "tool_result" }),
			blockDelta(1, { text: "exit 1", type: "json_partial" }),
		]);

		expect(turns[0]?.blocks[0]).toMatchObject({ isError: true, result: "exit 1" });
	});

	it("rebuilds a multi-round turn in order", () => {
		const turns = fold([
			blockStart(0, { type: "thinking" }),
			blockDelta(0, { text: "first", type: "thinking_delta" }),
			blockStart(1, { id: "call-1", input: {}, name: "read_file", type: "tool_use" }),
			blockStart(2, { tool_use_id: "call-1", type: "tool_result" }),
			blockDelta(2, { text: "content", type: "json_partial" }),
			blockStart(3, { type: "thinking" }),
			blockDelta(3, { text: "second", type: "thinking_delta" }),
			blockStart(4, { type: "text" }),
			blockDelta(4, { text: "done", type: "text_delta" }),
		]);

		expect(
			turns[0]?.blocks.map((block) =>
				block.kind === "tool" ? `${block.name}:${block.result ?? ""}` : block.text,
			),
		).toEqual(["first", "read_file:content", "second", "done"]);
	});

	it("keeps turns with different request ids apart even when indexes repeat", () => {
		const turns = fold([
			blockStart(0, { type: "text" }, firstRequestID),
			blockDelta(0, { text: "one", type: "text_delta" }, firstRequestID),
			blockStart(0, { type: "text" }, secondRequestID),
			blockDelta(0, { text: "two", type: "text_delta" }, secondRequestID),
		]);

		expect(turns.map((turn) => turn.blocks[0])).toEqual([
			{ key: `${firstRequestID}:0`, kind: "text", text: "one" },
			{ key: `${secondRequestID}:0`, kind: "text", text: "two" },
		]);
	});

	it("records the prompt and session of the turn", () => {
		const turns = fold([event("user_message.created", { message: "make a script" })]);

		expect(turns).toEqual([
			{
				blocks: [],
				isFinished: false,
				prompt: "make a script",
				requestID: firstRequestID,
				sessionID,
				slots: new Map(),
			},
		]);
	});

	it.each(["turn.completed", "turn.failed", "turn.cancelled"])(
		"marks the turn finished on %s",
		(terminalType) => {
			const turns = fold([event("turn.started", {}), event(terminalType, {})]);

			expect(turns[0]?.isFinished).toBe(true);
		},
	);

	it("records which event started a turn", () => {
		const turns = fold([
			event("user_message.created", { message: "fix CI" }),
			event("turn.started", { originEventType: "ci.build.failed" }),
		]);

		expect(turns[0]?.originEventType).toBe("ci.build.failed");
		expect(turns[0]?.prompt).toBe("fix CI");
	});

	it("leaves a typed turn without an origin", () => {
		const turns = fold([event("turn.started", { model: "aigate/model" })]);

		expect(turns[0]?.originEventType).toBeUndefined();
	});

	it("keeps another session's turn under its own session id", () => {
		const turns = fold([
			event(
				"user_message.created",
				{ message: "elsewhere" },
				firstRequestID,
				otherSessionID,
			),
		]);

		expect(turns[0]?.sessionID).toBe(otherSessionID);
	});

	it("joins multi-byte text split across deltas exactly", () => {
		const turns = fold([
			blockStart(0, { type: "text" }),
			blockDelta(0, { text: "naïve 🚀 ", type: "text_delta" }),
			blockDelta(0, { text: "日本語", type: "text_delta" }),
		]);

		expect(turns[0]?.blocks[0]).toMatchObject({ text: "naïve 🚀 日本語" });
	});

	it("keeps every delta of a long block", () => {
		const deltaCount = 1000;
		const deltas = Array.from({ length: deltaCount }, () =>
			blockDelta(0, { text: "x", type: "text_delta" }),
		);
		const turns = fold([blockStart(0, { type: "text" }), ...deltas]);

		expect(turns[0]?.blocks[0]).toMatchObject({ text: "x".repeat(deltaCount) });
	});

	it.each([
		[
			"a delta with no started block",
			[blockDelta(5, { text: "lost", type: "text_delta" })],
		],
		[
			"a tool result for an unknown call",
			[blockStart(0, { tool_use_id: "missing", type: "tool_result" })],
		],
		["a non-record payload", [event("content_block_start", "garbage")]],
		[
			"a missing index",
			[event("content_block_start", { content_block: { type: "text" } })],
		],
		[
			"a non-integer index",
			[event("content_block_start", { content_block: { type: "text" }, index: "0" })],
		],
		["an unknown block type", [blockStart(0, { type: "image" })]],
	])("ignores %s without adding content", (_name, events) => {
		const turns = fold(events);

		expect(turns.flatMap((turn) => turn.blocks)).toEqual([]);
	});

	it("ignores unknown delta types and empty text", () => {
		const turns = fold([
			blockStart(0, { type: "text" }),
			blockDelta(0, { text: "kept", type: "text_delta" }),
			blockDelta(0, { text: "dropped", type: "mystery_delta" }),
			blockDelta(0, { text: "", type: "text_delta" }),
			blockDelta(0, { text: "dropped", type: "thinking_delta" }),
		]);

		expect(turns[0]?.blocks[0]).toMatchObject({ text: "kept" });
	});

	it("ignores events without a session or request id", () => {
		const turns = fold([
			event("user_message.created", { message: "x" }, firstRequestID, null),
		]);

		expect(turns).toEqual([]);
	});

	it("does not mutate the previous list", () => {
		const before = fold([blockStart(0, { type: "text" })]);
		const snapshot = structuredClone(before);
		applyStreamEvent(before, blockDelta(0, { text: "new", type: "text_delta" }));

		expect(before).toEqual(snapshot);
	});

	describe("queued messages", () => {
		const runningTurn = [
			event("user_message.created", { message: "inspect" }),
			event("turn.started", {}),
			blockStart(0, { id: "call-1", name: "list_files", type: "tool_use" }),
		];
		const queued = [
			event("user_message.created", { message: "also check tests" }, secondRequestID),
			event("user_message.queued", { message: "also check tests" }, secondRequestID),
		];

		it("marks a message sent during a running turn as queued", () => {
			const turns = fold([...runningTurn, ...queued]);

			expect(turns[1]).toMatchObject({
				prompt: "also check tests",
				queue: "queued",
				requestID: secondRequestID,
			});
			expect(turns[0]?.queue).toBeUndefined();
		});

		it("moves a delivered message into the running turn after the tool result", () => {
			const turns = fold([
				...runningTurn,
				...queued,
				blockStart(1, { tool_use_id: "call-1", type: "tool_result" }),
				event(
					"user_message.delivered",
					{ message: "also check tests" },
					secondRequestID,
				),
				blockStart(2, { type: "text" }),
				blockDelta(2, { text: "Checked.", type: "text_delta" }),
			]);

			expect(turns[1]).toMatchObject({ isFinished: true, queue: "delivered" });
			expect(turns[0]?.blocks.map((block) => block.kind)).toEqual([
				"tool",
				"user",
				"text",
			]);
			expect(turns[0]?.blocks[1]).toMatchObject({
				kind: "user",
				text: "also check tests",
			});
		});

		it("keeps a delivered message as a plain bubble when no running turn is known", () => {
			const turns = fold([
				...queued,
				event(
					"user_message.delivered",
					{ message: "also check tests" },
					secondRequestID,
				),
			]);

			expect(turns).toHaveLength(1);
			expect(turns[0]).toMatchObject({ isFinished: true, queue: undefined });
		});
	});
});
