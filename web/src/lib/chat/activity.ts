import { isStreamProtocolEvent } from "$lib/chat/stream";
import { EVENT_TYPE_NOTICE_CREATED } from "$lib/common/constants";
import type {
	AgentRunToolResult,
	AgentRunToolUse,
	HarnessWarning,
	HarnessWarningItem,
	MessageFailed,
	NoticeCreated,
	ServerEvent,
} from "$lib/ws/generated";

const EVENT_TYPE_TOOL_USE = "tool.use";
const EVENT_TYPE_TOOL_RESULT = "tool.result";
const EVENT_TYPE_TURN_STARTED = "turn.started";
const EVENT_TYPE_TURN_COMPLETED = "turn.completed";
const EVENT_TYPE_USER_MESSAGE_CREATED = "userMessage.created";
const EVENT_TYPE_USER_MESSAGE_QUEUED = "userMessage.queued";
const EVENT_TYPE_USER_MESSAGE_DELIVERED = "userMessage.delivered";
const EVENT_TYPE_MESSAGE_COMPLETED = "message.completed";
const EVENT_TYPE_SESSION_EVENTS = "session.events";
const EVENT_TYPE_SKILL_INJECTED = "skill.injected";
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
const EVENT_TYPES_SHOWN_ELSEWHERE = new Set<string>([
	EVENT_TYPE_TOOL_USE,
	EVENT_TYPE_TOOL_RESULT,
	EVENT_TYPE_TURN_STARTED,
	EVENT_TYPE_TURN_COMPLETED,
	EVENT_TYPE_USER_MESSAGE_CREATED,
	EVENT_TYPE_USER_MESSAGE_QUEUED,
	EVENT_TYPE_USER_MESSAGE_DELIVERED,
	EVENT_TYPE_MESSAGE_COMPLETED,
	EVENT_TYPE_SESSION_EVENTS,
	EVENT_TYPE_SKILL_INJECTED,
	EVENT_TYPE_AGENT_RUN_TEXT_DELTA,
	EVENT_TYPE_AGENT_RUN_THINKING_DELTA,
	EVENT_TYPE_AGENT_RUN_ASSISTANT_MESSAGE,
]);

/**
 * Reports whether an event belongs in the activity list. Stream protocol
 * events and events the chat already renders do not.
 */
export function isActivityEvent(event: ServerEvent): boolean {
	return !isStreamProtocolEvent(event) && !EVENT_TYPES_SHOWN_ELSEWHERE.has(event.type);
}

export function activities(events: ServerEvent[]): AgentActivity[] {
	return events.filter(isActivityEvent).map(activityForEvent);
}

function activityForEvent(event: ServerEvent): AgentActivity {
	switch (event.type) {
		case EVENT_TYPE_AGENT_RUN_TOOL_USE:
			return toolActivity(event, CHILD_AGENT_TITLE, "default");
		case EVENT_TYPE_AGENT_RUN_TOOL_RESULT:
			return toolActivity(
				event,
				CHILD_AGENT_TITLE,
				event.data.event.isError ? "error" : "success",
			);
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
		case EVENT_TYPE_NOTICE_CREATED:
			return noticeActivity(event);
		default:
			return simpleActivity(event, event.type, undefined, "default");
	}
}

function noticeActivity(event: NoticeCreated): AgentActivity {
	return {
		data: event.data,
		detail: event.data.summary,
		id: event.id,
		title: event.data.type,
		tone: "default",
	};
}

function messageFailureActivity(event: MessageFailed): AgentActivity {
	return {
		code: event.data.code,
		data: event.data,
		detail: event.data.message,
		id: event.id,
		reason: event.data.reason,
		title: "Peen could not start this turn",
		tone: "error",
	};
}

function harnessWarningActivity(event: HarnessWarning): AgentActivity {
	return {
		data: event.data,
		detail:
			"Peen ignored invalid optional workspace configuration and loaded the rest.",
		id: event.id,
		reason: harnessWarningDetails(event.data.warnings),
		title: "Workspace configuration warning",
		tone: "warning",
	};
}

function harnessWarningDetails(warnings: HarnessWarningItem[]): string {
	return warnings
		.map((warning) => `${warning.kind} at ${warning.source}: ${warning.reason}`)
		.join("\n");
}

function toolActivity(
	event: AgentRunToolResult | AgentRunToolUse,
	detail: string,
	tone: ActivityTone,
): AgentActivity {
	return {
		data: event.data.event,
		detail,
		id: event.id,
		title: event.data.event.name,
		tone,
	};
}

function simpleActivity(
	event: ServerEvent,
	title: string,
	detail: string | undefined,
	tone: ActivityTone,
): AgentActivity {
	return { data: event.data, detail, id: event.id, title, tone };
}
