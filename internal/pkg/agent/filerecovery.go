package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

type fileReadRecord struct {
	Path   string `json:"path"`
	First  int    `json:"firstLine"`
	Last   int    `json:"lastLine"`
	SHA256 string `json:"sha256"`
}

type fileReadPresence struct {
	fileReadRecord
	Full        bool `json:"full"`
	Changed     bool `json:"changed"`
	Unavailable bool `json:"unavailable"`
}

type fileRangeReader func(context.Context, tools.ReadFileInput) (
	tools.ReadFileOutput, error,
)

type fileRecovery struct {
	reads     []fileReadRecord
	readCalls map[string]bool
	read      fileRangeReader
}

type fileInventory struct {
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256"`
	Full        [][2]int `json:"full"`
	Missing     [][2]int `json:"missing"`
	Changed     bool     `json:"changed"`
	Unavailable bool     `json:"unavailable"`
}

type fileRecoveryFit struct {
	messages           []elelem.Message
	entries            []fileReadPresence
	count              func([]elelem.Message) (int, error)
	budget, base, room int
	used               map[string]int
	dropped            int
	noteLimit          int
}

func readFileRecord(text string) (fileReadRecord, bool) {
	text = strings.TrimPrefix(text, fileRecoveryAttachmentLead)

	_, inner, _, wrapped := splitToolOutput(text)
	if wrapped {
		text = inner
	}

	var output tools.ReadFileOutput
	if json.Unmarshal([]byte(text), &output) != nil || output.Path == "" ||
		len(output.SHA256) != 64 || output.FirstLine < 1 ||
		output.LastLine < output.FirstLine {
		return fileReadRecord{}, false
	}

	return fileReadRecord{
		Path: output.Path, First: output.FirstLine,
		Last: output.LastLine, SHA256: output.SHA256,
	}, true
}

func (r *fileRecovery) observe(text string) {
	entry, ok := readFileRecord(text)
	if !ok {
		return
	}

	r.reads = slices.DeleteFunc(r.reads, func(old fileReadRecord) bool {
		return old.Path == entry.Path && old.First == entry.First &&
			old.Last == entry.Last && old.SHA256 == entry.SHA256
	})
	r.reads = append(r.reads, entry)
}

func (r *fileRecovery) presence(messages []elelem.Message) []fileReadPresence {
	present := make(map[[2]string]map[int]bool)

	for _, message := range messages {
		if !r.isReadResult(message) {
			continue
		}

		text := strings.TrimPrefix(message.Text(), fileRecoveryAttachmentLead)

		_, inner, _, wrapped := splitToolOutput(text)
		if wrapped {
			text = inner
		}

		var output tools.ReadFileOutput
		if json.Unmarshal([]byte(text), &output) != nil {
			continue
		}

		key := [2]string{output.Path, output.SHA256}
		if present[key] == nil {
			present[key] = make(map[int]bool)
		}

		collectFileLines(output, present[key])
	}

	entries := make([]fileReadPresence, 0, len(r.reads))
	for _, entry := range r.reads {
		lines := 0

		for line := range present[[2]string{entry.Path, entry.SHA256}] {
			if line >= entry.First && line <= entry.Last {
				lines++
			}
		}

		entries = append(entries, fileReadPresence{
			fileReadRecord: entry, Full: lines == entry.Last-entry.First+1,
		})
	}

	return entries
}

func (r *fileRecovery) isReadResult(message elelem.Message) bool {
	if message.ToolResultIsError {
		return false
	}

	if message.Role == elelem.RoleTool {
		return r.readCalls == nil || r.readCalls[message.ToolCallID]
	}

	return message.Role == elelem.RoleUser && isFileRecoveryMessage(message)
}

func collectFileLines(output tools.ReadFileOutput, lines map[int]bool) {
	for line := range strings.SplitSeq(output.Content, "\n") {
		prefix, _, numbered := strings.Cut(line, "\t")
		if !numbered {
			continue
		}

		number, err := strconv.Atoi(strings.TrimSpace(prefix))
		if err != nil || number < output.FirstLine || number > output.LastLine {
			continue
		}

		lines[number] = true
	}
}

func fileRecoveryLost(entries []fileReadPresence) bool {
	return slices.ContainsFunc(entries, func(entry fileReadPresence) bool {
		return !entry.Full
	})
}

func isFileRecoveryMessage(message elelem.Message) bool {
	if message.Injection == nil ||
		message.Injection.Tool != fileRecoveryInjectionTool {
		return false
	}

	return (message.Role == elelem.RoleSystem &&
		strings.HasPrefix(message.Text(), fileRecoveryNoteLead)) ||
		(message.Role == elelem.RoleUser &&
			strings.HasPrefix(message.Text(), fileRecoveryAttachmentLead))
}

