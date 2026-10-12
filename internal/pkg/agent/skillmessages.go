package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"text/template"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
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
// a useSkill call can race inside one turn.
//
// The skills come from two places. The stored history holds the ones delivered
// in earlier turns, and a compaction that summarizes part of that history
// takes those skills away from the model, so the ledger forgets them with it.
// The turn's own deliveries are never compacted and stay for the whole turn.
type skillLedger struct {
	mutex        sync.Mutex
	history      []*models.Message
	historyNames map[string]struct{}
	delivered    map[string]struct{}
}

func newSkillLedger(names ...string) *skillLedger {
	ledger := &skillLedger{
		historyNames: map[string]struct{}{},
		delivered:    make(map[string]struct{}, len(names)),
	}
	ledger.record(names...)

	return ledger
}

// newSkillLedgerFromHistory builds the ledger of a turn that starts on top of
// the stored history, which is what the model sees of earlier turns.
func newSkillLedgerFromHistory(history []*models.Message) *skillLedger {
	ledger := newSkillLedger()
	ledger.history = history
	ledger.historyNames = skillNameSet(history)

	return ledger
}

func skillNameSet(history []*models.Message) map[string]struct{} {
	names := deliveredSkillNames(history)
	set := make(map[string]struct{}, len(names))

	for _, name := range names {
		set[name] = struct{}{}
	}

	return set
}

// forgetThrough drops the skills held only by stored messages up to and
// including sequence, which a compaction has replaced with a summary.
func (l *skillLedger) forgetThrough(sequence int64) {
	if l == nil {
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	remaining := make([]*models.Message, 0, len(l.history))

	for _, message := range l.history {
		if message.Sequence > sequence {
			remaining = append(remaining, message)
		}
	}

	l.history = remaining
	l.historyNames = skillNameSet(remaining)
}

// held returns a copy of the delivered names. A nil ledger holds nothing.
func (l *skillLedger) held() map[string]struct{} {
	if l == nil {
		return map[string]struct{}{}
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	names := maps.Clone(l.historyNames)
	maps.Copy(names, l.delivered)

	return names
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

// deliveredSkillNames lists the skills the stored history already holds: the
// <skill> messages, and the skills the model loaded with useSkill, which
// include the skills those named. Only messages after the latest compaction
// are in the history, which is right: a compacted skill no longer reaches
// the model.
func deliveredSkillNames(history []*models.Message) []string {
	names := make([]string, 0)
	useSkillCalls := useSkillCallIDs(history)

	for _, message := range history {
		switch {
		case message.InjectedKind == models.InjectedKindSkill:
			if name, found := skillMessageName(message.Content); found {
				names = append(names, name)
			}
		case message.Role == models.MessageRoleTool:
			names = append(
				names,
				useSkillResultNames(message, useSkillCalls)...,
			)
		}
	}

	return names
}

// useSkillCallIDs collects the IDs of the useSkill calls the history's
// assistant messages made. A stored tool result carries only its call ID, so
// the call is where the tool's name is known.
func useSkillCallIDs(history []*models.Message) map[string]struct{} {
	ids := make(map[string]struct{})

	for _, message := range history {
		if message.Role != models.MessageRoleAssistant {
			continue
		}

		var calls []elelem.ToolCall
		if err := json.Unmarshal(
			[]byte(message.ToolCallsJSON),
			&calls,
		); err != nil {
			continue
		}

		for _, call := range calls {
			if call.Name == toolNameUseSkill {
				ids[call.ID] = struct{}{}
			}
		}
	}

	return ids
}

// useSkillResultNames reads the skills a stored useSkill result delivered.
// A result that is an error, belongs to another tool, or is not a useSkill
// output delivered nothing.
func useSkillResultNames(
	message *models.Message,
	useSkillCalls map[string]struct{},
) []string {
	if _, found := useSkillCalls[message.ToolCallID]; !found {
		return nil
	}

	if message.IsError {
		return nil
	}

	var output useSkillOutput
	if err := json.Unmarshal([]byte(message.Content), &output); err != nil {
		return nil
	}

	names := make([]string, 0, len(output.ReferencedSkills)+1)
	if output.Name != "" {
		names = append(names, output.Name)
	}

	for _, referenced := range output.ReferencedSkills {
		names = append(names, referenced.Name)
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

		runtimecontext.Logger(ctx).Info(
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
