package harness

import (
	"encoding/json"
	"maps"
	"sort"
	"strings"
)

const (
	skillCatalogueEmpty      = "No skills are available."
	skillCatalogueTitle      = "Available skills:\n"
	skillCatalogueItem       = "- "
	skillCatalogueIn         = " ("
	skillCatalogueEnd        = ")\n"
	agentCatalogueEmpty      = "No named agents are available."
	agentCatalogueTitle      = "Available named agents:\n"
	agentCatalogueToolsLabel = "allowed tools: "
	agentCatalogueSource     = "source: "

	promptBlockExtraCapacity = 3
)

// SourceKind identifies one type of discovered harness file.
type SourceKind string

const (
	SourceKindInstruction  SourceKind = "instruction"
	SourceKindSkill        SourceKind = "skill"
	SourceKindAgent        SourceKind = "agent"
	SourceKindEventHandler SourceKind = "event-handler"
	SourceKindHook         SourceKind = "hook"
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
	Metadata      map[string]any
	Homepage      string
	Permissions   map[string]any
	UserInvocable bool
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
	AllowedTools []string
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

// HookEvent identifies a lifecycle point configured in .agents/hooks.yaml.
type HookEvent string

const (
	HookEventPreUserMessage  HookEvent = "pre_user_message"
	HookEventPostUserMessage HookEvent = "post_user_message"
	HookEventSessionStart    HookEvent = "session_start"
	HookEventTurnStart       HookEvent = "turn_start"
	HookEventTurnStop        HookEvent = "turn_stop"
	HookEventTurnCancelled   HookEvent = "turn_cancelled"
	HookEventPreCompact      HookEvent = "pre_compact"
	HookEventPostCompact     HookEvent = "post_compact"
	HookEventPreToolUse      HookEvent = "pre_tool_use"
	HookEventPostToolUse     HookEvent = "post_tool_use"
	HookEventToolUseFailure  HookEvent = "tool_use_failure"

	HookEventPreReadFile          HookEvent = "pre_read_file"
	HookEventPostReadFile         HookEvent = "post_read_file"
	HookEventReadFileFailure      HookEvent = "read_file_failure"
	HookEventPreListFiles         HookEvent = "pre_list_files"
	HookEventPostListFiles        HookEvent = "post_list_files"
	HookEventListFilesFailure     HookEvent = "list_files_failure"
	HookEventPreSearchText        HookEvent = "pre_search_text"
	HookEventPostSearchText       HookEvent = "post_search_text"
	HookEventSearchTextFailure    HookEvent = "search_text_failure"
	HookEventPreWriteFile         HookEvent = "pre_write_file"
	HookEventPostWriteFile        HookEvent = "post_write_file"
	HookEventWriteFileFailure     HookEvent = "write_file_failure"
	HookEventPreEditFile          HookEvent = "pre_edit_file"
	HookEventPostEditFile         HookEvent = "post_edit_file"
	HookEventEditFileFailure      HookEvent = "edit_file_failure"
	HookEventPreApplyPatch        HookEvent = "pre_apply_patch"
	HookEventPostApplyPatch       HookEvent = "post_apply_patch"
	HookEventApplyPatchFailed     HookEvent = "apply_patch_failure"
	HookEventPreMovePath          HookEvent = "pre_move_path"
	HookEventPostMovePath         HookEvent = "post_move_path"
	HookEventMovePathFailure      HookEvent = "move_path_failure"
	HookEventPreRemovePath        HookEvent = "pre_remove_path"
	HookEventPostRemovePath       HookEvent = "post_remove_path"
	HookEventRemovePathFailed     HookEvent = "remove_path_failure"
	HookEventPreMakeDirectory     HookEvent = "pre_make_directory"
	HookEventPostMakeDirectory    HookEvent = "post_make_directory"
	HookEventMakeDirectoryFailure HookEvent = "make_directory_failure"
)

// HookActionType selects the work one hook action performs.
type HookActionType string

const (
	HookActionCommand   HookActionType = "command"
	HookActionInject    HookActionType = "inject"
	HookActionEmitEvent HookActionType = "emit_event"
	HookActionDeny      HookActionType = "deny"
)

// HookFailureMode controls what a failed action does. The zero value leaves
// the phase-specific runtime default in effect.
type HookFailureMode string

const (
	HookFailureDeny     HookFailureMode = "deny"
	HookFailureContinue HookFailureMode = "continue"
)

// HookInputMatch matches one JSON Pointer in a tool or lifecycle payload.
// Equals keeps the YAML value's type, so true and "true" never compare equal.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HookInputMatch struct {
	Exists *bool  `yaml:"exists" json:"exists,omitempty"`
	Equals any    `yaml:"equals" json:"equals,omitempty"`
	Regex  string `yaml:"regex" json:"regex,omitempty"`
}

// HookMatch selects hook actions by their structured invocation context.
// Every populated field is required to match.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HookMatch struct {
	Tool       string                    `yaml:"tool" json:"tool,omitempty"`
	Path       string                    `yaml:"path" json:"path,omitempty"`
	Extensions []string                  `yaml:"extensions" json:"extensions,omitempty"` //nolint:lll // Immutable schema tags.
	Root       string                    `yaml:"root" json:"root,omitempty"`
	Input      map[string]HookInputMatch `yaml:"input" json:"input,omitempty"`
}

