export function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Returns data[name] when data is a record and that field is a string. */
export function stringField(data: unknown, name: string): string | undefined {
	if (!isRecord(data)) {
		return undefined;
	}

	const value = data[name];

	return typeof value === "string" ? value : undefined;
}

export function formatJSON(value: unknown): string {
	try {
		return JSON.stringify(value, null, 2) ?? "null";
	} catch {
		return "[not serializable]";
	}
}
