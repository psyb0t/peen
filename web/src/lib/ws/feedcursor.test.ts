import { describe, expect, it, vi } from "vitest";

import type { components } from "$lib/api/generated";
import type { ServerEvent } from "$lib/ws/generated";
import type { IncomingFrame } from "$lib/ws/socket";

import { FeedCursor, feedFrameToEvent } from "./feedcursor";

type FeedFrame = components["schemas"]["FeedFrame"];
type FeedPage = components["schemas"]["FeedPage"];

const sessionID = "22222222-2222-4222-8222-222222222222";
const requestID = "33333333-3333-4333-8333-333333333333";

// A live frame valid against the generated contract: message.completed only
// requires `queued`, so it is a cheap stand-in wherever the test cares about
// sequence math rather than any particular event's data.
function liveFrame(
	sequence: number | undefined,
	type = "message.completed",
): IncomingFrame {
	return {
		event: {
			data: { queued: false },
			id: `44444444-4444-4444-8444-${String(sequence ?? 0).padStart(12, "0")}`,
			metadata: {
				requestId: requestID,
				sessionId: sessionID,
				...(sequence === undefined ? {} : { sequence }),
			},
			timestamp: 1,
			triggeredBy: null,
			type,
		} as ServerEvent,
		kind: "valid",
	};
}

// A stored feed frame. The default type and data validate against the
// generated contract; a caller testing invalid replay passes its own type
// and data to deliberately fail that validation.
function storedFrame(
	sequence: number,
	type = "message.completed",
	data: unknown = { queued: false },
): FeedFrame {
	return {
		createdAt: "2026-10-15T00:00:00Z",
		data,
		id: `55555555-5555-4555-8555-${String(sequence).padStart(12, "0")}`,
		requestId: requestID,
		sequence,
		sessionId: sessionID,
		type,
	};
}

function page(
	items: FeedFrame[],
	nextCursor: number,
	hasMore: boolean,
	head: number,
): FeedPage {
	return { hasMore, head, items, limit: 200, nextCursor };
}

/** A promise plus the function that settles it, for controlling fetch timing. */
function deferred<T>(): {
	promise: Promise<T>;
	resolve: (value: T) => void;
} {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((res) => {
		resolve = res;
	});

	return { promise, resolve };
}

/** A cursor opened at an empty session's head, ready for live frames. */
async function openedCursor(applied: ServerEvent[]): Promise<FeedCursor> {
	const cursor = new FeedCursor({
		applyFrame: (event) => applied.push(event),
		fetchPage: vi.fn(async () => page([], 0, false, 0)),
		onResync: vi.fn(),
		sessionID,
	});
	await cursor.open(0);

	return cursor;
}