// HookAction is one ordered operation in a hook group. Command is an
// executable path or name, never shell source; Args are passed verbatim.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HookAction struct {
	Name        string            `yaml:"name" json:"name,omitempty"`
	Type        HookActionType    `yaml:"type" json:"type"`
	When        HookMatch         `yaml:"when" json:"when,omitzero"`
	Command     string            `yaml:"command" json:"command,omitempty"`
	Args        []string          `yaml:"args" json:"args,omitempty"`
	Environment map[string]string `yaml:"environment" json:"environment,omitempty"` //nolint:lll // Immutable schema tags.
	//nolint:tagliatelle // YAML uses the public snake_case schema.
	WorkingDir string `yaml:"working_dir" json:"workingDir,omitempty"`
	//nolint:tagliatelle,lll // YAML uses the public snake_case schema.
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeoutSeconds,omitempty"`
	Message        string `yaml:"message" json:"message,omitempty"`
	//nolint:tagliatelle // YAML uses the public snake_case schema.
	EventType string         `yaml:"event_type" json:"eventType,omitempty"`
	Summary   string         `yaml:"summary" json:"summary,omitempty"`
	Data      map[string]any `yaml:"data" json:"data,omitempty"`
	Delivery  string         `yaml:"delivery" json:"delivery,omitempty"`
	Reason    string         `yaml:"reason" json:"reason,omitempty"`
	//nolint:tagliatelle // YAML uses the public snake_case schema.
	OnFailure HookFailureMode `yaml:"on_failure" json:"onFailure,omitempty"`
}

// Hook is one ordered matcher group from a layered hooks.yaml document.
// ConfigLayer is true only for PEEN_CONFIG_DIR's hook document.
type Hook struct {
	Name        string       `json:"name"`
	Event       HookEvent    `json:"event"`
	Match       HookMatch    `json:"match,omitzero"`
	Actions     []HookAction `json:"actions"`
	Source      string       `json:"source"`
	Priority    int          `json:"priority"`
	ConfigLayer bool         `json:"configLayer"`
	Hash        string       `json:"hash"`
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
	hooks         []Hook
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

// Hooks returns every additive hook group in layer and file order.
func (s Snapshot) Hooks() []Hook {
	return cloneHooks(s.hooks)
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

	blocks = append(blocks, s.skillCatalogueBlock(), s.agentCatalogueBlock())

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

func (s Snapshot) agentCatalogueBlock() PromptBlock {
	content := renderAgentCatalogue(s.agents)

	return PromptBlock{
		Kind:    SourceKindAgent,
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
	skill.Metadata = cloneSkillData(skill.Metadata)
	skill.Permissions = cloneSkillData(skill.Permissions)

	return skill
}

func cloneSkillData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}

	return cloneHookData(data)
}

func cloneAgents(agents []Agent) []Agent {
	cloned := append([]Agent(nil), agents...)
	for index := range cloned {
		cloned[index].AllowedTools = append(
			[]string(nil),
			agents[index].AllowedTools...,
		)
	}

	return cloned
}

func cloneEventHandlers(handlers []EventHandler) []EventHandler {
	return append([]EventHandler(nil), handlers...)
}

func cloneHooks(hooks []Hook) []Hook {
	cloned := make([]Hook, len(hooks))
	for index, hook := range hooks {
		cloned[index] = cloneHook(hook)
	}

	return cloned
}

func cloneHook(hook Hook) Hook {
	hook.Match = cloneHookMatch(hook.Match)
	actions := hook.Actions

	hook.Actions = make([]HookAction, len(actions))
	for index, action := range actions {
		hook.Actions[index] = cloneHookAction(action)
	}

	return hook
}

func cloneHookAction(action HookAction) HookAction {
	action.When = cloneHookMatch(action.When)
	action.Args = append([]string(nil), action.Args...)
	action.Environment = maps.Clone(action.Environment)
	action.Data = cloneHookData(action.Data)

	return action
}

func cloneHookMatch(match HookMatch) HookMatch {
	match.Extensions = append([]string(nil), match.Extensions...)
	if len(match.Input) == 0 {
		return match
	}

	match.Input = make(map[string]HookInputMatch, len(match.Input))
	for pointer, condition := range match.Input {
		if condition.Exists != nil {
			exists := *condition.Exists
			condition.Exists = &exists
		}

		condition.Equals = cloneHookValue(condition.Equals)
		match.Input[pointer] = condition
	}

	return match
}

func cloneHookData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(data))
	for key, value := range data {
		cloned[key] = cloneHookValue(value)
	}

	return cloned
}

func cloneHookValue(value any) any {
	if value == nil {
		return nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}

	cloned := any(nil)
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return value
	}

	return cloned
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

func renderAgentCatalogue(agents []Agent) string {
	if len(agents) == 0 {
		return agentCatalogueEmpty
	}

	cloned := cloneAgents(agents)
	sort.Slice(cloned, func(left, right int) bool {
		return cloned[left].Name < cloned[right].Name
	})

	var builder strings.Builder
	builder.WriteString(agentCatalogueTitle)

	for _, agent := range cloned {
		builder.WriteString(skillCatalogueItem)
		builder.WriteString(agent.Name)
		builder.WriteString(": ")
		builder.WriteString(agent.Description)
		builder.WriteString(skillCatalogueIn)
		builder.WriteString(agentCatalogueToolsLabel)
		builder.WriteString(strings.Join(agent.AllowedTools, ", "))
		builder.WriteString("; ")
		builder.WriteString(agentCatalogueSource)
		builder.WriteString(agent.Source)
		builder.WriteString(skillCatalogueEnd)
	}

	return builder.String()
}