func fileRecoveryNote(
	entries []fileReadPresence,
	omitted ...int,
) (elelem.Message, error) {
	var files []fileInventory

	positions := make(map[[2]string]int)

	for _, entry := range entries {
		key := [2]string{entry.Path, entry.SHA256}

		position, exists := positions[key]
		if !exists {
			position = len(files)
			positions[key] = position

			files = append(files, fileInventory{
				Path:   entry.Path,
				SHA256: entry.SHA256, Full: [][2]int{}, Missing: [][2]int{},
			})
		}

		file := &files[position]

		window := [2]int{entry.First, entry.Last}
		if entry.Full {
			file.Full = append(file.Full, window)
		} else {
			file.Missing = append(file.Missing, window)
		}

		file.Changed = file.Changed || entry.Changed
		file.Unavailable = file.Unavailable || entry.Unavailable
	}

	encoded, err := json.Marshal(files)
	if err != nil {
		return elelem.Message{}, ctxerrors.Wrap(err, "encode file inventory")
	}

	suffix := ""
	if len(omitted) > 0 && omitted[0] > 0 {
		suffix = fileRecoveryOmittedLead + strconv.Itoa(omitted[0]) +
			" older files. Reread their exact text before use."
	}

	return elelem.Message{
		Role:      elelem.RoleSystem,
		Injection: &elelem.MessageInjection{Tool: fileRecoveryInjectionTool},
		Content: elelem.Text(fileRecoveryNoteLead +
			fileRecoveryInstruction + string(encoded) + suffix),
	}, nil
}

// recover shapes only request copies. count includes the outgoing tools and
// counter. A later fit pass discards re-attachments before any other loss.
func (r *fileRecovery) recover(
	ctx context.Context,
	messages []elelem.Message,
	budget int,
	count func([]elelem.Message) (int, error),
) ([]elelem.Message, error) {
	messages = slices.DeleteFunc(slices.Clone(messages), isFileRecoveryMessage)

	entries := r.presence(messages)
	if !fileRecoveryLost(entries) {
		return messages, nil
	}

	baseTokens, err := count(messages)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count recovery base")
	}

	entries, note, dropped, err := boundedFileRecoveryNote(entries,
		min(budget/fileRecoveryNoteDivisor, budget-baseTokens), count)
	if err != nil {
		return nil, err
	}

	if note.Content == nil {
		return messages, nil
	}

	messages = append(messages, note)

	base, err := count(messages)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count file inventory")
	}

	fit := &fileRecoveryFit{
		messages: messages, entries: entries,
		count: count, budget: budget, base: base,
		room:    max(0, budget-base) / fileRecoveryRoomDivisor,
		used:    make(map[string]int),
		dropped: dropped, noteLimit: budget / fileRecoveryNoteDivisor,
	}
	if err := fit.restore(ctx, r); err != nil {
		return nil, err
	}

	return fit.finish(messages[:len(messages)-1])
}

func (f *fileRecoveryFit) finish(
	original []elelem.Message,
) ([]elelem.Message, error) {
	note, err := fileRecoveryNote(f.entries, f.dropped)
	if err != nil {
		return nil, err
	}

	f.messages[len(f.messages)-1] = note

	tokens, err := f.count(f.messages)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count final inventory")
	}

	noteCost, err := fileRecoveryNoteCost(note, f.count)
	if err != nil {
		return nil, err
	}

	if tokens > f.budget || noteCost > f.noteLimit {
		return original, nil
	}

	return f.messages, nil
}

func fileRecoveryNoteCost(
	note elelem.Message,
	count func([]elelem.Message) (int, error),
) (int, error) {
	base, err := count(nil)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count inventory baseline")
	}

	tokens, err := count([]elelem.Message{note})
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count inventory share")
	}

	return max(0, tokens-base), nil
}

