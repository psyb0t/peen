package agent

import (
	"strings"
)

const (
	// summaryCloseTag ends a summary block opened by compactionSummaryLead
	// or compactionTurnSummaryLead.
	summaryCloseTag = "\n</summary>"

	summaryHeadingMarker = "#"
	summaryHeadingWord   = "summary"
	summaryHeadingEnd    = ":"
)

// summaryCloseEscaper keeps a summary, which quotes the conversation it
// replaces, from closing its block early.
//
//nolint:gochecknoglobals // Stateless replacer.
var summaryCloseEscaper = strings.NewReplacer("</summary", "&lt;/summary")

// withoutToolOutputNotice keeps a wrapped tool result's label and text for
// the summarizer and drops the closing notice, which repeats the same
// sentence after every outside result and tells the summarizer nothing.
func withoutToolOutputNotice(text string) string {
	opening, inner, _, wrapped := splitToolOutput(text)
	if !wrapped {
		return text
	}

	return opening + inner + toolOutputCloseTag
}

// summaryMessageText renders a stored summary as the block that replaces
// the history it covers: the lead, the summary without a title line of its
// own, and the closing tag.
func summaryMessageText(lead, summary string) string {
	return lead + summaryCloseEscaper.Replace(stripSummaryHeading(summary)) +
		summaryCloseTag
}

// stripSummaryHeading drops a first line that only titles the summary, such
// as "Summary of this session:" or "# Summary". The block's tag already says
// what it is, and a second title reads like part of the conversation. Any
// other first line, a list heading included, is content and stays.
func stripSummaryHeading(summary string) string {
	summary = strings.TrimSpace(summary)

	first, rest, found := strings.Cut(summary, "\n")
	if !found || !isSummaryHeading(first) {
		return summary
	}

	return strings.TrimSpace(rest)
}

func isSummaryHeading(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, summaryHeadingMarker) {
		return true
	}

	lower := strings.ToLower(line)

	return strings.HasPrefix(lower, summaryHeadingWord) &&
		strings.HasSuffix(lower, summaryHeadingEnd)
}
