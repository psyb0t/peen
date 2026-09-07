package harness

import (
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/events"
	"go.yaml.in/yaml/v3" //nolint:depguard // Parses Peen's hook configuration.
)

const hookDocumentVersion = 1

type hookDocument struct {
	Version int                  `yaml:"version"`
	Events  map[string][]rawHook `yaml:",inline"`
}

type rawHook struct {
	Match   HookMatch    `yaml:"match"`
	Actions []HookAction `yaml:"actions"`
}

func (s *resolutionState) discoverHooks(
	agentsDirectory string,
	priority int,
	configLayer bool,
) error {
	source, content, found, err := s.readOptionalFile(
		filepath.Join(agentsDirectory, hooksFileName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read hook file")
	}

	if !found {
		return nil
	}

	hooks, err := parseHookDocument(content)
	if err != nil {
		return ctxerrors.Wrap(err, "validate hook file")
	}

	if len(s.hooks)+len(hooks) > s.limits.MaxHooks {
		return ctxerrors.Wrap(ErrResourceLimit, "hook limit exceeded")
	}

	contentHash := hashString(content)

	for index := range hooks {
		hooks[index].Source = source
		hooks[index].Priority = priority
		hooks[index].ConfigLayer = configLayer
		hooks[index].Hash = contentHash
	}

	s.hooks = append(s.hooks, hooks...)

	return nil
}

func parseHookDocument(content string) ([]Hook, error) {
	document := hookDocument{}
	decoder := yaml.NewDecoder(strings.NewReader(content))
	decoder.KnownFields(true)

	if err := decoder.Decode(&document); err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidHook, err),
			"decode hook YAML",
		)
	}

	var extraDocument any
	if err := decoder.Decode(&extraDocument); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, ctxerrors.Wrap(
				ErrInvalidHook,
				"hook file has multiple YAML documents",
			)
		}

		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidHook, err),
			"check hook YAML document count",
		)
	}

	if document.Version != hookDocumentVersion {
		return nil, ctxerrors.Wrapf(
			ErrInvalidHook,
			"hook version must be %d",
			hookDocumentVersion,
		)
	}

	return parseHookGroups(document.Events)
}

func parseHookGroups(hookGroups map[string][]rawHook) ([]Hook, error) {
	eventNames := sortedMapKeys(hookGroups)
	hooks := make([]Hook, 0)

	for _, eventName := range eventNames {
		event := HookEvent(eventName)
		if !validHookEvent(event) {
			return nil, ctxerrors.Wrapf(
				ErrInvalidHook,
				"unsupported hook event %q",
				eventName,
			)
		}

		for groupIndex, group := range hookGroups[eventName] {
			if err := validateHookMatch(group.Match); err != nil {
				return nil, ctxerrors.Wrapf(
					err,
					"validate %s hook group %d matcher",
					eventName,
					groupIndex,
				)
			}

			if len(group.Actions) == 0 {
				return nil, ctxerrors.Wrapf(
					ErrInvalidHook,
					"%s hook group %d has no actions",
					eventName,
					groupIndex,
				)
			}

			for actionIndex, action := range group.Actions {
				if err := validateHookAction(action); err != nil {
					return nil, ctxerrors.Wrapf(
						err,
						"validate %s hook group %d action %d",
						eventName,
						groupIndex,
						actionIndex,
					)
				}
			}

			actions := cloneHooks([]Hook{{Actions: group.Actions}})[0].Actions
			hooks = append(hooks, Hook{
				Event:   event,
				Match:   group.Match,
				Actions: actions,
			})
		}
	}

	return hooks, nil
}

