package telegrambot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/telegram"
	"github.com/psyb0t/peen/internal/pkg/telegramstore"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	mutex     sync.Mutex
	messages  []telegram.Message
	documents [][]byte
	downloads map[string][]byte
	polls     chan int64
	queued    []telegram.Update
}

func (f *fakeClient) GetUpdates(
	ctx context.Context, offset int64, _ int,
) ([]telegram.Update, error) {
	if f.polls != nil {
		select {
		case f.polls <- offset:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if offset == -1 {
		return f.queued, nil
	}
	<-ctx.Done()

	return nil, ctx.Err()
}

func (f *fakeClient) SendMessage(
	_ context.Context, id int64, text string,
) (telegram.Message, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	message := telegram.Message{
		ID:   int64(len(f.messages) + 1),
		Chat: telegram.Chat{ID: id}, Text: text,
	}
	f.messages = append(f.messages, message)

	return message, nil
}

func (f *fakeClient) SendDocument(
	_ context.Context, _ int64, _ string, data []byte,
) (telegram.Message, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.documents = append(f.documents, append([]byte(nil), data...))

	return telegram.Message{}, nil
}

func (f *fakeClient) Download(
	_ context.Context, id string, _ int64,
) ([]byte, error) {
	return f.downloads[id], nil
}

type botFixture struct {
	handle    *db.Handle
	engine    *Engine
	client    *fakeClient
	store     *telegramstore.Store
	turns     *session.Store
	registry  *control.Registry
	workspace string
	seen      chan control.ConversationRequest
	block     bool
}

func newBotFixture(t *testing.T) *botFixture {
	t.Helper()
	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	turns, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	store, err := telegramstore.New(handle)
	require.NoError(t, err)
	workspace := t.TempDir()
	policy, err := control.NewWorkspacePolicy([]string{workspace})
	require.NoError(t, err)
	spaces, err := worker.NewAgentSpaceSet([]worker.AgentSpace{
		{Name: "control", Kind: worker.KindControl},
	}, "control")
	require.NoError(t, err)
	registry, err := control.NewRegistry(control.RegistryOptions{
		Store: turns, Policy: policy, AgentSpaces: spaces,
		RootAgent: "core:general-purpose", DefaultModel: "example/model",
	})
	require.NoError(t, err)
	fixture := &botFixture{
		handle: handle,
		client: &fakeClient{downloads: make(map[string][]byte)},
		store:  store, turns: turns, registry: registry, workspace: workspace,
		seen: make(chan control.ConversationRequest, 32),
	}
	engine, err := New(Options{
		Client: fixture.client, Runner: fixture, Sessions: registry,
		SessionReader: turns, Store: store, Workspace: workspace,
		AgentSpace: "control", Models: []string{"example/model"},
		Policy: config.TelegramPolicy{
			Chats: map[int64]struct{}{-123: {}, -456: {}},
			Users: map[int64]struct{}{7: {}},
		},
	})
	require.NoError(t, err)
	fixture.engine = engine
	t.Cleanup(engine.Close)

	return fixture
}

func (f *botFixture) Run(ctx context.Context,
	input control.ConversationRequest,
) (*control.ConversationResult, error) {
	f.seen <- input
	if f.block {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	return &control.ConversationResult{
		SessionID: input.SessionID,
		Text:      "Answer", State: api.TurnStateCompleted,
	}, nil
}

func chatUpdate(text string) telegram.Update {
	return telegram.Update{ID: 10, Message: &telegram.Message{
		ID: 42, Chat: telegram.Chat{ID: -123}, From: &telegram.User{ID: 7},
		Text: text,
	}}
}

func waitBotIdle(t *testing.T, e *Engine) {
	t.Helper()
	require.Eventually(t, func() bool {
		e.mutex.Lock()
		defer e.mutex.Unlock()

		return len(e.active) == 0
	}, 5*time.Second, time.Millisecond)
}

func runUpdate(t *testing.T, fixture *botFixture, update telegram.Update) {
	t.Helper()
	require.NoError(t, fixture.engine.Handle(t.Context(), update))
	waitBotIdle(t, fixture.engine)
}

func receivedRequest(t *testing.T, fixture *botFixture) control.ConversationRequest {
	t.Helper()
	select {
	case input := <-fixture.seen:
		return input
	case <-time.After(5 * time.Second):
		t.Fatal("Telegram request did not reach runner")

		return control.ConversationRequest{}
	}
}
