package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	directoryToolUnavailable = "tool is unavailable in the current directory"
	directoryHopBatchError   = "call changeDirectory alone in its tool round"
)

type directoryCall struct {
	tool    elelem.Tool
	release func()
}

func (s *directoryScope) provideTools(
	ctx context.Context,
) (*elelem.ToolSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "provide directory tools")
	}

	if s.context() != s.applied {
		return nil, errDirectoryChanged
	}

	registered := s.context().tools.Definitions()
	for index := range registered {
		tool := &registered[index]
		tool.PreRun = s.beforeTool
		tool.Handler = s.handleTool
		tool.OnSuccess = s.toolHook
		tool.OnError = s.toolHook
		tool.PostRun = s.toolHook
		tool.OnSuccessMessageInjector = s.toolInjection
		tool.OnErrorMessageInjector = s.toolInjection
		tool.PostRunMessageInjector = s.toolInjection
	}

	return elelem.NewToolSet(registered...), nil
}

//nolint:nonamedreturns // Panic recovery must return its captured error.
func (s *directoryScope) beforeTool(
	ctx context.Context,
	event *elelem.ToolEvent,
) (err error) {
	isHop := event.Tool.Name == toolNameChangeDirectory

	release, err := s.gate.acquire(ctx, isHop)
	if err != nil {
		return ctxerrors.Wrap(err, "acquire tool directory scope")
	}

	selected, found := s.context().tools.Get(event.Tool.Name)
	if !found {
		selected = elelem.Tool{
			Name:    event.Tool.Name,
			Handler: unavailableDirectoryTool,
		}
	}

	if isHop && hasBatchedDirectoryHop(event.Messages) {
		selected.Handler = batchedDirectoryTool
	}

	s.callsMutex.Lock()
	if _, exists := s.calls[event.CallID]; exists {
		s.callsMutex.Unlock()
		release()

		return ctxerrors.Wrap(commerr.ErrConflict, "duplicate active tool call")
	}

	s.calls[event.CallID] = directoryCall{tool: selected, release: release}

	s.callsMutex.Unlock()
	defer s.recoverHook(ctx, event, &err)

	event.Tool = selected
	if selected.PreRun == nil {
		return nil
	}

	if err := selected.PreRun(ctx, event); err != nil {
		s.finishTool(event.CallID)

		return ctxerrors.Wrap(err, "run current directory pre-hook")
	}

	return nil
}

func (s *directoryScope) activeTool(callID string) (elelem.Tool, error) {
	s.callsMutex.Lock()
	defer s.callsMutex.Unlock()

	call, found := s.calls[callID]
	if !found {
		return elelem.Tool{}, ctxerrors.Wrap(
			commerr.ErrInvalidState, "tool directory scope is missing",
		)
	}

	return call.tool, nil
}

func (s *directoryScope) handleTool(
	ctx context.Context,
	input elelem.ToolInput,
) (elelem.ToolResult, error) {
	selected, err := s.activeTool(input.CallID)
	if err != nil {
		return elelem.ToolResult{}, ctxerrors.Wrap(err, "resolve current tool")
	}

	if selected.Handler == nil {
		return elelem.NewToolErrorResult(directoryToolUnavailable), nil
	}

	result, err := selected.Handler(ctx, input)
	if err != nil {
		return result, ctxerrors.Wrap(err, "run current directory tool")
	}

	return result, nil
}

func (s *directoryScope) resultDirectory(callID, fallback string) string {
	s.callsMutex.Lock()
	defer s.callsMutex.Unlock()

	directory, found := s.results[callID]
	delete(s.results, callID)

	if found {
		return directory
	}

	return fallback
}

//nolint:nonamedreturns // Panic recovery must return its captured error.
func (s *directoryScope) toolHook(
	ctx context.Context,
	event *elelem.ToolEvent,
) (err error) {
	defer s.recoverHook(ctx, event, &err)

	selected, err := s.activeTool(event.CallID)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve directory hook")
	}

	var hook elelem.ToolHook

	switch event.Phase {
	case elelem.ToolPhaseOnSuccess:
		hook = selected.OnSuccess
	case elelem.ToolPhaseOnError:
		hook = selected.OnError
	case elelem.ToolPhasePostRun:
		hook = selected.PostRun
	}

	if hook == nil {
		return nil
	}

	if err := hook(ctx, event); err != nil {
		s.finishTool(event.CallID)

		return ctxerrors.Wrap(err, "run directory tool hook")
	}

	return nil
}

