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
	embeddedFreshnessSkillName = "freshness"
	embeddedPlanningSkillName  = "planning"
)

type embeddedSkillAsset struct {
	name      string
	asset     string
	source    string
	directory string
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

// embeddedHarness holds the immutable base rules that ship in every binary.
// Filesystem layers may replace a skill with the same name, but never remove
// the baseline instruction block.
//
//go:embed builtin/AGENTS.md builtin/skills/freshness/SKILL.md builtin/skills/planning/SKILL.md
var embeddedHarness embed.FS

func embeddedHarnessAsset(name string) (string, error) {
	content, err := embeddedHarness.ReadFile(name)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read embedded asset")
	}

	return string(content), nil
}
