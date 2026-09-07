package harness

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	agentsFileName            = "AGENTS.md"
	agentsDirectoryName       = ".agents"
	skillsDirectoryName       = "skills"
	agentsSubdirectory        = "agents"
	eventHandlersSubdirectory = "events"
	hooksFileName             = "hooks.yaml"
	skillFileName             = "SKILL.md"
	agentsFileExtension       = ".md"
	rootDirectory             = "/"
	manifestVersion           = 1

	// homeDirectoryAlias is the shorthand a caller may use for the running
	// user's home directory. Only a leading "~" or "~/" expands; "~"
	// elsewhere in a path is a literal character, since it is legal in a
	// filename and rewriting it there would corrupt a real path.
	homeDirectoryAlias      = "~"
	homeDirectoryAliasSlash = "~/"
)

// Resolver discovers the layered harness context for one workspace at a time.
type Resolver struct {
	configRoot string
	limits     Limits
}

type resolutionState struct {
	limits        Limits
	files         int
	totalBytes    int64
	instructions  []Instruction
	skills        map[string]discoveredSkill
	agents        map[string]Agent
	eventHandlers map[string]EventHandler
	hooks         []Hook
}

type discoveredSkill struct {
	skill   Skill
	content string
}

type manifestSnapshot struct {
	Version       int             `json:"version"`
	ConfigRoot    string          `json:"configRoot"`
	Workspace     string          `json:"workspace"`
	Instructions  []Instruction   `json:"instructions"`
	Skills        []Skill         `json:"skills"`
	Agents        []Agent         `json:"agents"`
	EventHandlers []EventHandler  `json:"eventHandlers"`
	Hooks         []Hook          `json:"hooks"`
	Manifest      []ManifestEntry `json:"manifest"`
}

// NewResolver builds a resolver whose config root is always the first layer.
func NewResolver(configRoot string, limits Limits) (Resolver, error) {
	resolvedConfigRoot, err := canonicalDirectory(configRoot)
	if err != nil {
		return Resolver{}, ctxerrors.Wrap(
			err,
			"canonicalize harness config root",
		)
	}

	resolvedLimits := limits.withDefaults()
	if err := resolvedLimits.validate(); err != nil {
		return Resolver{}, ctxerrors.Wrap(
			err,
			"validate harness resolver limits",
		)
	}

	return Resolver{
		configRoot: resolvedConfigRoot,
		limits:     resolvedLimits,
	}, nil
}

// Resolve rebuilds the harness snapshot for the selected workspace.
func (r Resolver) Resolve(workspace string) (Snapshot, error) {
	resolvedWorkspace, err := canonicalDirectory(workspace)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "canonicalize harness workspace")
	}

	layers, err := resolutionLayers(r.configRoot, resolvedWorkspace)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(
			err,
			"build harness resolution layers",
		)
	}

	state := newResolutionState(r.limits)
	for priority, layer := range layers {
		if err := state.discoverLayer(
			layer,
			priority,
			layer == r.configRoot,
		); err != nil {
			return Snapshot{}, ctxerrors.Wrapf(
				err,
				"discover harness layer %s",
				layer,
			)
		}
	}

	snapshot, err := state.snapshot(r.configRoot, resolvedWorkspace)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "build harness snapshot")
	}

	return snapshot, nil
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ctxerrors.Wrap(ErrInvalidPath, "path is empty")
	}

	expandedPath, err := expandHome(path)
	if err != nil {
		return "", ctxerrors.Wrap(err, "expand home directory")
	}

	absolutePath, err := filepath.Abs(expandedPath)
	if err != nil {
		return "", ctxerrors.Wrap(err, "make path absolute")
	}

	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", wrapPathError(err, "resolve path symlinks")
	}

	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", wrapPathError(err, "stat path")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(ErrNotDirectory, "path is not a directory")
	}

	return canonicalPath, nil
}

