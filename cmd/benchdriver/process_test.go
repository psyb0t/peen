package main

import (
	"os"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseParentPIDReadsPastTheCommandName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		stat   string
		parent int
	}{
		{name: "plain", stat: "42 (peen) S 7 42 42 0", parent: 7},
		{name: "spaces", stat: "42 (my peen) S 9 42 42 0", parent: 9},
		{name: "parentheses", stat: "42 (a) (b)) R 11 42 42 0", parent: 11},
		{name: "newline", stat: "5 (sh) S 1 5 5 0\n", parent: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent, err := parseParentPID([]byte(tc.stat))
			require.NoError(t, err)
			assert.Equal(t, tc.parent, parent)
		})
	}
}

func TestParseParentPIDRefusesGarbage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		stat    string
		wantErr error
	}{
		{name: "empty", stat: "", wantErr: commerr.ErrParseFailed},
		{name: "no parentheses", stat: "42 peen S 7"},
		{name: "no fields", stat: "42 (peen)", wantErr: commerr.ErrParseFailed},
		{name: "not a number", stat: "1 (x) S y"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseParentPID([]byte(tc.stat))
			require.Error(t, err)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestWorkersFindsOnlyPeenWorkersUnderTheController(t *testing.T) {
	t.Parallel()

	processes := []processInfo{
		{PID: 10, ParentPID: 1, Args: []string{"/usr/local/bin/peen", "run"}},
		{PID: 11, ParentPID: 10, Args: []string{"peen", "worker"}},
		{PID: 12, ParentPID: 11, Args: []string{"sh", "-c", "go test"}},
		{PID: 13, ParentPID: 12, Args: []string{"peen", "worker"}},
		{PID: 20, ParentPID: 1, Args: []string{"peen", "worker"}},
		{PID: 21, ParentPID: 10, Args: []string{"/bin/peen", "exec"}},
	}

	found := workers(processes, 10)

	pids := make([]int, 0, len(found))
	for _, process := range found {
		pids = append(pids, process.PID)
	}

	assert.ElementsMatch(t, []int{11, 13}, pids)
	assert.Len(t, descendants(processes, 10), 4)
	assert.Empty(t, descendants(processes, 99))
}

func TestReadProcessSeesTheTestProcess(t *testing.T) {
	t.Parallel()

	info, err := readProcess(os.Getpid())
	require.NoError(t, err)

	assert.Equal(t, os.Getpid(), info.PID)
	assert.Equal(t, os.Getppid(), info.ParentPID)
	assert.NotEmpty(t, info.Args)

	resident, err := residentBytes(os.Getpid())
	require.NoError(t, err)
	assert.Positive(t, resident)
}
