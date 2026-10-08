import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import Markdown from "./Markdown.svelte";
import { safeLinkHref } from "./markdown";

describe("safe link targets", () => {
	it.each([
		["https://example.com/docs", "https://example.com/docs"],
		["http://localhost:8080/", "http://localhost:8080/"],
		["mailto:someone@example.com", "mailto:someone@example.com"],
		["docs/hooks.md", "docs/hooks.md"],
		["#section", "#section"],
	])("keeps %s", (href, expected) => {
		expect(safeLinkHref(href)).toBe(expected);
	});

	it.each([
		["javascript:alert(1)"],
		["JavaScript:alert(1)"],
		[" javascript:alert(1)"],
		["data:text/html,<script>alert(1)</script>"],
		["vbscript:msgbox(1)"],
		["file:///etc/passwd"],
		[""],
		[undefined],
		[null],
		[42],
	])("drops %s", (href) => {
		expect(safeLinkHref(href)).toBeUndefined();
	});
});

describe("markdown rendering", () => {
	afterEach(() => {
		cleanup();
	});

	it("renders emphasis, lists, code, and tables", () => {
		const { container } = render(Markdown, {
			source: "**bold** and `code`\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |",
		});

		expect(container.querySelector("strong")?.textContent).toBe("bold");
		expect(container.querySelector("code")?.textContent).toBe("code");
		expect(container.querySelectorAll("li")).toHaveLength(2);
		expect(container.querySelector("table td")?.textContent).toBe("1");
	});

	it("renders an unclosed code fence mid-stream without throwing", () => {
		const { container } = render(Markdown, { source: "```bash\necho hi" });

		expect(container.querySelector("pre code")?.textContent).toContain("echo hi");
	});

	it("never renders raw HTML from the source", () => {
		const { container } = render(Markdown, {
			source:
				'<script>window.pwned = true</script>\n\n<img src=x onerror="window.pwned = true">',
		});

		expect(container.querySelector("script")).toBeNull();
		expect(container.querySelector("img")).toBeNull();
		expect(container.querySelector("[onerror]")).toBeNull();
	});

	it("does not make script links clickable", () => {
		const { container } = render(Markdown, { source: "[click](javascript:alert(1))" });

		expect(container.querySelector("a")).toBeNull();
		expect(container.textContent).toContain("click");
	});

	it("keeps safe links and opens them in a new tab", () => {
		const { container } = render(Markdown, { source: "[docs](https://example.com)" });
		const link = container.querySelector("a");

		expect(link?.getAttribute("href")).toBe("https://example.com");
		expect(link?.getAttribute("target")).toBe("_blank");
		expect(link?.getAttribute("rel")).toBe("noopener noreferrer nofollow");
	});

	it("shows images as links instead of loading them", () => {
		const { container } = render(Markdown, {
			source: "![diagram](https://example.com/leak?data=secret)",
		});

		expect(container.querySelector("img")).toBeNull();
		expect(container.querySelector("a")?.textContent).toBe("diagram");
	});
});
