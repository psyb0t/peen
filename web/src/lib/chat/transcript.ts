import type { components } from "$lib/api/generated";
import type { LiveBlock, LiveTurn, LiveUserBlock } from "$lib/chat/stream";

import {
	EVENT_HANDLER_AGENT_CLOSE,
	EVENT_HANDLER_AGENT_OPEN,
	EVENT_HANDLER_BODY_GAP,
	EVENT_HANDLER_CLOSE,
	EVENT_HANDLER_OPEN,
	EVENT_HANDLER_TYPE_CLOSE,
	INJECTED_KIND_SKILL,
	SESSION_EVENT_CLOSE,
	SESSION_EVENT_OPEN,
	SESSION_EVENT_UNREADABLE,
	SESSION_EVENTS_CLOSE,
	SESSION_EVENTS_DROPPED_LABEL,
	SESSION_EVENTS_DROPPED_LEAD,
	SESSION_EVENTS_OPEN,
	SESSION_EVENTS_TITLE,
	SKILL_FALLBACK_NAME,
	SKILL_MESSAGE_NAME_PATTERN,
	SKILL_NOTE_NAME_PATTERN,
} from "$lib/common/constants";
import { formatJSON, stringField } from "$lib/common/json";

type Message = components["schemas"]["Message"];

const ROLE_ASSISTANT = "assistant";
const ROLE_TOOL = "tool";
const ORPHAN_RESULT_NAME = "Tool result";

export type ToolStatus = "running" | "done" | "error" | "missing";

export interface ToolCallView {
	arguments: string;
	key: string;
	kind: "tool";
	name: string;
	result: string | undefined;
	status: ToolStatus;
}

export type ReplyBlock =
	| { key: string; kind: "text"; text: string }
	| { key: string; kind: "thinking"; text: string }
	| { key: string; kind: "injected"; text: string }
	| { key: string; kind: "skill"; name: string; text: string }
	| ToolCallView;

export interface ReplyItem {
	blocks: ReplyBlock[];
	createdAt: string;
	id: string;
	kind: "reply";
}

export type TranscriptItem = { kind: "user"; message: Message } | ReplyItem;

/**
 * Folds stored messages into what the chat shows: each typed prompt, then one
 * reply per run of assistant, tool, and injected rows.
 *
 * Messages are grouped by turn, so a new turn always starts a new reply.
 * Updates Peen stores ahead of a turn's prompt are shown at the start of that
 * prompt's reply, and a turn started by an event, which has no typed prompt,
 * opens its reply with the handler's instructions.
 *
 * A tool row is attached to the call whose id it answers, so a call and its
 * result render as one card. A call without a result is "missing", and a
 * result whose call is not in the loaded page still renders on its own.
 */
export function groupTranscript(messages: Message[]): TranscriptItem[] {
	const items: TranscriptItem[] = [];
	const callsByID = new Map<string, ToolCallView>();

	for (const turn of splitTurns(messages)) {
		groupTurn(turn, items, callsByID);
	}

	return items;
}

function splitTurns(messages: Message[]): Message[][] {
	const turns: Message[][] = [];
	let current: Message[] = [];
	let turnID: string | undefined;

	for (const message of messages) {
		if (current.length > 0 && message.turnId !== turnID) {
			turns.push(current);
			current = [];
		}
		turnID = message.turnId;
		current.push(message);
	}
	if (current.length > 0) {
		turns.push(current);
	}

	return turns;
}

function groupTurn(
	turn: Message[],
	items: TranscriptItem[],
	callsByID: Map<string, ToolCallView>,
): void {
	const promptIndex = turn.findIndex(isTypedPrompt);
	const leading = promptIndex === -1 ? [] : turn.slice(0, promptIndex);
	let reply: ReplyItem | undefined;

	const currentReply = (message: Message): ReplyItem => {
		if (reply === undefined) {
			reply = {
				blocks: [],
				createdAt: message.createdAt,
				id: message.id,
				kind: "reply",
			};
			items.push(reply);
		}

		return reply;
	};

	for (const [index, message] of turn.entries()) {
		if (index < promptIndex) {
			continue;
		}
		if (message.role === ROLE_ASSISTANT) {
			appendAssistant(currentReply(message), message, callsByID);
			continue;
		}
		if (message.role === ROLE_TOOL) {
			attachResult(currentReply(message), message, callsByID);
			continue;
		}
		if (message.injected) {
			currentReply(message).blocks.push(injectedBlock(message));
			continue;
		}

		reply = undefined;
		items.push({ kind: "user", message });
		if (index === promptIndex) {
			for (const update of leading) {
				attachLeading(currentReply(update), update, callsByID);
			}
		}
	}
}

