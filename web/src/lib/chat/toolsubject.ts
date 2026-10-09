// The argument that says what each tool acted on, shown next to its name so a
// card reads "use_skill: release-notes" without opening it.
const SUBJECT_ARGUMENT: ReadonlyMap<string, string> = new Map([
	["use_skill", "name"],
	["run_command", "command"],
	["launch_agent", "agent"],
	["read_file", "path"],
	["write_file", "path"],
	["edit_file", "path"],
	["list_files", "path"],
	["search_text", "pattern"],
	["remove_path", "path"],
	["make_directory", "path"],
]);

const MOVE_PATH = "move_path";
const MOVE_SEPARATOR = " → ";
const MAX_SUBJECT_LENGTH = 80;
const ELLIPSIS = "…";
const WHITESPACE_RUN = /\s+/g;

/**
 * toolSubject returns the one argument that names what a tool call acted on,
 * flattened to one line and shortened, or "" when the tool has none or its
 * arguments cannot be read.
 */
export function toolSubject(name: string, argumentsJSON: string): string {
	const input = parseArguments(argumentsJSON);
	if (input === undefined) {
		return "";
	}

	if (name === MOVE_PATH) {
		const source = text(input.get("source"));
		const destination = text(input.get("destination"));

		return source === "" || destination === ""
			? ""
			: shorten(source + MOVE_SEPARATOR + destination);
	}

	const key = SUBJECT_ARGUMENT.get(name);

	return key === undefined ? "" : shorten(text(input.get(key)));
}

function parseArguments(argumentsJSON: string): Map<string, unknown> | undefined {
	try {
		const parsed: unknown = JSON.parse(argumentsJSON);

		return typeof parsed === "object" && parsed !== null
			? new Map(Object.entries(parsed))
			: undefined;
	} catch {
		// Arguments still streaming in, or a model that sent broken JSON: the
		// card shows the raw text in its body, so the label just goes without.
		return undefined;
	}
}

function text(value: unknown): string {
	return typeof value === "string" ? value.replace(WHITESPACE_RUN, " ").trim() : "";
}

function shorten(value: string): string {
	return value.length > MAX_SUBJECT_LENGTH
		? value.slice(0, MAX_SUBJECT_LENGTH) + ELLIPSIS
		: value;
}
