import {
	type MessageImage,
	type MessageImageMediaType,
	MessageImageMediaTypeValues,
} from "$lib/ws/generated";
import type { ImageInfo } from "$lib/chat/images";
import { IMAGE_MEDIA_TYPE_PREFIX } from "$lib/common/constants";

/** An image the user attached to the message they are writing. */
export interface Attachment {
	/** A key that stays the same while the attachment is listed. */
	id: string;
	name: string;
	image: MessageImage;
	info: ImageInfo;
}

/** The file picker's accept list: the formats Peen takes. */
export const ATTACHMENT_ACCEPT = MessageImageMediaTypeValues.join(",");

/** Thrown for a file Peen cannot send as an image. */
export class UnsupportedAttachmentError extends Error {
	constructor(fileName: string) {
		super(`${fileName} is not a PNG, JPEG, GIF, or WebP image.`);
		this.name = "UnsupportedAttachmentError";
	}
}

function isImageMediaType(value: string): value is MessageImageMediaType {
	return (MessageImageMediaTypeValues as readonly string[]).includes(value);
}

/**
 * Reads a file into an attachment. Only the formats the API accepts pass. The
 * server checks the count and size limits, which the deployment configures,
 * and that the bytes are the declared format.
 */
export async function readAttachment(file: File): Promise<Attachment> {
	if (!isImageMediaType(file.type)) {
		throw new UnsupportedAttachmentError(file.name);
	}

	const bytes = new Uint8Array(await file.arrayBuffer());

	return {
		id: crypto.randomUUID(),
		name: file.name,
		image: { mediaType: file.type, data: encodeBase64(bytes) },
		info: { mediaType: file.type, sizeBytes: bytes.byteLength },
	};
}

/** The image files in a paste or a drop, in order. */
export function imageFiles(items: FileList | readonly File[] | null): File[] {
	if (items === null) {
		return [];
	}

	return Array.from(items).filter((file) =>
		file.type.startsWith(IMAGE_MEDIA_TYPE_PREFIX),
	);
}

// btoa takes a binary string. Building it in chunks keeps a large image from
// overflowing the argument list of String.fromCharCode.
const BASE64_CHUNK_BYTES = 0x8000;

function encodeBase64(bytes: Uint8Array): string {
	const chunks: string[] = [];
	for (let start = 0; start < bytes.length; start += BASE64_CHUNK_BYTES) {
		chunks.push(
			String.fromCharCode(...bytes.subarray(start, start + BASE64_CHUNK_BYTES)),
		);
	}

	return btoa(chunks.join(""));
}