// expandHome resolves a leading "~" to the running user's home directory.
// "~user" (another user's home) is rejected rather than silently
// mis-resolved, and a path with no leading "~" is returned unchanged.
func expandHome(path string) (string, error) {
	if path == homeDirectoryAlias {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ctxerrors.Wrap(err, "resolve user home directory")
		}

		return home, nil
	}

	if rest, ok := strings.CutPrefix(path, homeDirectoryAliasSlash); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ctxerrors.Wrap(err, "resolve user home directory")
		}

		return filepath.Join(home, rest), nil
	}

	if strings.HasPrefix(path, homeDirectoryAlias) {
		return "", ctxerrors.Wrap(
			ErrUnsupportedHomeReference,
			"another user's home directory is not supported",
		)
	}

	return path, nil
}

func resolutionLayers(configRoot string, workspace string) ([]string, error) {
	ancestors, err := workspaceAncestors(workspace)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "discover workspace ancestors")
	}

	layers := make([]string, 0, len(ancestors)+1)

	layers = append(layers, configRoot)
	for _, ancestor := range ancestors {
		if ancestor == configRoot {
			continue
		}

		layers = append(layers, ancestor)
	}

	return layers, nil
}

func workspaceAncestors(workspace string) ([]string, error) {
	ancestors := make([]string, 0)

	current := workspace
	for {
		ancestors = append(ancestors, current)
		if current == rootDirectory {
			break
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil, ctxerrors.Wrap(
				ErrInvalidPath,
				"workspace has no filesystem root",
			)
		}

		current = parent
	}

	reverseStrings(ancestors)

	return ancestors, nil
}

func reverseStrings(values []string) {
	left := 0

	right := len(values) - 1
	for left < right {
		values[left], values[right] = values[right], values[left]
		left++
		right--
	}
}

func newResolutionState(limits Limits) *resolutionState {
	return &resolutionState{
		limits:        limits,
		skills:        make(map[string]discoveredSkill),
		agents:        make(map[string]Agent),
		eventHandlers: make(map[string]EventHandler),
	}
}

func (s *resolutionState) discoverLayer(
	layer string,
	priority int,
	configLayer bool,
) error {
	if err := s.discoverInstructions(layer, priority); err != nil {
		return ctxerrors.Wrap(err, "discover instruction file")
	}

	agentsDirectory, found, err := optionalDirectory(
		filepath.Join(layer, agentsDirectoryName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "discover agents directory")
	}

	if !found {
		return nil
	}

	if err := s.discoverSkills(agentsDirectory); err != nil {
		return ctxerrors.Wrap(err, "discover skills")
	}

	if err := s.discoverNamedAgents(agentsDirectory); err != nil {
		return ctxerrors.Wrap(err, "discover named agents")
	}

	if err := s.discoverEventHandlers(agentsDirectory); err != nil {
		return ctxerrors.Wrap(err, "discover event handlers")
	}

	if err := s.discoverHooks(
		agentsDirectory,
		priority,
		configLayer,
	); err != nil {
		return ctxerrors.Wrap(err, "discover hooks")
	}

	return nil
}

