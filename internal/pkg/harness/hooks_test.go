package harness

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHookDocument = `version: 1
pre_tool_use:
  - name: go-write-gate
    match:
      tool: write_file
      path: "**/*.go"
      extensions: [".go"]
      root: internal
      input:
        /expectedSha256:
          exists: true
    actions:
      - name: go-rule
        type: inject
        message: Read the Go instructions.
      - type: command
        command: scripts/check-go
        args: ["--strict"]
        environment:
          CHECK_MODE: strict
        timeout_seconds: 30
      - type: emit_event
        event_type: hook.write.checked
        summary: Go write checked
        data:
          language: go
        delivery: queue
  - match:
      tool: write_file
    actions:
      - type: deny
        reason: This fixture blocks writes.
post_read_file:
  - actions:
      - type: inject
        message: File read complete.
        when:
          path: "**/*_test.go"
`

func TestResolverDiscoversAdditiveLayeredHooks(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeHook(t, fixture.configRoot, testHookDocument)
	fixture.writeHook(t, fixture.workspace, `version: 1
pre_tool_use:
  - match:
      tool: write_file
    actions:
      - type: inject
        message: Workspace hook.
`)

	snapshot := resolveFixture(t, fixture)
	hooks := snapshot.Hooks()

	require.Len(t, hooks, 4)
	assert.Equal(t, HookEventPostReadFile, hooks[0].Event)
	assert.Equal(t, HookEventPreToolUse, hooks[1].Event)
	assert.Equal(t, HookEventPreToolUse, hooks[2].Event)
	assert.Equal(t, HookEventPreToolUse, hooks[3].Event)
	assert.True(t, hooks[0].ConfigLayer)
	assert.True(t, hooks[1].ConfigLayer)
	assert.True(t, hooks[2].ConfigLayer)
	assert.False(t, hooks[3].ConfigLayer)
	assert.Equal(t, 0, hooks[0].Priority)
	assert.Less(t, hooks[1].Priority, hooks[3].Priority)

	firstWrite := hooks[1]
	assert.Equal(t, "go-write-gate", firstWrite.Name)
	assert.Equal(t, "write_file", firstWrite.Match.Tool)
	assert.Equal(t, "**/*.go", firstWrite.Match.Path)
	assert.Equal(t, []string{".go"}, firstWrite.Match.Extensions)
	assert.Equal(t, "internal", firstWrite.Match.Root)
	require.Len(t, firstWrite.Actions, 3)
	assert.Equal(t, HookActionInject, firstWrite.Actions[0].Type)
	assert.Equal(t, "go-rule", firstWrite.Actions[0].Name)
	assert.Equal(t, HookActionCommand, firstWrite.Actions[1].Type)
	assert.Equal(t, HookActionEmitEvent, firstWrite.Actions[2].Type)
	assert.Equal(t, "scripts/check-go", firstWrite.Actions[1].Command)
	assert.Equal(t, []string{"--strict"}, firstWrite.Actions[1].Args)
	assert.Equal(t, "strict", firstWrite.Actions[1].Environment["CHECK_MODE"])
	assert.Equal(t, "hook.write.checked", firstWrite.Actions[2].EventType)
	assert.Equal(t, "go", firstWrite.Actions[2].Data["language"])
	assert.Equal(t, "post_read_file-1", hooks[0].Name)
	assert.Equal(t, "inject-1", hooks[0].Actions[0].Name)

	assert.Contains(t, manifestKinds(snapshot.Manifest()), SourceKindHook)
}

func TestResolverRejectsMalformedHookDocuments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		document string
	}{
		{
			name: "unknown event",
			document: `version: 1
unknown_event:
  - actions:
      - type: inject
        message: no
`,
		},
		{
			name: "event is not an action list",
			document: `version: 1
pre_tool_use:
  actions:
    - type: inject
      message: no
`,
		},
		{
			name: "action has unknown field",
			document: `version: 1
pre_tool_use:
  - actions:
      - type: inject
        message: no
        surprise: true
`,
		},
		{
			name: "empty action list",
			document: `version: 1
pre_tool_use:
  - actions: []
`,
		},
		{
			name: "invalid action predicate",
			document: `version: 1
pre_tool_use:
  - actions:
      - type: inject
        message: no
        when:
          input:
            path:
              exists: true
`,
		},
		{
			name: "invalid command timeout",
			document: `version: 1
pre_tool_use:
  - actions:
      - type: command
        command: check
        timeout_seconds: -1
`,
		},
		{
			name: "wrong version",
			document: `version: 2
pre_tool_use:
  - actions:
      - type: inject
        message: no
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			fixture.writeHook(t, fixture.configRoot, tc.document)

			resolver, err := NewResolver(fixture.configRoot, Limits{})
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			require.ErrorIs(t, err, ErrInvalidHook)
		})
	}
}

func TestHookChangeChangesSnapshotHashAndSnapshotRemainsImmutable(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeHook(t, fixture.configRoot, `version: 1
pre_tool_use:
  - actions:
      - type: inject
        message: First.
`)

	first := resolveFixture(t, fixture)
	mutated := first.Hooks()
	mutated[0].Actions[0].Message = "Mutated."
	assert.Equal(t, "First.", first.Hooks()[0].Actions[0].Message)

	fixture.writeHook(t, fixture.configRoot, `version: 1
pre_tool_use:
  - actions:
      - type: inject
        message: Second.
`)
	second := resolveFixture(t, fixture)
	assert.NotEqual(t, first.Hash(), second.Hash())
}

func TestResolverBoundsHookGroups(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeHook(t, fixture.configRoot, `version: 1
pre_tool_use:
  - actions:
      - type: inject
        message: first
  - actions:
      - type: inject
        message: second
`)

	limits := defaultLimits()
	limits.MaxHooks = 1
	resolver, err := NewResolver(fixture.configRoot, limits)
	require.NoError(t, err)

	_, err = resolver.Resolve(fixture.workspace)
	require.ErrorIs(t, err, ErrResourceLimit)
}

func (f resolverFixture) writeHook(
	t *testing.T,
	directory string,
	content string,
) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(directory, agentsDirectoryName, hooksFileName),
		content,
	)
}
