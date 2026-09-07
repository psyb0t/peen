package harness

import (
	"maps"
	"sort"
	"strings"
)

const (
	skillCatalogueEmpty = "No skills are available."
	skillCatalogueTitle = "Available skills:\n"
	skillCatalogueItem  = "- "
	skillCatalogueIn    = " ("
	skillCatalogueEnd   = ")\n"

	promptBlockExtraCapacity = 2
)

// SourceKind identifies one type of discovered harness file.
type SourceKind string

const (
	SourceKindInstruction  SourceKind = "instruction"
	SourceKindSkill        SourceKind = "skill"
	SourceKindAgent        SourceKind = "agent"
	SourceKindEventHandler SourceKind = "event-handler"
)

// Instruction is one ordered AGENTS.md block.
type Instruction struct {
	Source   string
	Priority int
	Content  string
	Hash     string
}

// Skill is the catalogue entry exposed before a skill is activated.
type Skill struct {
	Name          string
	Description   string
	Source        string
	Directory     string
	Hash          string
	Metadata      map[string]string
	License       string
	Compatibility string
	AllowedTools  string
}

// Agent is one named child or root agent definition.
type Agent struct {
	Name         string
	Description  string
	Source       string
	Instructions string
	Hash         string
}

// EventHandler is one layered instruction for a specific session event type.
type EventHandler struct {
	Type         string
	Agent        string
	Delivery     string
	Source       string
	Instructions string
	Hash         string
}

// ManifestEntry describes one resolved source without exposing mutable state.
type ManifestEntry struct {
	Kind     SourceKind `json:"kind"`
	Name     string     `json:"name"`
	Source   string     `json:"source"`
	Priority int        `json:"priority"`
	Hash     string     `json:"hash"`
}

// PromptBlock is an ordered, provenance-carrying context fragment.
type PromptBlock struct {
	Kind    SourceKind
	Name    string
	Source  string
	Content string
	Hash    string
}

// ActivatedSkill is the full content returned by the use_skill tool.
type ActivatedSkill struct {
	Skill
	Content string
}

// Snapshot is an immutable result of resolving one workspace.
type Snapshot struct {
	configRoot    string
	workspace     string
	hash          string
	instructions  []Instruction
	skills        []Skill
	agents        []Agent
	eventHandlers []EventHandler
	manifest      []ManifestEntry
	skillContents map[string]string
}

func (s Snapshot) ConfigRoot() string {
	return s.configRoot
}

func (s Snapshot) Workspace() string {
	return s.workspace
}

func (s Snapshot) Hash() string {
	return s.hash
}

func (s Snapshot) Instructions() []Instruction {
	return cloneInstructions(s.instructions)
}

func (s Snapshot) Skills() []Skill {
	return cloneSkills(s.skills)
}

func (s Snapshot) Agents() []Agent {
	return cloneAgents(s.agents)
}

func (s Snapshot) EventHandlers() []EventHandler {
	return cloneEventHandlers(s.eventHandlers)
}

func (s Snapshot) Manifest() []ManifestEntry {
	return append([]ManifestEntry(nil), s.manifest...)
}

func (s Snapshot) ActivateSkill(name string) (ActivatedSkill, error) {
	for _, skill := range s.skills {
		if skill.Name == name {
			return ActivatedSkill{
				Skill:   cloneSkill(skill),
				Content: s.skillContents[name],
			}, nil
		}
	}

	return ActivatedSkill{}, ErrSkillNotFound
}

func (s Snapshot) Agent(name string) (Agent, error) {
	for _, agent := range s.agents {
		if agent.Name == name {
			return agent, nil
		}
	}

	return Agent{}, ErrAgentNotFound
}

func (s Snapshot) EventHandler(eventType string) (EventHandler, error) {
	for _, handler := range s.eventHandlers {
		if handler.Type == eventType {
			return handler, nil
		}
	}

	return EventHandler{}, ErrEventHandlerNotFound
}

func (s Snapshot) PromptBlocks(rootAgent string) ([]PromptBlock, error) {
	blockCapacity := len(s.instructions) + promptBlockExtraCapacity

	blocks := make([]PromptBlock, 0, blockCapacity)
	for _, instruction := range s.instructions {
		blocks = append(blocks, PromptBlock{
			Kind:    SourceKindInstruction,
			Source:  instruction.Source,
			Content: instruction.Content,
			Hash:    instruction.Hash,
		})
	}

	if rootAgent != "" {
		agent, err := s.Agent(rootAgent)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, PromptBlock{
			Kind:    SourceKindAgent,
			Name:    agent.Name,
			Source:  agent.Source,
			Content: agent.Instructions,
			Hash:    agent.Hash,
		})
	}

	blocks = append(blocks, s.skillCatalogueBlock())

	return blocks, nil
}

func (s Snapshot) skillCatalogueBlock() PromptBlock {
	content := renderSkillCatalogue(s.skills)

	return PromptBlock{
		Kind:    SourceKindSkill,
		Name:    "catalogue",
		Content: content,
		Hash:    hashString(content),
	}
}

func cloneInstructions(instructions []Instruction) []Instruction {
	return append([]Instruction(nil), instructions...)
}

func cloneSkills(skills []Skill) []Skill {
	cloned := make([]Skill, len(skills))
	for index, skill := range skills {
		cloned[index] = cloneSkill(skill)
	}

	return cloned
}

func cloneSkill(skill Skill) Skill {
	skill.Metadata = maps.Clone(skill.Metadata)

	return skill
}

func cloneAgents(agents []Agent) []Agent {
	return append([]Agent(nil), agents...)
}

func cloneEventHandlers(handlers []EventHandler) []EventHandler {
	return append([]EventHandler(nil), handlers...)
}

func renderSkillCatalogue(skills []Skill) string {
	if len(skills) == 0 {
		return skillCatalogueEmpty
	}

	cloned := cloneSkills(skills)
	sort.Slice(cloned, func(left, right int) bool {
		return cloned[left].Name < cloned[right].Name
	})

	var builder strings.Builder
	builder.WriteString(skillCatalogueTitle)

	for _, skill := range cloned {
		builder.WriteString(skillCatalogueItem)
		builder.WriteString(skill.Name)
		builder.WriteString(": ")
		builder.WriteString(skill.Description)
		builder.WriteString(skillCatalogueIn)
		builder.WriteString(skill.Source)
		builder.WriteString(skillCatalogueEnd)
	}

	return builder.String()
}
