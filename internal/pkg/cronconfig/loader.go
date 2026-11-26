package cronconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // Bundle IANA zones in minimal runtime images.
	"unicode"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"
)

type rawJob struct {
	Name            string `yaml:"name"`
	Schedule        string `yaml:"schedule"`
	TimeZone        string `yaml:"timeZone"`
	Workspace       string `yaml:"workspace"`
	AgentSpace      string `yaml:"agentSpace"`
	Message         string `yaml:"message"`
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoningEffort"`
	Timeout         string `yaml:"timeout"`
	TelegramChatID  int64  `yaml:"telegramChatId"`
}

type yamlInspection struct {
	nodes   int
	aliases int
}

// Load reads .agents/cron.yaml once and returns validated jobs.
func Load(configDirectory string, options Options) ([]Job, error) {
	filename := filepath.Join(configDirectory, ".agents", cronConfigFileName)

	data, err := readCronConfiguration(filename)
	if errors.Is(err, os.ErrNotExist) {
		return []Job{}, nil
	}

	if err != nil {
		return nil, err
	}

	return Parse(data, options)
}

func readCronConfiguration(filename string) ([]byte, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "stat cron configuration")
	}

	if !info.Mode().IsRegular() {
		return nil, validationError("cron configuration must be a regular file")
	}

	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open cron configuration")
	}

	openedInfo, statErr := file.Stat()
	if statErr != nil {
		closeErr := file.Close()

		return nil, errors.Join(
			ctxerrors.Wrap(statErr, "stat opened cron configuration"),
			wrapOptionalError(closeErr, "close cron configuration"),
		)
	}

	if !openedInfo.Mode().IsRegular() {
		closeErr := file.Close()

		return nil, errors.Join(
			validationError("cron configuration must be a regular file"),
			wrapOptionalError(closeErr, "close cron configuration"),
		)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))

	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapOptionalError(readErr, "read cron configuration"),
			wrapOptionalError(closeErr, "close cron configuration"),
		)
	}

	return data, nil
}

func wrapOptionalError(err error, operation string) error {
	if err == nil {
		return nil
	}

	return ctxerrors.Wrap(err, operation)
}

// Parse validates one bounded YAML document without reading deployment files.
func Parse(data []byte, options Options) ([]Job, error) {
	if len(data) > maximumConfigBytes {
		return nil, validationError("cron configuration exceeds its byte limit")
	}

	options, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}

	jobsNode, err := parseJobsNode(data)
	if err != nil {
		return nil, err
	}

	if len(jobsNode.Content) > maximumJobs {
		return nil, validationError("cron configuration has too many jobs")
	}

	jobs := make([]Job, 0, len(jobsNode.Content))

	seen := make(map[string]struct{}, len(jobsNode.Content))

	for index, node := range jobsNode.Content {
		job, err := parseJob(node, index, options)
		if err != nil {
			return nil, err
		}

		if _, exists := seen[job.Name]; exists {
			return nil, jobFieldError(job.Name, jobFieldName, "is duplicated")
		}

		seen[job.Name] = struct{}{}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.MaxMessageBytes < 0 || options.MaxSchemaBytes < 0 {
		return Options{}, validationError(
			"cron parser limits must not be negative")
	}

	if options.MaxMessageBytes == 0 {
		options.MaxMessageBytes = defaultMaximumMessage
	}

	if options.MaxSchemaBytes == 0 {
		options.MaxSchemaBytes = defaultMaximumSchema
	}

	return options, nil
}

func parseJobsNode(data []byte) (*yaml.Node, error) {
	document, err := decodeYAMLDocument(data)
	if err != nil {
		return nil, err
	}

	if document.Kind == yaml.SequenceNode {
		return document, nil
	}

	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, validationError(
			"cron configuration must be a YAML mapping",
		)
	}

	root := resolveYAMLAlias(document.Content[0])

	rootFields, err := mappingFields(root)
	if err != nil {
		return nil, err
	}

	if err := validateRootFields(rootFields); err != nil {
		return nil, err
	}

	jobsNode := resolveYAMLAlias(rootFields[cronJobsField])
	if jobsNode == nil || jobsNode.Kind != yaml.SequenceNode {
		return nil, validationError(
			"cron configuration field jobs must be a sequence",
		)
	}

	return jobsNode, nil
}

