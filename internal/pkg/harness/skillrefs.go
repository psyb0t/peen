package harness

import (
	"fmt"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
)

const (
	skillReferenceMarker = ':'

	// skillReferenceBoundaryRunes end a reference besides whitespace.
	skillReferenceBoundaryRunes = ",.;!?)]}"
)

// SkillReferences finds standalone :skill-name references in a user message or
// in a skill body. A reference is exact, not semantic: it starts the text or
// follows whitespace, and it ends at a normal token boundary. A core skill is
// written :core:skill-name, and only that exact prefix may carry a colon. Each
// name appears once, in the order of its first reference.
func SkillReferences(text string) []string {
	names := make([]string, 0)

	for index := 0; index < len(text); index++ {
		if text[index] != skillReferenceMarker ||
			!isSkillReferenceStart(text, index) {
			continue
		}

		end := skillReferenceNameEnd(text, index+1)
		if end == index+1 || !isSkillReferenceBoundary(text, end) {
			continue
		}

		names = append(names, text[index+1:end])
		index = end - 1
	}

	return uniqueSkillNames(names)
}

// skillReferenceNameEnd returns where the skill name starting at start ends.
// After the core prefix it reads the core skill's own name. A prefix with
// nothing after it ends where the plain name before the colon ends, so the
// boundary check rejects it.
func skillReferenceNameEnd(text string, start int) int {
	end := skillNameEnd(text, start)
	if !strings.HasPrefix(text[start:], CorePrefix) {
		return end
	}

	nameStart := start + len(CorePrefix)

	coreEnd := skillNameEnd(text, nameStart)
	if coreEnd == nameStart {
		return end
	}

	return coreEnd
}

func skillNameEnd(text string, start int) int {
	end := start
	for end < len(text) && isSkillNameByte(text[end]) {
		end++
	}

	return end
}

func isSkillReferenceStart(text string, index int) bool {
	if index == 0 {
		return true
	}

	previous, _ := utf8.DecodeLastRuneInString(text[:index])

	return unicode.IsSpace(previous)
}

func isSkillNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '-'
}

func isSkillReferenceBoundary(text string, index int) bool {
	if index == len(text) {
		return true
	}

	next, _ := utf8.DecodeRuneInString(text[index:])
	if unicode.IsSpace(next) {
		return true
	}

	return strings.ContainsRune(skillReferenceBoundaryRunes, next)
}

func uniqueSkillNames(names []string) []string {
	unique := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))

	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}

		seen[name] = struct{}{}
		unique = append(unique, name)
	}

	return unique
}

// maxListedSkills bounds how many skill names an unknown-skill message lists.
const maxListedSkills = 10

// UnknownSkillError reports a :name that matches no skill. Message is written
// for the person who typed the name: it holds no source location, and it
// travels unchanged across the worker socket and into the WebSocket failure.
type UnknownSkillError struct {
	Message string
}

func (e *UnknownSkillError) Error() string {
	return e.Message
}

// Is makes the error match ErrSkillNotFound.
func (e *UnknownSkillError) Is(target error) bool {
	return target == ErrSkillNotFound
}

// NewUnknownSkillError names the unknown reference and lists the skills that
// exist, cutting a long list to maxListedSkills names and a count.
func NewUnknownSkillError(name string, available []string) *UnknownSkillError {
	head := fmt.Sprintf("unknown skill %q in :%s", name, name)
	if len(available) == 0 {
		return &UnknownSkillError{Message: head + "; no skills are available"}
	}

	listed := available
	if len(listed) > maxListedSkills {
		listed = listed[:maxListedSkills]
	}

	list := strings.Join(listed, ", ")
	if extra := len(available) - len(listed); extra > 0 {
		list = fmt.Sprintf("%s, and %d more", list, extra)
	}

	return &UnknownSkillError{
		Message: fmt.Sprintf("%s; available skills: %s", head, list),
	}
}

// SkillDelivery is one skill reaching a conversation through ExpandSkills.
type SkillDelivery struct {
	Skill ActivatedSkill

	// Referenced is true when another skill's body named this skill, rather
	// than the caller.
	Referenced bool

	// AlreadyDelivered marks a skill the caller named that the conversation
	// already holds. Only a note is due, not a second copy.
	AlreadyDelivered bool
}

