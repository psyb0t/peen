import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import type { ToolCallView } from "./transcript";

import ToolCard from "./ToolCard.svelte";

function toolCall(name: string): ToolCallView {
	return {
		arguments: "{}",
		key: "toolu_1",
		kind: "tool",
		name,
		result: "ok",
		status: "done",
	};
}

function toolName(container: HTMLElement): HTMLElement {
	const element = container.querySelector<HTMLElement>(".tool-name");
	if (element === null) {
		throw new Error("no tool name rendered");
	}

	return element;
}

describe("ToolCard", () => {
	afterEach(() => {
		cleanup();
	});

	it.each(["applyPatch", "readFile", "launchAgent", "runCommand"])(
		"shows the tool name %s exactly as named",
		(name) => {
			const { container } = render(ToolCard, { call: toolCall(name) });

			expect(toolName(container).textContent).toBe(name);
		},
	);
});
