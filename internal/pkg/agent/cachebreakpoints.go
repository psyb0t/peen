package agent

import (
	"github.com/psyb0t/elelem"
)

// markCacheBreakpoints marks where a provider that caches only what it is
// told to, such as Anthropic, may store the request prefix. A breakpoint
// covers everything before it, tools and system prompt included, so the last
// message caches the whole request for the next round. The first message is
// a stable anchor for turns whose later history changes, and the assistant
// message that opened the latest tool round keeps a breakpoint within reach
// when a round adds many results. Providers that cache on their own ignore
// the marks. They mark the request copy only: savedHistory clears them so a
// saved conversation never collects more than the provider allows.
func markCacheBreakpoints(history []elelem.Message) {
	if len(history) == 0 {
		return
	}

	last := len(history) - 1
	history[0].CacheHint = elelem.CacheHintShort
	history[last].CacheHint = elelem.CacheHintShort

	for index := last - 1; index > 0; index-- {
		if history[index].Role != elelem.RoleAssistant {
			continue
		}

		history[index].CacheHint = elelem.CacheHintShort

		return
	}
}
