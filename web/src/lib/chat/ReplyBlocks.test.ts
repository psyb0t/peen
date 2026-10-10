import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import type { ReplyBlock } from "./transcript";

import ReplyBlocks from "./ReplyBlocks.svelte";

const skillText =
	"<skill>\n<name>core:planning</name>\n<description>Plan work.</description>\n" +
	"<directory>embedded://skills/planning</directory>\n\nSteps.\n</skill>";

function skillDetails(container: HTMLElement): HTMLDetailsElement {
	const details = container.querySelector<HTMLDetailsElement>("details.skill");
	if (details === null) {
		throw new Error("no skill marker rendered");
	}

	return details;
}

describe("ReplyBlocks skill marker", () => {
	afterEach(() => {
		cleanup();
	});

	it("shows a skill message as a collapsed marker named after the skill", () => {
		const blocks: ReplyBlock[] = [
			{ key: "s", kind: "skill", name: "core:planning", text: skillText },
		];

		const { container } = render(ReplyBlocks, { blocks });

		const details = skillDetails(container);
		expect(details.open).toBe(false);
		expect(details.querySelector("summary")?.textContent).toBe("skill: core:planning");
		expect(screen.queryByText("skill: core:planning")).not.toBeNull();
	});

	it("expands on click and shows the skill content", async () => {
		const blocks: ReplyBlock[] = [
			{ key: "s", kind: "skill", name: "core:planning", text: skillText },
			{ key: "t", kind: "text", text: "Planned." },
		];

		const { container } = render(ReplyBlocks, { blocks });
		const details = skillDetails(container);
		const summary = details.querySelector("summary");
		if (summary === null) {
			throw new Error("skill marker has no summary");
		}

		await fireEvent.click(summary);

		expect(details.open).toBe(true);
		expect(details.querySelector("pre")?.textContent).toBe(skillText);
		expect(container.textContent).toContain("Planned.");
	});

	it("keeps a skill marker apart from a background update", () => {
		const blocks: ReplyBlock[] = [
			{ key: "i", kind: "injected", text: "job finished" },
			{ key: "s", kind: "skill", name: "planning", text: "<note>loaded</note>" },
		];

		const { container } = render(ReplyBlocks, { blocks });

		const summaries = [...container.querySelectorAll("summary")].map(
			(summary) => summary.textContent,
		);
		expect(summaries).toEqual(["Background update", "skill: planning"]);
		expect(container.querySelectorAll("details.skill")).toHaveLength(1);
	});

	it("shows a handler wake message as a background update without raw tags", () => {
		const blocks: ReplyBlock[] = [
			{
				key: "h",
				kind: "injected",
				text: "Event handler for job.exited\n\nFind the cause.",
			},
		];

		const { container } = render(ReplyBlocks, { blocks });

		expect(container.querySelector("summary")?.textContent).toBe("Background update");
		expect(container.textContent).not.toContain("<handler>");
		expect(container.textContent).not.toContain("<event>");
	});
});
