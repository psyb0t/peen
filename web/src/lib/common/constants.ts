export const API_BASE_URL = "/v1";
export const AUTHORIZATION_HEADER = "Authorization";
export const BEARER_PREFIX = "Bearer ";
export const SESSION_ID_HEADER = "X-Session-ID";
export const WEBSOCKET_PATH = "/v1/ws";
export const WEBSOCKET_PROTOCOL = "peen.v1";
export const WEBSOCKET_BEARER_PROTOCOL_PREFIX = "peen.bearer.";
export const WEBSOCKET_MESSAGE_SEND = "message.send";
export const REASONING_EFFORTS = [
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;
export const DEFAULT_REASONING_EFFORT = "medium";
export const MAX_LIVE_EVENTS = 200;
export const INJECTED_KIND_SKILL = "skill";
// A skill message starts with its name tag. The note that a skill is already
// loaded is a note that names the skill in its first sentence.
export const SKILL_MESSAGE_NAME_PATTERN = /^<skill>\s*<name>([^<\s]+)<\/name>/;
export const SKILL_NOTE_NAME_PATTERN = /^<note>Skill (\S+) is already loaded/;
export const SKILL_FALLBACK_NAME = "skill";
// A turn an event handler started opens with its handler block: the event
// type, an optional agent, a blank line, then the instructions.
export const EVENT_HANDLER_OPEN = "<handler>\n<event>";
export const EVENT_HANDLER_TYPE_CLOSE = "</event>\n";
export const EVENT_HANDLER_AGENT_OPEN = "<agent>";
export const EVENT_HANDLER_AGENT_CLOSE = "</agent>\n";
export const EVENT_HANDLER_BODY_GAP = "\n";
export const EVENT_HANDLER_CLOSE = "\n</handler>";
export const SESSION_PAGE_LIMIT = 200;
export const SESSION_DETAIL_LIMIT = 100;
