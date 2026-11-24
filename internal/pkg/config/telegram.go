package config

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	environmentTelegramToken  = "PEEN_TELEGRAM_TOKEN"
	maximumTelegramIDs        = 1024
	maximumTelegramTokenBytes = 256
)

// TelegramPolicy contains the operator's explicit chat and user grants.
type TelegramPolicy struct {
	Chats map[int64]struct{}
	Users map[int64]struct{}
}

func (c Config) TelegramPolicy() (TelegramPolicy, error) {
	policy := TelegramPolicy{}
	if !c.Telegram {
		return policy, nil
	}

	if !validTelegramToken(c.TelegramToken) {
		return policy, ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_TELEGRAM_TOKEN is missing or invalid")
	}

	if !filepath.IsAbs(c.TelegramWorkspace) ||
		strings.ContainsAny(c.TelegramWorkspace, "\x00\r\n") {
		return policy, ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_TELEGRAM_WORKSPACE must be absolute")
	}

	chats, err := parseTelegramIDs(c.TelegramAllowedChats, false)
	if err != nil || len(chats) == 0 {
		return policy, ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_TELEGRAM_ALLOWED_CHATS requires nonzero chat IDs")
	}

	users, err := parseTelegramIDs(c.TelegramAllowedUsers, true)
	if err != nil {
		return policy, ctxerrors.Wrap(ErrInvalidConfig,
			"PEEN_TELEGRAM_ALLOWED_USERS requires positive user IDs")
	}

	policy.Chats, policy.Users = chats, users

	return policy, nil
}

func parseTelegramIDs(value string, users bool) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	if value == "" {
		return result, nil
	}

	entries := strings.Split(value, ",")
	if len(entries) > maximumTelegramIDs {
		return nil, ctxerrors.Wrap(ErrInvalidConfig, "too many Telegram IDs")
	}

	for _, entry := range entries {
		id, err := strconv.ParseInt(strings.TrimSpace(entry), 10, 64)
		if err != nil || id == 0 || (users && id < 0) {
			return nil, ctxerrors.Wrap(ErrInvalidConfig, "invalid Telegram ID")
		}

		result[id] = struct{}{}
	}

	return result, nil
}

func validTelegramToken(value string) bool {
	if len(value) == 0 || len(value) > maximumTelegramTokenBytes {
		return false
	}

	prefix, suffix, found := strings.Cut(value, ":")
	if !found || suffix == "" {
		return false
	}

	id, err := strconv.ParseUint(prefix, 10, 64)
	if err != nil || id == 0 {
		return false
	}

	for _, character := range suffix {
		if !telegramTokenCharacter(character) {
			return false
		}
	}

	return true
}

func telegramTokenCharacter(character rune) bool {
	return character == '_' || character == '-' ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}