func (s *resolutionState) discoverInstructions(
	layer string,
	priority int,
) error {
	source, content, found, err := s.readOptionalFile(
		filepath.Join(layer, agentsFileName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read instruction file")
	}

	if !found {
		return nil
	}

	if strings.TrimSpace(content) == "" {
		return ctxerrors.Wrap(
			ErrInvalidInstruction,
			"instruction file is empty",
		)
	}

	if len(s.instructions) >= s.limits.MaxInstructions {
		return ctxerrors.Wrap(ErrResourceLimit, "instruction limit exceeded")
	}

	s.instructions = append(s.instructions, Instruction{
		Source:   source,
		Priority: priority,
		Content:  content,
		Hash:     hashString(content),
	})

	return nil
}

func (s *resolutionState) discoverSkills(agentsDirectory string) error {
	skillsDirectory, found, err := optionalDirectory(
		filepath.Join(agentsDirectory, skillsDirectoryName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "discover skills directory")
	}

	if !found {
		return nil
	}

	entries, err := sortedDirectoryEntries(
		skillsDirectory,
		s.limits.MaxDirectoryEntries,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read skills directory")
	}

	for _, entry := range entries {
		if err := s.discoverSkill(skillsDirectory, entry.Name()); err != nil {
			return ctxerrors.Wrap(err, "discover skill")
		}
	}

	return nil
}

func (s *resolutionState) discoverSkill(
	skillsDirectory string,
	entryName string,
) error {
	skillDirectory, err := canonicalDirectory(
		filepath.Join(skillsDirectory, entryName),
	)
	if err != nil {
		return ctxerrors.Wrap(ErrInvalidSkill, "skill entry is not a directory")
	}

	source, content, found, err := s.readOptionalFile(
		filepath.Join(skillDirectory, skillFileName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read skill file")
	}

	if !found {
		return ctxerrors.Wrap(
			ErrInvalidSkill,
			"skill directory has no SKILL.md",
		)
	}

	metadata, _, err := parseSkillDocument(content)
	if err != nil {
		return ctxerrors.Wrap(err, "validate skill document")
	}

	if metadata.Name != entryName {
		return ctxerrors.Wrap(
			ErrInvalidSkill,
			"skill name does not match directory",
		)
	}

	if _, exists := s.skills[metadata.Name]; !exists &&
		len(s.skills) >= s.limits.MaxSkills {
		return ctxerrors.Wrap(ErrResourceLimit, "skill limit exceeded")
	}

	s.skills[metadata.Name] = discoveredSkill{
		skill: Skill{
			Name:          metadata.Name,
			Description:   metadata.Description,
			Source:        source,
			Directory:     skillDirectory,
			Hash:          hashString(content),
			Metadata:      cloneMetadata(metadata.Metadata),
			License:       metadata.License,
			Compatibility: metadata.Compatibility,
			AllowedTools:  metadata.AllowedTools,
		},
		content: content,
	}

	return nil
}

func (s *resolutionState) discoverNamedAgents(agentsDirectory string) error {
	namedAgentsDirectory, found, err := optionalDirectory(
		filepath.Join(agentsDirectory, agentsSubdirectory),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "discover named agents directory")
	}

	if !found {
		return nil
	}

	entries, err := sortedDirectoryEntries(
		namedAgentsDirectory,
		s.limits.MaxDirectoryEntries,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read named agents directory")
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != agentsFileExtension {
			continue
		}

		if err := s.discoverNamedAgent(
			namedAgentsDirectory,
			entry.Name(),
		); err != nil {
			return ctxerrors.Wrap(err, "discover named agent")
		}
	}

	return nil
}

func (s *resolutionState) discoverNamedAgent(
	agentsDirectory string,
	entryName string,
) error {
	source, content, found, err := s.readOptionalFile(
		filepath.Join(agentsDirectory, entryName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read named agent file")
	}

	if !found {
		return ctxerrors.Wrap(ErrInvalidAgent, "named agent file is missing")
	}

	metadata, body, err := parseAgentDocument(content)
	if err != nil {
		return ctxerrors.Wrap(err, "validate named agent document")
	}

	nameFromFile := strings.TrimSuffix(entryName, agentsFileExtension)
	if metadata.Name != nameFromFile {
		return ctxerrors.Wrap(
			ErrInvalidAgent,
			"named agent name does not match file",
		)
	}

	if _, exists := s.agents[metadata.Name]; !exists &&
		len(s.agents) >= s.limits.MaxAgents {
		return ctxerrors.Wrap(ErrResourceLimit, "named agent limit exceeded")
	}

	s.agents[metadata.Name] = Agent{
		Name:         metadata.Name,
		Description:  metadata.Description,
		Source:       source,
		Instructions: body,
		Hash:         hashString(content),
	}

	return nil
}

func (s *resolutionState) discoverEventHandlers(agentsDirectory string) error {
	eventHandlersDirectory, found, err := optionalDirectory(
		filepath.Join(agentsDirectory, eventHandlersSubdirectory),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "discover event handlers directory")
	}

	if !found {
		return nil
	}

	entries, err := sortedDirectoryEntries(
		eventHandlersDirectory,
		s.limits.MaxDirectoryEntries,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read event handlers directory")
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != agentsFileExtension {
			continue
		}

		if err := s.discoverEventHandler(
			eventHandlersDirectory,
			entry.Name(),
		); err != nil {
			return ctxerrors.Wrap(err, "discover event handler")
		}
	}

	return nil
}

func (s *resolutionState) discoverEventHandler(
	eventHandlersDirectory string,
	entryName string,
) error {
	source, content, found, err := s.readOptionalFile(
		filepath.Join(eventHandlersDirectory, entryName),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "read event handler file")
	}

	if !found {
		return ctxerrors.Wrap(
			ErrInvalidEventHandler,
			"event handler file is missing",
		)
	}

	metadata, body, err := parseEventHandlerDocument(content)
	if err != nil {
		return ctxerrors.Wrap(err, "validate event handler document")
	}

	typeFromFile := strings.TrimSuffix(entryName, agentsFileExtension)
	if metadata.Type != typeFromFile {
		return ctxerrors.Wrap(
			ErrInvalidEventHandler,
			"event handler type does not match file",
		)
	}

	if _, exists := s.eventHandlers[metadata.Type]; !exists &&
		len(s.eventHandlers) >= s.limits.MaxEventHandlers {
		return ctxerrors.Wrap(ErrResourceLimit, "event handler limit exceeded")
	}

	s.eventHandlers[metadata.Type] = EventHandler{
		Type:         metadata.Type,
		Agent:        metadata.Agent,
		Delivery:     metadata.Delivery,
		Source:       source,
		Instructions: body,
		Hash:         hashString(content),
	}

	return nil
}

