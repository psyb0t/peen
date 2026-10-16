import { describe, expect, it } from "vitest";

import type { components } from "./api/generated";
import { agentLabel, openRootQuestion, runningAgents, sendTarget } from "./agents";
import { SEND_TARGET_ROOT } from "./common/constants";
import type { ServerEvent } from "./ws/generated";

type AgentRun = components["schemas"]["AgentRun"];

function agentRun(overrides: Partial<AgentRun>): AgentRun {
	return {
		agentRunId: "11111111-2222-4333-8444-555555555555",
		allowedTools: [],
		cancelRequested: false,
		completionTokenCount: 0,
		definition: "stored",
		depth: 1,
		eventCount: 0,
		failureClassification: "",
		failureDetail: "",
		finishReason: "",
		instructions: "",
		model: "model",
		modelReference: "provider/model",
		name: "reviewer",
		parentTurnId: "66666666-7777-4888-8999-000000000000",
		promptTokenCount: 0,
		requestId: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		responseMessages: [],
		responseText: "",
		responseThinking: "",
		sessionId: "99999999-8888-4777-8666-555555555555",
		startedAt: "2026-10-11T00:00:00Z",
		state: "running",
		systemPrompt: "",
		task: "review",
		waiting: false,
		workspace: "/workspace",
		...overrides,
	};
}

// A pragmatic cast: these fixtures build the exact shape each type's
// generated schema requires, but the helper itself stays untyped so every
// call site can hand it whatever `data` its scenario needs.
function socketEvent(type: string, data: unknown): ServerEvent {
	return {
		data,
		id: crypto.randomUUID(),
		metadata: { requestId: "request", sessionId: "session" },
		timestamp: 1,
		triggeredBy: null,
		type,
	} as ServerEvent;
}

describe("runningAgents", () => {
	it("keeps only the children still running", () => {
		const running = agentRun({ agentRunId: "running-run" });
		const done = agentRun({ agentRunId: "done-run", state: "completed" });

		expect(runningAgents([running, done])).toEqual([running]);
		expect(runningAgents([])).toEqual([]);
	});
});

describe("agentLabel", () => {
	it("names the agent and the start of its run", () => {
		expect(agentLabel(agentRun({}))).toBe("reviewer 11111111");
	});
});

describe("sendTarget", () => {
	it.each([
		{
			name: "keeps a child that still runs",
			selected: "11111111-2222-4333-8444-555555555555",
			running: [agentRun({})],
			want: "11111111-2222-4333-8444-555555555555",
		},
		{
			name: "falls back to the root once the child ends",
			selected: "gone",
			running: [agentRun({})],
			want: SEND_TARGET_ROOT,
		},
		{
			name: "keeps the root",
			selected: SEND_TARGET_ROOT,
			running: [],
			want: SEND_TARGET_ROOT,
		},
	])("$name", ({ selected, running, want }) => {
		expect(sendTarget(selected, running)).toBe(want);
	});
});

describe("openRootQuestion", () => {
	it.each([
		{
			name: "shows a question nobody answered yet",
			events: [socketEvent("question.asked", { question: "Which branch?" })],
			want: "Which branch?",
		},
		{
			name: "closes the question once it is answered",
			events: [
				socketEvent("question.asked", { question: "Which branch?" }),
				socketEvent("question.answered", { answered: true }),
			],
			want: undefined,
		},
		{
			name: "closes the question when the turn ends",
			events: [
				socketEvent("question.asked", { question: "Which branch?" }),
				socketEvent("turn.cancelled", { reason: "cancelled" }),
			],
			want: undefined,
		},
		{
			name: "shows the latest of several questions",
			events: [
				socketEvent("question.asked", { question: "First?" }),
				socketEvent("question.answered", { answered: true }),
				socketEvent("question.asked", { question: "Second?" }),
			],
			want: "Second?",
		},
		{ name: "has nothing to show without events", events: [], want: undefined },
	])("$name", ({ events, want }) => {
		expect(openRootQuestion(events)).toBe(want);
	});
});
