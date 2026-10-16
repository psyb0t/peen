import { logBrowserEvent } from "$lib/browser/log";
import {
	RECONNECT_BASE_DELAY_MS,
	RECONNECT_JITTER_RATIO,
	RECONNECT_MAX_DELAY_MS,
	WEBSOCKET_BEARER_PROTOCOL_PREFIX,
	WEBSOCKET_MESSAGE_SEND,
	WEBSOCKET_PATH,
	WEBSOCKET_PROTOCOL,
} from "$lib/common/constants";
import { isRecord } from "$lib/common/json";
import {
	ValidationError,
	validateMessageSend,
	validateServerEvent,
	type MessageSend,
	type MessageSendData,
	type MessageSendReasoningEffort,
	type ServerEvent,
} from "$lib/ws/generated";

export type ReasoningEffort = MessageSendReasoningEffort;
export type SocketState = "closed" | "connecting" | "open" | "reconnecting";

/** A frame whose envelope parsed but whose contract validation failed: an
 * unknown `type` the client has not learned yet, or a known `type` whose
 * `data` does not fit its schema. Only the fields needed to keep the feed
 * cursor moving and to log a diagnostic survive; the payload never does. */
export interface InvalidFrame {
	sequence: number | undefined;
	sessionId: string | undefined;
	type: string;
}

export type IncomingFrame =
	{ event: ServerEvent; kind: "valid" } | { invalid: InvalidFrame; kind: "invalid" };

export interface TurnSettings {
	model: string;
	reasoningEffort: ReasoningEffort;
}

export interface PeenSocketOptions {
	onError: (message: string) => void;
	onEvent: (frame: IncomingFrame) => void;
	onState: (state: SocketState) => void;
	token: string;
}

/**
 * The one global WebSocket Peen's control surface keeps. There is no
 * per-session presence socket: the server keeps work running with nobody
 * attached, and a client that reconnects catches up from the session feed
 * instead. An unexpected close reconnects on a doubling backoff with
 * jitter; an explicit close() never reconnects.
 */
export class PeenSocket {
	private readonly options: PeenSocketOptions;
	private socket: WebSocket | undefined;
	private explicitlyClosed = true;
	private reconnectAttempt = 0;
	private reconnectTimer: ReturnType<typeof setTimeout> | undefined;

	public constructor(options: PeenSocketOptions) {
		this.options = options;
	}

	public connect(): void {
		this.cancelReconnect();
		this.explicitlyClosed = false;
		this.reconnectAttempt = 0;
		this.openSocket();
	}

	public close(): void {
		this.explicitlyClosed = true;
		this.cancelReconnect();
		const previous = this.socket;
		this.socket = undefined;
		previous?.close();
		// WebSocket.close() is fire-and-forget: a real browser fires its own
		// "close" event asynchronously, by which point this.socket is already
		// undefined and the close listener's own-socket guard would ignore it.
		// An explicit close() still reports "closed" at once.
		this.options.onState("closed");
	}

	/**
	 * Sends a message to a session. Settings apply to a new turn only: a
	 * message queued into a running turn must leave them out, because the
	 * server rejects a queued message that tries to change them. A message to a
	 * running child agent names its run in agentRunID, starts no turn, and
	 * never carries settings.
	 */
	public send(
		sessionID: string,
		message: string,
		settings?: TurnSettings,
		agentRunID?: string,
	): string {
		if (this.socket?.readyState !== WebSocket.OPEN) {
			throw new Error("The WebSocket is not connected.");
		}

		const data: MessageSendData = { message };
		if (agentRunID !== undefined) {
			data.to = agentRunID;
		} else if (settings !== undefined) {
			data.model = settings.model;
			data.reasoningEffort = settings.reasoningEffort;
		}

		const event: MessageSend = {
			data,
			id: crypto.randomUUID(),
			metadata: { sessionId: sessionID },
			timestamp: Math.floor(Date.now() / 1000),
			triggeredBy: null,
			type: WEBSOCKET_MESSAGE_SEND,
		};
		validateMessageSend(event);

		this.socket.send(JSON.stringify(event));
		logBrowserEvent("socket.message.sent", {
			event_id: event.id,
			model: data.model,
			reasoning_effort: data.reasoningEffort,
			session_id: sessionID,
		});

		return event.id;
	}

