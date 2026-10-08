import type { PeenSocketEvent } from "$lib/ws/socket";

import { isRecord, stringField } from "$lib/common/json";

const EVENT_TYPE_USER_MESSAGE_CREATED = "user_message.created";
const EVENT_TYPE_TURN_STARTED = "turn.started";
const EVENT_TYPE_CONTENT_BLOCK_START = "content_block_start";
const EVENT_TYPE_CONTENT_BLOCK_DELTA = "content_block_delta";
const EVENT_TYPE_CONTENT_BLOCK_STOP = "content_block_stop";
const EVENT_TYPE_MESSAGE_START = "message_start";
const EVENT_TYPE_MESSAGE_DELTA = "message_delta";
const EVENT_TYPE_MESSAGE_STOP = "message_stop";
const EVENT_TYPE_PING = "ping";
const EVENT_TYPE_TURN_COMPLETED = "turn.completed";
const EVENT_TYPE_TURN_FAILED = "turn.failed";
const EVENT_TYPE_TURN_CANCELLED = "turn.cancelled";

const BLOCK_TYPE_TEXT = "text";
const BLOCK_TYPE_THINKING = "thinking";
const BLOCK_TYPE_TOOL_USE = "tool_use";
const BLOCK_TYPE_TOOL_RESULT = "tool_result";

const DELTA_TYPE_TEXT = "text_delta";
const DELTA_TYPE_THINKING = "thinking_delta";
const DELTA_TYPE_INPUT_JSON = "input_json_delta";
const DELTA_TYPE_JSON_PARTIAL = "json_partial";

const TURN_TERMINAL_EVENT_TYPES = new Set([
	EVENT_TYPE_TURN_COMPLETED,
	EVENT_TYPE_TURN_FAILED,
	EVENT_TYPE_TURN_CANCELLED,
]);

// Protocol events that only build the live reply. They carry no meaning on
// their own, so the activity list must never render them as rows.
const STREAM_PROTOCOL_EVENT_TYPES = new Set([
	EVENT_TYPE_CONTENT_BLOCK_START,
	EVENT_TYPE_CONTENT_BLOCK_DELTA,
	EVENT_TYPE_CONTENT_BLOCK_STOP,
	EVENT_TYPE_MESSAGE_START,
	EVENT_TYPE_MESSAGE_DELTA,
	EVENT_TYPE_MESSAGE_STOP,
	EVENT_TYPE_PING,
]);

export interface LiveProseBlock {
	key: string;
	kind: "text" | "thinking";
	text: string;
}

export interface LiveToolBlock {
	input: string;
	isError: boolean;
	key: string;
	kind: "tool";
	name: string;
	result: string | undefined;
	toolUseID: string;
}

export type LiveBlock = LiveProseBlock | LiveToolBlock;

/**
 * One turn's reply as it streams, rebuilt from essessey content blocks.
 *
 * Block indexes are only unique within a turn, so a turn is identified by the
 * request ID its events carry. `slots` maps a wire index to the block it
 * feeds; a tool_result index maps to the tool_use block it answers.
 */
export interface LiveTurn {
	blocks: LiveBlock[];
	isFinished: boolean;
	prompt: string | undefined;
	requestID: string;
	sessionID: string;
	slots: ReadonlyMap<number, string>;
}

export function isStreamProtocolEvent(event: PeenSocketEvent): boolean {
	return STREAM_PROTOCOL_EVENT_TYPES.has(event.type);
}

export function isTurnTerminalEvent(event: PeenSocketEvent): boolean {
	return TURN_TERMINAL_EVENT_TYPES.has(event.type);
}

/**
 * Folds one socket event into the live turns and returns the next list.
 *
 * Events without a session and request ID, and block payloads that do not
 * match the essessey shapes, leave the list unchanged. The input list is never
 * mutated.
 */
export function applyStreamEvent(
	turns: LiveTurn[],
	event: PeenSocketEvent,
): LiveTurn[] {
	const sessionID = event.metadata.sessionId;
	const requestID = event.metadata.requestId;
	if (sessionID === undefined || requestID === undefined) {
		return turns;
	}

	const current = turns.find((turn) => turn.requestID === requestID);
	const next = nextTurn(current, sessionID, requestID, event);
	if (next === current) {
		return turns;
	}
	if (current === undefined) {
		return next === undefined ? turns : [...turns, next];
	}

	return turns.map((turn) => (turn === current ? (next ?? turn) : turn));
}