func (s *resolutionState) readOptionalFile(
	path string,
) (string, string, bool, error) {
	canonicalPath, found, err := optionalCanonicalPath(path)
	if err != nil {
		return "", "", false, ctxerrors.Wrap(err, "resolve file symlinks")
	}

	if !found {
		return "", "", false, nil
	}

	if s.files >= s.limits.MaxFiles {
		return "", "", false, ctxerrors.Wrap(
			ErrResourceLimit,
			"file count limit exceeded",
		)
	}

	remainingBytes := s.limits.MaxTotalContextBytes - s.totalBytes
	readLimit := min(s.limits.MaxFileBytes, remainingBytes)

	content, err := readRegularFileLimited(canonicalPath, readLimit)
	if err != nil {
		return "", "", false, ctxerrors.Wrap(err, "read bounded file")
	}

	if err := s.consumeFile(int64(len(content))); err != nil {
		return "", "", false, ctxerrors.Wrap(err, "consume discovered file")
	}

	return canonicalPath, string(content), true, nil
}

func (s *resolutionState) ensureFileSize(fileSize int64) error {
	if fileSize > s.limits.MaxFileBytes {
		return ctxerrors.Wrap(
			ErrResourceLimit,
			"individual file size limit exceeded",
		)
	}

	return nil
}

func (s *resolutionState) consumeFile(fileSize int64) error {
	if s.files >= s.limits.MaxFiles {
		return ctxerrors.Wrap(ErrResourceLimit, "file count limit exceeded")
	}

	if err := s.ensureFileSize(fileSize); err != nil {
		return ctxerrors.Wrap(err, "validate consumed file size")
	}

	if fileSize > s.limits.MaxTotalContextBytes-s.totalBytes {
		return ctxerrors.Wrap(
			ErrResourceLimit,
			"total context byte limit exceeded",
		)
	}

	s.files++
	s.totalBytes += fileSize

	return nil
}

