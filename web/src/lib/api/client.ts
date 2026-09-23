import createClient from "openapi-fetch";

import {
	API_BASE_URL,
	AUTHORIZATION_HEADER,
	BEARER_PREFIX,
} from "$lib/common/constants";

import type { components, paths } from "./generated";

export type PeenAPI = ReturnType<typeof createClient<paths>>;
export type APIErrorBody = components["schemas"]["Error"];

export class PeenAPIError extends Error {
	public readonly status: number;

	public constructor(status: number, message: string) {
		super(message);
		this.name = "PeenAPIError";
		this.status = status;
	}
}

export function createPeenAPI(token: string): PeenAPI {
	const trimmedToken = token.trim();
	const baseUrl = new URL(API_BASE_URL, window.location.origin).toString();
	const headers =
		trimmedToken === ""
			? undefined
			: { [AUTHORIZATION_HEADER]: BEARER_PREFIX + trimmedToken };

	return createClient<paths>({ baseUrl, headers });
}

export async function requireData<T>(
	result: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
	const { data, error, response } = await result;
	if (data !== undefined) {
		return data;
	}

	throw new PeenAPIError(response.status, errorMessage(error));
}

function errorMessage(error: unknown): string {
	if (typeof error === "string" && error !== "") {
		return error;
	}

	if (typeof error === "object" && error !== null && "message" in error) {
		const message = error.message;
		if (typeof message === "string" && message !== "") {
			return message;
		}
	}

	return "The controller rejected the request.";
}