func boundedFileRecoveryNote(
	entries []fileReadPresence,
	limit int,
	count func([]elelem.Message) (int, error),
) ([]fileReadPresence, elelem.Message, int, error) {
	var groups [][2]string

	seen := make(map[[2]string]bool)

	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]

		key := [2]string{entry.Path, entry.SHA256}
		if seen[key] {
			continue
		}

		seen[key] = true
		groups = append(groups, key)
	}

	slices.Reverse(groups)

	total := len(groups)
	groups = groups[max(0, total-fileRecoveryMaxFiles):]

	for {
		kept := slices.DeleteFunc(slices.Clone(entries),
			func(entry fileReadPresence) bool {
				key := [2]string{entry.Path, entry.SHA256}

				return !slices.Contains(groups, key)
			})
		dropped := total - len(groups)

		note, err := fileRecoveryNote(kept, dropped)
		if err != nil {
			return nil, elelem.Message{}, 0, err
		}

		tokens, err := fileRecoveryNoteCost(note, count)
		if err != nil {
			return nil, elelem.Message{}, 0, err
		}

		if tokens <= limit {
			return kept, note, dropped, nil
		}

		if len(groups) == 0 {
			return nil, elelem.Message{}, total, nil
		}

		groups = groups[1:]
	}
}

func (f *fileRecoveryFit) restore(ctx context.Context, r *fileRecovery) error {
	for index := len(f.entries) - 1; index >= 0 && f.room > 0; index-- {
		entry := f.entries[index]
		if entry.Full || r.read == nil ||
			f.used[entry.Path] >= fileRecoveryPerFileTokens {
			continue
		}

		if err := f.restoreRange(ctx, r, index); err != nil {
			return err
		}
	}

	return nil
}

func (f *fileRecoveryFit) restoreRange(
	ctx context.Context,
	r *fileRecovery,
	index int,
) error {
	entry := &f.entries[index]

	attachment, changed, unavailable, err := r.attachment(ctx, *entry)
	if err != nil {
		return err
	}

	entry.Changed, entry.Unavailable = changed, unavailable
	if changed || unavailable {
		return nil
	}

	candidate, next, err := f.candidate(r, attachment)
	if err != nil {
		return err
	}

	return f.acceptCandidate(candidate, next, entry.Path)
}

func (f *fileRecoveryFit) acceptCandidate(
	candidate []elelem.Message,
	next []fileReadPresence,
	path string,
) error {
	tokens, err := f.count(candidate)
	if err != nil {
		return ctxerrors.Wrap(err, "count recovered file range")
	}

	cost := max(0, tokens-f.base)

	noteCost, err := fileRecoveryNoteCost(candidate[len(candidate)-1], f.count)
	if err != nil {
		return err
	}

	if noteCost > f.noteLimit || tokens > f.budget || cost > f.room ||
		cost+f.used[path] > fileRecoveryPerFileTokens {
		return nil
	}

	f.messages, f.entries, f.base = candidate, next, tokens
	f.room -= cost
	f.used[path] += cost

	return nil
}

func (f *fileRecoveryFit) candidate(
	r *fileRecovery,
	attachment elelem.Message,
) ([]elelem.Message, []fileReadPresence, error) {
	candidate := slices.Insert(slices.Clone(f.messages), len(f.messages)-1,
		attachment)

	selected := *r
	selected.reads = nil

	for _, entry := range f.entries {
		selected.reads = append(selected.reads, entry.fileReadRecord)
	}

	next := selected.presence(candidate)
	for position := range next {
		next[position].Changed = f.entries[position].Changed
		next[position].Unavailable = f.entries[position].Unavailable
	}

	note, err := fileRecoveryNote(next, f.dropped)
	if err != nil {
		return nil, nil, err
	}

	candidate[len(candidate)-1] = note

	return candidate, next, nil
}

func (r *fileRecovery) attachment(
	ctx context.Context,
	entry fileReadPresence,
) (elelem.Message, bool, bool, error) {
	output, err := r.read(ctx, tools.ReadFileInput{
		Path:   entry.Path,
		Offset: entry.First, Limit: entry.Last - entry.First + 1,
	})
	if err != nil {
		if ctx.Err() != nil {
			return elelem.Message{}, false, false,
				ctxerrors.Wrap(ctx.Err(), "recover file context")
		}

		return elelem.Message{}, false, true, nil
	}

	if output.SHA256 != entry.SHA256 {
		return elelem.Message{}, true, false, nil
	}

	if output.FirstLine != entry.First || output.LastLine != entry.Last {
		return elelem.Message{}, false, true, nil
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		return elelem.Message{}, false, false,
			ctxerrors.Wrap(err, "encode recovered range")
	}

	wrapped, err := markToolOutput(ctx, toolNameReadFile, string(encoded), 0)
	if err != nil {
		return elelem.Message{}, false, false,
			ctxerrors.Wrap(err, "label recovered range")
	}

	injection := &elelem.MessageInjection{Tool: fileRecoveryInjectionTool}

	return elelem.Message{
			Role:      elelem.RoleUser,
			Injection: injection,
			Content:   elelem.Text(fileRecoveryAttachmentLead + wrapped),
		},
		false, false, nil
}
