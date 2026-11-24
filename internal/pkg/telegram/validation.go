package telegram

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func validToken(token string) bool {
	if len(token) > maxTokenBytes {
		return false
	}

	identity, secret, found := strings.Cut(token, ":")
	if !found || len(secret) == 0 || len(secret) > maxTokenSecretBytes {
		return false
	}

	if !validBotIdentity(identity) {
		return false
	}

	for _, value := range secret {
		if !safeIdentifierCharacter(value) {
			return false
		}
	}

	return true
}

func safeIdentifierCharacter(value rune) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') || value == '_' || value == '-'
}

func validFileID(value string) bool {
	return value != "" && len(value) <= maxFileIDBytes &&
		!strings.ContainsAny(value, "\x00\r\n") && utf8.ValidString(value)
}

func validFilename(value string) bool {
	return value != "" && len(value) <= maxFilenameBytes &&
		!strings.ContainsAny(value, "/\\\x00\r\n") && utf8.ValidString(value)
}

func validFilePath(value string) bool {
	if value == "" || len(value) > maxFilePathBytes {
		return false
	}

	for part := range strings.SplitSeq(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}

		for _, character := range part {
			if character != '.' && !safeIdentifierCharacter(character) {
				return false
			}
		}
	}

	return true
}

func validBotIdentity(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}

	id, err := strconv.ParseUint(value, 10, 64)

	return err == nil && id != 0
}
