package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// processInfo is what the driver reads about one process from /proc.
type processInfo struct {
	PID       int      `json:"pid"`
	ParentPID int      `json:"parentPid"`
	Args      []string `json:"args"`
}

// readProcessTable reads every process this user can see under /proc. A
// process that exits while it is read is skipped.
func readProcessTable() ([]processInfo, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list processes")
	}

	processes := make([]processInfo, 0, len(entries))

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		info, err := readProcess(pid)
		if err != nil {
			continue
		}

		processes = append(processes, info)
	}

	return processes, nil
}

func readProcess(pid int) (processInfo, error) {
	directory := filepath.Join(procRoot, strconv.Itoa(pid))

	stat, err := os.ReadFile(filepath.Join(directory, procStatName))
	if err != nil {
		return processInfo{}, ctxerrors.Wrap(err, "read process stat")
	}

	parent, err := parseParentPID(stat)
	if err != nil {
		return processInfo{}, err
	}

	cmdline, err := os.ReadFile(filepath.Join(directory, procCmdlineName))
	if err != nil {
		return processInfo{}, ctxerrors.Wrap(err, "read process cmdline")
	}

	args := strings.Split(
		strings.TrimRight(string(cmdline), cmdlineSeparator),
		cmdlineSeparator,
	)

	return processInfo{PID: pid, ParentPID: parent, Args: args}, nil
}

// parseParentPID reads the parent PID from a /proc/<pid>/stat line. The
// command name sits in parentheses and may itself hold spaces or
// parentheses, so the fields are read after the last closing one.
func parseParentPID(stat []byte) (int, error) {
	closing := bytes.LastIndexByte(stat, ')')
	if closing < 0 {
		return 0, ctxerrors.Wrap(commerr.ErrParseFailed, "process stat")
	}

	fields := strings.Fields(string(stat[closing+1:]))
	if len(fields) <= statParentField {
		return 0, ctxerrors.Wrap(commerr.ErrParseFailed, "process stat")
	}

	parent, err := strconv.Atoi(fields[statParentField])
	if err != nil {
		return 0, ctxerrors.Wrap(err, "parse parent pid")
	}

	return parent, nil
}

// descendants returns every process under root, not including root.
func descendants(processes []processInfo, root int) []processInfo {
	children := map[int][]processInfo{}
	for _, process := range processes {
		children[process.ParentPID] = append(
			children[process.ParentPID],
			process,
		)
	}

	found := []processInfo{}
	queue := []int{root}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, child := range children[current] {
			found = append(found, child)
			queue = append(queue, child.PID)
		}
	}

	return found
}

// workers returns the Peen worker processes under the controller.
func workers(processes []processInfo, controller int) []processInfo {
	found := []processInfo{}

	for _, process := range descendants(processes, controller) {
		if !isWorker(process.Args) {
			continue
		}

		found = append(found, process)
	}

	return found
}

func isWorker(args []string) bool {
	return len(args) > 1 &&
		filepath.Base(args[0]) == filepath.Base(defaultPeenBinary) &&
		args[1] == workerCommand
}

// residentBytes reads a process's resident set size.
func residentBytes(pid int) (int64, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), procStatusName)

	file, err := os.Open(path)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "open process status")
	}

	defer func() {
		_ = file.Close() // Read-only /proc file; nothing to flush.
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, residentPrefix) {
			continue
		}

		fields := strings.Fields(strings.TrimPrefix(line, residentPrefix))
		if len(fields) == 0 {
			break
		}

		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "parse resident size")
		}

		return kib * kibibyte, nil
	}

	if err := scanner.Err(); err != nil {
		return 0, ctxerrors.Wrap(err, "read process status")
	}

	return 0, nil
}
