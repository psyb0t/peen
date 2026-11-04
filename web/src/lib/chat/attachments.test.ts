import { describe, expect, it } from "vitest";

import {
	ATTACHMENT_ACCEPT,
	imageFiles,
	readAttachment,
	UnsupportedAttachmentError,
} from "$lib/chat/attachments";

const pngBytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

function decode(base64: string): Uint8Array {
	return Uint8Array.from(atob(base64), (character) => character.charCodeAt(0));
}

describe("readAttachment", () => {
	it("reads an image into base64 with its type and size", async () => {
		const attachment = await readAttachment(
			new File([pngBytes], "shot.png", { type: "image/png" }),
		);

		expect(attachment.name).toBe("shot.png");
		expect(attachment.image.mediaType).toBe("image/png");
		expect(decode(attachment.image.data)).toEqual(pngBytes);
		expect(attachment.info).toEqual({ mediaType: "image/png", sizeBytes: 8 });
		expect(attachment.id).not.toBe("");
	});

	it.each([
		["exactly one encoding chunk", 0x8000],
		["one byte past a chunk", 0x8001],
		["several chunks", 70_000],
	])("encodes an image of %s", async (_, size) => {
		const bytes = new Uint8Array(size).map((_, index) => index % 251);
		const attachment = await readAttachment(
			new File([bytes], "large.webp", { type: "image/webp" }),
		);

		expect(decode(attachment.image.data)).toEqual(bytes);
		expect(attachment.info.sizeBytes).toBe(size);
	});

	it("gives two reads of the same file different ids", async () => {
		const file = new File([pngBytes], "shot.png", { type: "image/png" });

		const first = await readAttachment(file);
		const second = await readAttachment(file);

		expect(first.id).not.toBe(second.id);
	});

	it.each([
		["notes.txt", "text/plain"],
		["drawing.svg", "image/svg+xml"],
		["photo.heic", "image/heic"],
		["unknown", ""],
	])("refuses %s", async (name, type) => {
		await expect(
			readAttachment(new File([pngBytes], name, { type })),
		).rejects.toBeInstanceOf(UnsupportedAttachmentError);
	});
});

describe("imageFiles", () => {
	it("keeps image files in order and drops the rest", () => {
		const png = new File([pngBytes], "a.png", { type: "image/png" });
		const text = new File(["x"], "b.txt", { type: "text/plain" });
		const jpeg = new File([pngBytes], "c.jpg", { type: "image/jpeg" });

		expect(imageFiles([png, text, jpeg])).toEqual([png, jpeg]);
	});

	it("returns nothing for no files", () => {
		expect(imageFiles(null)).toEqual([]);
		expect(imageFiles([])).toEqual([]);
	});
});

describe("ATTACHMENT_ACCEPT", () => {
	it("lists the formats Peen takes", () => {
		expect(ATTACHMENT_ACCEPT).toBe("image/png,image/jpeg,image/gif,image/webp");
	});
});
