// Package greet reads names out of greetings.
package greet

import (
	"errors"
	"strings"
)

// ErrEmpty is returned for an empty greeting.
var ErrEmpty = errors.New("greet: recieved an empty greeting")

// Parse returns the name in a greeting of the form "hello, NAME".
func Parse(greeting string) (string, error) {
	if strings.TrimSpace(greeting) == "" {
		return "", ErrEmpty
	}

	name, found := strings.CutPrefix(strings.ToLower(greeting), "hello, ")
	if !found {
		return "", errors.New("greet: not a greeting")
	}

	return name, nil
}
