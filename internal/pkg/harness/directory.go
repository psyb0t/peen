package harness

import (
	"path/filepath"
	"slices"

	"github.com/psyb0t/ctxerrors"
)

// ResolveDirectory reads the current directory and its ancestors, without
// loading definitions from descendants the agent has not entered.
func (r Resolver) ResolveDirectory(directory string) (Snapshot, error) {
	state := newResolutionState(r.limits)
	if err := state.discoverEmbedded(
		r.disabledSkills, r.disabledAgents,
	); err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "discover directory baseline")
	}

	if err := state.discoverLayer(r.configRoot, 0, true); err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "discover trusted layer")
	}

	state.reader = r.filesystem

	canonical, err := state.canonicalDirectory(directory)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "canonicalize harness directory")
	}

	layers, err := r.directoryLayers(canonical)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "resolve directory ancestors")
	}

	for index, layer := range layers {
		if r.filesystem == nil && layer == r.configRoot {
			continue
		}

		if err := state.discoverLayer(layer, index+1, false); err != nil {
			return Snapshot{}, ctxerrors.Wrap(err, "discover directory layer")
		}
	}

	if err := r.discoverMemoryLayers(state, len(layers)+1); err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "discover caller harness")
	}

	return state.snapshot(r.configRoot, canonical)
}

func (r Resolver) directoryLayers(directory string) ([]string, error) {
	if r.filesystem == nil {
		return workspaceAncestors(directory)
	}

	layers := []string{directory}
	for current := directory; current != virtualHarnessRoot; {
		current = filepath.Dir(current)
		layers = append(layers, current)
	}

	slices.Reverse(layers)

	return layers, nil
}
