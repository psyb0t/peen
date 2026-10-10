import type { PeenSocketEvent } from "$lib/ws/socket";

import { isStreamProtocolEvent } from "$lib/chat/stream";
import { isRecord, stringField } from "$lib/common/json";

const EVENT_TYPE_TOOL_USE = "tool.use";
const EVENT_TYPE_TOOL_RESULT = "tool.result";
const EVENT_TYPE_TURN_STARTED = "turn.started";
const EVENT_TYPE_TURN_COMPLETED = "turn.completed";
const EVENT_TYPE_USER_MESSAGE_CREATED = "user_message.created";
const EVENT_TYPE_USER_MESSAGE_QUEUED = "user_message.queued";
const EVENT_TYPE_USER_MESSAGE_DELIVERED = "user_message.delivered";
const EVENT_TYPE_MESSAGE_COMPLETED = "message.completed";
const EVENT_TYPE_SESSION_EVENTS = "session.events";
const EVENT_TYPE_AGENT_RUN_TEXT_DELTA = "agent.run.text.delta";
const EVENT_TYPE_AGENT_RUN_THINKING_DELTA = "agent.run.thinking.delta";
const EVENT_TYPE_AGENT_RUN_ASSISTANT_MESSAGE = "agent.run.assistant.message";
const EVENT_TYPE_TURN_FAILED = "turn.failed";
const EVENT_TYPE_TURN_CANCELLED = "turn.cancelled";
const EVENT_TYPE_MESSAGE_FAILED = "message.failed";
const EVENT_TYPE_HARNESS_WARNING = "harness.warning";
const EVENT_TYPE_PROVIDER_RETRY = "provider.retry";
const EVENT_TYPE_AGENT_RUN_STARTED = "agent.run.started";
const EVENT_TYPE_AGENT_RUN_COMPLETED = "agent.run.completed";
const EVENT_TYPE_AGENT_RUN_FAILED = "agent.run.failed";
const EVENT_TYPE_AGENT_RUN_CANCELLED = "agent.run.cancelled";
const EVENT_TYPE_AGENT_RUN_TOOL_USE = "agent.run.tool.use";
const EVENT_TYPE_AGENT_RUN_TOOL_RESULT = "agent.run.tool.result";

const RUNNING_DETAIL = "Running";
const COMPLETE_DETAIL = "Complete";
const FAILED_DETAIL = "Failed";
const CANCELLED_DETAIL = "Cancelled";
const RETRYING_DETAIL = "Retrying the provider";
const CHILD_AGENT_TITLE = "Child agent";
const AGENT_FINISHED_TITLE = "Agent finished";

export type ActivityTone = "default" | "error" | "success" | "warning";

export interface AgentActivity {
	code?: string;
	id: string;
	title: string;
	detail?: string;
	reason?: string;
	data?: unknown;
	tone: ActivityTone;
}

// Events the live reply or the persisted messages already show. Listing them
// as activity rows would repeat the chat.
const EVENT_TYPES_SHOWN_ELSEWHERE = new Set([
	EVENT_TYPE_TOOL_USE,
	EVENT_TYPE_TOOL_RESULT,
	EVENT_TYPE_TURN_STARTED,
	EVENT_TYPE_TURN_COMPLETED,
	EVENT_TYPE_USER_MESSAGE_CREATED,
	EVENT_TYPE_USER_MESSAGE_QUEUED,
	EVENT_TYPE_USER_MESSAGE_DELIVERED,
	EVENT_TYPE_MESSAGE_COMPLETED,
	EVENT_TYPE_SESSION_EVENTS,
	EVENT_TYPE_AGENT_RUN_TEXT_DELTA,
	EVENT_TYPE_AGENT_RUN_THINKING_DELTA,
	EVENT_TYPE_AGENT_RUN_ASSISTANT_MESSAGE,
]);

/**
 * Reports whether an event belongs in the activity list. Stream protocol
 * events and events the chat already renders do not.
 */
export function isActivityEvent(event: PeenSocketEvent): boolean {
	return !isStreamProtocolEvent(event) && !EVENT_TYPES_SHOWN_ELSEWHERE.has(event.type);
}