func decodeYAMLDocument(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	document := new(yaml.Node)
	if err := decoder.Decode(document); err != nil {
		if errors.Is(err, io.EOF) {
			return &yaml.Node{Kind: yaml.SequenceNode}, nil
		}

		return nil, parseError("cron configuration YAML is invalid")
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, validationError(
			"cron configuration must contain one YAML document",
		)
	}

	inspection := yamlInspection{}
	if err := inspectYAMLNode(
		document,
		&inspection,
		make(map[*yaml.Node]bool),
		0,
	); err != nil {
		return nil, err
	}

	return document, nil
}

func validateRootFields(fields map[string]*yaml.Node) error {
	for name := range fields {
		if name != cronJobsField {
			return validationError("cron configuration has an unknown field")
		}
	}

	return nil
}

func inspectYAMLNode(
	node *yaml.Node,
	inspection *yamlInspection,
	active map[*yaml.Node]bool,
	depth int,
) error {
	if node == nil || depth > maximumYAMLDepth {
		return validationError("cron configuration YAML nesting is too deep")
	}

	inspection.nodes++
	if inspection.nodes > maximumYAMLNodes {
		return validationError(
			"cron configuration YAML contains too many nodes",
		)
	}

	if node.Kind == yaml.AliasNode {
		return inspectYAMLAlias(node, inspection, active, depth)
	}

	if active[node] {
		return validationError("cron configuration YAML contains a cycle")
	}

	active[node] = true
	defer delete(active, node)

	if node.Kind == yaml.MappingNode {
		if err := validateUniqueYAMLKeys(node); err != nil {
			return err
		}
	}

	for _, child := range node.Content {
		if err := inspectYAMLNode(
			child,
			inspection,
			active,
			depth+1,
		); err != nil {
			return err
		}
	}

	return nil
}

func inspectYAMLAlias(
	node *yaml.Node,
	inspection *yamlInspection,
	active map[*yaml.Node]bool,
	depth int,
) error {
	inspection.aliases++
	if inspection.aliases > maximumYAMLAliases || node.Alias == nil {
		return validationError(
			"cron configuration YAML has too many aliases",
		)
	}

	// Follow aliases through the same depth and cycle checks as ordinary nodes.
	return inspectYAMLNode(node.Alias, inspection, active, depth+1)
}

func validateUniqueYAMLKeys(node *yaml.Node) error {
	if len(node.Content)%yamlMappingPairWidth != 0 {
		return validationError("cron configuration YAML mapping is invalid")
	}

	seen := make(map[string]struct{}, len(node.Content)/yamlMappingPairWidth)
	for index := 0; index < len(node.Content); index += yamlMappingPairWidth {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag {
			return validationError(
				"cron configuration YAML keys must be strings",
			)
		}

		if _, exists := seen[key.Value]; exists {
			return validationError(
				"cron configuration contains a duplicate YAML key",
			)
		}

		seen[key.Value] = struct{}{}
	}

	return nil
}

func mappingFields(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode ||
		len(node.Content)%yamlMappingPairWidth != 0 {
		return nil, validationError("cron configuration mapping is invalid")
	}

	fields := make(map[string]*yaml.Node,
		len(node.Content)/yamlMappingPairWidth)
	for index := 0; index < len(node.Content); index += yamlMappingPairWidth {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag {
			return nil, validationError(
				"cron configuration keys must be strings",
			)
		}

		fields[key.Value] = node.Content[index+1]
	}

	return fields, nil
}

func resolveYAMLAlias(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}

	return node
}

