import { SESSION_LINK_PARAMETER, SESSION_LINK_PATTERN } from "$lib/common/constants";

// sessionFromLink returns the session ID a link such as `/?session=<uuid>`
// names, or "" when the link names none or names something that is not a
// session ID. The `peen` command prints such a link after it opens a
// workspace, so the page can show that session once it connects.
export function sessionFromLink(search: string): string {
	const sessionID = new URLSearchParams(search).get(SESSION_LINK_PARAMETER) ?? "";

	return SESSION_LINK_PATTERN.test(sessionID) ? sessionID.toLowerCase() : "";
}
