import { logBrowserEvent, startBrowserTimer } from "$lib/browser/log";
import { FEED_RESYNC_THRESHOLD, FEED_SEQUENCE_NONE } from "$lib/common/constants";
import type { ServerEvent } from "$lib/ws/generated";
import { classifyServerEvent, type IncomingFrame } from "$lib/ws/socket";

import type { components } from "$lib/api/generated";

type FeedFrame = components["schemas"]["FeedFrame"];
type FeedPage = components["schemas"]["FeedPage"];

export type FeedDecision = "ack" | "apply" | "duplicate" | "gap";

/**
 * The envelope shape a replayed feed item carries before it has been run
 * through contract validation. `feedFrameToEvent` only reshapes the stored
 * row; it never asserts that `data` fits `type`'s schema.
 */
export interface FeedEnvelope {
	data: unknown;
	id: string;
	metadata: { requestId?: string; sequence: number; sessionId: string };
	timestamp: number;
	triggeredBy: null;
	type: string;
}

export interface FeedCursorOptions {
	/** Applies one frame through the same reducer path a live frame uses. */
	applyFrame: (event: ServerEvent) => void;
	fetchPage: (cursor: number) => Promise<FeedPage>;
	onCatchUpError?: (cause: unknown) => void;
	/** Called instead of paging when the gap exceeds the resync threshold. */
	onResync: () => void;
	resyncThreshold?: number;
	sessionID: string;
}

function sequenceOf(frame: IncomingFrame): number | undefined {
	return frame.kind === "valid"
		? frame.event.metadata.sequence
		: frame.invalid.sequence;
}

function applyIfValid(
	frame: IncomingFrame,
	applyFrame: (event: ServerEvent) => void,
): void {
	if (frame.kind === "valid") {
		applyFrame(frame.event);
	}
}

/**
 * Tracks one session's position in its live feed: the last sequence applied,
 * a buffer for frames that arrived ahead of it, and a single-flight catch-up
 * that pages `GET /session/feed` and replays missed frames through the same
 * reducer a live frame uses. A frame with no sequence (a `message.completed`
 * or `message.failed` acknowledgement) always applies at once. A gap that
 * would replay more than the resync threshold skips paging and reloads the
 * session instead.
 */
export class FeedCursor {
	private readonly options: FeedCursorOptions;
	private readonly resyncThreshold: number;
	private lastSequence = FEED_SEQUENCE_NONE;
	private primed = false;
	private buffer: IncomingFrame[] = [];
	private catchingUp: Promise<void> | undefined;

	public constructor(options: FeedCursorOptions) {
		this.options = options;
		this.resyncThreshold = options.resyncThreshold ?? FEED_RESYNC_THRESHOLD;
	}

	public get sessionID(): string {
		return this.options.sessionID;
	}

	public get lastApplied(): number {
		return this.lastSequence;
	}

	public get isPrimed(): boolean {
		return this.primed;
	}

	/**
	 * Classifies one live frame against the cursor. An ack (no sequence)
	 * applies at once. A duplicate (sequence <= last) is dropped. The next
	 * frame in order (sequence == last+1) applies and drains any buffered
	 * frames it unblocks. Anything further ahead is buffered and starts a
	 * catch-up once the cursor is primed. A frame that failed contract
	 * validation never applies, but a sequence it carries still advances the
	 * cursor so it can never block catch-up behind a frame it will never apply.
	 */
	public accept(frame: IncomingFrame): FeedDecision {
		const sequence = sequenceOf(frame);
		if (sequence === undefined) {
			applyIfValid(frame, this.options.applyFrame);

			return "ack";
		}
		// Until open() sets the starting point, lastSequence is not a position,
		// so every sequenced frame waits in the buffer for the first catch-up.
		if (!this.primed) {
			this.buffer.push(frame);

			return "gap";
		}
		if (sequence <= this.lastSequence) {
			return "duplicate";
		}
		if (sequence === this.lastSequence + 1) {
			applyIfValid(frame, this.options.applyFrame);
			this.lastSequence = sequence;
			this.drainBuffer();

			return "apply";
		}

		this.buffer.push(frame);
		void this.startCatchUp();

		return "gap";
	}

