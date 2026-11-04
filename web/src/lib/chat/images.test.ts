import { describe, expect, it } from "vitest";

import { describeImages } from "$lib/chat/images";

describe("describeImages", () => {
	it("describes nothing when a message has no images", () => {
		expect(describeImages(undefined)).toBeUndefined();
		expect(describeImages([])).toBeUndefined();
	});

	it.each([
		[[{ mediaType: "image/png", sizeBytes: 122_880 }], "Image: PNG, 120 KB"],
		[[{ mediaType: "image/gif", sizeBytes: 1023 }], "Image: GIF, 1023 B"],
		[[{ mediaType: "image/gif", sizeBytes: 1024 }], "Image: GIF, 1 KB"],
		[
			[
				{ mediaType: "image/jpeg", sizeBytes: 1_258_291 },
				{ mediaType: "image/webp", sizeBytes: 512 },
			],
			"2 images: JPEG, 1.2 MB · WEBP, 512 B",
		],
	])("describes %j as %j", (images, expected) => {
		expect(describeImages(images)).toBe(expected);
	});
});
