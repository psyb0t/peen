package telegrambot

import (
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/telegram"
)

func New(options Options) (*Engine, error) {
	if options.Client == nil || options.Runner == nil ||
		options.Sessions == nil || options.SessionReader == nil ||
		options.Store == nil || len(options.Policy.Chats) == 0 {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"Telegram controller dependencies")
	}

	canonical, err := options.Sessions.ResolveWorkspace(options.Workspace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate Telegram workspace")
	}

	options.Workspace = canonical
	options.Policy.Chats = maps.Clone(options.Policy.Chats)
	options.Policy.Users = maps.Clone(options.Policy.Users)
	options.Models = slices.Clone(options.Models)
	options = defaultLimits(options)

	return &Engine{
		options: options,
		active:  make(map[int64]context.CancelFunc),
	}, nil
}

func defaultLimits(options Options) Options {
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMessageBytes
	}

	if options.MaxSystemPromptBytes <= 0 {
		options.MaxSystemPromptBytes = defaultPromptBytes
	}

	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = maximumFileBytes
	}

	options.MaxFileBytes = min(options.MaxFileBytes, maximumFileBytes)
	if options.MaxImageBytes <= 0 {
		options.MaxImageBytes = defaultImageBytes
	}

	if options.MaxImages <= 0 {
		options.MaxImages = defaultImages
	}

	return options
}

func (e *Engine) Run(ctx context.Context) error {
	e.mutex.Lock()
	if e.closed || e.started {
		e.mutex.Unlock()

		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"Telegram controller already started or closed")
	}

	ctx, e.cancel = context.WithCancel(ctx)
	e.started = true
	e.workers.Add(1)

	e.mutex.Unlock()
	defer e.workers.Done()
	defer e.cancel()

	offset, err := e.skipQueuedUpdates(ctx)
	if err != nil {
		return err
	}

	for ctx.Err() == nil {
		updates, err := e.options.Client.GetUpdates(ctx, offset, pollSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			ctxscope.GetLogger(ctx).Warn("Telegram polling failed",
				"reason", "pollFailed", "err", err)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(retryDelay):
			}

			continue
		}

		offset = e.consumeUpdates(ctx, offset, updates)
	}

	return nil
}

func (e *Engine) skipQueuedUpdates(ctx context.Context) (int64, error) {
	updates, err := e.options.Client.GetUpdates(ctx, -1, 0)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "discard queued Telegram updates")
	}

	var offset int64
	for _, update := range updates {
		if update.ID >= offset && update.ID < math.MaxInt64 {
			offset = update.ID + 1
		}
	}

	return offset, nil
}

func (e *Engine) Close() {
	e.mutex.Lock()

	e.closed = true
	if e.cancel != nil {
		e.cancel()
	}

	for _, cancel := range e.active {
		cancel()
	}
	e.mutex.Unlock()
	e.workers.Wait()
}

func (e *Engine) allowed(message *telegram.Message) bool {
	if message == nil || message.ID <= 0 || message.From == nil ||
		message.From.IsBot {
		return false
	}

	if _, allowed := e.options.Policy.Chats[message.Chat.ID]; !allowed {
		return false
	}

	if len(e.options.Policy.Users) != 0 {
		_, allowed := e.options.Policy.Users[message.From.ID]

		return allowed
	}

	return message.From.ID > 0
}

// Handle admits at most one task per chat. Turns run outside the polling loop.
func (e *Engine) Handle(ctx context.Context, update telegram.Update) error {
	message := update.Message
	if !e.allowed(message) {
		return nil
	}

	if len(message.Text)+len(message.Caption) > e.options.MaxMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram message is too large")
	}

	command, _, _ := strings.Cut(strings.TrimSpace(message.Text), " ")

	e.mutex.Lock()
	if e.closed || ctx.Err() != nil {
		e.mutex.Unlock()

		return ctxerrors.Wrap(commerr.ErrCancelled, "Telegram is stopping")
	}

	cancel, busy := e.active[message.Chat.ID]
	if command == commandCancel || command == commandStatus {
		text := controlReply(command, cancel, busy)
		e.mutex.Unlock()

		return e.sendText(ctx, message.Chat.ID, text)
	}

	if busy {
		e.mutex.Unlock()

		return e.sendText(ctx, message.Chat.ID, replyBusy)
	}

	task, stop := context.WithCancel(ctx)
	e.active[message.Chat.ID] = stop
	e.workers.Add(1)

	e.mutex.Unlock()
	go e.process(task, stop, *message)

	return nil
}

func (e *Engine) process(
	ctx context.Context, cancel context.CancelFunc, message telegram.Message,
) {
	defer e.workers.Done()
	defer cancel()
	defer func() {
		e.mutex.Lock()
		delete(e.active, message.Chat.ID)
		e.mutex.Unlock()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error("Telegram task panicked",
				"reason", "taskPanicked")
		}
	}()

	if err := e.handleMessage(ctx, message); err != nil && ctx.Err() == nil {
		reply := replyFailed
		if errors.Is(err, session.ErrSessionBusy) ||
			errors.Is(err, commerr.ErrLockHeld) {
			reply = replyWorkspaceBusy
		} else {
			ctxscope.GetLogger(ctx).Warn("Telegram request failed",
				"reason", "requestFailed")
		}

		if err := e.sendText(ctx, message.Chat.ID, reply); err != nil {
			ctxscope.GetLogger(ctx).Warn("Telegram failure reply failed",
				"reason", "replyFailed", "err", err)
		}
	}
}

func (e *Engine) consumeUpdates(
	ctx context.Context, offset int64, updates []telegram.Update,
) int64 {
	for _, update := range updates {
		if update.ID < offset || update.ID < 0 || update.ID == math.MaxInt64 {
			continue
		}

		offset = update.ID + 1
		if err := e.Handle(ctx, update); err != nil && ctx.Err() == nil {
			ctxscope.GetLogger(ctx).Warn("Telegram update rejected",
				"reason", "updateRejected")
		}
	}

	return offset
}

func controlReply(
	command string, cancel context.CancelFunc, busy bool,
) string {
	if !busy {
		return replyIdle
	}

	if command == commandCancel {
		cancel()

		return replyCancelled
	}

	return replyBusy
}