	/** Sets the starting point and catches up to the feed head from there. */
	public async open(fromSequence: number): Promise<void> {
		this.lastSequence = fromSequence;
		this.primed = true;
		await this.startCatchUp();
	}

	/** Re-runs catch-up from the current position, such as after a reconnect. */
	public async catchUp(): Promise<void> {
		if (!this.primed) {
			return;
		}
		await this.startCatchUp();
	}

	private async startCatchUp(): Promise<void> {
		if (this.catchingUp !== undefined) {
			await this.catchingUp;

			return;
		}

		const run = this.runCatchUp();
		this.catchingUp = run;
		try {
			await run;
		} finally {
			this.catchingUp = undefined;
		}
	}

	private async runCatchUp(): Promise<void> {
		const elapsed = startBrowserTimer();
		logBrowserEvent("feed.catchup.start", { session_id: this.options.sessionID });

		let applied = 0;
		try {
			let cursor = this.lastSequence;
			for (;;) {
				const page = await this.options.fetchPage(cursor);
				if (page.head - this.lastSequence > this.resyncThreshold) {
					this.buffer = [];
					logBrowserEvent("feed.resync", { session_id: this.options.sessionID });
					this.options.onResync();

					return;
				}

				for (const item of page.items) {
					if (item.sequence <= this.lastSequence) {
						continue;
					}
					const frame = classifyServerEvent(feedFrameToEvent(item));
					if (frame.kind === "valid") {
						this.options.applyFrame(frame.event);
						applied += 1;
					} else {
						logBrowserEvent("feed.frame.invalid", {
							event_type: item.type,
							sequence: item.sequence,
						});
					}
					// A replayed frame that fails contract validation still moves the
					// cursor past it: it will never become applicable, so stopping
					// here would stall catch-up behind it forever.
					this.lastSequence = item.sequence;
				}

				cursor = page.nextCursor;
				if (!page.hasMore) {
					break;
				}
			}

			this.drainBuffer();
			logBrowserEvent("feed.catchup.complete", {
				count: applied,
				duration_ms: elapsed(),
				session_id: this.options.sessionID,
			});
		} catch (cause) {
			logBrowserEvent("feed.catchup.fail", {
				duration_ms: elapsed(),
				session_id: this.options.sessionID,
			});
			this.options.onCatchUpError?.(cause);
		}
	}

	/** Applies every buffered frame the catch-up unblocked, in sequence order. */
	private drainBuffer(): void {
		for (;;) {
			const index = this.buffer.findIndex(
				(frame) => sequenceOf(frame) === this.lastSequence + 1,
			);
			if (index === -1) {
				break;
			}

			const [next] = this.buffer.splice(index, 1);
			if (next === undefined) {
				break;
			}

			applyIfValid(next, this.options.applyFrame);
			this.lastSequence = sequenceOf(next) ?? this.lastSequence;
		}

		this.buffer = this.buffer.filter(
			(frame) => (sequenceOf(frame) ?? FEED_SEQUENCE_NONE) > this.lastSequence,
		);
	}
}

/**
 * Converts one page item back into the envelope shape its live frame
 * carried. The result is not validated here: every caller runs it through
 * `classifyServerEvent` so replay checks the same contract a live frame does.
 */
export function feedFrameToEvent(frame: FeedFrame): FeedEnvelope {
	return {
		data: frame.data,
		id: frame.id,
		metadata: {
			sequence: frame.sequence,
			sessionId: frame.sessionId,
			...(frame.requestId === null ? {} : { requestId: frame.requestId }),
		},
		timestamp: Math.floor(new Date(frame.createdAt).getTime() / 1000),
		triggeredBy: null,
		type: frame.type,
	};
}