func parseJob(node *yaml.Node, index int, options Options) (Job, error) {
	node = resolveYAMLAlias(node)

	fields, err := mappingFields(node)
	if err != nil {
		return Job{}, ctxerrors.Wrapf(commerr.ErrValidationFailed,
			"job at index %d is invalid", index)
	}

	name := safeJobName(fields[jobFieldName])
	if name == "" {
		return Job{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"job at index %d field %s is invalid",
			index,
			jobFieldName,
		)
	}

	if err := validateJobFields(name, fields); err != nil {
		return Job{}, err
	}

	var raw rawJob
	if err := node.Decode(&raw); err != nil {
		return Job{}, jobFieldError(name, "configuration", "is invalid")
	}

	return resolveJob(name, raw, fields, options)
}

func safeJobName(node *yaml.Node) string {
	node = resolveYAMLAlias(node)
	if !isJobNameNode(node) {
		return ""
	}

	for _, char := range node.Value {
		if !isJobNameCharacter(char) {
			return ""
		}
	}

	return node.Value
}

func isJobNameNode(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode &&
		node.Tag == yamlStringTag && len(node.Value) > 0 &&
		len(node.Value) <= maximumJobNameBytes
}

func isJobNameCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '_' || char == '-'
}

func validateJobFields(name string, fields map[string]*yaml.Node) error {
	if err := validateKnownJobFields(name, fields); err != nil {
		return err
	}

	if err := validateJobFieldTypes(name, fields); err != nil {
		return err
	}

	return validateRequiredJobFields(name, fields)
}

func validateKnownJobFields(name string, fields map[string]*yaml.Node) error {
	for field := range fields {
		if !knownJobField(field) {
			return jobFieldError(name, unknownFieldLabel(field), "is unknown")
		}
	}

	return nil
}

func knownJobField(field string) bool {
	switch field {
	case jobFieldName, jobFieldSchedule, jobFieldTimeZone,
		jobFieldWorkspace, jobFieldAgentSpace,
		jobFieldMessage, jobFieldModel, jobFieldReasoningEffort,
		jobFieldTimeout, jobFieldOutputSchema, jobFieldTelegramChatID:
		return true
	default:
		return false
	}
}

func validateJobFieldTypes(name string, fields map[string]*yaml.Node) error {
	for _, field := range []string{
		jobFieldName, jobFieldSchedule, jobFieldTimeZone,
		jobFieldWorkspace, jobFieldAgentSpace,
		jobFieldMessage, jobFieldModel, jobFieldReasoningEffort,
		jobFieldTimeout,
	} {
		if err := requireStringField(name, field, fields[field]); err != nil {
			return err
		}
	}

	if err := requireIntegerField(
		name,
		jobFieldTelegramChatID,
		fields[jobFieldTelegramChatID],
	); err != nil {
		return err
	}

	return requireMappingField(name, jobFieldOutputSchema,
		fields[jobFieldOutputSchema])
}

func requireStringField(name, field string, node *yaml.Node) error {
	node = resolveYAMLAlias(node)
	if node != nil && (node.Kind != yaml.ScalarNode ||
		node.Tag != yamlStringTag) {
		return jobFieldError(name, field, "must be a string")
	}

	return nil
}

func requireIntegerField(name, field string, node *yaml.Node) error {
	node = resolveYAMLAlias(node)
	if node != nil && (node.Kind != yaml.ScalarNode || node.Tag != "!!int") {
		return jobFieldError(name, field, "must be an integer")
	}

	return nil
}

func requireMappingField(name, field string, node *yaml.Node) error {
	node = resolveYAMLAlias(node)
	if node != nil && node.Kind != yaml.MappingNode {
		return jobFieldError(name, field, "must be a mapping")
	}

	return nil
}

func validateRequiredJobFields(
	name string,
	fields map[string]*yaml.Node,
) error {
	for _, field := range []string{
		jobFieldName, jobFieldSchedule, jobFieldWorkspace, jobFieldMessage,
	} {
		if fields[field] == nil {
			return jobFieldError(name, field, "is required")
		}
	}

	return nil
}

