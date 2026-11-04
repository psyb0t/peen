import type { components } from "$lib/api/generated";

export type MCPServerStatus = components["schemas"]["MCPServerStatus"];

/** The name a server shows under. A refused mcp.json file has none. */
export function mcpServerName(server: MCPServerStatus): string {
	return server.name === "" ? `${server.source} mcp.json` : server.name;
}

/**
 * Summarizes one server in a line: "ready, 3 tools from config over stdio" or
 * "failed: <why>".
 */
export function describeMCPServer(server: MCPServerStatus): string {
	if (server.state === "failed") {
		return `failed: ${server.error ?? "no reason given"}`;
	}

	const count = server.tools.length;
	const tools = count === 1 ? "1 tool" : `${count} tools`;
	const transport = server.transport === undefined ? "" : ` over ${server.transport}`;

	return `ready, ${tools} from ${server.source}${transport}`;
}
