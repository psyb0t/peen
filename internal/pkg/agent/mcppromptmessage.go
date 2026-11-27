package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// mcpPromptCommandPrefix starts a user message that invokes an MCP
	// prompt: /mcp:<server>:<prompt> name=value ..., on the message's first
	// line. The lines after it are the user's own text.
	mcpPromptCommandPrefix    = "/mcp:"
	mcpPromptNameSeparator    = ":"
	mcpPromptArgumentAssigner = "="
	mcpPromptQuote            = '"'
	mcpPromptEscape           = '\\'
	mcpPromptLineSeparator    = "\n"
	mcpPromptTextSeparator    = "\n\n"
)

// expandMCPPrompt replaces the /mcp:<server>:<prompt> line a user message
// starts with by the prompt the server fills in, as an <mcpPrompt> block
// followed by the rest of the message. The expanded text is the message the
// turn stores and the model reads, so a replay never asks the server again.
// A deferred message was expanded when it was first accepted.
//
// An unknown server or prompt, arguments the server refuses, or an expanded
// message over PEEN_MAX_MESSAGE_BYTES fail the message with a validation
// error before anything is stored.
func (r *Runtime) expandMCPPrompt(
	ctx context.Context,
	input TurnRequest,
) (TurnRequest, error) {
	if deferredRootFromContext(ctx) != nil ||
		!strings.HasPrefix(input.Message, mcpPromptCommandPrefix) {
		return input, nil
	}

	line, rest, _ := strings.Cut(input.Message, mcpPromptLineSeparator)

	server, name, arguments, err := parseMCPPromptCommand(line)
	if err != nil {
		return input, err
	}

	if !slices.Contains(r.mcp.PromptServers(), server) {
		return input, ctxerrors.Wrapf(commerr.ErrValidationFailed,
			"no running MCP server named %q offers prompts", server)
	}

	result, err := r.mcp.GetPrompt(ctx, server, name, arguments)
	if err != nil {
		if isTurnEndingError(err) {
			return input, ctxerrors.Wrap(err, "get MCP prompt")
		}

		return input, ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, err),
			"get MCP prompt")
	}

	block, _, err := renderMCPPrompt(server, name, result, false)
	if err != nil {
		return input, err
	}

	expanded := block
	if strings.TrimSpace(rest) != "" {
		expanded += mcpPromptTextSeparator + rest
	}

	if len(expanded) > r.maxMessageBytes {
		return input, ctxerrors.Wrapf(
			errors.Join(commerr.ErrValidationFailed, ErrMessageTooLarge),
			"the message with MCP prompt %q is %d bytes, over the %d byte "+
				"limit", name, len(expanded), r.maxMessageBytes)
	}

	runtimecontext.Logger(ctx).Info("MCP prompt expanded",
		"server", server,
		"prompt", name,
		"argument_count", len(arguments),
		"message_bytes", len(expanded),
	)

	input.Message = expanded

	return input, nil
}

// parseMCPPromptCommand reads /mcp:<server>:<prompt> and the name=value
// arguments after it. A value with spaces is a double-quoted JSON string.
func parseMCPPromptCommand(
	line string,
) (string, string, map[string]string, error) {
	command := strings.TrimPrefix(strings.TrimSpace(line),
		mcpPromptCommandPrefix)
	target, rawArguments, _ := strings.Cut(command, " ")

	server, name, found := strings.Cut(target, mcpPromptNameSeparator)
	if !found || server == "" || name == "" {
		return "", "", nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"an MCP prompt is invoked as /mcp:<server>:<prompt> "+
				"name=value ...")
	}

	arguments, err := parseMCPPromptArguments(rawArguments)
	if err != nil {
		return "", "", nil, err
	}

	return server, name, arguments, nil
}

func parseMCPPromptArguments(text string) (map[string]string, error) {
	arguments := map[string]string{}
	rest := strings.TrimLeftFunc(text, unicode.IsSpace)

	for rest != "" {
		name, value, found := strings.Cut(rest, mcpPromptArgumentAssigner)
		if !found || name == "" || strings.ContainsFunc(name, unicode.IsSpace) {
			return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"an MCP prompt argument is name=value or "+
					`name="a quoted value"`)
		}

		parsed, remaining, err := parseMCPPromptValue(value)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "argument %q", name)
		}

		if _, repeated := arguments[name]; repeated {
			return nil, ctxerrors.Wrapf(commerr.ErrValidationFailed,
				"argument %q is given twice", name)
		}

		arguments[name] = parsed
		rest = strings.TrimLeftFunc(remaining, unicode.IsSpace)
	}

	return arguments, nil
}

// parseMCPPromptValue reads one value: a double-quoted JSON string, or the
// text up to the next space.
func parseMCPPromptValue(text string) (string, string, error) {
	if text == "" || text[0] != mcpPromptQuote {
		end := strings.IndexFunc(text, unicode.IsSpace)
		if end < 0 {
			return text, "", nil
		}

		return text[:end], text[end:], nil
	}

	for index := 1; index < len(text); index++ {
		switch text[index] {
		case mcpPromptEscape:
			index++
		case mcpPromptQuote:
			var value string
			if err := json.Unmarshal([]byte(text[:index+1]),
				&value); err != nil {
				return "", "", ctxerrors.Wrap(
					errors.Join(commerr.ErrValidationFailed, err),
					"decode quoted value")
			}

			return value, text[index+1:], nil
		}
	}

	return "", "", ctxerrors.Wrap(commerr.ErrValidationFailed,
		"a quoted value has no closing quote")
}
