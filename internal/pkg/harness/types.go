package harness

import (
	"encoding/json"
	"maps"
	"sort"

	"github.com/psyb0t/ctxerrors"
)

const promptBlockExtraCapacity = 3

// SourceKind identifies one type of discovered harness file.
type SourceKind string

const (
	SourceKindInstruction  SourceKind = "instruction"
	SourceKindRule         SourceKind = "rule"
	SourceKindSkill        SourceKind = "skill"
	SourceKindAgent        SourceKind = "agent"
	SourceKindEventHandler SourceKind = "event-handler"
	SourceKindHook         SourceKind = "hook"
)

// Instruction is one ordered, always-on harness instruction block.
type Instruction struct {
	Kind     SourceKind
	Source   string
	Priority int
	Content  string
	Hash     string

	// Scope is the directory, relative to the workspace, that a nested
	// AGENTS.md covers. It is empty for every file outside the workspace
	// tree and for the workspace's own AGENTS.md.
	Scope string

	// Name is a rule's file name without the .md extension.
	Name string
	// Paths are a rule's glob patterns from its frontmatter. A rule with
	// paths stays out of the system prompt and reaches the model when a file
	// tool touches a matching file. Content never holds the frontmatter.
	Paths []string
	// Base is the directory a scoped rule's patterns are relative to: the
	// directory holding the rule's .agents or .claude directory, or the
	// resolved directory for a rule from the configuration directory.
	Base string
}

// IsScoped reports whether the instruction is a rule that loads only for
// matching files.
func (i Instruction) IsScoped() bool {
	return len(i.Paths) > 0
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
	HookEventPreUserMessage  HookEvent = "preUserMessage"
	HookEventPostUserMessage HookEvent = "postUserMessage"
	HookEventSessionStart    HookEvent = "sessionStart"
	HookEventTurnStart       HookEvent = "turnStart"
	HookEventTurnStop        HookEvent = "turnStop"
	HookEventTurnCancelled   HookEvent = "turnCancelled"
	HookEventPreCompact      HookEvent = "preCompact"
	HookEventPostCompact     HookEvent = "postCompact"
	HookEventPreToolUse      HookEvent = "preToolUse"
	HookEventPostToolUse     HookEvent = "postToolUse"
	HookEventToolUseFailure  HookEvent = "toolUseFailure"

	HookEventPreReadFile          HookEvent = "preReadFile"
	HookEventPostReadFile         HookEvent = "postReadFile"
	HookEventReadFileFailure      HookEvent = "readFileFailure"
	HookEventPreListFiles         HookEvent = "preListFiles"
	HookEventPostListFiles        HookEvent = "postListFiles"
	HookEventListFilesFailure     HookEvent = "listFilesFailure"
	HookEventPreSearchText        HookEvent = "preSearchText"
	HookEventPostSearchText       HookEvent = "postSearchText"
	HookEventSearchTextFailure    HookEvent = "searchTextFailure"
	HookEventPreWriteFile         HookEvent = "preWriteFile"
	HookEventPostWriteFile        HookEvent = "postWriteFile"
	HookEventWriteFileFailure     HookEvent = "writeFileFailure"
	HookEventPreEditFile          HookEvent = "preEditFile"
	HookEventPostEditFile         HookEvent = "postEditFile"
	HookEventEditFileFailure      HookEvent = "editFileFailure"
	HookEventPreApplyPatch        HookEvent = "preApplyPatch"
	HookEventPostApplyPatch       HookEvent = "postApplyPatch"
	HookEventApplyPatchFailed     HookEvent = "applyPatchFailure"
	HookEventPreMovePath          HookEvent = "preMovePath"
	HookEventPostMovePath         HookEvent = "postMovePath"
	HookEventMovePathFailure      HookEvent = "movePathFailure"
	HookEventPreRemovePath        HookEvent = "preRemovePath"
	HookEventPostRemovePath       HookEvent = "postRemovePath"
	HookEventRemovePathFailed     HookEvent = "removePathFailure"
	HookEventPreMakeDirectory     HookEvent = "preMakeDirectory"
	HookEventPostMakeDirectory    HookEvent = "postMakeDirectory"
	HookEventMakeDirectoryFailure HookEvent = "makeDirectoryFailure"
)

