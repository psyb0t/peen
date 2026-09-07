package harness

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testEventHandlerBody           = "Investigate the report and act on it."
	testEventHandlerDocumentFormat = `---
type: %s
%s---
%s`
)

func TestDiscoverEventHandlersResolvesLayeredFiles(t *testing.T) {
	t.Parallel()

	t.Run("resolved from config root", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t,
			fixture.configRoot,
			"app.error",
			"incident-responder",
			events.DeliveryWake,
			testEventHandlerBody,
		)

		snapshot := resolveFixture(t, fixture)

		require.Len(t, snapshot.EventHandlers(), 1)
		handler := snapshot.EventHandlers()[0]
		assert.Equal(t, "app.error", handler.Type)
		assert.Equal(t, "incident-responder", handler.Agent)
		assert.Equal(t, events.DeliveryWake, handler.Delivery)
		assert.Equal(t, testEventHandlerBody, handler.Instructions)
		assert.NotEmpty(t, handler.Source)
		assert.NotEmpty(t, handler.Hash)

		resolved, err := snapshot.EventHandler("app.error")
		require.NoError(t, err)
		assert.Equal(t, handler, resolved)

		assert.Contains(t, manifestKinds(snapshot.Manifest()),
			SourceKindEventHandler)
	})

	t.Run("workspace layer replaces config root handler", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t,
			fixture.configRoot,
			"app.error",
			"",
			"",
			"config instructions",
		)
		fixture.writeEventHandler(
			t,
			fixture.workspace,
			"app.error",
			"incident-responder",
			events.DeliveryWake,
			"workspace instructions",
		)

		snapshot := resolveFixture(t, fixture)

		require.Len(t, snapshot.EventHandlers(), 1)
		handler := snapshot.EventHandlers()[0]
		assert.Equal(t, "incident-responder", handler.Agent)
		assert.Equal(t, events.DeliveryWake, handler.Delivery)
		assert.Equal(t, "workspace instructions", handler.Instructions)
	})

	t.Run("several handler types coexist", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t, fixture.configRoot, "app.error", "", "", testEventHandlerBody,
		)
		fixture.writeEventHandler(
			t, fixture.configRoot, "ci.failed", "", "", testEventHandlerBody,
		)
		fixture.writeEventHandler(
			t,
			fixture.configRoot,
			"deploy.finished",
			"",
			"",
			testEventHandlerBody,
		)

		snapshot := resolveFixture(t, fixture)

		assert.ElementsMatch(
			t,
			[]string{"app.error", "ci.failed", "deploy.finished"},
			eventHandlerTypes(snapshot.EventHandlers()),
		)
	})

	t.Run("dotted multi-segment type resolves", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t,
			fixture.configRoot,
			"app.error.fatal",
			"",
			"",
			testEventHandlerBody,
		)

		snapshot := resolveFixture(t, fixture)

		require.Len(t, snapshot.EventHandlers(), 1)
		assert.Equal(t, "app.error.fatal", snapshot.EventHandlers()[0].Type)
	})

	t.Run("optional agent and delivery default to queue", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t, fixture.configRoot, "app.error", "", "", testEventHandlerBody,
		)

		snapshot := resolveFixture(t, fixture)

		require.Len(t, snapshot.EventHandlers(), 1)
		handler := snapshot.EventHandlers()[0]
		assert.Empty(t, handler.Agent)
		assert.Equal(t, events.DeliveryQueue, handler.Delivery)
	})

	t.Run("missing events directory is not an error", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)

		snapshot := resolveFixture(t, fixture)

		assert.Empty(t, snapshot.EventHandlers())
	})

	t.Run("non-md files are ignored", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t, fixture.configRoot, "app.error", "", "", testEventHandlerBody,
		)
		writeFile(
			t,
			filepath.Join(
				fixture.configRoot,
				agentsDirectoryName,
				eventHandlersSubdirectory,
				"notes.txt",
			),
			"ignore me",
		)

		snapshot := resolveFixture(t, fixture)

		require.Len(t, snapshot.EventHandlers(), 1)
		assert.Equal(t, "app.error", snapshot.EventHandlers()[0].Type)
	})

	t.Run("event handlers never reach prompt blocks", func(t *testing.T) {
		t.Parallel()

		fixture := newResolverFixture(t)
		fixture.writeEventHandler(
			t, fixture.configRoot, "app.error", "", "", testEventHandlerBody,
		)

		snapshot := resolveFixture(t, fixture)

		blocks, err := snapshot.PromptBlocks("")
		require.NoError(t, err)

		for _, block := range blocks {
			assert.NotEqual(t, SourceKindEventHandler, block.Kind)
		}
	})
}

func TestDiscoverEventHandlersRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "type does not match file name",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					eventHandlerDocument(
						"other.type",
						"",
						testEventHandlerBody,
					),
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "missing type field",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					"---\nagent: incident-responder\n---\n"+
						testEventHandlerBody,
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "empty body",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					"---\ntype: app.error\n---\n   ",
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "unknown frontmatter key",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					"---\ntype: app.error\nmodel: gpt-4\n---\n"+
						testEventHandlerBody,
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "malformed yaml",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					"---\ntype: [unterminated\n---\n"+testEventHandlerBody,
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "malformed frontmatter delimiters",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app.error.md",
					"type: app.error\n"+testEventHandlerBody,
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "invalid event type",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandlerNamed(
					t,
					fixture.configRoot,
					"app_error.md",
					eventHandlerDocument(
						"app_error",
						"",
						testEventHandlerBody,
					),
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
		{
			name: "invalid delivery mode",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandler(
					t,
					fixture.configRoot,
					"app.error",
					"",
					"sync",
					testEventHandlerBody,
				)
			},
			wantErr: ErrInvalidEventHandler,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)
			resolver, err := NewResolver(fixture.configRoot, Limits{})
			require.NoError(t, err)
			_, err = resolver.Resolve(fixture.workspace)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestDiscoverEventHandlersEnforcesMaxEventHandlers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "replacement of the same type is accepted",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandler(
					t,
					fixture.configRoot,
					"app.error",
					"",
					"",
					"config instructions",
				)
				fixture.writeEventHandler(
					t,
					fixture.workspace,
					"app.error",
					"",
					"",
					"workspace instructions",
				)
			},
		},
		{
			name: "a second effective type is rejected",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeEventHandler(
					t,
					fixture.configRoot,
					"app.error",
					"",
					"",
					testEventHandlerBody,
				)
				fixture.writeEventHandler(
					t,
					fixture.workspace,
					"ci.failed",
					"",
					"",
					testEventHandlerBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)

			limits := defaultLimits()
			limits.MaxEventHandlers = 1

			resolver, err := NewResolver(fixture.configRoot, limits)
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestSnapshotEventHandlerNotFound(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeEventHandler(
		t, fixture.configRoot, "app.error", "", "", testEventHandlerBody,
	)

	snapshot := resolveFixture(t, fixture)

	_, err := snapshot.EventHandler("ci.failed")
	require.ErrorIs(t, err, ErrEventHandlerNotFound)
}

func TestEventHandlerChangeChangesSnapshotHash(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeEventHandler(
		t, fixture.configRoot, "app.error", "", "", "first instructions",
	)

	first := resolveFixture(t, fixture)

	fixture.writeEventHandler(
		t, fixture.configRoot, "app.error", "", "", "second instructions",
	)

	second := resolveFixture(t, fixture)

	assert.NotEqual(t, first.Hash(), second.Hash())
}

func eventHandlerDocument(
	eventType string,
	extraFrontMatter string,
	body string,
) string {
	return fmt.Sprintf(
		testEventHandlerDocumentFormat,
		eventType,
		extraFrontMatter,
		body,
	)
}

func extraEventHandlerFrontMatter(agent string, delivery string) string {
	switch {
	case agent != "" && delivery != "":
		return fmt.Sprintf("agent: %s\ndelivery: %s\n", agent, delivery)
	case agent != "":
		return fmt.Sprintf("agent: %s\n", agent)
	case delivery != "":
		return fmt.Sprintf("delivery: %s\n", delivery)
	default:
		return ""
	}
}

func (f resolverFixture) writeEventHandler(
	t *testing.T,
	directory string,
	eventType string,
	agent string,
	delivery string,
	body string,
) {
	t.Helper()

	f.writeEventHandlerNamed(
		t,
		directory,
		eventType+agentsFileExtension,
		eventHandlerDocument(
			eventType,
			extraEventHandlerFrontMatter(agent, delivery),
			body,
		),
	)
}

func (f resolverFixture) writeEventHandlerNamed(
	t *testing.T,
	directory string,
	fileName string,
	content string,
) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(
			directory,
			agentsDirectoryName,
			eventHandlersSubdirectory,
			fileName,
		),
		content,
	)
}

func eventHandlerTypes(handlers []EventHandler) []string {
	types := make([]string, 0, len(handlers))
	for _, handler := range handlers {
		types = append(types, handler.Type)
	}

	return types
}

func manifestKinds(manifest []ManifestEntry) []SourceKind {
	kinds := make([]SourceKind, 0, len(manifest))
	for _, entry := range manifest {
		kinds = append(kinds, entry.Kind)
	}

	return kinds
}
