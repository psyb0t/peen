import type { components } from "$lib/api/generated";
import {
	AGENT_SPACE_CONTROL_NOTE,
	AGENT_SPACE_HOST_ROOT_NOTE,
	AGENT_SPACE_KIND_CONTROL,
	AGENT_SPACE_SUDO_OFF,
	AGENT_SPACE_SUDO_ON,
	DOCKER_ACCESS_FULL,
} from "$lib/common/constants";

export type AgentSpace = components["schemas"]["AgentSpace"];

// agentSpaceName is the short name a reader recognises: `control`, or the
// Docker profile behind a `docker:<profile>` space.
export function agentSpaceName(space: AgentSpace): string {
	if (space.kind === AGENT_SPACE_KIND_CONTROL) {
		return space.name;
	}

	return space.profile ?? space.name;
}

// agentSpaceGrants lists what a space lets its agent do, in the order the
// operator writes them in a Docker profile. The control space has no grants to
// list, so it says where it runs instead.
export function agentSpaceGrants(space: AgentSpace): string[] {
	if (space.kind === AGENT_SPACE_KIND_CONTROL) {
		return [AGENT_SPACE_CONTROL_NOTE];
	}

	const docker = space.docker ?? "";
	const dockerGrant =
		docker === DOCKER_ACCESS_FULL
			? `docker: ${docker} = ${AGENT_SPACE_HOST_ROOT_NOTE}`
			: `docker: ${docker}`;

	return [
		dockerGrant,
		`network: ${space.network ?? ""}`,
		`sudo: ${space.sudo === true ? AGENT_SPACE_SUDO_ON : AGENT_SPACE_SUDO_OFF}`,
	];
}

// agentSpaceLabel is the text of one dropdown option: the space with its grants
// next to it, for example `ops (docker: full = host root, network: bridge,
// sudo: no)`.
export function agentSpaceLabel(space: AgentSpace): string {
	return `${agentSpaceName(space)} (${agentSpaceGrants(space).join(", ")})`;
}
