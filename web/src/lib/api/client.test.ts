import { describe, expect, it } from "vitest";

import { requireData } from "./client";

describe("requireData", () => {
	it("preserves the controller's actionable error message", async () => {
		await expect(
			requireData(
				Promise.resolve({
					error: {
						code: "WORKSPACE_NOT_FOUND",
						message: "workspace directory does not exist",
					},
					response: new Response(null, { status: 404 }),
				}),
			),
		).rejects.toEqual(
			expect.objectContaining({
				message: "workspace directory does not exist",
				name: "PeenAPIError",
				status: 404,
			}),
		);
	});

	it("uses the generic message only when the controller sends none", async () => {
		await expect(
			requireData(
				Promise.resolve({
					error: { code: "WORKSPACE_NOT_FOUND" },
					response: new Response(null, { status: 404 }),
				}),
			),
		).rejects.toEqual(
			expect.objectContaining({
				message: "The controller rejected the request.",
				name: "PeenAPIError",
				status: 404,
			}),
		);
	});
});
