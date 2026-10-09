import { describe, expect, it } from "vitest";

import { toolSubject } from "./toolsubject";

describe("toolSubject", () => {
	it.each([
		["use_skill", { name: "release-notes" }, "release-notes"],
		["run_command", { command: "go test ./...", purpose: "tests" }, "go test ./..."],
		["launch_agent", { agent: "reviewer", task: "review" }, "reviewer"],
		["read_file", { path: "src/main.go" }, "src/main.go"],
		["write_file", { path: "greet.sh", content: "echo hi" }, "greet.sh"],
		["edit_file", { path: "greet.sh", edits: [] }, "greet.sh"],
		["list_files", { path: "." }, "."],
		["search_text", { path: "src", pattern: "TODO" }, "TODO"],
		["move_path", { source: "a.txt", destination: "b.txt" }, "a.txt → b.txt"],
		["remove_path", { path: "build" }, "build"],
		["make_directory", { path: "out" }, "out"],
	])("names what %s acted on", (name, input, want) => {
		expect(toolSubject(name, JSON.stringify(input))).toBe(want);
	});

	it("puts a multi-line command on one line", () => {
		const subject = toolSubject(
			"run_command",
			JSON.stringify({ command: "cd app\n  make test" }),
		);

		expect(subject).toBe("cd app make test");
	});

	it("shortens a long subject", () => {
		const subject = toolSubject(
			"run_command",
			JSON.stringify({ command: "x".repeat(200) }),
		);

		expect(subject.length).toBeLessThanOrEqual(81);
		expect(subject.endsWith("…")).toBe(true);
	});

	it.each([
		["a tool with no named argument", "list_jobs", JSON.stringify({})],
		["an unknown tool", "something_else", JSON.stringify({ path: "x" })],
		["arguments that are not JSON", "run_command", "{not json"],
		["no arguments", "run_command", ""],
		["a key argument that is not text", "use_skill", JSON.stringify({ name: 7 })],
	])("returns nothing for %s", (_, name, input) => {
		expect(toolSubject(name, input)).toBe("");
	});
});
