import { logBrowserEvent } from "$lib/browser/log";
import {
	WEBSOCKET_BEARER_PROTOCOL_PREFIX,
	WEBSOCKET_MESSAGE_SEND,
	WEBSOCKET_PATH,
	WEBSOCKET_PROTOCOL,
} from "$lib/common/constants";
import { isRecord } from "$lib/common/json";

import type { components } from "$lib/api/generated";

type MessageSendData = components["schemas"]["WebSocketMessageSendData"];
type MessageSendEvent = components["schemas"]["WebSocketMessageSendEvent"];

export type PeenSocketEvent = components["schemas"]["WebSocketServerEvent"];
export type SocketState = "closed" | "connecting" | "open";

export interface PeenSocketOptions {
	onError: (message: string) => void;
	onEvent: (event: PeenSocketEvent) => void;
	onState: (state: SocketState) => void;
	token: string;
}

export class PeenSocket {
	private readonly options: PeenSocketOptions;
	private socket: WebSocket | undefined;

	public constructor(options: PeenSocketOptions) {
		this.options = options;
	}

	public connect(): void {
		this.close();
		logBrowserEvent("socket.connect.start", { socket_state: "connecting" });
		this.options.onState("connecting");

		const socket = new WebSocket(
			webSocketURL(),
			webSocketProtocols(this.options.token),
		);
		this.socket = socket;

		socket.addEventListener("open", () => {
			if (this.socket === socket) {
				logBrowserEvent("socket.open", { socket_state: "open" });
				this.options.onState("open");
			}
		});

		socket.addEventListener("close", () => {
			if (this.socket === socket) {
				this.socket = undefined;
				logBrowserEvent("socket.close", { socket_state: "closed" });
				this.options.onState("closed");
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

			const event = parseServerEvent(message.data);
			if (event === undefined) {
				logBrowserEvent("socket.frame.rejected", { reason: "invalid_event" });
				this.options.onError("The controller sent an invalid WebSocket event.");

				return;
			}

			logBrowserEvent("socket.event.received", {
				event_id: event.id,
				event_type: event.type,
				request_id: event.metadata.requestId,
				session_id: event.metadata.sessionId,
			});
			this.options.onEvent(event);
		});
	}

	public close(): void {
		this.socket?.close();
		this.socket = undefined;
	}

	public send(sessionID: string, message: string, model: string): void {
		if (this.socket?.readyState !== WebSocket.OPEN) {
			throw new Error("The WebSocket is not connected.");
		}

		const data: MessageSendData = { message };
		if (model !== "") {
			data.model = model;
		}

		const event: MessageSendEvent = {
			data,
			id: crypto.randomUUID(),
			metadata: { sessionId: sessionID },
			timestamp: Math.floor(Date.now() / 1000),
			triggeredBy: null,
			type: WEBSOCKET_MESSAGE_SEND,
		};

		this.socket.send(JSON.stringify(event));
		logBrowserEvent("socket.message.sent", {
			event_id: event.id,
			model: data.model,
			session_id: sessionID,
		});
	}
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

export function parseServerEvent(raw: string): PeenSocketEvent | undefined {
	let decoded: unknown;
	try {
		decoded = JSON.parse(raw);
	} catch {
		return undefined;
	}

	if (!isRecord(decoded)) {
		return undefined;
	}

	const { data, id, metadata, timestamp, triggeredBy, type } = decoded;
	if (
		typeof id !== "string" ||
		typeof type !== "string" ||
		typeof timestamp !== "number" ||
		!isRecord(metadata) ||
		(triggeredBy !== null && typeof triggeredBy !== "string")
	) {
		return undefined;
	}

	const requestID = metadata.requestId;
	const sessionID = metadata.sessionId;
	if (
		(requestID !== undefined && typeof requestID !== "string") ||
		(sessionID !== undefined && typeof sessionID !== "string")
	) {
		return undefined;
	}

	return {
		data,
		id,
		metadata: {
			...(requestID === undefined ? {} : { requestId: requestID }),
			...(sessionID === undefined ? {} : { sessionId: sessionID }),
		},
		timestamp,
		triggeredBy,
		type,
	};
}
