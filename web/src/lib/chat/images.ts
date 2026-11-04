import {
	BYTES_PER_KIB,
	IMAGE_MEDIA_TYPE_PREFIX,
	IMAGE_SIZE_UNITS,
} from "$lib/common/constants";

/** An image a message carries, as the API describes it, without its bytes. */
export interface ImageInfo {
	mediaType: string;
	sizeBytes: number;
}

/**
 * Describes a message's images in one line, such as "Image: PNG, 120 KB" or
 * "2 images: PNG, 120 KB · JPEG, 1.2 MB". Returns undefined when there are
 * none.
 */
export function describeImages(
	images: readonly ImageInfo[] | undefined,
): string | undefined {
	if (images === undefined || images.length === 0) {
		return undefined;
	}

	const parts = images.map(
		(image) => `${imageFormat(image.mediaType)}, ${imageSize(image.sizeBytes)}`,
	);
	const lead = images.length === 1 ? "Image" : `${images.length} images`;

	return `${lead}: ${parts.join(" · ")}`;
}

function imageFormat(mediaType: string): string {
	const format = mediaType.startsWith(IMAGE_MEDIA_TYPE_PREFIX)
		? mediaType.slice(IMAGE_MEDIA_TYPE_PREFIX.length)
		: mediaType;

	return format.toUpperCase();
}

function imageSize(bytes: number): string {
	let value = bytes;
	let unit = 0;
	while (value >= BYTES_PER_KIB && unit < IMAGE_SIZE_UNITS.length - 1) {
		value /= BYTES_PER_KIB;
		unit++;
	}

	const rounded = unit === 0 ? String(value) : value.toFixed(1).replace(/\.0$/, "");

	return `${rounded} ${IMAGE_SIZE_UNITS[unit]}`;
}