function nextTurn(
	current: LiveTurn | undefined,
	sessionID: string,
	requestID: string,
	event: PeenSocketEvent,
): LiveTurn | undefined {
	switch (event.type) {
		case EVENT_TYPE_USER_MESSAGE_CREATED:
			return {
				...(current ?? emptyTurn(sessionID, requestID)),
				prompt: stringField(event.data, "message"),
			};
		case EVENT_TYPE_TURN_STARTED:
			return current ?? emptyTurn(sessionID, requestID);
		case EVENT_TYPE_CONTENT_BLOCK_START:
			return startBlock(current ?? emptyTurn(sessionID, requestID), event.data);
		case EVENT_TYPE_CONTENT_BLOCK_DELTA:
			return current === undefined ? undefined : applyDelta(current, event.data);
		default:
			if (current !== undefined && isTurnTerminalEvent(event)) {
				return { ...current, isFinished: true };
			}

			return current;
	}
}

function emptyTurn(sessionID: string, requestID: string): LiveTurn {
	return {
		blocks: [],
		isFinished: false,
		prompt: undefined,
		requestID,
		sessionID,
		slots: new Map(),
	};
}

function startBlock(turn: LiveTurn, data: unknown): LiveTurn {
	const index = blockIndex(data);
	const block = isRecord(data) ? data.content_block : undefined;
	if (index === undefined || !isRecord(block)) {
		return turn;
	}

	const key = `${turn.requestID}:${index}`;
	switch (block.type) {
		case BLOCK_TYPE_TEXT:
			return addBlock(turn, index, { key, kind: BLOCK_TYPE_TEXT, text: "" });
		case BLOCK_TYPE_THINKING:
			return addBlock(turn, index, { key, kind: BLOCK_TYPE_THINKING, text: "" });
		case BLOCK_TYPE_TOOL_USE:
			return addBlock(turn, index, {
				input: "",
				isError: false,
				key,
				kind: "tool",
				name: stringField(block, "name") ?? "Tool",
				result: undefined,
				toolUseID: stringField(block, "id") ?? key,
			});
		case BLOCK_TYPE_TOOL_RESULT:
			return attachToolResult(turn, index, block);
		default:
			return turn;
	}
}

function addBlock(turn: LiveTurn, index: number, block: LiveBlock): LiveTurn {
	return {
		...turn,
		blocks: [...turn.blocks, block],
		slots: new Map(turn.slots).set(index, block.key),
	};
}

function attachToolResult(
	turn: LiveTurn,
	index: number,
	block: Record<string, unknown>,
): LiveTurn {
	const toolUseID = stringField(block, "tool_use_id");
	const tool = turn.blocks.find(
		(candidate) => candidate.kind === "tool" && candidate.toolUseID === toolUseID,
	);
	if (tool === undefined) {
		return turn;
	}

	return {
		...turn,
		blocks: turn.blocks.map((candidate) =>
			candidate === tool
				? { ...candidate, isError: block.is_error === true, result: "" }
				: candidate,
		),
		slots: new Map(turn.slots).set(index, tool.key),
	};
}

function applyDelta(turn: LiveTurn, data: unknown): LiveTurn {
	const index = blockIndex(data);
	const delta = isRecord(data) ? data.delta : undefined;
	if (index === undefined || !isRecord(delta)) {
		return turn;
	}

	const key = turn.slots.get(index);
	if (key === undefined) {
		return turn;
	}

	return {
		...turn,
		blocks: turn.blocks.map((block) =>
			block.key === key ? appendDelta(block, delta) : block,
		),
	};
}

function appendDelta(block: LiveBlock, delta: Record<string, unknown>): LiveBlock {
	switch (delta.type) {
		case DELTA_TYPE_TEXT:
			return appendProse(block, BLOCK_TYPE_TEXT, stringField(delta, "text"));
		case DELTA_TYPE_THINKING:
			return appendProse(block, BLOCK_TYPE_THINKING, stringField(delta, "text"));
		case DELTA_TYPE_INPUT_JSON:
			if (block.kind !== "tool") {
				return block;
			}

			return {
				...block,
				input: block.input + (stringField(delta, "partial_json") ?? ""),
			};
		case DELTA_TYPE_JSON_PARTIAL:
			if (block.kind !== "tool") {
				return block;
			}

			return {
				...block,
				result: (block.result ?? "") + (stringField(delta, "text") ?? ""),
			};
		default:
			return block;
	}
}

function appendProse(
	block: LiveBlock,
	kind: LiveProseBlock["kind"],
	text: string | undefined,
): LiveBlock {
	if (block.kind !== kind || text === undefined || text === "") {
		return block;
	}

	return { ...block, text: block.text + text };
}

function blockIndex(data: unknown): number | undefined {
	if (!isRecord(data) || !Number.isInteger(data.index)) {
		return undefined;
	}

	return data.index as number;
}