func resolveJob(
	name string,
	raw rawJob,
	fields map[string]*yaml.Node,
	options Options,
) (Job, error) {
	if err := validateJobScalars(name, raw, options); err != nil {
		return Job{}, err
	}

	timeZone, err := resolveJobTimeZone(name, raw.TimeZone)
	if err != nil {
		return Job{}, err
	}

	schedule, err := parseSchedule(raw.Schedule, timeZone)
	if err != nil {
		return Job{}, jobFieldError(name, "schedule", "is invalid")
	}

	timeout, err := parseTimeout(raw.Timeout)
	if err != nil {
		return Job{}, jobFieldError(name, jobFieldTimeout, "is invalid")
	}

	schema, err := parseOutputSchema(
		name,
		fields[jobFieldOutputSchema],
		options.MaxSchemaBytes,
	)
	if err != nil {
		return Job{}, err
	}

	return Job{
		Name:            raw.Name,
		ScheduleText:    raw.Schedule,
		Schedule:        schedule,
		TimeZone:        timeZone,
		Workspace:       raw.Workspace,
		AgentSpace:      raw.AgentSpace,
		Message:         raw.Message,
		Model:           raw.Model,
		ReasoningEffort: raw.ReasoningEffort,
		Timeout:         timeout,
		OutputSchema:    schema,
		TelegramChatID:  raw.TelegramChatID,
	}, nil
}

func resolveJobTimeZone(name, timeZone string) (string, error) {
	if timeZone == "" {
		timeZone = defaultTimeZone
	}

	if !validTimeZoneName(timeZone) || timeZone == "Local" {
		return "", jobFieldError(name, jobFieldTimeZone, "is invalid")
	}

	location, err := time.LoadLocation(timeZone)
	if err != nil || location == nil {
		return "", jobFieldError(name, jobFieldTimeZone, "is invalid")
	}

	return timeZone, nil
}

func validateJobScalars(name string, raw rawJob, options Options) error {
	if err := validateScheduleScalar(name, raw.Schedule); err != nil {
		return err
	}

	if err := validateWorkspaceScalar(name, raw.Workspace); err != nil {
		return err
	}

	if err := validateAgentSpaceScalar(name, raw.AgentSpace); err != nil {
		return err
	}

	if err := validateMessageScalar(
		name,
		raw.Message,
		options.MaxMessageBytes,
	); err != nil {
		return err
	}

	if err := validateOptionalJobScalars(name, raw, options); err != nil {
		return err
	}

	return nil
}

func validateScheduleScalar(name, schedule string) error {
	if len(schedule) == 0 || len(schedule) > maximumScheduleBytes {
		return jobFieldError(name, jobFieldSchedule, "has an invalid length")
	}

	return nil
}

func validateWorkspaceScalar(name, workspace string) error {
	if len(workspace) == 0 || len(workspace) > maximumWorkspaceBytes ||
		!filepath.IsAbs(workspace) {
		return jobFieldError(name, jobFieldWorkspace,
			"must be an absolute path")
	}

	return nil
}

func validateAgentSpaceScalar(name, agentSpace string) error {
	if len(agentSpace) > maximumAgentSpaceBytes {
		return jobFieldError(name, jobFieldAgentSpace, "has an invalid length")
	}

	return nil
}

func validateMessageScalar(name, message string, maximum int) error {
	if len(message) == 0 || len(message) > maximum ||
		strings.TrimSpace(message) == "" {
		return jobFieldError(name, jobFieldMessage, "has an invalid length")
	}

	return nil
}

func validateOptionalJobScalars(
	name string,
	raw rawJob,
	options Options,
) error {
	if err := validateModelScalars(name, raw, options); err != nil {
		return err
	}

	return validateOptionalBounds(name, raw, options)
}

func validateModelScalars(
	name string,
	raw rawJob,
	options Options,
) error {
	if len(raw.Model) > maximumModelIDBytes ||
		(raw.Model != "" && !slices.Contains(options.ModelIDs, raw.Model)) {
		return jobFieldError(name, jobFieldModel, "is not configured")
	}

	if len(raw.ReasoningEffort) > maximumEffortBytes ||
		(raw.ReasoningEffort != "" &&
			!validReasoningEffort(raw.ReasoningEffort)) {
		return jobFieldError(name, jobFieldReasoningEffort, "is invalid")
	}

	return nil
}