// ExpandSkills resolves the skills a caller named and every skill their bodies
// reference, depth first in reference order. Each skill appears once.
//
// delivered holds the resolved names (as ActivatedSkill.Name reports them) the
// conversation already has. It is read, never changed; the caller records what
// it delivers. A named skill in delivered comes back marked AlreadyDelivered
// and its references are not followed. A referenced skill in delivered is left
// out. A skill named twice in one call, including by a plain and a core
// spelling of one name, is delivered once.
//
// An unknown name the caller gave fails the call with ErrSkillNotFound. An
// unknown reference inside a skill body is skipped; the resolver already
// reported it as a warning. Delivered content over the harness file size
// limit fails with ErrSkillContentTooLarge.
func (s Snapshot) ExpandSkills(
	names []string,
	delivered map[string]struct{},
) ([]SkillDelivery, error) {
	named := make([]ActivatedSkill, 0, len(names))

	for _, name := range names {
		skill, err := s.ActivateSkill(name)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "resolve skill :%s", name)
		}

		named = append(named, skill)
	}

	expansion := skillExpansion{
		snapshot: s,
		prior:    delivered,
		seen:     make(map[string]struct{}, len(delivered)+len(named)),
	}
	maps.Copy(expansion.seen, delivered)

	for _, skill := range named {
		expansion.addNamed(skill)
	}

	if err := s.checkDeliveredSize(expansion.deliveries); err != nil {
		return nil, err
	}

	return expansion.deliveries, nil
}

type skillExpansion struct {
	snapshot   Snapshot
	prior      map[string]struct{}
	seen       map[string]struct{}
	deliveries []SkillDelivery
}

func (e *skillExpansion) addNamed(skill ActivatedSkill) {
	if _, held := e.prior[skill.Name]; held {
		e.noteRepeat(skill)

		return
	}

	e.add(skill, false)
}

// noteRepeat adds one note for a held skill, however many spellings of it the
// caller used.
func (e *skillExpansion) noteRepeat(skill ActivatedSkill) {
	for _, delivery := range e.deliveries {
		if delivery.Skill.Name == skill.Name {
			return
		}
	}

	e.deliveries = append(e.deliveries, SkillDelivery{
		Skill:            skill,
		AlreadyDelivered: true,
	})
}

func (e *skillExpansion) add(skill ActivatedSkill, referenced bool) {
	if _, held := e.seen[skill.Name]; held {
		return
	}

	e.seen[skill.Name] = struct{}{}
	e.deliveries = append(e.deliveries, SkillDelivery{
		Skill:      skill,
		Referenced: referenced,
	})

	for _, reference := range SkillReferences(skillBody(skill.Content)) {
		target, err := e.snapshot.ActivateSkill(reference)
		if err != nil {
			continue
		}

		e.add(target, true)
	}
}

// skillBody is a skill document without its frontmatter, so a colon in the
// description never reads as a reference.
func skillBody(content string) string {
	_, body, err := splitFrontMatter(content, ErrInvalidSkill)
	if err != nil {
		return content
	}

	return body
}

func (s Snapshot) checkDeliveredSize(deliveries []SkillDelivery) error {
	budget := s.skillBudget
	if budget <= 0 {
		budget = defaultMaxFileBytes
	}

	var total int64

	for _, delivery := range deliveries {
		if delivery.AlreadyDelivered {
			continue
		}

		total += int64(len(delivery.Skill.Content))
	}

	if total <= budget {
		return nil
	}

	return ctxerrors.Wrapf(
		ErrSkillContentTooLarge,
		"the skills in one message total %d bytes, over the %d byte limit",
		total,
		budget,
	)
}

// unknownSkillReferences lists the references in one skill body that name no
// skill in the snapshot.
func (s Snapshot) unknownSkillReferences(content string) []string {
	unknown := make([]string, 0)

	for _, reference := range SkillReferences(skillBody(content)) {
		if _, err := s.ActivateSkill(reference); err != nil {
			unknown = append(unknown, reference)
		}
	}

	return unknown
}