func readRegularFileLimited(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, wrapPathError(err, "open file")
	}

	info, statErr := file.Stat()
	if statErr != nil {
		return nil, closeFile(file, wrapPathError(statErr, "stat open file"))
	}

	if !info.Mode().IsRegular() {
		return nil, closeFile(file, ctxerrors.Wrap(
			ErrUnreadableLayer,
			"discovered file is not regular",
		))
	}

	if info.Size() > maxBytes {
		return nil, closeFile(file, ctxerrors.Wrap(
			ErrResourceLimit,
			"discovered file exceeds remaining byte limit",
		))
	}

	content, exceeded, readErr := readLimited(file, maxBytes)
	if readErr != nil {
		return nil, closeFile(file, ctxerrors.Wrap(readErr, "read file"))
	}

	if exceeded {
		return nil, closeFile(file, ctxerrors.Wrap(
			ErrResourceLimit,
			"discovered file grew beyond remaining byte limit",
		))
	}

	if closeErr := closeFile(file, nil); closeErr != nil {
		return nil, closeErr
	}

	return content, nil
}

func readLimited(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "read limited content")
	}

	if int64(len(content)) > maxBytes {
		return nil, true, nil
	}

	return content, false, nil
}

func closeFile(file *os.File, previousError error) error {
	if err := file.Close(); err != nil {
		return errors.Join(previousError, wrapPathError(err, "close file"))
	}

	return previousError
}

func optionalDirectory(path string) (string, bool, error) {
	canonicalPath, found, err := optionalCanonicalPath(path)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "resolve directory symlinks")
	}

	if !found {
		return "", false, nil
	}

	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", false, wrapPathError(err, "stat directory")
	}

	if !info.IsDir() {
		return "", false, ctxerrors.Wrap(
			ErrUnreadableLayer,
			"discovered directory is not a directory",
		)
	}

	return canonicalPath, true, nil
}

func optionalCanonicalPath(path string) (string, bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}

		return "", false, wrapPathError(err, "lstat optional path")
	}

	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, wrapPathError(err, "resolve optional path symlinks")
	}

	return canonicalPath, true, nil
}

func sortedDirectoryEntries(
	directory string,
	maxEntries int,
) ([]os.DirEntry, error) {
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return nil, wrapPathError(err, "open directory")
	}

	readLimit := maxEntries
	if maxEntries < int(^uint(0)>>1) {
		readLimit++
	}

	entries, readErr := directoryHandle.ReadDir(readLimit)

	closeErr := closeFile(directoryHandle, nil)
	if readErr != nil {
		return nil, errors.Join(
			wrapPathError(readErr, "read directory"),
			closeErr,
		)
	}

	if closeErr != nil {
		return nil, closeErr
	}

	if len(entries) > maxEntries {
		return nil, ctxerrors.Wrap(
			ErrResourceLimit,
			"directory entry limit exceeded",
		)
	}

	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})

	return entries, nil
}

func (s *resolutionState) snapshot(
	configRoot string,
	workspace string,
) (Snapshot, error) {
	skills, skillContents := resolvedSkills(s.skills)
	agents := resolvedAgents(s.agents)
	eventHandlers := resolvedEventHandlers(s.eventHandlers)
	hooks := cloneHooks(s.hooks)
	manifest := resolvedManifest(
		s.instructions,
		skills,
		agents,
		eventHandlers,
		hooks,
	)

	hash, err := snapshotHash(
		configRoot,
		workspace,
		s.instructions,
		skills,
		agents,
		eventHandlers,
		hooks,
		manifest,
	)
	if err != nil {
		return Snapshot{}, ctxerrors.Wrap(err, "hash resolved harness snapshot")
	}

	return Snapshot{
		configRoot:    configRoot,
		workspace:     workspace,
		hash:          hash,
		instructions:  cloneInstructions(s.instructions),
		skills:        cloneSkills(skills),
		agents:        cloneAgents(agents),
		eventHandlers: cloneEventHandlers(eventHandlers),
		hooks:         hooks,
		manifest:      append([]ManifestEntry(nil), manifest...),
		skillContents: skillContents,
	}, nil
}

