package harness

import (
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/pathglob"
	"go.yaml.in/yaml/v3" //nolint:depguard // Parses rule frontmatter shared with other agents.
)

const (
	// rulePathsKey is Claude Code's rule scope. ruleGlobsKey and
	// ruleApplyToKey are the Cursor and GitHub Copilot spellings of the same
	// idea, and ruleAlwaysApplyKey is Cursor's switch back to an unscoped rule.
	rulePathsKey       = "paths"
	ruleGlobsKey       = "globs"
	ruleApplyToKey     = "applyTo"
	ruleAlwaysApplyKey = "alwaysApply"

	rulePatternSeparator = ","
	parentDirectoryName  = ".."
)

// ruleDocument is a rule file split into the text the model reads and the
// patterns that scope it.
type ruleDocument struct {
	body  string
	paths []string
}

// parseRuleDocument strips a leading YAML frontmatter block from a rule and
// reads its scope. Other fields are ignored, because the same file may serve
// Claude Code, Cursor, or GitHub Copilot.
//
// A rule without frontmatter is returned whole. Frontmatter that does not
// parse, or a scope field with the wrong type, still leaves the body without
// the block and no patterns, so the rule loads on every turn as Claude Code
// loads it. The returned error explains the problem for a warning.
func parseRuleDocument(content string) (ruleDocument, error) {
	frontMatter, body, hasFrontMatter := cutRuleFrontMatter(content)
	if !hasFrontMatter {
		return ruleDocument{body: content}, nil
	}

	fields := map[string]any{}
	if err := yaml.Unmarshal([]byte(frontMatter), &fields); err != nil {
		lenient, found := lenientRuleScope(frontMatter)
		if !found {
			return ruleDocument{body: body}, ctxerrors.Wrapf(
				ErrInvalidInstruction,
				"rule frontmatter is not valid YAML, so the rule loads "+
					"unscoped: %s",
				err.Error(),
			)
		}

		fields = lenient
	}

	if alwaysApply, isBool := fields[ruleAlwaysApplyKey].(bool); isBool &&
		alwaysApply {
		return ruleDocument{body: body}, nil
	}

	patterns := make([]string, 0)

	for _, key := range []string{rulePathsKey, ruleGlobsKey, ruleApplyToKey} {
		value, present := fields[key]
		if !present || value == nil {
			continue
		}

		values, err := rulePatternValues(key, value)
		if err != nil {
			return ruleDocument{body: body}, err
		}

		patterns = append(patterns, values...)
	}

	document := ruleDocument{body: body, paths: uniqueStrings(patterns)}

	return document, invalidRulePatterns(document.paths)
}

// lenientRuleScope reads `key: value` scope lines from frontmatter that is not
// valid YAML. Cursor writes globs unquoted, as in `globs: *.ts,*.tsx`, which
// YAML reads as an alias. Only single-line string values are read.
func lenientRuleScope(frontMatter string) (map[string]any, bool) {
	fields := map[string]any{}

	for line := range strings.SplitSeq(frontMatter, "\n") {
		key, value, hasValue := strings.Cut(line, ":")
		if !hasValue {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		switch key {
		case rulePathsKey, ruleGlobsKey, ruleApplyToKey:
			if value != "" {
				fields[key] = value
			}
		case ruleAlwaysApplyKey:
			fields[key] = value == "true"
		}
	}

	return fields, len(fields) > 0
}

// cutRuleFrontMatter splits a leading block between two "---" lines from the
// body. A file whose first line is not "---", or whose block never closes, has
// no frontmatter.
func cutRuleFrontMatter(content string) (string, string, bool) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != frontMatterDelimiter {
		return "", content, false
	}

	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != frontMatterDelimiter {
			continue
		}

		frontMatter := strings.Join(lines[1:index], "\n")
		body := strings.TrimLeft(strings.Join(lines[index+1:], "\n"), "\n")

		return frontMatter, body, true
	}

	return "", content, false
}

// rulePatternValues reads one scope field: a list of patterns, or one string
// holding comma-separated patterns.
func rulePatternValues(key string, value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return splitRulePatterns(typed), nil
	case []any:
		patterns := make([]string, 0, len(typed))

		for _, item := range typed {
			text, isText := item.(string)
			if !isText {
				return nil, ctxerrors.Wrapf(
					ErrInvalidInstruction,
					"rule %s must list strings, so the rule loads unscoped",
					key,
				)
			}

			patterns = appendTrimmed(patterns, text)
		}

		return patterns, nil
	default:
		return nil, ctxerrors.Wrapf(
			ErrInvalidInstruction,
			"rule %s must be a string or a list, so the rule loads unscoped",
			key,
		)
	}
}

// splitRulePatterns breaks a comma-separated scope string. A comma inside a
// brace group belongs to the group, so `src/*.{ts,tsx}` stays one pattern.
func splitRulePatterns(value string) []string {
	patterns := make([]string, 0)
	depth, start := 0, 0

	for index := range len(value) {
		switch value[index] {
		case '{':
			depth++
		case '}':
			depth = max(depth-1, 0)
		case rulePatternSeparator[0]:
			if depth > 0 {
				continue
			}

			patterns = appendTrimmed(patterns, value[start:index])
			start = index + 1
		}
	}

	return appendTrimmed(patterns, value[start:])
}

func appendTrimmed(values []string, value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return values
	}

	return append(values, trimmed)
}

// invalidRulePatterns reports the patterns that match nothing because they do
// not parse. The rule keeps its other patterns.
func invalidRulePatterns(patterns []string) error {
	invalid := make([]string, 0)

	for _, pattern := range patterns {
		if pathglob.Validate(pattern) != nil {
			invalid = append(invalid, pattern)
		}
	}

	if len(invalid) == 0 {
		return nil
	}

	return ctxerrors.Wrapf(
		ErrInvalidInstruction,
		"rule patterns %q are not valid globs and match nothing",
		invalid,
	)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))

	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		unique = append(unique, value)
	}

	return unique
}

// MatchingRules returns the scoped rules whose patterns match at least one of
// paths, in prompt order. Each path is made relative to the rule's base, and a
// path outside the base matches nothing.
func (s Snapshot) MatchingRules(paths []string) []Instruction {
	matched := make([]Instruction, 0)

	for _, instruction := range s.instructions {
		if !instruction.IsScoped() {
			continue
		}

		if ruleMatchesAny(instruction, paths) {
			matched = append(matched, cloneInstructions(
				[]Instruction{instruction},
			)[0])
		}
	}

	return matched
}

func ruleMatchesAny(rule Instruction, paths []string) bool {
	for _, affected := range paths {
		relative, err := filepath.Rel(rule.Base, affected)
		if err != nil || isOutsideBase(relative) {
			continue
		}

		slashed := filepath.ToSlash(relative)
		for _, pattern := range rule.Paths {
			if pathglob.Match(pattern, slashed) {
				return true
			}
		}
	}

	return false
}

func isOutsideBase(relative string) bool {
	return relative == parentDirectoryName ||
		strings.HasPrefix(
			relative,
			parentDirectoryName+string(filepath.Separator),
		)
}
