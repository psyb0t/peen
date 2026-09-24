import { describe, expect, it } from "vitest";

import { activities, liveText, liveThinking } from "./activity";

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
	it("keeps streamed text and thinking in their own ordered chat regions", () => {
		const events = [
			event("text-one", "text.delta", { text: "first " }),
			event("thinking-one", "thinking.delta", { text: "inspect " }),
			event("text-two", "text.delta", { text: "answer" }),
			event("thinking-two", "thinking.delta", { text: "files" }),
		];

		expect(liveText(events)).toBe("first answer");
		expect(liveThinking(events)).toBe("inspect files");
		expect(activities(events)).toEqual([]);
	});

	it("renders paired tool activity with details without treating tool output as text", () => {
		const events = [
			event("tool-start", "tool.use", {
				arguments: { path: "README.md" },
				callId: "call-1",
				name: "read_file",
			}),
			event("tool-result", "tool.result", {
				callId: "call-1",
				content: "file content",
				isError: false,
				name: "read_file",
			}),
		];

		expect(activities(events)).toEqual([
			{
				data: events[0]?.data,
				detail: "Running",
				id: "tool-start",
				title: "read_file",
				tone: "default",
			},
			{
				data: events[1]?.data,
				detail: "Complete",
				id: "tool-result",
				title: "read_file",
				tone: "success",
			},
		]);
	});

	it("marks failed turns and failed tool results without leaking a provider error", () => {
		const events = [
			event("tool-failure", "tool.result", {
				content: "private tool output",
				isError: true,
				name: "apply_patch",
			}),
			event("turn-failure", "turn.failed", { reason: "private provider detail" }),
		];

		expect(activities(events)).toEqual([
			{
				data: events[0]?.data,
				detail: "Failed",
				id: "tool-failure",
				title: "apply_patch",
				tone: "error",
			},
			{
				data: events[1]?.data,
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
