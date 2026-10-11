import { describe, expect, it } from "vitest";

import {
	agentSpaceGrants,
	agentSpaceLabel,
	agentSpaceName,
	type AgentSpace,
} from "./agentspace";

const controlSpace: AgentSpace = {
	capabilityWarnings: [],
	hostRootEquivalent: false,
	kind: "control",
	name: "control",
	revision: 0,
};

function dockerSpace(overrides: Partial<AgentSpace>): AgentSpace {
	return {
		capabilityWarnings: [],
		docker: "none",
		hostRootEquivalent: false,
		kind: "docker",
		name: "docker:default",
		network: "bridge",
		profile: "default",
		revision: 0,
		sudo: false,
		...overrides,
	};
}

describe("agentSpaceLabel", () => {
	it("says where the control space runs", () => {
		expect(agentSpaceLabel(controlSpace)).toBe("control (runs where Peen runs)");
	});

	it("lists every grant of a sealed Docker profile", () => {
		expect(agentSpaceLabel(dockerSpace({}))).toBe(
			"default (docker: none, network: bridge, sudo: no)",
		);
	});

	it("marks docker full as host root", () => {
		const space = dockerSpace({
			docker: "full",
			hostRootEquivalent: true,
			name: "docker:ops",
			profile: "ops",
		});

		expect(agentSpaceLabel(space)).toBe(
			"ops (docker: full = host root, network: bridge, sudo: no)",
		);
	});

	it("shows the network mode and sudo of a Docker profile", () => {
		const space = dockerSpace({
			name: "docker:lab",
			network: "host",
			profile: "lab",
			sudo: true,
		});

		expect(agentSpaceLabel(space)).toBe("lab (docker: none, network: host, sudo: yes)");
	});

	it("falls back to the full name when a Docker space has no profile", () => {
		const space = dockerSpace({ name: "docker:ops", profile: undefined });

		expect(agentSpaceName(space)).toBe("docker:ops");
	});
});

describe("agentSpaceGrants", () => {
	it("lists the control space as running where Peen runs", () => {
		expect(agentSpaceGrants(controlSpace)).toEqual(["runs where Peen runs"]);
	});

	it("lists docker, network, and sudo for a Docker space", () => {
		expect(agentSpaceGrants(dockerSpace({ sudo: true }))).toEqual([
			"docker: none",
			"network: bridge",
			"sudo: yes",
		]);
	});
});