// HookActionType selects the work one hook action performs.
type HookActionType string

const (
	HookActionCommand   HookActionType = "command"
	HookActionInject    HookActionType = "inject"
	HookActionEmitEvent HookActionType = "emitEvent"
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
// Timeout is a Go duration string such as "30s" bounding one command action;
// empty takes the runner's default.
//
//nolint:tagalign // Preserve the YAML-first tag convention.
type HookAction struct {
	Name        string            `yaml:"name" json:"name,omitempty"`
	Type        HookActionType    `yaml:"type" json:"type"`
	When        HookMatch         `yaml:"when" json:"when,omitzero"`
	Command     string            `yaml:"command" json:"command,omitempty"`
	Args        []string          `yaml:"args" json:"args,omitempty"`
	Environment map[string]string `yaml:"environment" json:"environment,omitempty"` //nolint:lll // Immutable schema tags.
	WorkingDir  string            `yaml:"workingDir" json:"workingDir,omitempty"`   //nolint:lll // Immutable schema tags.
	Timeout     string            `yaml:"timeout" json:"timeout,omitempty"`
	Message     string            `yaml:"message" json:"message,omitempty"`
	MessageFile string            `yaml:"messageFile" json:"messageFile,omitempty"` //nolint:lll // Immutable schema tags.
	EventType   string            `yaml:"eventType" json:"eventType,omitempty"`
	Summary     string            `yaml:"summary" json:"summary,omitempty"`
	Data        map[string]any    `yaml:"data" json:"data,omitempty"`
	Delivery    string            `yaml:"delivery" json:"delivery,omitempty"`
	Reason      string            `yaml:"reason" json:"reason,omitempty"`
	OnFailure   HookFailureMode   `yaml:"onFailure" json:"onFailure,omitempty"`
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

// Warning reports one optional harness source Peen ignored after it failed
// validation. The remaining valid sources still form the active snapshot.
type Warning struct {
	Kind   SourceKind `json:"kind"`
	Source string     `json:"source"`
	Reason string     `json:"reason"`
}

// PromptBlock is an ordered, provenance-carrying context fragment.
type PromptBlock struct {
	Kind    SourceKind
	Name    string
	Source  string
	Content string
	Hash    string
}

// ActivatedSkill is the full content returned by the useSkill tool.
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
	warnings      []Warning
	skillContents map[string]string

	// skillBudget bounds the skill content one ExpandSkills call may return.
	skillBudget int64
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

// Warnings returns invalid optional sources Peen ignored while resolving this
// snapshot. The returned slice is safe for callers to modify.
func (s Snapshot) Warnings() []Warning {
	return append([]Warning(nil), s.warnings...)
}

// ActivateSkill resolves a skill by name. A plain name finds the custom skill
// first and the core skill second. A name with the core prefix finds only the
// core skill.
func (s Snapshot) ActivateSkill(name string) (ActivatedSkill, error) {
	for _, address := range lookupAddresses(name) {
		for _, skill := range s.skills {
			if skill.Name == address {
				return ActivatedSkill{
					Skill:   cloneSkill(skill),
					Content: s.skillContents[address],
				}, nil
			}
		}
	}

	return ActivatedSkill{}, ErrSkillNotFound
}

// Agent resolves an agent by name with the same rules as ActivateSkill.
// NoRootAgent resolves to an agent with no instructions and no tool limit.
func (s Snapshot) Agent(name string) (Agent, error) {
	if name == NoRootAgent {
		return Agent{Name: NoRootAgent}, nil
	}

	for _, address := range lookupAddresses(name) {
		for _, agent := range s.agents {
			if agent.Name == address {
				return agent, nil
			}
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
	blockCapacity := len(s.instructions) + promptBlockExtraCapacity +
		len(s.warnings)

	blocks := make([]PromptBlock, 0, blockCapacity)

	for _, instruction := range s.instructions {
		if instruction.IsScoped() {
			continue
		}

		block, err := instructionBlock(instruction)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, block)
	}

	if rootAgent != "" && rootAgent != NoRootAgent {
		block, err := s.rootAgentBlock(rootAgent)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, block)
	}

	catalogues, err := s.catalogueBlocks()
	if err != nil {
		return nil, err
	}

	blocks = append(blocks, catalogues...)

	warningBlock, ok, err := s.warningBlock()
	if err != nil {
		return nil, err
	}

	if ok {
		blocks = append(blocks, warningBlock)
	}

	return blocks, nil
}

func (s Snapshot) rootAgentBlock(rootAgent string) (PromptBlock, error) {
	agent, err := s.Agent(rootAgent)
	if err != nil {
		return PromptBlock{}, err
	}

	content, err := renderAgentInstructions(agent)
	if err != nil {
		return PromptBlock{}, err
	}

	return PromptBlock{
		Kind:    SourceKindAgent,
		Name:    agent.Name,
		Source:  agent.Source,
		Content: content,
		Hash:    hashString(content),
	}, nil
}

func (s Snapshot) warningBlock() (PromptBlock, bool, error) {
	if len(s.warnings) == 0 {
		return PromptBlock{}, false, nil
	}

	encoded, err := json.Marshal(s.warnings)
	if err != nil {
		return PromptBlock{}, false, ctxerrors.Wrap(err,
			"encode harness diagnostics")
	}

	rendered, err := renderDiagnostics(string(encoded))
	if err != nil {
		return PromptBlock{}, false, err
	}

	return PromptBlock{
		Kind:    SourceKindInstruction,
		Name:    "harness-diagnostics",
		Content: rendered,
		Hash:    hashString(rendered),
	}, true, nil
}

// instructionBlock renders one always-on AGENTS.md or rule in its tag.
func instructionBlock(instruction Instruction) (PromptBlock, error) {
	content, err := renderInstruction(instruction)
	if err != nil {
		return PromptBlock{}, err
	}

	return PromptBlock{
		Kind:    instruction.kind(),
		Source:  instruction.Source,
		Content: content,
		Hash:    hashString(content),
	}, nil
}

func (i Instruction) kind() SourceKind {
	if i.Kind == "" {
		return SourceKindInstruction
	}

	return i.Kind
}

// catalogueBlocks renders the skill and agent catalogues.
func (s Snapshot) catalogueBlocks() ([]PromptBlock, error) {
	skills, err := renderSkillCatalogue(s.skills)
	if err != nil {
		return nil, err
	}

	agents, err := renderAgentCatalogue(s.agents)
	if err != nil {
		return nil, err
	}

	return []PromptBlock{
		{
			Kind:    SourceKindSkill,
			Name:    "catalogue",
			Content: skills,
			Hash:    hashString(skills),
		},
		{
			Kind:    SourceKindAgent,
			Name:    "catalogue",
			Content: agents,
			Hash:    hashString(agents),
		},
	}, nil
}

func cloneInstructions(instructions []Instruction) []Instruction {
	cloned := append([]Instruction(nil), instructions...)
	for index := range cloned {
		cloned[index].Paths = append([]string(nil), cloned[index].Paths...)
	}

	return cloned
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

	original := match.Input
	match.Input = make(map[string]HookInputMatch, len(original))

	for pointer, condition := range original {
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

// sortSkills orders skills by name in place.
func sortSkills(skills []Skill) {
	sort.Slice(skills, func(left, right int) bool {
		return skills[left].Name < skills[right].Name
	})
}

func partitionSkills(skills []Skill) ([]Skill, []Skill) {
	var core, custom []Skill

	for _, skill := range skills {
		if isCoreName(skill.Name) {
			core = append(core, skill)

			continue
		}

		custom = append(custom, skill)
	}

	return core, custom
}

// sortAgents orders agents by name in place.
func sortAgents(agents []Agent) {
	sort.Slice(agents, func(left, right int) bool {
		return agents[left].Name < agents[right].Name
	})
}

func partitionAgents(agents []Agent) ([]Agent, []Agent) {
	var core, custom []Agent

	for _, agent := range agents {
		if isCoreName(agent.Name) {
			core = append(core, agent)

			continue
		}

		custom = append(custom, agent)
	}

	return core, custom
}
