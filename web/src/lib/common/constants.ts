export const API_BASE_URL = "/api/v1";
export const AUTHORIZATION_HEADER = "Authorization";
export const BEARER_PREFIX = "Bearer ";
export const SESSION_ID_HEADER = "X-Session-ID";
export const WEBSOCKET_PATH = "/ws/v1";
export const WEBSOCKET_PROTOCOL = "peen.v1";
export const WEBSOCKET_BEARER_PROTOCOL_PREFIX = "peen.bearer.";
export const WEBSOCKET_MESSAGE_SEND = "message.send";
// The global socket reconnects on an unexpected close with a doubling
// backoff and jitter, reset after a successful open.
export const RECONNECT_BASE_DELAY_MS = 500;
export const RECONNECT_MAX_DELAY_MS = 10_000;
export const RECONNECT_JITTER_RATIO = 0.2;
// GET /session/feed pages at up to 200 items per the API contract. A gap
// further behind than this is replayed by reloading the session instead of
// paging the whole way.
export const FEED_PAGE_LIMIT = 200;
export const FEED_RESYNC_THRESHOLD = 5000;
export const FEED_SEQUENCE_NONE = 0;
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
// Events delivered to a turn arrive as one block: a preamble for the model, an
// optional line counting dropped events, then one JSON event per line.
export const SESSION_EVENTS_OPEN = "<events>\n";
export const SESSION_EVENTS_CLOSE = "</events>";
export const SESSION_EVENT_OPEN = "<event>";
export const SESSION_EVENT_CLOSE = "</event>";
export const SESSION_EVENTS_DROPPED_LEAD = "Older events were dropped before these: ";
export const SESSION_EVENTS_TITLE = "Session events";
export const SESSION_EVENTS_DROPPED_LABEL = "Older events dropped: ";
export const SESSION_EVENT_UNREADABLE = "unreadable event";
export const SESSION_PAGE_LIMIT = 200;
// A link to the page can name one session to show after connecting. It never
// carries the API token.
export const SESSION_LINK_PARAMETER = "session";
export const SESSION_LINK_PATTERN =
	/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const SESSION_DETAIL_LIMIT = 100;
// Child agents. A message goes to the root conversation unless the composer
// names a running child. The root's own question to the user opens with a
// question.asked event and closes with question.answered.
export const AGENT_RUN_STATE_RUNNING = "running";
export const AGENT_RUN_ID_SHORT_LENGTH = 8;
export const SEND_TARGET_ROOT = "";
export const SEND_TARGET_ROOT_LABEL = "Root conversation";
export const EVENT_TYPE_QUESTION_ASKED = "question.asked";
export const EVENT_TYPE_QUESTION_ANSWERED = "question.answered";
export const EVENT_TYPE_NOTICE_CREATED = "notice.created";
export const AGENT_POLL_INTERVAL_MS = 3000;
// An agent space is where a workspace's agent runs: `control`, or a Docker
// profile. The label next to each one lists what it grants.
export const AGENT_SPACE_KIND_CONTROL = "control";
export const AGENT_SPACE_CONTROL_NOTE = "runs where Peen runs";
export const AGENT_SPACE_HOST_ROOT_NOTE = "host root";
export const AGENT_SPACE_SUDO_ON = "yes";
export const AGENT_SPACE_SUDO_OFF = "no";
export const DOCKER_ACCESS_FULL = "full";
export const AGENT_SPACE_DEFAULT_OPTION = "Controller default";