function isTypedPrompt(message: Message): boolean {
	return (
		message.role !== ROLE_ASSISTANT &&
		message.role !== ROLE_TOOL &&
		message.injected !== true
	);
}

// Rows stored ahead of a prompt are normally injected updates. Anything else
// keeps its usual rendering so nothing in the transcript is dropped.
function attachLeading(
	reply: ReplyItem,
	message: Message,
	callsByID: Map<string, ToolCallView>,
): void {
	if (message.role === ROLE_ASSISTANT) {
		appendAssistant(reply, message, callsByID);

		return;
	}
	if (message.role === ROLE_TOOL) {
		attachResult(reply, message, callsByID);

		return;
	}
	reply.blocks.push(injectedBlock(message));
}

function injectedBlock(message: Message): ReplyBlock {
	if (message.injectedKind === INJECTED_KIND_SKILL) {
		return {
			key: message.id,
			kind: "skill",
			name: skillName(message.content),
			text: message.content,
		};
	}

	return { key: message.id, kind: "injected", text: injectedText(message.content) };
}

/**
 * Shows a handler wake message or a delivered events block as plain text.
 * Any other injected text is shown as stored.
 */
function injectedText(content: string): string {
	return handlerText(content) ?? eventsText(content) ?? content;
}

/**
 * Reads a handler wake message as the event type, then the instructions.
 * Returns undefined for text that is not a well-formed handler block.
 */
function handlerText(content: string): string | undefined {
	if (
		!content.startsWith(EVENT_HANDLER_OPEN) ||
		!content.endsWith(EVENT_HANDLER_CLOSE)
	) {
		return undefined;
	}

	const typeEnd = content.indexOf(EVENT_HANDLER_TYPE_CLOSE);
	if (typeEnd < 0) {
		return undefined;
	}

	const type = content.slice(EVENT_HANDLER_OPEN.length, typeEnd);
	let rest = content.slice(typeEnd + EVENT_HANDLER_TYPE_CLOSE.length);
	if (rest.startsWith(EVENT_HANDLER_AGENT_OPEN)) {
		const agentEnd = rest.indexOf(EVENT_HANDLER_AGENT_CLOSE);
		if (agentEnd < 0) {
			return undefined;
		}
		rest = rest.slice(agentEnd + EVENT_HANDLER_AGENT_CLOSE.length);
	}
	if (!rest.startsWith(EVENT_HANDLER_BODY_GAP)) {
		return undefined;
	}

	const instructions = rest.slice(
		EVENT_HANDLER_BODY_GAP.length,
		rest.length - EVENT_HANDLER_CLOSE.length,
	);

	return `Event handler for ${unescapeElement(type)}\n\n${instructions}`;
}

/**
 * Reads a delivered events block as a title, the count of dropped events when
 * there is one, then each event's type and summary on its own line. The
 * preamble written for the model is left out. Returns undefined for text that
 * is not an events block or holds no event and no dropped count.
 */
function eventsText(content: string): string | undefined {
	if (
		!content.startsWith(SESSION_EVENTS_OPEN) ||
		!content.endsWith(SESSION_EVENTS_CLOSE)
	) {
		return undefined;
	}

	const body = content.slice(
		SESSION_EVENTS_OPEN.length,
		content.length - SESSION_EVENTS_CLOSE.length,
	);
	const eventLines: string[] = [];
	let dropped: string | undefined;

	for (const line of body.split("\n")) {
		if (line.startsWith(SESSION_EVENTS_DROPPED_LEAD)) {
			dropped = line.slice(SESSION_EVENTS_DROPPED_LEAD.length);
			continue;
		}
		if (line.startsWith(SESSION_EVENT_OPEN) && line.endsWith(SESSION_EVENT_CLOSE)) {
			eventLines.push(
				eventSummaryLine(
					line.slice(
						SESSION_EVENT_OPEN.length,
						line.length - SESSION_EVENT_CLOSE.length,
					),
				),
			);
		}
	}
	if (dropped === undefined && eventLines.length === 0) {
		return undefined;
	}

	const parts = [SESSION_EVENTS_TITLE];
	if (dropped !== undefined) {
		parts.push(`${SESSION_EVENTS_DROPPED_LABEL}${dropped}`);
	}
	if (eventLines.length > 0) {
		parts.push(eventLines.join("\n"));
	}

	return parts.join("\n\n");
}

