package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// skillMessageTemplateText is the user-role message a skill reaches the
	// model as. The content is the whole SKILL.md, frontmatter included.
	skillMessageTemplateText = `<skill>
<name>{{.Name}}</name>
<description>{{.Description}}</description>
<directory>{{.Directory}}</directory>

{{.Content}}
</skill>`

	skillMessageTemplateName = "skill-message"

	// skillAlreadyLoadedFormat is the one-line message sent instead of a
	// second copy when the user names a skill the conversation already has.
	skillAlreadyLoadedFormat = "<note>Skill %s is already loaded earlier " +
		"in this conversation.</note>"

	skillMessagePrefix = "<skill>\n"
	skillNameOpenTag   = "<name>"
	skillNameCloseTag  = "</name>"
)

// skillMessageData is what skillMessageTemplateText renders.
type skillMessageData struct {
	Name        string
	Description string
	Directory   string
	Content     string
}

// skillMessage is one user-role message that follows the user message that
// named a skill: the skill itself, or the note that it is already loaded.
type skillMessage struct {
	name   string
	text   string
	isNote bool
}

// skillInjectedPayload is the data of one skill.injected event.
type skillInjectedPayload struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// skillLedger records which skills a conversation already holds, so each is
// delivered at most once. It is safe for concurrent use: a queued message and
// a use_skill call can race inside one turn.
type skillLedger struct {
	mutex     sync.Mutex
	delivered map[string]struct{}
}

func newSkillLedger(names ...string) *skillLedger {
	ledger := &skillLedger{delivered: make(map[string]struct{}, len(names))}
	ledger.record(names...)

	return ledger
}

// held returns a copy of the delivered names. A nil ledger holds nothing.
func (l *skillLedger) held() map[string]struct{} {
	if l == nil {
		return map[string]struct{}{}
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	return maps.Clone(l.delivered)
}

func (l *skillLedger) record(names ...string) {
	if l == nil {
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	for _, name := range names {
		l.delivered[name] = struct{}{}
	}
}

// recordMessages records every skill, not note, in messages.
func (l *skillLedger) recordMessages(messages []skillMessage) {
	for _, message := range messages {
		if !message.isNote {
			l.record(message.name)
		}
	}
}

// deliveredSkillNames lists the skills the stored history already holds. Only
// messages after the latest compaction are in the history, which is right: a
// compacted skill message no longer reaches the model.
func deliveredSkillNames(history []*models.Message) []string {
	names := make([]string, 0)

	for _, message := range history {
		if message.InjectedKind != models.InjectedKindSkill {
			continue
		}

		if name, found := skillMessageName(message.Content); found {
			names = append(names, name)
		}
	}

	return names
}

// skillMessageName reads the skill name out of a rendered skill message. A
// note has no <skill> prefix and names nothing.
func skillMessageName(content string) (string, bool) {
	rest, found := strings.CutPrefix(content, skillMessagePrefix)
	if !found {
		return "", false
	}

	rest, found = strings.CutPrefix(rest, skillNameOpenTag)
	if !found {
		return "", false
	}

	name, _, found := strings.Cut(rest, skillNameCloseTag)

	return name, found
}

// buildSkillMessages resolves the :name references in text, and in the skills
// they name, into the messages that follow text. A skill the ledger holds is
// replaced by a note when text names it, and skipped when only another skill
// does. It does not change the ledger: the caller records the messages once
// they are accepted.
//
// An unknown name, or more skill content than the harness limit allows,
// fails with a validation error and nothing is built.
func buildSkillMessages(
	ctx context.Context,
	snapshot harness.Snapshot,
	ledger *skillLedger,
	text string,
) ([]skillMessage, error) {
	names := harness.SkillReferences(text)
	if len(names) == 0 {
		return nil, nil
	}

	if err := requireKnownSkillReferences(snapshot, text); err != nil {
		return nil, err
	}

	deliveries, err := snapshot.ExpandSkills(names, ledger.held())
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, err),
			"expand skill references",
		)
	}

	messages := make([]skillMessage, 0, len(deliveries))

	for _, delivery := range deliveries {
		message, err := newSkillMessage(delivery)
		if err != nil {
			return nil, err
		}

		messages = append(messages, message)

		ctxscope.GetLogger(ctx).Info(
			"skill delivered as a message",
			"skill_name", delivery.Skill.Name,
			"skill_hash", delivery.Skill.Hash,
			"skill_source", delivery.Skill.Source,
			"referenced", delivery.Referenced,
			"already_loaded", delivery.AlreadyDelivered,
		)
	}

	return messages, nil
}

func newSkillMessage(delivery harness.SkillDelivery) (skillMessage, error) {
	skill := delivery.Skill
	if delivery.AlreadyDelivered {
		return skillMessage{
			name:   skill.Name,
			text:   fmt.Sprintf(skillAlreadyLoadedFormat, skill.Name),
			isNote: true,
		}, nil
	}

	parsed, err := template.New(skillMessageTemplateName).
		Parse(skillMessageTemplateText)
	if err != nil {
		return skillMessage{}, ctxerrors.Wrap(err, "parse skill template")
	}

	var rendered strings.Builder

	if err := parsed.Execute(&rendered, skillMessageData{
		Name:        skill.Name,
		Description: skill.Description,
		Directory:   skill.Directory,
		Content:     skill.Content,
	}); err != nil {
		return skillMessage{}, ctxerrors.Wrap(err, "render skill message")
	}

	return skillMessage{name: skill.Name, text: rendered.String()}, nil
}

// storedInput is the transcript row for a skill message: injected, and
// labelled so a client can show it as a skill instead of a user bubble.
func (m skillMessage) storedInput(workspace string) session.MessageInput {
	return session.MessageInput{
		Role:         models.MessageRoleUser,
		Content:      m.text,
		Workspace:    workspace,
		Injected:     true,
		InjectedKind: models.InjectedKindSkill,
	}
}

// injectedPayload is the skill.injected event data for this message.
func (m skillMessage) injectedPayload() skillInjectedPayload {
	return skillInjectedPayload{Name: m.name, Content: m.text}
}
