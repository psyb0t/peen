package codegen

import (
	"go/token"
	"strconv"
	"strings"
	"unicode"
)

// isCommonInitialism reports whether an upper-case word is written in full
// caps inside Go identifiers, following the golint list.
func isCommonInitialism(word string) bool {
	switch word {
	case "ACL", "API", "ASCII", "CPU", "CSS", "DNS",
		"EOF", "GUID", "HTML", "HTTP", "HTTPS", "ID",
		"IP", "JSON", "LHS", "QPS", "RAM", "RHS",
		"RPC", "SLA", "SMTP", "SQL", "SSH", "TCP",
		"TLS", "TTL", "UDP", "UI", "UID", "UUID",
		"URI", "URL", "UTF8", "VM", "XML", "XMPP",
		"XSRF", "XSS", "WS", "WSS":
		return true
	default:
		return false
	}
}

const fallbackIdentifier = "Value"

// GoName turns an arbitrary spec name into an exported Go identifier.
// "instrument_rates", "instrumentRates" and "instrument-rates" all become
// "InstrumentRates"; "userId" becomes "UserID".
func GoName(name string) string {
	words := splitWords(name)
	if len(words) == 0 {
		return fallbackIdentifier
	}

	var builder strings.Builder

	for _, word := range words {
		upper := strings.ToUpper(word)
		if isCommonInitialism(upper) {
			builder.WriteString(upper)

			continue
		}

		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToUpper(runes[0])
		builder.WriteString(string(runes))
	}

	identifier := builder.String()
	if unicode.IsDigit([]rune(identifier)[0]) {
		identifier = "N" + identifier
	}

	if token.IsKeyword(identifier) {
		identifier += "_"
	}

	return identifier
}

// splitWords breaks name on non-alphanumerics and on lower-to-upper case
// changes, keeping runs of capitals ("HTTPServer") as one word followed by the
// next ("Server").
func splitWords(name string) []string {
	var (
		words   []string
		current []rune
	)

	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = nil
		}
	}

	runes := []rune(name)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()

			continue
		}

		if len(current) > 0 && startsNewWord(runes, i) {
			flush()
		}

		current = append(current, r)
	}

	flush()

	return words
}

func startsNewWord(runes []rune, i int) bool {
	previous := runes[i-1]
	r := runes[i]

	startsAfterLowerOrDigit := unicode.IsLower(previous) ||
		unicode.IsDigit(previous)
	if unicode.IsUpper(r) && startsAfterLowerOrDigit {
		return true
	}

	hasNext := i+1 < len(runes)

	return unicode.IsUpper(r) &&
		unicode.IsUpper(previous) &&
		hasNext &&
		unicode.IsLower(runes[i+1])
}

// nameSet hands out unique Go identifiers, appending a counter on collision.
type nameSet struct {
	used map[string]bool
}

func newNameSet(reserved ...string) *nameSet {
	set := &nameSet{used: map[string]bool{}}
	for _, name := range reserved {
		set.used[name] = true
	}

	return set
}

func (s *nameSet) claim(name string) string {
	if !s.used[name] {
		s.used[name] = true

		return name
	}

	for i := 2; ; i++ {
		candidate := name + strconv.Itoa(i)
		if !s.used[candidate] {
			s.used[candidate] = true

			return candidate
		}
	}
}