function eventSummaryLine(encoded: string): string {
	let parsed: unknown;
	try {
		parsed = JSON.parse(encoded);
	} catch {
		return SESSION_EVENT_UNREADABLE;
	}

	const type = stringField(parsed, "type");
	if (type === undefined) {
		return SESSION_EVENT_UNREADABLE;
	}
	const summary = stringField(parsed, "summary");

	return summary ? `${type}: ${summary}` : type;
}

function unescapeElement(value: string): string {
	return value.replaceAll("&lt;", "<").replaceAll("&gt;", ">").replaceAll("&amp;", "&");
}

/**
 * Reads the skill a stored skill row is about: the name inside a skill
 * message, or the name a note says is already loaded.
 */
function skillName(content: string): string {
	const named =
		SKILL_MESSAGE_NAME_PATTERN.exec(content) ?? SKILL_NOTE_NAME_PATTERN.exec(content);

	return named?.[1] ?? SKILL_FALLBACK_NAME;
}

function appendAssistant(
	reply: ReplyItem,
	message: Message,
	callsByID: Map<string, ToolCallView>,
): void {
	if (message.thinking) {
		reply.blocks.push({
			key: `${message.id}:thinking`,
			kind: "thinking",
			text: message.thinking,
		});
	}
	if (message.content !== "") {
		reply.blocks.push({
			key: `${message.id}:text`,
			kind: "text",
			text: message.content,
		});
	}

	for (const call of message.toolCalls ?? []) {
		const view: ToolCallView = {
			arguments: formatJSON(call.arguments),
			key: `${message.id}:${call.id}`,
			kind: "tool",
			name: call.name,
			result: undefined,
			status: "missing",
		};
		callsByID.set(call.id, view);
		reply.blocks.push(view);
	}
}

function attachResult(
	reply: ReplyItem,
	message: Message,
	callsByID: Map<string, ToolCallView>,
): void {
	const status: ToolStatus = message.isError ? "error" : "done";
	const call = message.toolCallId ? callsByID.get(message.toolCallId) : undefined;
	if (call !== undefined) {
		call.result = message.content;
		call.status = status;

		return;
	}

	reply.blocks.push({
		arguments: "",
		key: message.id,
		kind: "tool",
		name: ORPHAN_RESULT_NAME,
		result: message.content,
		status,
	});
}

const LIVE_ORIGIN_PROMPT_KEY = "origin-prompt";

export type LiveSegment =
	{ blocks: ReplyBlock[]; key: string; kind: "reply" } | LiveUserBlock;

/**
 * Splits a streaming turn into what the chat shows, in order: runs of reply
 * blocks, broken by each message the user queued that reached the model
 * mid-turn. A turn an event started opens with the handler's instructions,
 * shown as an update rather than as something a person typed.
 */
export function liveSegments(turn: LiveTurn): LiveSegment[] {
	const segments: LiveSegment[] = [];
	let reply: ReplyBlock[] = [];

	if (turn.originEventType !== undefined && turn.prompt !== undefined) {
		reply.push({
			key: LIVE_ORIGIN_PROMPT_KEY,
			kind: "injected",
			text: injectedText(turn.prompt),
		});
	}

	const closeReply = (): void => {
		if (reply.length === 0) {
			return;
		}

		segments.push({
			blocks: reply,
			key: `${turn.requestID}:${segments.length}`,
			kind: "reply",
		});
		reply = [];
	};

	for (const block of turn.blocks) {
		if (block.kind === "user") {
			closeReply();
			segments.push(block);
			continue;
		}

		reply.push(liveReplyBlock(block));
	}

	closeReply();

	return segments;
}

/** Converts a streaming block into the shape a stored reply uses. */
export function liveReplyBlock(block: Exclude<LiveBlock, LiveUserBlock>): ReplyBlock {
	if (block.kind !== "tool") {
		return block;
	}

	return {
		arguments: prettyArguments(block.input),
		key: block.key,
		kind: "tool",
		name: block.name,
		result: block.result,
		status: liveToolStatus(block.result, block.isError),
	};
}

function liveToolStatus(result: string | undefined, isError: boolean): ToolStatus {
	if (result === undefined) {
		return "running";
	}

	return isError ? "error" : "done";
}

function prettyArguments(raw: string): string {
	try {
		return formatJSON(JSON.parse(raw));
	} catch {
		// Arguments that are not complete JSON yet are shown exactly as sent.
		return raw;
	}
}
