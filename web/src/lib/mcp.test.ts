import { describe, expect, it } from "vitest";

import { describeMCPServer, mcpServerName, type MCPServerStatus } from "$lib/mcp";

function server(overrides: Partial<MCPServerStatus>): MCPServerStatus {
	return {
		name: "docs",
		source: "config",
		state: "ready",
		tools: [],
		...overrides,
	};
}

describe("describeMCPServer", () => {
	it.each([
		[
			"a ready stdio server",
			server({ tools: ["mcp__docs__a", "mcp__docs__b"], transport: "stdio" }),
			"ready, 2 tools from config over stdio",
		],
		[
			"one tool",
			server({ tools: ["mcp__docs__a"], transport: "http", source: "workspace" }),
			"ready, 1 tool from workspace over http",
		],
		["no tools and no transport", server({}), "ready, 0 tools from config"],
		[
			"a failed server",
			server({ state: "failed", error: "connect: no such file" }),
			"failed: connect: no such file",
		],
		[
			"a failure with no reason",
			server({ state: "failed" }),
			"failed: no reason given",
		],
	])("describes %s", (_name, status, expected) => {
		expect(describeMCPServer(status)).toBe(expected);
	});
});

describe("mcpServerName", () => {
	it("uses the server name", () => {
		expect(mcpServerName(server({}))).toBe("docs");
	});

	it("names a refused file by its layer", () => {
		expect(mcpServerName(server({ name: "", source: "workspace" }))).toBe(
			"workspace mcp.json",
		);
	});
});
