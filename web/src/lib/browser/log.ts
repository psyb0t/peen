import { isRecord } from "$lib/common/json";

// Every record is one console.debug call: this prefix, a space, then one JSON
// object. A record holds an allowlisted event name and allowlisted primitive
// metadata only. Tokens, message text, request and response bodies, WebSocket
// data, durable-record JSON, and error text never reach the console: the public
// type has no field for them, and at runtime anything that is not an
// allowlisted key holding a value of the expected shape is left out.
export const BROWSER_LOG_PREFIX = "peen.browser";

export const BROWSER_LOG_EVENTS = [
	"controller.refresh.complete",
	"controller.refresh.fail",
	"controller.refresh.start",
	"message.send.fail",
	"message.send.start",
	"session.cancel.complete",
	"session.cancel.fail",
	"session.cancel.start",
	"session.load.complete",
	"session.load.fail",
	"session.load.start",
	"session.reconfigure.complete",
	"session.reconfigure.fail",
	"session.reconfigure.start",
	"socket.close",
	"socket.connect.start",
	"socket.error",
	"socket.event.received",
	"socket.frame.rejected",
	"socket.message.sent",
	"socket.open",
	"ui.error",
	"workspace.open.complete",
	"workspace.open.fail",
	"workspace.open.start",
] as const;

export const BROWSER_LOG_OPERATIONS = [
	"controller.refresh",
	"message.send",
	"session.cancel",
	"session.load",
	"session.reconfigure",
	"socket",
	"workspace.open",
] as const;

export const BROWSER_SOCKET_STATES = ["closed", "connecting", "open"] as const;

export const BROWSER_FRAME_REJECT_REASONS = ["invalid_event", "non_text"] as const;

export type BrowserLogEvent = (typeof BROWSER_LOG_EVENTS)[number];
export type BrowserLogOperation = (typeof BROWSER_LOG_OPERATIONS)[number];
export type BrowserSocketState = (typeof BROWSER_SOCKET_STATES)[number];
export type BrowserFrameRejectReason = (typeof BROWSER_FRAME_REJECT_REASONS)[number];

// Keys are lowercase_snake so a browser record greps the same as the
// controller's own log fields.
export interface BrowserLogMetadata {
	count?: number;
	duration_ms?: number;
	event_id?: string;
	event_type?: string;
	model?: string;
	operation?: BrowserLogOperation;
	reason?: BrowserFrameRejectReason;
	request_id?: string;
	session_id?: string;
	socket_state?: BrowserSocketState;
	status?: number;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu;
const EVENT_TYPE_PATTERN = /^[a-z][a-z0-9._-]{0,63}$/u;
const MODEL_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$/u;

const MIN_HTTP_STATUS = 100;
const MAX_HTTP_STATUS = 599;

const allowedEvents: ReadonlySet<string> = new Set(BROWSER_LOG_EVENTS);
const allowedOperations: ReadonlySet<string> = new Set(BROWSER_LOG_OPERATIONS);
const allowedSocketStates: ReadonlySet<string> = new Set(BROWSER_SOCKET_STATES);
const allowedRejectReasons: ReadonlySet<string> = new Set(BROWSER_FRAME_REJECT_REASONS);

// logBrowserEvent writes one diagnostic record. An event name outside the
// allowlist writes nothing. A metadata value that is not an allowlisted key of
// the expected shape is left out of the record rather than rejecting it.
export function logBrowserEvent(
	event: BrowserLogEvent,
	metadata: BrowserLogMetadata = {},
): void {
	if (!allowedEvents.has(event)) {
		return;
	}

	const record = { event, ...safeMetadata(metadata) };

	console.debug(`${BROWSER_LOG_PREFIX} ${JSON.stringify(record)}`);
}

// startBrowserTimer returns a function reporting the milliseconds elapsed
// since the timer started.
export function startBrowserTimer(): () => number {
	const startedAt = performance.now();

	return () => performance.now() - startedAt;
}

function safeMetadata(metadata: unknown): BrowserLogMetadata {
	if (!isRecord(metadata)) {
		return {};
	}

	return {
		count: safeCount(metadata.count),
		duration_ms: safeDuration(metadata.duration_ms),
		event_id: matching(metadata.event_id, UUID_PATTERN),
		event_type: matching(metadata.event_type, EVENT_TYPE_PATTERN),
		model: matching(metadata.model, MODEL_PATTERN),
		operation: oneOf<BrowserLogOperation>(metadata.operation, allowedOperations),
		reason: oneOf<BrowserFrameRejectReason>(metadata.reason, allowedRejectReasons),
		request_id: matching(metadata.request_id, UUID_PATTERN),
		session_id: matching(metadata.session_id, UUID_PATTERN),
		socket_state: oneOf<BrowserSocketState>(metadata.socket_state, allowedSocketStates),
		status: safeStatus(metadata.status),
	};
}

function matching(value: unknown, pattern: RegExp): string | undefined {
	if (typeof value !== "string" || !pattern.test(value)) {
		return undefined;
	}

	return value;
}

function oneOf<T extends string>(
	value: unknown,
	allowed: ReadonlySet<string>,
): T | undefined {
	if (typeof value !== "string" || !allowed.has(value)) {
		return undefined;
	}

	return value as T;
}

function safeCount(value: unknown): number | undefined {
	if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
		return undefined;
	}

	return value;
}

function safeDuration(value: unknown): number | undefined {
	if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
		return undefined;
	}

	return Math.round(value);
}

function safeStatus(value: unknown): number | undefined {
	if (
		typeof value !== "number" ||
		!Number.isInteger(value) ||
		value < MIN_HTTP_STATUS ||
		value > MAX_HTTP_STATUS
	) {
		return undefined;
	}

	return value;
}
