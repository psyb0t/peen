package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/harness"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextSnapshotManifestDecodesTheStoredWriterShape ties the manifest the
// writer stores to the manifest the read API decodes.
//
// The writer stores json.Marshal of harness.Snapshot.Manifest(), which is a
// list. The reader once decoded into a map, so every stored snapshot failed to
// read. Nothing caught it: the write path only checks json.Valid, which a list
// satisfies, and every fixture hand-wrote an object literal instead of the
// writer's own output.
func TestContextSnapshotManifestDecodesTheStoredWriterShape(t *testing.T) {
	t.Parallel()

	entries := []harness.ManifestEntry{
		{
			Kind:     harness.SourceKindInstruction,
			Name:     "AGENTS.md",
			Source:   "/workspace/AGENTS.md",
			Priority: 1,
			Hash:     "instruction-hash",
		},
		{
			Kind:     harness.SourceKindSkill,
			Name:     "fixture-service",
			Source:   "/workspace/.agents/skills/fixture-service/SKILL.md",
			Priority: 2,
			Hash:     "skill-hash",
		},
	}

	want := []api.ContextManifestEntry{
		{
			Kind:     string(harness.SourceKindInstruction),
			Name:     "AGENTS.md",
			Source:   "/workspace/AGENTS.md",
			Priority: 1,
			Hash:     "instruction-hash",
		},
		{
			Kind:     string(harness.SourceKindSkill),
			Name:     "fixture-service",
			Source:   "/workspace/.agents/skills/fixture-service/SKILL.md",
			Priority: 2,
			Hash:     "skill-hash",
		},
	}

	stored, err := json.Marshal(entries)
	require.NoError(t, err)
	require.True(t, json.Valid(stored))

	decoded := []api.ContextManifestEntry{}
	require.NoError(t, json.Unmarshal(stored, &decoded))
	assert.Equal(t, want, decoded)
}

// TestContextSnapshotManifestRejectsAnObjectManifest keeps the old broken shape
// from passing again. An object is valid JSON, so only the decode target says
// it is wrong.
func TestContextSnapshotManifestRejectsAnObjectManifest(t *testing.T) {
	t.Parallel()

	stored := []byte(`{"files":["AGENTS.md"]}`)
	require.True(t, json.Valid(stored))

	decoded := []api.ContextManifestEntry{}
	require.Error(t, json.Unmarshal(stored, &decoded))
}
