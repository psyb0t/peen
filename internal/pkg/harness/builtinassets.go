package harness

import (
	"embed"

	"github.com/psyb0t/ctxerrors"
)

const (
	embeddedSourcePrefix       = "embedded://"
	embeddedInstructionAsset   = "builtin/AGENTS.md"
	embeddedInstructionSource  = embeddedSourcePrefix + "AGENTS.md"
	embeddedSkillsAssetPrefix  = "builtin/skills/"
	embeddedSkillsSourcePrefix = embeddedSourcePrefix + "skills/"
	embeddedAgentsAssetPrefix  = "builtin/agents/"
	embeddedAgentsSourcePrefix = embeddedSourcePrefix + "agents/"
	embeddedFreshnessSkillName = "freshness"
	embeddedPlanningSkillName  = "planning"
	embeddedDefaultAgentName   = "default"
)

type embeddedSkillAsset struct {
	name      string
	asset     string
	source    string
	directory string
}

type embeddedAgentAsset struct {
	name   string
	asset  string
	source string
}

func embeddedSkillAssets() []embeddedSkillAsset {
	return []embeddedSkillAsset{
		embeddedSkillAssetFor(embeddedFreshnessSkillName),
		embeddedSkillAssetFor(embeddedPlanningSkillName),
	}
}

func embeddedSkillAssetFor(name string) embeddedSkillAsset {
	directory := embeddedSkillsSourcePrefix + name

	return embeddedSkillAsset{
		name:      name,
		asset:     embeddedSkillsAssetPrefix + name + "/" + skillFileName,
		source:    directory + "/" + skillFileName,
		directory: directory,
	}
}

func embeddedAgentAssets() []embeddedAgentAsset {
	return []embeddedAgentAsset{
		embeddedAgentAssetFor(embeddedDefaultAgentName),
	}
}

func embeddedAgentAssetFor(name string) embeddedAgentAsset {
	return embeddedAgentAsset{
		name:   name,
		asset:  embeddedAgentsAssetPrefix + name + agentsFileExtension,
		source: embeddedAgentsSourcePrefix + name + agentsFileExtension,
	}
}

// embeddedHarness holds the immutable base context that ships in every binary.
// Filesystem layers may replace same-named skills and agents, but never remove
// the baseline instruction block.
//
//go:embed builtin/AGENTS.md
//go:embed builtin/agents/default.md
//go:embed builtin/skills/freshness/SKILL.md
//go:embed builtin/skills/planning/SKILL.md
var embeddedHarness embed.FS

func embeddedHarnessAsset(name string) (string, error) {
	content, err := embeddedHarness.ReadFile(name)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read embedded asset")
	}

	return string(content), nil
}
