import { describe, expect, it } from "vitest";

import { toolSubject } from "./toolsubject";

describe("toolSubject", () => {
	it.each([
		["useSkill", { name: "release-notes" }, "release-notes"],
		["runCommand", { command: "go test ./...", purpose: "tests" }, "go test ./..."],
		["launchAgent", { agent: "reviewer", task: "review" }, "reviewer"],
		["readFile", { path: "src/main.go" }, "src/main.go"],
		["writeFile", { path: "greet.sh", content: "echo hi" }, "greet.sh"],
		["editFile", { path: "greet.sh", edits: [] }, "greet.sh"],
		["listFiles", { path: "." }, "."],
		["searchText", { path: "src", pattern: "TODO" }, "TODO"],
		["movePath", { source: "a.txt", destination: "b.txt" }, "a.txt → b.txt"],
		["removePath", { path: "build" }, "build"],
		["makeDirectory", { path: "out" }, "out"],
	])("names what %s acted on", (name, input, want) => {
		expect(toolSubject(name, JSON.stringify(input))).toBe(want);
	});

	it("puts a multi-line command on one line", () => {
		const subject = toolSubject(
			"runCommand",
			JSON.stringify({ command: "cd app\n  make test" }),
		);

		expect(subject).toBe("cd app make test");
	});

	it("shortens a long subject", () => {
		const subject = toolSubject(
			"runCommand",
			JSON.stringify({ command: "x".repeat(200) }),
		);

		expect(subject.length).toBeLessThanOrEqual(81);
		expect(subject.endsWith("…")).toBe(true);
	});

	it.each([
		["a tool with no named argument", "listJobs", JSON.stringify({})],
		["an unknown tool", "somethingElse", JSON.stringify({ path: "x" })],
		["arguments that are not JSON", "runCommand", "{not json"],
		["no arguments", "runCommand", ""],
		["a key argument that is not text", "useSkill", JSON.stringify({ name: 7 })],
	])("returns nothing for %s", (_, name, input) => {
		expect(toolSubject(name, input)).toBe("");
	});
});
