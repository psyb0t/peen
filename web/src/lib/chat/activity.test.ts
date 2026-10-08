import { describe, expect, it } from "vitest";

import { activities } from "./activity";

const sessionID = "22222222-2222-4222-8222-222222222222";

function event(id: string, type: string, data: unknown) {
	return {
		data,
		id,
		metadata: { sessionId: sessionID },
		timestamp: 1,
		triggeredBy: null,
		type,
	};
}

describe("agent activity", () => {
	it.each([
		["content_block_start", { content_block: { type: "text" }, index: 0 }],
		["content_block_delta", { delta: { text: "hi", type: "text_delta" }, index: 0 }],
		["content_block_stop", { index: 0 }],
		["message_start", {}],
		["message_delta", {}],
		["message_stop", {}],
		["ping", {}],
		["tool.use", { callId: "call-1", name: "read_file" }],
		["tool.result", { callId: "call-1", isError: false, name: "read_file" }],
		["turn.started", {}],
		["turn.completed", { text: "done" }],
		["user_message.created", { message: "hello" }],
		["message.completed", { queued: false }],
		["session.events", { notices: [] }],
		["agent.run.text.delta", { text: "child text" }],
		["agent.run.thinking.delta", { text: "child thought" }],
		["agent.run.assistant.message", { content: "child reply" }],
	])("keeps %s out of the activity list because the chat shows it", (type, data) => {
		expect(activities([event("shown-elsewhere", type, data)])).toEqual([]);
	});

	it("marks a failed turn without leaking a provider error", () => {
		const events = [
			event("turn-failure", "turn.failed", { reason: "private provider detail" }),
		];

		expect(activities(events)).toEqual([
			{
				data: events[0]?.data,
				detail: "Failed",
				id: "turn-failure",
				title: "Agent finished",
				tone: "error",
			},
		]);
	});

	it("renders a websocket submission failure as an actionable chat activity", () => {
		const events = [
			event("submission-failure", "message.failed", {
				code: "HARNESS_CONFIGURATION_INVALID",
				message:
					"Peen could not start this turn because the workspace agent configuration is invalid.",
				reason:
					"Each directory in .agents/skills must contain a SKILL.md file whose name matches the directory.",
			}),
		];

		expect(activities(events)).toEqual([
			{
				code: "HARNESS_CONFIGURATION_INVALID",
				data: events[0]?.data,
				detail:
					"Peen could not start this turn because the workspace agent configuration is invalid.",
				id: "submission-failure",
				reason:
					"Each directory in .agents/skills must contain a SKILL.md file whose name matches the directory.",
				title: "Peen could not start this turn",
				tone: "error",
			},
		]);
	});

	it("shows ignored harness sources as a visible warning", () => {
		const events = [
			event("harness-warning", "harness.warning", {
				warnings: [
					{
						kind: "skill",
						reason: "skill directory has no SKILL.md",
						source: "/workspace/.agents/skills/broken-skill",
					},
				],
			}),
		];

		expect(activities(events)).toEqual([
			{
				data: events[0]?.data,
				detail:
					"Peen ignored invalid optional workspace configuration and loaded the rest.",
				id: "harness-warning",
				reason:
					"skill at /workspace/.agents/skills/broken-skill: skill directory has no SKILL.md",
				title: "Workspace configuration warning",
				tone: "warning",
			},
		]);
	});
});
