package harness

import (
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/events"
	"go.yaml.in/yaml/v3" //nolint:depguard // Parses the Agent Skills YAML contract.
)

const (
	frontMatterDelimiter      = "---"
	frontMatterMetaKey        = "metadata"
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
	maxCompatibilityLength    = 500
	yamlMappingPairSize       = 2
	yamlStringTag             = "!!str"
)

type skillFrontMatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	//nolint:tagliatelle // Agent Skills requires the exact YAML spelling.
	AllowedTools string `yaml:"allowed-tools"`
}

type agentFrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type eventHandlerFrontMatter struct {
	Type     string `yaml:"type"`
	Agent    string `yaml:"agent"`
	Delivery string `yaml:"delivery"`
}

func parseSkillDocument(content string) (skillFrontMatter, error) {
	frontMatter, _, err := splitFrontMatter(content, ErrInvalidSkill)
	if err != nil {
		return skillFrontMatter{}, ctxerrors.Wrap(
			err,
			"split skill frontmatter",
		)
	}

	metadata := skillFrontMatter{}

	values, err := validateSkillYAMLTypes(frontMatter)
	if err != nil {
		return skillFrontMatter{}, ctxerrors.Wrap(
			err,
			"validate skill YAML types",
		)
	}

	if err := decodeFrontMatter(
		frontMatter,
		&metadata,
		ErrInvalidSkill,
	); err != nil {
		return skillFrontMatter{}, ctxerrors.Wrap(
			err,
			"decode skill frontmatter",
		)
	}

	_, compatibilityPresent := values["compatibility"]
	if err := validateSkillFrontMatter(
		metadata,
		compatibilityPresent,
	); err != nil {
		return skillFrontMatter{}, ctxerrors.Wrap(
			err,
			"validate skill frontmatter",
		)
	}

	return metadata, nil
}

func parseAgentDocument(content string) (agentFrontMatter, string, error) {
	frontMatter, body, err := splitFrontMatter(content, ErrInvalidAgent)
	if err != nil {
		return agentFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"split named agent frontmatter",
		)
	}

	metadata := agentFrontMatter{}

	if err := validateAgentYAMLTypes(frontMatter); err != nil {
		return agentFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"validate named agent YAML types",
		)
	}

	if err := decodeFrontMatter(
		frontMatter,
		&metadata,
		ErrInvalidAgent,
	); err != nil {
		return agentFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"decode named agent frontmatter",
		)
	}

	if metadata.Name == "" || metadata.Description == "" {
		return agentFrontMatter{}, "", ctxerrors.Wrap(
			ErrInvalidAgent,
			"named agent name and description are required",
		)
	}

	if err := validateKebabName(metadata.Name); err != nil {
		return agentFrontMatter{}, "", ctxerrors.Wrap(
			ErrInvalidAgent,
			"validate named agent name",
		)
	}

	return metadata, body, nil
}

func parseEventHandlerDocument(
	content string,
) (eventHandlerFrontMatter, string, error) {
	frontMatter, body, err := splitFrontMatter(content, ErrInvalidEventHandler)
	if err != nil {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"split event handler frontmatter",
		)
	}

	metadata := eventHandlerFrontMatter{}

	if err := validateEventHandlerYAMLTypes(frontMatter); err != nil {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"validate event handler YAML types",
		)
	}

	if err := decodeFrontMatter(
		frontMatter,
		&metadata,
		ErrInvalidEventHandler,
	); err != nil {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			err,
			"decode event handler frontmatter",
		)
	}

	if metadata.Type == "" {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			ErrInvalidEventHandler,
			"event handler type is required",
		)
	}

	if err := events.ValidateType(metadata.Type); err != nil {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			ErrInvalidEventHandler,
			"validate event handler type",
		)
	}

	normalizedDelivery, err := events.ValidateDelivery(metadata.Delivery)
	if err != nil {
		return eventHandlerFrontMatter{}, "", ctxerrors.Wrap(
			ErrInvalidEventHandler,
			"validate event handler delivery",
		)
	}

	metadata.Delivery = normalizedDelivery

	return metadata, body, nil
}

func splitFrontMatter(
	content string,
	invalidError error,
) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != frontMatterDelimiter {
		return "", "", ctxerrors.Wrap(invalidError, "missing frontmatter")
	}

	closingIndex := frontMatterClosingIndex(lines)
	if closingIndex < 0 {
		return "", "", ctxerrors.Wrap(invalidError, "unterminated frontmatter")
	}

	body := strings.Join(lines[closingIndex+1:], "\n")
	if strings.TrimSpace(body) == "" {
		return "", "", ctxerrors.Wrap(invalidError, "missing instructions")
	}

	return strings.Join(lines[1:closingIndex], "\n"), body, nil
}

func frontMatterClosingIndex(lines []string) int {
	for index := 1; index < len(lines); index++ {
		if lines[index] == frontMatterDelimiter {
			return index
		}
	}

	return -1
}

func decodeFrontMatter(
	frontMatter string,
	target any,
	invalidError error,
) error {
	decoder := yaml.NewDecoder(strings.NewReader(frontMatter))
	decoder.KnownFields(true)

	if err := decoder.Decode(target); err != nil {
		return ctxerrors.Wrap(
			errors.Join(invalidError, err),
			"decode strict YAML frontmatter",
		)
	}

	var extraDocument any
	if err := decoder.Decode(&extraDocument); !errors.Is(err, io.EOF) {
		if err == nil {
			return ctxerrors.Wrap(
				invalidError,
				"frontmatter has multiple YAML documents",
			)
		}

		return ctxerrors.Wrap(
			errors.Join(invalidError, err),
			"check strict YAML frontmatter document count",
		)
	}

	return nil
}

