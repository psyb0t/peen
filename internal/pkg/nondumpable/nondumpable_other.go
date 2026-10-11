//go:build !linux

package nondumpable

import (
	"context"
)

// Enable does nothing outside Linux, where Peen has no /proc environment for
// a same-user process to read.
func Enable(_ context.Context) error {
	return nil
}