func validHookEvent(event HookEvent) bool {
	switch event {
	case HookEventPreUserMessage,
		HookEventPostUserMessage,
		HookEventSessionStart,
		HookEventTurnStart,
		HookEventTurnStop,
		HookEventTurnCancelled,
		HookEventPreToolUse,
		HookEventPostToolUse,
		HookEventToolUseFailure,
		HookEventPreReadFile,
		HookEventPostReadFile,
		HookEventReadFileFailure,
		HookEventPreListFiles,
		HookEventPostListFiles,
		HookEventListFilesFailure,
		HookEventPreSearchText,
		HookEventPostSearchText,
		HookEventSearchTextFailure,
		HookEventPreWriteFile,
		HookEventPostWriteFile,
		HookEventWriteFileFailure,
		HookEventPreEditFile,
		HookEventPostEditFile,
		HookEventEditFileFailure,
		HookEventPreApplyPatch,
		HookEventPostApplyPatch,
		HookEventApplyPatchFailed,
		HookEventPreMovePath,
		HookEventPostMovePath,
		HookEventMovePathFailure,
		HookEventPreRemovePath,
		HookEventPostRemovePath,
		HookEventRemovePathFailed,
		HookEventPreMakeDirectory,
		HookEventPostMakeDirectory,
		HookEventMakeDirectoryFailure:
		return true
	default:
		return false
	}
}

//nolint:cyclop // Each action kind has distinct required configuration.
func validateHookAction(action HookAction) error {
	if err := validateHookMatch(action.When); err != nil {
		return ctxerrors.Wrap(err, "validate action matcher")
	}

	switch action.Type {
	case HookActionCommand:
		if strings.TrimSpace(action.Command) == "" {
			return ctxerrors.Wrap(
				ErrInvalidHook,
				"command action needs command",
			)
		}

		if action.TimeoutSeconds < 0 {
			return ctxerrors.Wrap(ErrInvalidHook, "command timeout is negative")
		}
	case HookActionInject:
		if strings.TrimSpace(action.Message) == "" {
			return ctxerrors.Wrap(ErrInvalidHook, "inject action needs message")
		}
	case HookActionEmitEvent:
		if err := events.ValidateType(action.EventType); err != nil {
			return ctxerrors.Wrap(err, "validate emitted event type")
		}

		if _, err := events.ValidateDelivery(action.Delivery); err != nil {
			return ctxerrors.Wrap(err, "validate emitted event delivery")
		}
	case HookActionDeny:
		if strings.TrimSpace(action.Reason) == "" {
			return ctxerrors.Wrap(ErrInvalidHook, "deny action needs reason")
		}
	default:
		return ctxerrors.Wrapf(
			ErrInvalidHook,
			"unsupported hook action %q",
			action.Type,
		)
	}

	if action.OnFailure != "" && action.OnFailure != HookFailureDeny &&
		action.OnFailure != HookFailureContinue {
		return ctxerrors.Wrap(ErrInvalidHook, "invalid action failure mode")
	}

	return nil
}

//nolint:cyclop // Every matcher dimension has independent validation rules.
func validateHookMatch(match HookMatch) error {
	if strings.TrimSpace(match.Tool) != "" &&
		strings.ContainsAny(match.Tool, "\t\n\r") {
		return ctxerrors.Wrap(
			ErrInvalidHook,
			"tool matcher contains whitespace",
		)
	}

	for _, extension := range match.Extensions {
		if len(extension) < 2 || !strings.HasPrefix(extension, ".") ||
			strings.ContainsRune(extension, filepath.Separator) {
			return ctxerrors.Wrap(ErrInvalidHook, "invalid extension matcher")
		}
	}

	for pointer, condition := range match.Input {
		if pointer != "" && !strings.HasPrefix(pointer, "/") {
			return ctxerrors.Wrap(
				ErrInvalidHook,
				"input matcher key is not a JSON Pointer",
			)
		}

		if condition.Exists == nil && condition.Equals == nil &&
			condition.Regex == "" {
			return ctxerrors.Wrap(
				ErrInvalidHook,
				"input matcher has no predicate",
			)
		}

		if condition.Regex != "" {
			if _, err := regexp.Compile(condition.Regex); err != nil {
				return ctxerrors.Wrap(err, "compile input matcher regex")
			}
		}
	}

	return nil
}