func resolvedSkills(
	skills map[string]discoveredSkill,
) ([]Skill, map[string]string) {
	names := sortedMapKeys(skills)
	resolved := make([]Skill, 0, len(names))

	contents := make(map[string]string, len(names))
	for _, name := range names {
		discovered := skills[name]
		resolved = append(resolved, cloneSkill(discovered.skill))
		contents[name] = discovered.content
	}

	return resolved, contents
}

func resolvedAgents(agents map[string]Agent) []Agent {
	names := sortedMapKeys(agents)

	resolved := make([]Agent, 0, len(names))
	for _, name := range names {
		resolved = append(resolved, agents[name])
	}

	return resolved
}

func resolvedEventHandlers(
	eventHandlers map[string]EventHandler,
) []EventHandler {
	types := sortedMapKeys(eventHandlers)

	resolved := make([]EventHandler, 0, len(types))
	for _, eventType := range types {
		resolved = append(resolved, eventHandlers[eventType])
	}

	return resolved
}

func resolvedManifest(
	instructions []Instruction,
	skills []Skill,
	agents []Agent,
	eventHandlers []EventHandler,
	hooks []Hook,
) []ManifestEntry {
	manifestCapacity := len(instructions) + len(skills) + len(agents) +
		len(eventHandlers) + len(hooks)

	manifest := make([]ManifestEntry, 0, manifestCapacity)
	for _, instruction := range instructions {
		manifest = append(manifest, ManifestEntry{
			Kind:     SourceKindInstruction,
			Source:   instruction.Source,
			Priority: instruction.Priority,
			Hash:     instruction.Hash,
		})
	}

	for _, skill := range skills {
		manifest = append(manifest, ManifestEntry{
			Kind:   SourceKindSkill,
			Name:   skill.Name,
			Source: skill.Source,
			Hash:   skill.Hash,
		})
	}

	for _, agent := range agents {
		manifest = append(manifest, ManifestEntry{
			Kind:   SourceKindAgent,
			Name:   agent.Name,
			Source: agent.Source,
			Hash:   agent.Hash,
		})
	}

	for _, eventHandler := range eventHandlers {
		manifest = append(manifest, ManifestEntry{
			Kind:   SourceKindEventHandler,
			Name:   eventHandler.Type,
			Source: eventHandler.Source,
			Hash:   eventHandler.Hash,
		})
	}

	for _, hook := range hooks {
		manifest = append(manifest, ManifestEntry{
			Kind:     SourceKindHook,
			Name:     string(hook.Event),
			Source:   hook.Source,
			Priority: hook.Priority,
			Hash:     hook.Hash,
		})
	}

	return manifest
}

func snapshotHash(
	configRoot string,
	workspace string,
	instructions []Instruction,
	skills []Skill,
	agents []Agent,
	eventHandlers []EventHandler,
	hooks []Hook,
	manifest []ManifestEntry,
) (string, error) {
	manifestData := manifestSnapshot{
		Version:       manifestVersion,
		ConfigRoot:    configRoot,
		Workspace:     workspace,
		Instructions:  instructions,
		Skills:        skills,
		Agents:        agents,
		EventHandlers: eventHandlers,
		Hooks:         hooks,
		Manifest:      manifest,
	}
	//nolint:musttag // Internal hash includes typed contract values.
	encoded, err := json.Marshal(manifestData)
	if err != nil {
		return "", ctxerrors.Wrap(err, "marshal harness manifest")
	}

	return hashString(string(encoded)), nil
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata))
	maps.Copy(cloned, metadata)

	return cloned
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func wrapPathError(err error, operation string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ctxerrors.Wrap(errors.Join(ErrInvalidPath, err), operation)
	}

	return ctxerrors.Wrap(errors.Join(ErrUnreadableLayer, err), operation)
}
