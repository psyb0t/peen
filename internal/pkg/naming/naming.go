// Package naming holds the shape rule for names Peen invents and owns: tool
// names, hook event names, and session event types.
package naming

import "strings"

const partSeparator = "."

// IsValid reports whether name follows Peen's naming rule: one or more
// dot-separated parts, each starting with a lowercase ASCII letter and holding
// only ASCII letters and digits. A part is camelCase, so it never holds an
// underscore or a hyphen.
func IsValid(name string) bool {
	if name == "" {
		return false
	}

	for part := range strings.SplitSeq(name, partSeparator) {
		if !isValidPart(part) {
			return false
		}
	}

	return true
}

func isValidPart(part string) bool {
	if part == "" || !isLowerLetter(part[0]) {
		return false
	}

	for index := 1; index < len(part); index++ {
		character := part[index]
		if !isLowerLetter(character) &&
			!isUpperLetter(character) &&
			!isDigit(character) {
			return false
		}
	}

	return true
}

func isLowerLetter(character byte) bool {
	return character >= 'a' && character <= 'z'
}

func isUpperLetter(character byte) bool {
	return character >= 'A' && character <= 'Z'
}

func isDigit(character byte) bool {
	return character >= '0' && character <= '9'
}
