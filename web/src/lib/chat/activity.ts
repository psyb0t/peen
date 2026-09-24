import type { PeenSocketEvent } from "$lib/ws/socket";

import { isRecord } from "$lib/common/json";

const EVENT_TYPE_TEXT_DELTA = "text.delta";
const EVENT_TYPE_THINKING_DELTA = "thinking.delta";
const EVENT_TYPE_TOOL_USE = "tool.use";
const EVENT_TYPE_TOOL_RESULT = "tool.result";
const EVENT_TYPE_TURN_STARTED = "turn.started";
const EVENT_TYPE_TURN_COMPLETED = "turn.completed";
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
const AGENT_STARTED_TITLE = "Agent started";
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

export function liveText(events: PeenSocketEvent[]): string {
	return events
		.filter((event) => event.type === EVENT_TYPE_TEXT_DELTA)
		.map((event) => textField(event.data))
		.join("");
}

export function liveThinking(events: PeenSocketEvent[]): string {
	return events
		.filter((event) => event.type === EVENT_TYPE_THINKING_DELTA)
		.map((event) => textField(event.data))
		.join("");
}

export function activities(events: PeenSocketEvent[]): AgentActivity[] {
	return events.flatMap((event) => {
		const activity = activityForEvent(event);

		return activity === undefined ? [] : [activity];
	});
}

function activityForEvent(event: PeenSocketEvent): AgentActivity | undefined {
	switch (event.type) {
		case EVENT_TYPE_TEXT_DELTA:
		case EVENT_TYPE_THINKING_DELTA:
			return undefined;
		case EVENT_TYPE_TOOL_USE:
			return toolActivity(event, RUNNING_DETAIL, "default");
		case EVENT_TYPE_TOOL_RESULT:
			return toolActivity(
				event,
				toolResultDetail(event.data),
				toolResultTone(event.data),
			);
		case EVENT_TYPE_AGENT_RUN_TOOL_USE:
			return toolActivity(event, CHILD_AGENT_TITLE, "default");
		case EVENT_TYPE_AGENT_RUN_TOOL_RESULT:
			return toolActivity(event, CHILD_AGENT_TITLE, toolResultTone(event.data));
		case EVENT_TYPE_TURN_STARTED:
			return simpleActivity(event, AGENT_STARTED_TITLE, RUNNING_DETAIL, "default");
		case EVENT_TYPE_TURN_COMPLETED:
			return simpleActivity(event, AGENT_FINISHED_TITLE, COMPLETE_DETAIL, "success");
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

function toolResultDetail(data: unknown): string {
	return isError(data) ? FAILED_DETAIL : COMPLETE_DETAIL;
}

function toolResultTone(data: unknown): ActivityTone {
	return isError(data) ? "error" : "success";
}

function isError(data: unknown): boolean {
	return isRecord(data) && data.isError === true;
}

function textField(data: unknown): string {
	return stringField(data, "text") ?? "";
}

function stringField(data: unknown, name: string): string | undefined {
	if (!isRecord(data)) {
		return undefined;
	}

	const value = data[name];

	return typeof value === "string" ? value : undefined;
}
