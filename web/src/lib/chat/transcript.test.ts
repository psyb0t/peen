import { describe, expect, it } from "vitest";

import type { components } from "$lib/api/generated";
import type { LiveTurn } from "$lib/chat/stream";

import { groupTranscript, liveReplyBlock, liveReplyBlocks } from "./transcript";

type Message = components["schemas"]["Message"];

const workspace = "/workspace/catalog";
let sequence = 0;

function message(fields: Partial<Message> & Pick<Message, "role">): Message {
	sequence += 1;

	return {
		content: "",
		createdAt: "2026-09-24T00:00:00Z",
		id: `00000000-0000-4000-8000-${String(sequence).padStart(12, "0")}`,
		sequence,
		workspace,
		...fields,
	};
}

describe("groupTranscript", () => {
	it("returns nothing for an empty transcript", () => {
		expect(groupTranscript([])).toEqual([]);
	});

	it("attaches each tool result to the call it answers", () => {
		const prompt = message({ content: "read it", role: "user" });
		const call = message({
			content: "",
			role: "assistant",
			thinking: "look first",
			toolCalls: [
				{ arguments: { path: "a.md" }, id: "call-a", name: "read_file" },
				{ arguments: { path: "b.md" }, id: "call-b", name: "read_file" },
			],
		});
		const resultB = message({ content: "B", role: "tool", toolCallId: "call-b" });
		const resultA = message({
			content: "denied",
			isError: true,
			role: "tool",
			toolCallId: "call-a",
		});
		const answer = message({ content: "Both read.", role: "assistant" });

		const items = groupTranscript([prompt, call, resultB, resultA, answer]);

		expect(items).toHaveLength(2);
		expect(items[0]).toEqual({ kind: "user", message: prompt });
		expect(items[1]).toMatchObject({
			blocks: [
				{ kind: "thinking", text: "look first" },
				{
					kind: "tool",
					name: "read_file",
					result: "denied",
					status: "error",
				},
				{ kind: "tool", name: "read_file", result: "B", status: "done" },
				{ kind: "text", text: "Both read." },
			],
			id: call.id,
			kind: "reply",
		});
	});

	it("marks a call without a stored result as missing", () => {
		const call = message({
			role: "assistant",
			toolCalls: [{ arguments: {}, id: "call-lost", name: "run" }],
		});

		expect(groupTranscript([call])).toMatchObject([
			{ blocks: [{ kind: "tool", result: undefined, status: "missing" }] },
		]);
	});

	it("shows a result whose call is outside the loaded page on its own", () => {
		const orphan = message({ content: "late", role: "tool", toolCallId: "call-old" });

		expect(groupTranscript([orphan])).toMatchObject([
			{
				blocks: [
					{
						arguments: "",
						kind: "tool",
						name: "Tool result",
						result: "late",
						status: "done",
					},
				],
			},
		]);
	});

	it("keeps an injected update inside the reply instead of as a prompt", () => {
		const prompt = message({ content: "go", role: "user" });
		const injected = message({ content: "job done", injected: true, role: "user" });
		const answer = message({ content: "Noted.", role: "assistant" });
		const next = message({ content: "again", role: "user" });

		const items = groupTranscript([prompt, injected, answer, next]);

		expect(items.map((item) => item.kind)).toEqual(["user", "reply", "user"]);
		expect(items[1]).toMatchObject({
			blocks: [
				{ kind: "injected", text: "job done" },
				{ kind: "text", text: "Noted." },
			],
		});
	});
});

describe("groupTranscript turns", () => {
	const firstTurn = "11111111-1111-4111-8111-111111111111";
	const secondTurn = "22222222-2222-4222-8222-222222222222";
	const wakeTurn = "33333333-3333-4333-8333-333333333333";

	it("shows an update stored before a prompt at the start of that prompt's reply", () => {
		const prompt = message({ content: "first", role: "user", turnId: firstTurn });
		const answer = message({ content: "one", role: "assistant", turnId: firstTurn });
		const update = message({
			content: "job finished",
			injected: true,
			role: "user",
			turnId: secondTurn,
		});
		const next = message({ content: "second", role: "user", turnId: secondTurn });
		const reply = message({ content: "two", role: "assistant", turnId: secondTurn });

		const items = groupTranscript([prompt, answer, update, next, reply]);

		expect(items.map((item) => item.kind)).toEqual(["user", "reply", "user", "reply"]);
		expect(items[1]).toMatchObject({ blocks: [{ kind: "text", text: "one" }] });
		expect(items[3]).toMatchObject({
			blocks: [
				{ kind: "injected", text: "job finished" },
				{ kind: "text", text: "two" },
			],
		});
	});

	it("gives a turn an event started its own reply, opened by the handler's instructions", () => {
		const prompt = message({ content: "hi", role: "user", turnId: firstTurn });
		const answer = message({ content: "hello", role: "assistant", turnId: firstTurn });
		const events = message({
			content: "<session-events>",
			injected: true,
			role: "user",
			turnId: wakeTurn,
		});
		const instructions = message({
			content: "CI failed, fix it",
			injected: true,
			role: "user",
			turnId: wakeTurn,
		});
		const fix = message({ content: "fixed", role: "assistant", turnId: wakeTurn });

		const items = groupTranscript([prompt, answer, events, instructions, fix]);

		expect(items.map((item) => item.kind)).toEqual(["user", "reply", "reply"]);
		expect(items[2]).toMatchObject({
			blocks: [
				{ kind: "injected", text: "<session-events>" },
				{ kind: "injected", text: "CI failed, fix it" },
				{ kind: "text", text: "fixed" },
			],
		});
	});
});

describe("liveReplyBlocks", () => {
	const liveTurn: LiveTurn = {
		blocks: [{ key: "k", kind: "text", text: "working" }],
		isFinished: false,
		originEventType: undefined,
		prompt: "typed by a person",
		requestID: "r",
		sessionID: "s",
		slots: new Map(),
	};

	it("leaves a typed prompt out of the reply", () => {
		expect(liveReplyBlocks(liveTurn)).toEqual([
			{ key: "k", kind: "text", text: "working" },
		]);
	});

	it("opens an event-started reply with the handler's instructions", () => {
		const woken = { ...liveTurn, originEventType: "ci.build.failed", prompt: "fix CI" };

		expect(liveReplyBlocks(woken)).toMatchObject([
			{ kind: "injected", text: "fix CI" },
			{ kind: "text", text: "working" },
		]);
	});
});

describe("liveReplyBlock", () => {
	it("pretty-prints complete arguments and reports a running call", () => {
		expect(
			liveReplyBlock({
				input: '{"path":"run.sh"}',
				isError: false,
				key: "k",
				kind: "tool",
				name: "write_file",
				result: undefined,
				toolUseID: "call-write",
			}),
		).toEqual({
			arguments: '{\n  "path": "run.sh"\n}',
			key: "k",
			kind: "tool",
			name: "write_file",
			result: undefined,
			status: "running",
		});
	});

	it("shows partial arguments exactly as streamed", () => {
		expect(
			liveReplyBlock({
				input: '{"pa',
				isError: true,
				key: "k",
				kind: "tool",
				name: "write_file",
				result: "boom",
				toolUseID: "call-write",
			}),
		).toMatchObject({ arguments: '{"pa', status: "error" });
	});
});
