import type { components } from "$lib/api/generated";
import type { LiveBlock, LiveTurn } from "$lib/chat/stream";

import { formatJSON } from "$lib/common/json";

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
	return { key: message.id, kind: "injected", text: message.content };
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

/**
 * Returns a streaming turn's reply blocks in the shape a stored reply uses.
 * A turn an event started opens with the handler's instructions, shown as an
 * update rather than as something a person typed.
 */
export function liveReplyBlocks(turn: LiveTurn): ReplyBlock[] {
	const blocks = turn.blocks.map(liveReplyBlock);
	if (turn.originEventType === undefined || turn.prompt === undefined) {
		return blocks;
	}

	return [
		{ key: LIVE_ORIGIN_PROMPT_KEY, kind: "injected", text: turn.prompt },
		...blocks,
	];
}

/** Converts a streaming block into the shape a stored reply uses. */
export function liveReplyBlock(block: LiveBlock): ReplyBlock {
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
