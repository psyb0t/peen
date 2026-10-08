const ALLOWED_LINK_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);
const LINK_RESOLUTION_BASE = "https://peen.invalid/";

/**
 * Returns href when a markdown link may stay clickable, or undefined.
 *
 * Model output is untrusted, and the renderer passes link targets through as
 * written, so a `javascript:` or `data:` URL would run in the control surface.
 * Only http, https, mailto, and relative targets survive.
 */
export function safeLinkHref(href: unknown): string | undefined {
	if (typeof href !== "string" || href.trim() === "") {
		return undefined;
	}

	let parsed: URL;
	try {
		parsed = new URL(href, LINK_RESOLUTION_BASE);
	} catch {
		return undefined;
	}

	return ALLOWED_LINK_PROTOCOLS.has(parsed.protocol) ? href : undefined;
}
