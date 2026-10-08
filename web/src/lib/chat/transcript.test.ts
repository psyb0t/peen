import { describe, expect, it } from "vitest";

import type { components } from "$lib/api/generated";

import { groupTranscript, liveReplyBlock } from "./transcript";

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
