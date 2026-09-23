package agent

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryListModelsReturnsSortedPublicMetadata(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver()
	registry, err := NewStaticRegistry(map[string]ModelClient{
		"zai/glm-5.3": {
			Client: elelem.New(driver),
			Model:  elelem.Model{ID: "glm-5.3", ContextSize: 128000},
		},
		"aigate/fast": {
			Client: elelem.New(driver),
			Model:  elelem.Model{ID: "fast", ContextSize: 64000},
		},
		"aigate/path/model": {
			Client: elelem.New(driver),
			Model:  elelem.Model{ID: "path/model", ContextSize: 32000},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, api.ModelList{Models: []api.Model{
		{
			Name:                "aigate/fast",
			ConnectionName:      "aigate",
			ModelId:             "fast",
			ContextWindowTokens: 64000,
		},
		{
			Name:                "aigate/path/model",
			ConnectionName:      "aigate",
			ModelId:             "path/model",
			ContextWindowTokens: 32000,
		},
		{
			Name:                "zai/glm-5.3",
			ConnectionName:      "zai",
			ModelId:             "glm-5.3",
			ContextWindowTokens: 128000,
		},
	}}, registry.ListModels())
}

func TestRuntimeListModelsWithResolverOnlyReturnsEmptyCatalogue(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{models: modelResolverOnly{}}

	assert.Equal(t, api.ModelList{Models: []api.Model{}}, runtime.ListModels())
}

type modelResolverOnly struct{}

func (modelResolverOnly) ResolveModel(string) (ModelClient, error) {
	return ModelClient{}, nil
}