func validateOptionalBounds(
	name string,
	raw rawJob,
	options Options,
) error {
	if len(raw.TimeZone) > maximumTimeZoneBytes {
		return jobFieldError(name, jobFieldTimeZone, "has an invalid length")
	}

	if len(raw.Timeout) > maximumTimeoutBytes {
		return jobFieldError(name, jobFieldTimeout, "has an invalid length")
	}

	if raw.TelegramChatID != 0 && !options.TelegramEnabled {
		return jobFieldError(
			name,
			jobFieldTelegramChatID,
			"requires Telegram to be enabled",
		)
	}

	return nil
}

//nolint:ireturn // Cron owns the parsed schedule interface.
func parseSchedule(scheduleText, timeZone string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(scheduleText)

	fields := strings.Fields(trimmed)
	if hasEmbeddedTimeZone(fields) {
		return nil, validationError("invalid schedule")
	}

	if fields[0] == "@every" {
		if err := validateIntervalDescriptor(fields); err != nil {
			return nil, err
		}
	}

	schedule, err := newScheduleParser().Parse(
		"CRON_TZ=" + timeZone + " " + trimmed,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"cron schedule is invalid")
	}

	if schedule.Next(time.Date(2000, time.January, 1, 0, 0, 0, 0,
		time.UTC)).IsZero() {
		return nil, validationError("schedule has no occurrence")
	}

	return schedule, nil
}

func hasEmbeddedTimeZone(fields []string) bool {
	if len(fields) == 0 {
		return true
	}

	return strings.HasPrefix(fields[0], "CRON_TZ=") ||
		strings.HasPrefix(fields[0], "TZ=")
}

func validateIntervalDescriptor(fields []string) error {
	if len(fields) != cronIntervalFieldCount {
		return validationError("invalid interval descriptor")
	}

	interval, err := time.ParseDuration(fields[1])
	if err != nil || interval <= 0 || interval%time.Second != 0 {
		return validationError("interval must be positive whole seconds")
	}

	return nil
}

func parseTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	timeout, err := time.ParseDuration(value)
	if err != nil || timeout < 0 || timeout%time.Second != 0 ||
		timeout/time.Second > agent.MaxTurnTimeoutSeconds {
		return 0, validationError("invalid timeout")
	}

	return timeout, nil
}

func parseOutputSchema(
	jobName string,
	node *yaml.Node,
	maximum int,
) (json.RawMessage, error) {
	if node == nil {
		return nil, nil
	}

	node = resolveYAMLAlias(node)

	var value any
	if err := node.Decode(&value); err != nil {
		return nil, jobFieldError(jobName, jobFieldOutputSchema, "is invalid")
	}

	schema, err := json.Marshal(value)
	if err != nil || len(schema) > maximum ||
		agent.ValidateOutputSchema(schema, maximum) != nil {
		return nil, jobFieldError(jobName, jobFieldOutputSchema, "is invalid")
	}

	return json.RawMessage(schema), nil
}

func validReasoningEffort(effort string) bool {
	return slices.Contains([]string{
		elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh,
		elelem.ReasoningEffortMax,
	}, effort)
}

func jobFieldError(name, field, reason string) error {
	return ctxerrors.Wrapf(commerr.ErrValidationFailed,
		"job %q field %s %s", name, field, reason)
}

func validationError(message string) error {
	return ctxerrors.Wrap(commerr.ErrValidationFailed, message)
}

func parseError(message string) error {
	return ctxerrors.Wrap(commerr.ErrParseFailed, message)
}

func validTimeZoneName(timeZone string) bool {
	return timeZone != "" && strings.IndexFunc(timeZone, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char)
	}) == -1
}

func unknownFieldLabel(field string) string {
	if len(field) == 0 || len(field) > maximumJobNameBytes {
		return "unknown"
	}

	for _, char := range field {
		if !isUnknownFieldCharacter(char) {
			return "unknown"
		}
	}

	return field
}

func isUnknownFieldCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '_'
}
