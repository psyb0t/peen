import { describe, expect, it } from "vitest";

import { isSystemSession, systemAgentPrompt } from "$lib/system";

describe("systemAgentPrompt", () => {
	it("reads a quoted prompt", () => {
		expect(systemAgentPrompt('/system-agent "check the metrics"')).toBe(
			"check the metrics",
		);
	});

	it("reads an unquoted prompt", () => {
		expect(systemAgentPrompt("/system-agent check the metrics")).toBe(
			"check the metrics",
		);
	});

	it("gives an empty prompt for the bare command", () => {
		expect(systemAgentPrompt("/system-agent")).toBe("");
		expect(systemAgentPrompt('/system-agent ""')).toBe("");
	});

	it("leaves every other message alone", () => {
		expect(systemAgentPrompt("hello")).toBeUndefined();
		expect(systemAgentPrompt("/system-agents do it")).toBeUndefined();
		expect(systemAgentPrompt("please run /system-agent now")).toBeUndefined();
	});
});

describe("isSystemSession", () => {
	it("recognizes only the system agent", () => {
		expect(isSystemSession({ agent: "core:system" })).toBe(true);
		expect(isSystemSession({ agent: "core:general-purpose" })).toBe(false);
		expect(isSystemSession({ agent: "system" })).toBe(false);
		expect(isSystemSession(undefined)).toBe(false);
	});
});