	private openSocket(): void {
		const previous = this.socket;
		this.socket = undefined;
		previous?.close();

		logBrowserEvent("socket.connect.start", { socket_state: "connecting" });
		this.options.onState("connecting");

		const socket = new WebSocket(
			webSocketURL(),
			webSocketProtocols(this.options.token),
		);
		this.socket = socket;

		socket.addEventListener("open", () => {
			if (this.socket === socket) {
				this.reconnectAttempt = 0;
				logBrowserEvent("socket.open", { socket_state: "open" });
				this.options.onState("open");
			}
		});

		socket.addEventListener("close", () => {
			if (this.socket === socket) {
				this.socket = undefined;
				logBrowserEvent("socket.close", { socket_state: "closed" });
				if (this.explicitlyClosed) {
					this.options.onState("closed");

					return;
				}

				this.scheduleReconnect();
			}
		});

		socket.addEventListener("error", () => {
			logBrowserEvent("socket.error");
			this.options.onError("The WebSocket connection failed.");
		});

		socket.addEventListener("message", (message) => {
			if (typeof message.data !== "string") {
				logBrowserEvent("socket.frame.rejected", { reason: "non_text" });
				this.options.onError("The controller sent a non-text WebSocket frame.");

				return;
			}

			const frame = parseServerEvent(message.data);
			if (frame === undefined) {
				logBrowserEvent("socket.frame.rejected", { reason: "invalid_event" });
				this.options.onError("The controller sent an invalid WebSocket event.");

				return;
			}

			if (frame.kind === "invalid") {
				logBrowserEvent("socket.frame.invalid", {
					event_type: frame.invalid.type,
					sequence: frame.invalid.sequence,
				});
				this.options.onEvent(frame);

				return;
			}

			logBrowserEvent("socket.event.received", {
				event_id: frame.event.id,
				event_type: frame.event.type,
				request_id: frame.event.metadata.requestId,
				session_id: frame.event.metadata.sessionId,
			});
			this.options.onEvent(frame);
		});
	}

	private scheduleReconnect(): void {
		this.options.onState("reconnecting");
		const delay = reconnectDelay(this.reconnectAttempt);
		this.reconnectAttempt += 1;
		logBrowserEvent("socket.reconnect.scheduled", {
			duration_ms: delay,
			socket_state: "reconnecting",
		});
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = undefined;
			this.openSocket();
		}, delay);
	}

	private cancelReconnect(): void {
		if (this.reconnectTimer !== undefined) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = undefined;
		}
	}
}

/** Capped exponential backoff with jitter added on top of the capped base. */
function reconnectDelay(attempt: number): number {
	const base = Math.min(RECONNECT_BASE_DELAY_MS * 2 ** attempt, RECONNECT_MAX_DELAY_MS);

	return base + Math.random() * base * RECONNECT_JITTER_RATIO;
}

export function webSocketURL(): string {
	const url = new URL(window.location.href);
	url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
	url.pathname = WEBSOCKET_PATH;
	url.search = "";
	url.hash = "";

	return url.toString();
}

export function webSocketProtocols(token: string): string[] {
	const result = [WEBSOCKET_PROTOCOL];
	if (token.trim() !== "") {
		result.push(WEBSOCKET_BEARER_PROTOCOL_PREFIX + base64URL(token.trim()));
	}

	return result;
}

function base64URL(value: string): string {
	const bytes = new TextEncoder().encode(value);
	let binary = "";
	for (const byte of bytes) {
		binary += String.fromCharCode(byte);
	}

	return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

/**
 * Validates one WebSocket frame against the generated AsyncAPI contract.
 * A frame that decodes but does not fit any known `type`, or whose `data`
 * does not fit that `type`'s schema, comes back as "invalid" rather than
 * being dropped outright: the feed cursor still needs its sequence number
 * to avoid stalling behind a frame it will never apply.
 */
export function classifyServerEvent(candidate: unknown): IncomingFrame {
	try {
		validateServerEvent(candidate);

		return { event: candidate, kind: "valid" };
	} catch (err) {
		if (!(err instanceof ValidationError)) {
			throw err;
		}

		return { invalid: invalidFrame(candidate), kind: "invalid" };
	}
}

function invalidFrame(candidate: unknown): InvalidFrame {
	if (!isRecord(candidate)) {
		return { sequence: undefined, sessionId: undefined, type: "unknown" };
	}

	const metadata = candidate.metadata;
	const sequence =
		isRecord(metadata) && typeof metadata.sequence === "number"
			? metadata.sequence
			: undefined;
	const sessionId =
		isRecord(metadata) && typeof metadata.sessionId === "string"
			? metadata.sessionId
			: undefined;

	return {
		sequence,
		sessionId,
		type: typeof candidate.type === "string" ? candidate.type : "unknown",
	};
}

/**
 * Parses one WebSocket text frame. Malformed JSON or a non-object payload
 * comes back as `undefined`: there is no envelope to report a type or
 * sequence from. Anything shaped like an envelope goes through contract
 * validation and comes back classified, valid or not.
 */
export function parseServerEvent(raw: string): IncomingFrame | undefined {
	let decoded: unknown;
	try {
		decoded = JSON.parse(raw);
	} catch {
		return undefined;
	}

	if (!isRecord(decoded)) {
		return undefined;
	}

	return classifyServerEvent(decoded);
}
