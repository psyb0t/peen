import type { components } from "$lib/api/generated";
import type { LiveBlock } from "$lib/chat/stream";

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
 * A tool row is attached to the call whose id it answers, so a call and its
 * result render as one card. A call without a result is "missing", and a
 * result whose call is not in the loaded page still renders on its own.
 */
export function groupTranscript(messages: Message[]): TranscriptItem[] {
	const items: TranscriptItem[] = [];
	const callsByID = new Map<string, ToolCallView>();
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

	for (const message of messages) {
		if (message.role === ROLE_ASSISTANT) {
			appendAssistant(currentReply(message), message, callsByID);
			continue;
		}
		if (message.role === ROLE_TOOL) {
			attachResult(currentReply(message), message, callsByID);
			continue;
		}
		if (message.injected) {
			currentReply(message).blocks.push({
				key: message.id,
				kind: "injected",
				text: message.content,
			});
			continue;
		}

		reply = undefined;
		items.push({ kind: "user", message });
	}

	return items;
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
