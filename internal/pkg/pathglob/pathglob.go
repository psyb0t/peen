// Package pathglob matches slash-separated relative paths against the glob
// patterns hook matchers and scoped rules share, so one pattern means the same
// thing in both.
//
// The syntax follows the patterns Claude Code rules, Cursor rules, and GitHub
// Copilot instructions document:
//
//   - `*` matches any run of characters inside one path segment.
//   - `**` as a whole segment matches zero or more segments, so `**/*.go`
//     matches `main.go` and `cmd/app/main.go`.
//   - `?` matches one character other than `/`.
//   - `[abc]`, `[a-z]`, and `[!abc]` or `[^abc]` match one character from, or
//     not from, the set. They never match `/`.
//   - `{a,b}` matches either alternative, and alternatives may nest.
//   - `\` makes the next character literal.
//
// A brace group with no comma, such as `{a}`, is literal text.
package pathglob

import (
	"errors"
	"regexp"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	separator = '/'

	anySegments       = "(?:.*/)?"
	anyText           = ".*"
	anyInSegment      = "[^/]*"
	oneInSegment      = "[^/]"
	expressionStart   = "^"
	expressionEnd     = "$"
	groupStart        = "(?:"
	groupEnd          = ")"
	alternative       = "|"
	classStart        = "["
	classNegatedStart = "[^/"
	classEnd          = "]"
)

// Compile translates pattern into an anchored regular expression.
//
// A pattern with an unclosed `[` or `{`, or a trailing `\`, fails with
// commerr.ErrValidationFailed.
func Compile(pattern string) (*regexp.Regexp, error) {
	parsed := parser{pattern: pattern}

	body, err := parsed.sequence(false)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "parse glob %q", pattern)
	}

	expression, err := regexp.Compile(expressionStart + body + expressionEnd)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			errors.Join(commerr.ErrValidationFailed, err),
			"compile glob %q",
			pattern,
		)
	}

	return expression, nil
}

// Validate reports whether pattern compiles.
func Validate(pattern string) error {
	_, err := Compile(pattern)

	return err
}

// Match reports whether the slash-separated relative path matches pattern. An
// invalid pattern matches nothing.
func Match(pattern string, path string) bool {
	expression, err := Compile(pattern)
	if err != nil {
		return false
	}

	return expression.MatchString(path)
}

type parser struct {
	pattern string
	index   int
}

// sequence reads pattern text up to the end, or inside a brace group up to the
// `,` or `}` that ends the current alternative, and returns its expression.
func (p *parser) sequence(inGroup bool) (string, error) {
	var expression strings.Builder

	for p.index < len(p.pattern) {
		character := p.pattern[p.index]

		if inGroup && (character == ',' || character == '}') {
			return expression.String(), nil
		}

		part, err := p.element()
		if err != nil {
			return "", err
		}

		expression.WriteString(part)
	}

	if inGroup {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unclosed brace group",
		)
	}

	return expression.String(), nil
}

// element reads one pattern element at the current index.
func (p *parser) element() (string, error) {
	character := p.pattern[p.index]

	switch character {
	case '*':
		return p.star(), nil
	case '?':
		p.index++

		return oneInSegment, nil
	case '[':
		return p.class()
	case '{':
		return p.group()
	case '\\':
		if p.index+1 >= len(p.pattern) {
			return "", ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"pattern ends with an escape",
			)
		}

		p.index += 2

		return regexp.QuoteMeta(string(p.pattern[p.index-1])), nil
	default:
		p.index++

		return regexp.QuoteMeta(string(character)), nil
	}
}

// star reads `*` or `**`. A `**` that is a whole segment followed by `/`
// matches zero or more directories; any other `**` matches any text.
func (p *parser) star() string {
	isDouble := p.index+1 < len(p.pattern) && p.pattern[p.index+1] == '*'
	if !isDouble {
		p.index++

		return anyInSegment
	}

	startsSegment := p.index == 0 || isSegmentStart(p.pattern[p.index-1])
	endsSegment := p.index+2 < len(p.pattern) &&
		p.pattern[p.index+2] == separator

	if startsSegment && endsSegment {
		p.index += 3

		return anySegments
	}

	p.index += 2

	return anyText
}

// isSegmentStart reports whether the character before a `**` starts a new
// segment: a separator, or the start of a brace alternative.
func isSegmentStart(previous byte) bool {
	return previous == separator || previous == '{' || previous == ','
}

// class reads a bracket expression. Its members are kept literal except for
// ranges, and it never matches the path separator.
func (p *parser) class() (string, error) {
	start := p.index
	p.index++

	opening := classStart
	if p.index < len(p.pattern) &&
		(p.pattern[p.index] == '!' || p.pattern[p.index] == '^') {
		opening = classNegatedStart
		p.index++
	}

	var members strings.Builder

	for first := true; p.index < len(p.pattern); first = false {
		character := p.pattern[p.index]
		if character == ']' && !first {
			p.index++

			return opening + members.String() + classEnd, nil
		}

		if character == '\\' && p.index+1 < len(p.pattern) {
			p.index++
			character = p.pattern[p.index]
		}

		members.WriteString(classMember(character))

		p.index++
	}

	return "", ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"unclosed bracket expression at %d",
		start,
	)
}

// classMember escapes one bracket member. A hyphen stays a range operator.
func classMember(character byte) string {
	switch character {
	case '\\', ']', '[', '^':
		return `\` + string(character)
	default:
		return string(character)
	}
}

// group reads a brace group. A group without a top-level comma is literal.
func (p *parser) group() (string, error) {
	start := p.index
	p.index++

	alternatives := make([]string, 0)

	for {
		alternativeExpression, err := p.sequence(true)
		if err != nil {
			return "", ctxerrors.Wrapf(err, "brace group at %d", start)
		}

		alternatives = append(alternatives, alternativeExpression)

		closing := p.pattern[p.index]
		p.index++

		if closing == '}' {
			break
		}
	}

	if len(alternatives) == 1 {
		return regexp.QuoteMeta("{") + alternatives[0] +
			regexp.QuoteMeta("}"), nil
	}

	return groupStart + strings.Join(alternatives, alternative) + groupEnd, nil
}
