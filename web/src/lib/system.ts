export const SYSTEM_AGENT_COMMAND = "/system-agent";
export const SYSTEM_AGENT_NAME = "core:system";
const QUOTE = '"';

/**
 * Reads a `/system-agent "prompt"` line. It returns undefined for any other
 * text, and an empty prompt when the command names none. The command is only
 * this client's shorthand: the server never reads it out of a message.
 */
export function systemAgentPrompt(text: string): string | undefined {
	const trimmed = text.trim();
	if (
		trimmed !== SYSTEM_AGENT_COMMAND &&
		!trimmed.startsWith(`${SYSTEM_AGENT_COMMAND} `)
	) {
		return undefined;
	}

	const prompt = trimmed.slice(SYSTEM_AGENT_COMMAND.length).trim();
	const isQuoted =
		prompt.length >= 2 && prompt.startsWith(QUOTE) && prompt.endsWith(QUOTE);

	return isQuoted ? prompt.slice(1, -1).trim() : prompt;
}

export function isSystemSession(session: { agent: string } | undefined): boolean {
	return session?.agent === SYSTEM_AGENT_NAME;
}