//nolint:nonamedreturns // Panic recovery must return its captured error.
func (s *directoryScope) toolInjection(
	ctx context.Context,
	event *elelem.ToolEvent,
) (injection *elelem.MessageInjection, err error) {
	if event.Phase == elelem.ToolPhasePostRun {
		defer func() {
			if err == nil {
				err = s.commitDirectory(ctx, event)
			}

			s.finishTool(event.CallID)
		}()
	}
	defer s.recoverHook(ctx, event, &err)

	selected, err := s.activeTool(event.CallID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve directory injection")
	}

	var injector elelem.MessageInjector

	switch event.Phase {
	case elelem.ToolPhaseOnSuccess:
		injector = selected.OnSuccessMessageInjector
	case elelem.ToolPhaseOnError:
		injector = selected.OnErrorMessageInjector
	case elelem.ToolPhasePostRun:
		injector = selected.PostRunMessageInjector
	}

	if injector == nil {
		return nil, nil //nolint:nilnil // No injector adds no message.
	}

	injection, err = injector(ctx, event)
	if err != nil {
		s.finishTool(event.CallID)

		return nil, ctxerrors.Wrap(err, "inject directory tool context")
	}

	return injection, nil
}

func (s *directoryScope) finishTool(callID string) {
	s.callsMutex.Lock()
	call, found := s.calls[callID]
	delete(s.calls, callID)
	s.callsMutex.Unlock()

	if found {
		if call.tool.Name == toolNameChangeDirectory {
			s.mutex.Lock()
			s.pending = nil
			s.mutex.Unlock()
		}

		call.release()
	}
}

func (s *directoryScope) commitDirectory(
	ctx context.Context,
	event *elelem.ToolEvent,
) error {
	if event.Tool.Name != toolNameChangeDirectory ||
		event.Result == nil || event.Result.IsError {
		return nil
	}

	s.mutex.RLock()
	candidate := s.pending
	s.mutex.RUnlock()

	if candidate == nil {
		return nil
	}

	if err := s.notifyWarnings(ctx, candidate.snapshot.Warnings()); err != nil {
		return ctxerrors.Wrap(err, "report accepted scope warnings")
	}

	if err := s.publishContext(ctx, candidate); err != nil {
		return ctxerrors.Wrap(err, "publish accepted directory scope")
	}

	s.callsMutex.Lock()
	s.results[event.CallID] = candidate.executor.Workspace()
	s.callsMutex.Unlock()
	runtimecontext.Logger(ctx).Info(
		"agent directory changed",
		"directory", candidate.executor.Workspace(),
		"harness_hash", candidate.snapshot.Hash(),
	)

	return nil
}

func (s *directoryScope) recoverHook(
	ctx context.Context,
	event *elelem.ToolEvent,
	err *error,
) {
	if recovered := recover(); recovered != nil {
		s.finishTool(event.CallID)
		runtimecontext.Logger(ctx).Error(
			"directory tool hook panicked",
			"tool", event.Tool.Name,
		)

		*err = ctxerrors.New("directory tool hook panicked")
	}
}

func hasBatchedDirectoryHop(messages []elelem.Message) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == elelem.RoleAssistant &&
			len(messages[index].ToolCalls) > 0 {
			return len(messages[index].ToolCalls) > 1
		}
	}

	return false
}

func unavailableDirectoryTool(
	context.Context,
	elelem.ToolInput,
) (elelem.ToolResult, error) {
	return elelem.NewToolErrorResult(directoryToolUnavailable), nil
}

func batchedDirectoryTool(
	context.Context,
	elelem.ToolInput,
) (elelem.ToolResult, error) {
	return elelem.NewToolErrorResult(directoryHopBatchError), nil
}
