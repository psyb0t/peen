package peen_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionContinuesAcrossTurnsAndCloses(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(
			elelemtest.Text("first"), elelemtest.Text("second"),
		)), Model: elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	subscription, err := session.Subscribe(context.Background())
	require.NoError(t, err)
	t.Cleanup(subscription.Close)
	for _, message := range []string{"first request", "second request"} {
		_, err := session.Send(context.Background(), peen.MessageRequest{Message: message}, nil)
		require.NoError(t, err)
		for {
			event := receiveEvent(t, subscription.Events)
			assert.Equal(t, session.ID(), event.SessionID)
			assert.NotEmpty(t, event.RequestID)
			if event.Type == "turn.completed" {
				break
			}
		}
	}
	assert.Zero(t, subscription.Dropped())
	require.NoError(t, runtime.Close(context.Background()))
	_, open := <-subscription.Events
	assert.False(t, open)
	subscription.Close()
}

func TestSubscriptionReceivesWakeTurn(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	writeTestFile(t, filepath.Join(options.ConfigDirectory, ".agents", "events", "app.updated.md"),
		"---\ntype: app.updated\ndelivery: wake\n---\nReview the update.")
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(elelemtest.Text("wake reply"))),
		Model:  elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	subscription, err := session.Subscribe(context.Background())
	require.NoError(t, err)
	t.Cleanup(subscription.Close)
	notice, err := session.PublishEvent(context.Background(), peen.NoticeRequest{
		Type: "app.updated", Summary: "new data", Delivery: "wake",
	})
	require.NoError(t, err)
	require.NotEmpty(t, notice.ID)
	for {
		event := receiveEvent(t, subscription.Events)
		assert.Equal(t, session.ID(), event.SessionID)
		if event.Type == "turn.completed" {
			break
		}
	}
	turns, err := session.Turns(context.Background(), peen.PageRequest{})
	require.NoError(t, err)
	require.Len(t, turns.Turns, 1)
	assert.Equal(t, peen.TurnStateCompleted, turns.Turns[0].State)
}

type heldChildDriver struct {
	*elelemtest.ScriptedDriver
	child   *elelemtest.ScriptedDriver
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *heldChildDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	for _, message := range request.Messages {
		if message.Role != elelem.RoleUser {
			continue
		}
		if !strings.Contains(message.Text(), "bounded child task") {
			break
		}
		d.once.Do(func() { close(d.started) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return elelem.Usage{}, ctx.Err()
		}

		return d.child.Stream(ctx, request, onDelta)
	}

	return d.ScriptedDriver.Stream(ctx, request, onDelta)
}

func TestSubscriptionReceivesChildAfterParentEnds(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	writeTestFile(t, filepath.Join(options.ConfigDirectory, ".agents", "events", "agent.finished.md"),
		"---\ntype: agent.finished\ndelivery: queue\n---\nQueue the child result.")
	driver := &heldChildDriver{
		ScriptedDriver: elelemtest.NewScriptedDriver(
			elelemtest.ToolCall("launch", "launchAgent", `{"agent":"default","task":"bounded child task"}`),
			elelemtest.Text("parent done"),
		),
		child:   elelemtest.NewScriptedDriver(elelemtest.Text("child done")),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(driver), Model: elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	subscription, err := session.Subscribe(context.Background())
	require.NoError(t, err)
	t.Cleanup(subscription.Close)
	result, err := session.Send(context.Background(), peen.MessageRequest{Message: "launch child"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "parent done", result.Message)
	select {
	case <-driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("child did not start")
	}
	waitForSubscriptionEvent(t, subscription.Events, "turn.completed")
	close(driver.release)
	for {
		event := receiveEvent(t, subscription.Events)
		assert.Equal(t, session.ID(), event.SessionID)
		if event.Type == "agent.run.completed" {
			break
		}
	}
	agents, err := session.Agents().List(context.Background(), peen.AgentListRequest{
		State: peen.AgentRunStateCompleted,
	})
	require.NoError(t, err)
	require.Len(t, agents.Agents, 1)
	assert.Equal(t, peen.AgentRunStateCompleted, agents.Agents[0].State)
}

func waitForSubscriptionEvent(t *testing.T, channel <-chan peen.Event, eventType string) peen.Event {
	t.Helper()
	for {
		event := receiveEvent(t, channel)
		if event.Type == eventType {
			return event
		}
	}
}

func TestSlowSubscriptionDropsWithoutBlockingAndCloses(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.EventLimits.SubscriberBuffer = 1
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(
			elelemtest.Text("completed without a reader"),
		)), Model: elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(context.Background()))
	})
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	subscription, err := session.Subscribe(context.Background())
	require.NoError(t, err)
	t.Cleanup(subscription.Close)
	ctx, cancel := context.WithTimeout(context.Background(), testWaitTimeout)
	t.Cleanup(cancel)
	result, err := session.Send(ctx,
		peen.MessageRequest{Message: "finish"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "completed without a reader", result.Message)
	assert.Positive(t, subscription.Dropped())
	page, err := session.Events(ctx, peen.PageRequest{})
	require.NoError(t, err)
	assert.Greater(t, len(page.Events), 1)
	subscription.Close()
	subscription.Close()
	assert.Equal(t, session.ID(), receiveEvent(t, subscription.Events).SessionID)
	_, open := <-subscription.Events
	assert.False(t, open)
}
