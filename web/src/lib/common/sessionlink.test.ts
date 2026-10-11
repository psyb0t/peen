import { describe, expect, it } from "vitest";

import { sessionFromLink } from "$lib/common/sessionlink";

describe("sessionFromLink", () => {
	it.each([
		[
			"?session=22222222-2222-4222-8222-222222222222",
			"22222222-2222-4222-8222-222222222222",
		],
		[
			"?session=ABCDEF01-2222-4222-8222-222222222222",
			"abcdef01-2222-4222-8222-222222222222",
		],
		[
			"?other=1&session=22222222-2222-4222-8222-222222222222",
			"22222222-2222-4222-8222-222222222222",
		],
		["", ""],
		["?session=", ""],
		["?session=not-a-session", ""],
		["?session=22222222-2222-4222-8222-222222222222x", ""],
		["?session=../v1/sessions", ""],
		["?session=%2Fv1%2Fsessions", ""],
	])("reads %j as %j", (search, expected) => {
		expect(sessionFromLink(search)).toBe(expected);
	});
});