export function activities(events: PeenSocketEvent[]): AgentActivity[] {
	return events.filter(isActivityEvent).map(activityForEvent);
}

function activityForEvent(event: PeenSocketEvent): AgentActivity {
	switch (event.type) {
		case EVENT_TYPE_AGENT_RUN_TOOL_USE:
			return toolActivity(event, CHILD_AGENT_TITLE, "default");
		case EVENT_TYPE_AGENT_RUN_TOOL_RESULT:
			return toolActivity(event, CHILD_AGENT_TITLE, toolResultTone(event.data));
		case EVENT_TYPE_TURN_FAILED:
			return simpleActivity(event, AGENT_FINISHED_TITLE, FAILED_DETAIL, "error");
		case EVENT_TYPE_TURN_CANCELLED:
			return simpleActivity(event, AGENT_FINISHED_TITLE, CANCELLED_DETAIL, "warning");
		case EVENT_TYPE_MESSAGE_FAILED:
			return messageFailureActivity(event);
		case EVENT_TYPE_HARNESS_WARNING:
			return harnessWarningActivity(event);
		case EVENT_TYPE_PROVIDER_RETRY:
			return simpleActivity(event, "Provider retry", RETRYING_DETAIL, "warning");
		case EVENT_TYPE_AGENT_RUN_STARTED:
			return simpleActivity(event, CHILD_AGENT_TITLE, RUNNING_DETAIL, "default");
		case EVENT_TYPE_AGENT_RUN_COMPLETED:
			return simpleActivity(event, CHILD_AGENT_TITLE, COMPLETE_DETAIL, "success");
		case EVENT_TYPE_AGENT_RUN_FAILED:
			return simpleActivity(event, CHILD_AGENT_TITLE, FAILED_DETAIL, "error");
		case EVENT_TYPE_AGENT_RUN_CANCELLED:
			return simpleActivity(event, CHILD_AGENT_TITLE, CANCELLED_DETAIL, "warning");
		default:
			return simpleActivity(event, event.type, undefined, "default");
	}
}

function messageFailureActivity(event: PeenSocketEvent): AgentActivity {
	return {
		code: stringField(event.data, "code"),
		data: event.data,
		detail: stringField(event.data, "message") ?? "Peen could not start this turn.",
		id: event.id,
		reason: stringField(event.data, "reason"),
		title: "Peen could not start this turn",
		tone: "error",
	};
}

function harnessWarningActivity(event: PeenSocketEvent): AgentActivity {
	const warnings = harnessWarningDetails(event.data);

	return {
		data: event.data,
		detail:
			"Peen ignored invalid optional workspace configuration and loaded the rest.",
		id: event.id,
		reason: warnings.join("\n"),
		title: "Workspace configuration warning",
		tone: "warning",
	};
}

function harnessWarningDetails(data: unknown): string[] {
	if (!isRecord(data) || !Array.isArray(data.warnings)) {
		return ["Peen did not receive details for the ignored configuration."];
	}

	const details: string[] = [];
	for (const warning of data.warnings) {
		if (!isRecord(warning)) {
			continue;
		}

		const kind = stringField(warning, "kind") ?? "configuration";
		const source = stringField(warning, "source") ?? "an unknown source";
		const reason = stringField(warning, "reason") ?? "is invalid";
		details.push(`${kind} at ${source}: ${reason}`);
	}

	return details.length > 0
		? details
		: ["Peen did not receive details for the ignored configuration."];
}

function toolActivity(
	event: PeenSocketEvent,
	detail: string,
	tone: ActivityTone,
): AgentActivity {
	const name = stringField(event.data, "name") ?? "Tool";

	return {
		data: event.data,
		detail,
		id: event.id,
		title: name,
		tone,
	};
}

function simpleActivity(
	event: PeenSocketEvent,
	title: string,
	detail: string | undefined,
	tone: ActivityTone,
): AgentActivity {
	return { data: event.data, detail, id: event.id, title, tone };
}

function toolResultTone(data: unknown): ActivityTone {
	return isError(data) ? "error" : "success";
}

function isError(data: unknown): boolean {
	return isRecord(data) && data.isError === true;
}