func validateSkillYAMLTypes(
	frontMatter string,
) (map[string]*yaml.Node, error) {
	values, err := frontMatterValues(frontMatter, ErrInvalidSkill)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse skill YAML nodes")
	}

	for key, value := range values {
		if key == frontMatterMetaKey {
			if err := validateMetadataNode(value, ErrInvalidSkill); err != nil {
				return nil, ctxerrors.Wrap(err, "validate skill metadata")
			}

			continue
		}

		if err := validateStringNode(value, ErrInvalidSkill); err != nil {
			return nil, ctxerrors.Wrapf(err, "validate skill field %s", key)
		}
	}

	return values, nil
}

func validateAgentYAMLTypes(frontMatter string) error {
	values, err := frontMatterValues(frontMatter, ErrInvalidAgent)
	if err != nil {
		return ctxerrors.Wrap(err, "parse named agent YAML nodes")
	}

	for key, value := range values {
		if err := validateStringNode(value, ErrInvalidAgent); err != nil {
			return ctxerrors.Wrapf(err, "validate named agent field %s", key)
		}
	}

	return nil
}

func validateEventHandlerYAMLTypes(frontMatter string) error {
	values, err := frontMatterValues(frontMatter, ErrInvalidEventHandler)
	if err != nil {
		return ctxerrors.Wrap(err, "parse event handler YAML nodes")
	}

	for key, value := range values {
		if err := validateStringNode(
			value,
			ErrInvalidEventHandler,
		); err != nil {
			return ctxerrors.Wrapf(
				err,
				"validate event handler field %s",
				key,
			)
		}
	}

	return nil
}

func frontMatterValues(
	frontMatter string,
	invalidError error,
) (map[string]*yaml.Node, error) {
	document := yaml.Node{}
	if err := yaml.Unmarshal([]byte(frontMatter), &document); err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(invalidError, err),
			"unmarshal YAML nodes",
		)
	}

	if len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode {
		return nil, ctxerrors.Wrap(
			invalidError,
			"frontmatter must be a YAML mapping",
		)
	}

	mapping := document.Content[0]

	values := make(
		map[string]*yaml.Node,
		len(mapping.Content)/yamlMappingPairSize,
	)
	for index := 0; index < len(mapping.Content); index += yamlMappingPairSize {
		keyNode := mapping.Content[index]
		valueNode := mapping.Content[index+1]
		values[keyNode.Value] = valueNode
	}

	return values, nil
}

func validateMetadataNode(value *yaml.Node, invalidError error) error {
	if value.Kind != yaml.MappingNode {
		return ctxerrors.Wrap(invalidError, "metadata must be a string mapping")
	}

	for index := 0; index < len(value.Content); index += yamlMappingPairSize {
		if err := validateStringNode(
			value.Content[index],
			invalidError,
		); err != nil {
			return ctxerrors.Wrap(err, "metadata key is not a string")
		}

		if err := validateStringNode(
			value.Content[index+1],
			invalidError,
		); err != nil {
			return ctxerrors.Wrap(err, "metadata value is not a string")
		}
	}

	return nil
}

func validateStringNode(value *yaml.Node, invalidError error) error {
	if value.Kind != yaml.ScalarNode || value.Tag != yamlStringTag {
		return ctxerrors.Wrap(
			invalidError,
			"frontmatter value must be a string",
		)
	}

	return nil
}

func validateSkillFrontMatter(
	metadata skillFrontMatter,
	compatibilityPresent bool,
) error {
	if metadata.Name == "" || metadata.Description == "" {
		return ctxerrors.Wrap(
			ErrInvalidSkill,
			"skill name and description are required",
		)
	}

	descriptionLength := utf8.RuneCountInString(metadata.Description)

	compatibilityLength := utf8.RuneCountInString(metadata.Compatibility)
	if compatibilityPresent && compatibilityLength == 0 {
		return ctxerrors.Wrap(
			ErrInvalidSkill,
			"skill compatibility must not be empty when present",
		)
	}

	nameLength := utf8.RuneCountInString(metadata.Name)
	if nameLength > maxSkillNameLength ||
		descriptionLength > maxSkillDescriptionLength ||
		compatibilityLength > maxCompatibilityLength {
		return ctxerrors.Wrap(
			ErrInvalidSkill,
			"skill frontmatter exceeds specification limits",
		)
	}

	if err := validateKebabName(metadata.Name); err != nil {
		return ctxerrors.Wrap(ErrInvalidSkill, "validate skill name")
	}

	return nil
}

func validateKebabName(name string) error {
	if hasInvalidKebabEdges(name) {
		return ctxerrors.Wrap(
			ErrInvalidPath,
			"name is not lowercase kebab case",
		)
	}

	for _, character := range name {
		if isKebabCharacter(character) {
			continue
		}

		return ctxerrors.Wrap(
			ErrInvalidPath,
			"name is not lowercase kebab case",
		)
	}

	return nil
}

func hasInvalidKebabEdges(name string) bool {
	return name == "" || strings.HasPrefix(name, "-") ||
		strings.HasSuffix(name, "-") || strings.Contains(name, "--")
}

func isKebabCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' || character == '-'
}
