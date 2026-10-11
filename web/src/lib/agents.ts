import type { components } from "$lib/api/generated";
import { isTurnTerminalEvent } from "$lib/chat/stream";
import {
	AGENT_RUN_ID_SHORT_LENGTH,
	AGENT_RUN_STATE_RUNNING,
	EVENT_TYPE_QUESTION_ANSWERED,
	EVENT_TYPE_QUESTION_ASKED,
	SEND_TARGET_ROOT,
} from "$lib/common/constants";
import { isRecord } from "$lib/common/json";
import type { PeenSocketEvent } from "$lib/ws/socket";

type AgentRun = components["schemas"]["AgentRun"];

/** The child agents still running, which a person can message or stop. */
export function runningAgents(agents: AgentRun[]): AgentRun[] {
	return agents.filter((agent) => agent.state === AGENT_RUN_STATE_RUNNING);
}

/** A short name for a child: its agent name and the start of its run ID. */
export function agentLabel(agent: AgentRun): string {
	return `${agent.name} ${agent.agentRunId.slice(0, AGENT_RUN_ID_SHORT_LENGTH)}`;
}

/**
 * Keeps the composer's target while that child still runs, and falls back to
 * the root conversation once it ends.
 */
export function sendTarget(selected: string, running: AgentRun[]): string {
	if (selected === SEND_TARGET_ROOT) {
		return SEND_TARGET_ROOT;
	}

	return running.some((agent) => agent.agentRunId === selected)
		? selected
		: SEND_TARGET_ROOT;
}

/**
 * The question the root conversation asked the user and still waits on, read
 * from the live feed. An answer or the end of the turn closes it.
 */
export function openRootQuestion(events: PeenSocketEvent[]): string | undefined {
	let question: string | undefined;

	for (const event of events) {
		if (event.type === EVENT_TYPE_QUESTION_ASKED) {
			question = questionText(event.data);

			continue;
		}

		if (event.type === EVENT_TYPE_QUESTION_ANSWERED || isTurnTerminalEvent(event)) {
			question = undefined;
		}
	}

	return question;
}

function questionText(data: unknown): string | undefined {
	if (!isRecord(data) || typeof data.question !== "string") {
		return undefined;
	}

	return data.question;
}