describe("FeedCursor", () => {
	it("applies the next sequence and advances", async () => {
		const applied: ServerEvent[] = [];
		const cursor = await openedCursor(applied);
		const frame = liveFrame(1);

		const decision = cursor.accept(frame);

		expect(decision).toBe("apply");
		expect(cursor.lastApplied).toBe(1);
		expect(applied).toEqual(frame.kind === "valid" ? [frame.event] : []);
	});

	it("drops a duplicate at or behind the last applied sequence", async () => {
		const applied: ServerEvent[] = [];
		const cursor = await openedCursor(applied);

		cursor.accept(liveFrame(1));
		expect(cursor.accept(liveFrame(1))).toBe("duplicate");
		expect(cursor.accept(liveFrame(0))).toBe("duplicate");
		expect(applied).toHaveLength(1);
	});

	it("holds frames that arrive before open and applies each once", async () => {
		const applied: ServerEvent[] = [];
		const fetchPage = vi.fn(async () => page([storedFrame(1)], 1, false, 1));
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});

		// A brand-new session's first frame lands while the snapshot loads.
		expect(cursor.accept(liveFrame(1))).toBe("gap");
		expect(applied).toHaveLength(0);
		expect(fetchPage).not.toHaveBeenCalled();

		await cursor.open(0);

		expect(cursor.lastApplied).toBe(1);
		expect(applied.map((event) => event.metadata.sequence)).toEqual([1]);
	});

	it("applies an unsequenced frame as a live ack without moving the cursor", async () => {
		const applied: ServerEvent[] = [];
		const cursor = await openedCursor(applied);

		cursor.accept(liveFrame(1));
		const decision = cursor.accept(liveFrame(undefined, "message.completed"));

		expect(decision).toBe("ack");
		expect(cursor.lastApplied).toBe(1);
		expect(applied).toHaveLength(2);
	});

	it("buffers a gap, catches up once primed, and drains the buffer in order", async () => {
		const applied: ServerEvent[] = [];
		// Priming at 2 finds nothing new yet (an empty page at the head).
		const fetchPage = vi.fn(async () => page([], 2, false, 2));
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});
		await cursor.open(2);
		expect(cursor.lastApplied).toBe(2);
		fetchPage.mockClear();

		// The gap this triggers pages from the current cursor and finds the
		// frame the client missed.
		fetchPage.mockImplementationOnce(async () => page([storedFrame(3)], 3, false, 3));

		expect(cursor.accept(liveFrame(4))).toBe("gap");
		// A later frame arrives before the catch-up it triggered resolves.
		expect(cursor.accept(liveFrame(5))).toBe("gap");

		await vi.waitFor(() => expect(cursor.lastApplied).toBe(5));

		expect(fetchPage).toHaveBeenCalledTimes(1);
		expect(applied.map((event) => event.metadata.sequence)).toEqual([3, 4, 5]);
	});

	it("does not start a second catch-up while one is already in flight", async () => {
		const applied: ServerEvent[] = [];
		const first = deferred<FeedPage>();
		const fetchPage = vi.fn(() => first.promise);
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});

		const opened = cursor.open(0);
		expect(fetchPage).toHaveBeenCalledTimes(1);

		cursor.accept(liveFrame(2));
		cursor.accept(liveFrame(3));
		expect(fetchPage).toHaveBeenCalledTimes(1);

		first.resolve(page([storedFrame(1)], 1, false, 3));
		await opened;

		expect(cursor.lastApplied).toBe(3);
		expect(applied.map((event) => event.metadata.sequence)).toEqual([1, 2, 3]);
	});

	it("pages with hasMore until the feed is exhausted", async () => {
		const applied: ServerEvent[] = [];
		const fetchPage = vi.fn(async (cursor: number) => {
			if (cursor === 0) {
				return page([storedFrame(1)], 1, true, 2);
			}
			if (cursor === 1) {
				return page([storedFrame(2)], 2, false, 2);
			}
			throw new Error(`unexpected cursor ${cursor}`);
		});
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});

		await cursor.open(0);

		expect(fetchPage).toHaveBeenCalledTimes(2);
		expect(fetchPage).toHaveBeenNthCalledWith(1, 0);
		expect(fetchPage).toHaveBeenNthCalledWith(2, 1);
		expect(applied.map((event) => event.metadata.sequence)).toEqual([1, 2]);
		expect(cursor.lastApplied).toBe(2);
	});

	it("advances past a replayed item that fails contract validation, without looping", async () => {
		const applied: ServerEvent[] = [];
		// turn.started requires sessionId, model, and workspace: this item's
		// data has none of them, so it fails validation on replay.
		const fetchPage = vi.fn(async () =>
			page([storedFrame(1, "turn.started", { anything: "goes" })], 1, false, 1),
		);
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});

		await cursor.open(0);

		expect(cursor.lastApplied).toBe(1);
		expect(applied).toEqual([]);
		expect(fetchPage).toHaveBeenCalledTimes(1);
	});

	it("skips replay and resyncs when the gap exceeds the threshold", async () => {
		const applied: ServerEvent[] = [];
		const onResync = vi.fn();
		const fetchPage = vi.fn(async () => page([], 0, false, 1000));
		const cursor = new FeedCursor({
			applyFrame: (event) => applied.push(event),
			fetchPage,
			onResync,
			resyncThreshold: 100,
			sessionID,
		});

		await cursor.open(0);

		expect(onResync).toHaveBeenCalledTimes(1);
		expect(applied).toEqual([]);
	});

	it("ignores catchUp before the cursor has been opened", async () => {
		const fetchPage = vi.fn();
		const cursor = new FeedCursor({
			applyFrame: vi.fn(),
			fetchPage,
			onResync: vi.fn(),
			sessionID,
		});

		await cursor.catchUp();

		expect(fetchPage).not.toHaveBeenCalled();
	});

	it("reports a catch-up failure without throwing", async () => {
		const onCatchUpError = vi.fn();
		const cursor = new FeedCursor({
			applyFrame: vi.fn(),
			fetchPage: () => Promise.reject(new Error("network down")),
			onCatchUpError,
			onResync: vi.fn(),
			sessionID,
		});

		await cursor.open(0);

		expect(onCatchUpError).toHaveBeenCalledTimes(1);
	});

	it("converts a stored feed frame back into the shape its live frame carried", () => {
		const frame = storedFrame(7, "notice.created", { sequence: 7 });
		const event = feedFrameToEvent(frame);

		expect(event).toEqual({
			data: { sequence: 7 },
			id: frame.id,
			metadata: { requestId: requestID, sequence: 7, sessionId: sessionID },
			timestamp: Math.floor(new Date(frame.createdAt).getTime() / 1000),
			triggeredBy: null,
			type: "notice.created",
		});
	});

	it("leaves out requestId when the stored frame carries none", () => {
		const frame = { ...storedFrame(1), requestId: null };
		const event = feedFrameToEvent(frame);

		expect(event.metadata).toEqual({ sequence: 1, sessionId: sessionID });
		expect(event.type).toBe(frame.type);
	});
});
