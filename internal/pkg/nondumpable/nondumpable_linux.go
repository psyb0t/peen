//go:build linux

package nondumpable

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"golang.org/x/sys/unix"
)

// dumpableOff is the PR_SET_DUMPABLE argument that clears the flag.
const dumpableOff = 0

// Enable clears the process's dumpable flag with prctl(PR_SET_DUMPABLE, 0).
//
// The kernel then makes /proc/<pid>/environ, mem, maps, and the other private
// /proc files owned by root and refuses ptrace from processes with the same
// user ID. A process with CAP_SYS_PTRACE, root included, can still read them.
// The flag resets on execve, so programs this process starts are unaffected.
func Enable(ctx context.Context) error {
	if err := unix.Prctl(
		unix.PR_SET_DUMPABLE,
		dumpableOff,
		0,
		0,
		0,
	); err != nil {
		return ctxerrors.Wrap(err, "mark the process non-dumpable")
	}

	ctxscope.GetLogger(ctx).Info("process is non-dumpable")

	return nil
}
