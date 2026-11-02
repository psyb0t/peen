package agent

import (
	"context"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// ruleBlockTemplateText is how a scoped rule reaches the model, after the
	// result of the first file tool call that touched a matching file. The
	// body is the user's own configuration, so it is not escaped.
	//
	//nolint:lll // The opening tag is one line in the rendered block.
	ruleBlockTemplateText = `<rule name="{{attr .Name}}" source="{{attr .Source}}" paths="{{attr .Paths}}">
{{.Content}}
</rule>`

	ruleBlockTemplateName = "rule"
	ruleBlockOpening      = "<rule name=\""
	ruleSourceAttribute   = `source="`
	ruleAttributeEnd      = `"`
	rulePathsSeparator    = ", "
	hookBlockOpening      = "<hook>\n"

	// scopedRuleRefreshDivisor makes the refresh distance half the prompt
	// budget, which stays clear of any system prompt and tool list that fits
	// in the other half.
	scopedRuleRefreshDivisor = 2
)

// ruleTriggerTools are the tools that load a matching scoped rule, the
// counterparts of the Read, Write, and Edit tools Claude Code loads them on.
//
//nolint:gochecknoglobals // Immutable lookup table.
var ruleTriggerTools = []string{
	toolNameReadFile,
	toolNameWriteFile,
	toolNameEditFile,
	toolNameApplyPatch,
	toolNameMovePath,
}

type ruleBlockData struct {
	Name    string
	Source  string
	Paths   string
	Content string
}

// scopedRuleBlocks renders the scoped rules this tool call loads: the rules
// matching a path the call touched that the conversation does not already
// hold. Within one round, the first call that matches a rule delivers it.
func (h *toolHookRuntime) scopedRuleBlocks(
	ctx context.Context,
	event *elelem.ToolEvent,
) ([]string, error) {
	if !slices.Contains(ruleTriggerTools, event.Tool.Name) {
		return nil, nil
	}

	rules := h.snapshot.MatchingRules(
		h.paths(event.Tool.Name, event.RawArguments),
	)
	if len(rules) == 0 {
		return nil, nil
	}

	held, err := h.heldRules(event.Messages)
	if err != nil {
		return nil, err
	}

	for _, source := range h.rulesClaimedEarlierInRound(event) {
		held[source] = struct{}{}
	}

	blocks := make([]string, 0, len(rules))

	for _, rule := range rules {
		if _, isHeld := held[escapeAttribute(rule.Source)]; isHeld {
			continue
		}

		block, err := renderTemplate(
			ruleBlockTemplateName,
			ruleBlockTemplateText,
			ruleBlockData{
				Name:    rule.Name,
				Source:  rule.Source,
				Paths:   strings.Join(rule.Paths, rulePathsSeparator),
				Content: strings.TrimSpace(rule.Content),
			},
		)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "render scoped rule")
		}

		runtimecontext.Logger(ctx).Info(
			"scoped rule delivered after a tool result",
			"rule_name", rule.Name,
			"rule_source", rule.Source,
			"tool", event.Tool.Name,
		)

		blocks = append(blocks, block)
	}

	return blocks, nil
}

// rulesClaimedEarlierInRound lists the rule sources, escaped as they appear in
// a block, that a call before this one in the same assistant message matches.
// Calls in a round run concurrently, so the first call in the model's order
// delivers a rule and the others leave it out.
func (h *toolHookRuntime) rulesClaimedEarlierInRound(
	event *elelem.ToolEvent,
) []string {
	calls := roundToolCalls(event.Messages, event.CallID)
	claimed := make([]string, 0)

	for _, call := range calls {
		if call.ID == event.CallID {
			break
		}

		if !slices.Contains(ruleTriggerTools, call.Name) {
			continue
		}

		for _, rule := range h.snapshot.MatchingRules(
			h.paths(call.Name, call.Arguments),
		) {
			claimed = append(claimed, escapeAttribute(rule.Source))
		}
	}

	return claimed
}

// roundToolCalls returns the tool calls of the assistant message that made
// callID, or nothing when the messages do not hold it.
func roundToolCalls(
	messages []elelem.Message,
	callID string,
) []elelem.ToolCall {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != elelem.RoleAssistant {
			continue
		}

		for _, call := range message.ToolCalls {
			if call.ID == callID {
				return message.ToolCalls
			}
		}
	}

	return nil
}

// heldRules lists the escaped sources of the rules the model still has. A rule
// is held while a block for it is in the conversation and, when
// ruleRefreshTokens is set, fewer than that many tokens follow the block.
//
// The distance matters because the conversation a tool sees is not always the
// request the model gets. Under drop-oldest the request leaves out the oldest
// history while the saved conversation keeps it. A block that far back is past
// the distance, so the rule comes back. A rule far back in a long context is
// sent again too, where the model would otherwise give it less weight.
func (h *toolHookRuntime) heldRules(
	messages []elelem.Message,
) (map[string]struct{}, error) {
	held := map[string]struct{}{}

	for source, index := range ruleBlockPositions(messages) {
		if h.ruleRefreshTokens > 0 {
			following, err := h.tokenCounter.Count(messages[index:], nil)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "count tokens after rule block")
			}

			if following >= h.ruleRefreshTokens {
				continue
			}
		}

		held[source] = struct{}{}
	}

	return held, nil
}

// ruleBlockPositions maps the escaped source of every rule block in the
// messages Peen injected after tool results to the index of its latest block.
// Only a user message that starts with a hook or rule block counts, so text
// another agent or a client wrote cannot pass for a delivered rule.
func ruleBlockPositions(messages []elelem.Message) map[string]int {
	positions := map[string]int{}

	for index, message := range messages {
		if message.Role != elelem.RoleUser {
			continue
		}

		text := message.Text()
		if !strings.HasPrefix(text, ruleBlockOpening) &&
			!strings.HasPrefix(text, hookBlockOpening) {
			continue
		}

		for line := range strings.SplitSeq(text, "\n") {
			if source, found := ruleBlockSource(line); found {
				positions[source] = index
			}
		}
	}

	return positions
}

// ruleBlockSource reads the source attribute of a rule block's opening line.
func ruleBlockSource(line string) (string, bool) {
	if !strings.HasPrefix(line, ruleBlockOpening) {
		return "", false
	}

	_, rest, found := strings.Cut(line, ruleSourceAttribute)
	if !found {
		return "", false
	}

	source, _, found := strings.Cut(rest, ruleAttributeEnd)
	if !found {
		return "", false
	}

	return source, true
}
