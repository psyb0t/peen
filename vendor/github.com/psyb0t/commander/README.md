# Commander

[![CI](https://github.com/psyb0t/commander/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/commander/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/commander/badges/coverage.svg)](https://github.com/psyb0t/commander/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/commander/badges/version.svg)](https://github.com/psyb0t/commander/releases)
[![license](https://raw.githubusercontent.com/psyb0t/commander/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/commander/badges/importers.svg)](https://github.com/psyb0t/commander/blob/badges/importers.md)

Go's `os/exec` made me want to shart on my laptop's screen.
Every time I had to wire up pipes, babysit goroutines, and pray
that `cmd.Wait()` wouldn't give me digital hemorrhoids, I died
a little inside. So I built this instead.

Commander wraps `os/exec` into something that doesn't make you
want to fucking quit programming — proper process lifecycle
management, real-time output streaming, graceful termination,
and a mock system for testing. No more hanging processes, no
more race conditions, no more timeout bullshit that makes you
question your career choices.

## Install

```bash
go get github.com/psyb0t/commander
```

## Quick Start

```go
cmd := commander.New()
ctx := context.Background()

// Run and forget
err := cmd.Run(ctx, "echo", []string{"hello world"})

// Get output like a civilized person
stdout, stderr, err := cmd.Output(
    ctx, "ls", []string{"-la", "/tmp"},
)

// Combined stdout+stderr when you don't give a shit
output, err := cmd.CombinedOutput(
    ctx, "git", []string{"status"},
)
```

That's it. No pipe juggling, no goroutine babysitting, no 47
lines of boilerplate just to run a fucking command and get its
output.

## Interfaces

### Commander

```go
type Commander interface {
    Run(
        ctx context.Context,
        name string,
        args []string,
        opts ...Option,
    ) error

    Output(
        ctx context.Context,
        name string,
        args []string,
        opts ...Option,
    ) (stdout []byte, stderr []byte, err error)

    CombinedOutput(
        ctx context.Context,
        name string,
        args []string,
        opts ...Option,
    ) (output []byte, err error)

    Start(
        ctx context.Context,
        name string,
        args []string,
        opts ...Option,
    ) (Process, error)
}
```

### Process

```go
type Process interface {
    Start() error
    Wait() error
    StdinPipe() (io.WriteCloser, error)
    Stream(stdout, stderr chan<- string)
    Stop(ctx context.Context) error
    Kill(ctx context.Context) error
    PID() int
}
```

### Options

```go
// feed input to the command
commander.WithStdin(reader)

// set environment variables
commander.WithEnv([]string{})

// set working directory
commander.WithDir("/path")
```

## Real-time Streaming

Stream stdout and stderr as they come in, not after the
process is done like some caveman asshole reading yesterday's
newspaper:

```go
proc, err := cmd.Start(
    ctx, "ping", []string{"-c", "10", "google.com"},
)
if err != nil {
    log.Fatal(err)
}

stdout := make(chan string, 100)
stderr := make(chan string, 100)
proc.Stream(stdout, stderr)

go func() {
    for line := range stdout {
        fmt.Printf("[OUT] %s\n", line)
    }
}()

go func() {
    for line := range stderr {
        fmt.Printf("[ERR] %s\n", line)
    }
}()

err = proc.Wait()
```

Multiple listeners can subscribe to the same process — each
call to `Stream()` adds a new broadcast subscriber. Pass `nil`
for channels you don't care about.

## Process Control

### Graceful Stop

`Stop` sends SIGTERM first, then SIGKILL if the stubborn
bastard doesn't exit within the context deadline. No more
zombie processes haunting your system like the ghosts of shitty
code past.

```go
proc, _ := cmd.Start(
    ctx, "tail", []string{"-f", "/var/log/syslog"},
)

// 5 second grace period before we get violent
stopCtx, cancel := context.WithTimeout(
    ctx, 5*time.Second,
)
defer cancel()

err := proc.Stop(stopCtx)
```

If the context has no deadline, a default 3-second timeout is
used before SIGKILL.

Child processes get killed too — the whole process group gets
the signal, so nothing escapes. No more orphaned shit lurking
in your process table.

### Immediate Kill

When you're done being polite and just want the fucker dead:

```go
err := proc.Kill(ctx) // straight to SIGKILL, no negotiation
```

## Timeouts

Context controls everything. No redundant timeout parameters,
no bullshit — just standard Go context patterns:

```go
ctx, cancel := context.WithTimeout(
    context.Background(), 2*time.Second,
)
defer cancel()

err := cmd.Run(ctx, "sleep", []string{"10"})
if errors.Is(err, commonerrors.ErrTimeout) {
    fmt.Println("timed out, as expected")
}
```

## Error Handling

You get specific error types so you know exactly what went
wrong instead of parsing error strings like a fucking animal:

```go
import commonerrors "github.com/psyb0t/common-go/errors"

// context deadline exceeded
errors.Is(err, commonerrors.ErrTimeout)

// killed by SIGTERM
errors.Is(err, commonerrors.ErrTerminated)

// killed by SIGKILL
errors.Is(err, commonerrors.ErrKilled)

// non-zero exit code (includes stderr + exit code)
errors.Is(err, commonerrors.ErrFailed)
```

## Testing with Mocks

The whole thing is designed to be testable. The `Commander`
interface means you can mock everything without actually
spawning processes in your tests like some kind of
hemorrhoid-inducing integration test nightmare.

### Basic

```go
func TestDeploy(t *testing.T) {
    mock := commander.NewMock()

    mock.Expect("git", "status").
        ReturnOutput([]byte("On branch main"))
    mock.Expect("git", "push").
        ReturnError(errors.New("push failed"))

    err := deploy(mock)
    assert.Error(t, err)
    require.NoError(t, mock.VerifyExpectations())
}
```

### Argument Matchers

For when you don't want to match every argument exactly
because you're not a fucking psychopath:

```go
mock.ExpectWithMatchers(
    "grep",
    commander.Regex("^error.*"),
    commander.Exact("logfile.txt"),
)

mock.ExpectWithMatchers(
    "find",
    commander.Any(),
    commander.Any(),
)
```

### Process Mocking

Mock streaming processes too — the mock will feed lines
through the stream channels just like a real process would:

```go
mock.Expect("tail", "-f", "/var/log/messages").
    ReturnOutput([]byte("line 1\nline 2\nline 3"))

proc, err := mock.Start(
    ctx,
    "tail",
    []string{"-f", "/var/log/messages"},
)
require.NoError(t, err)

stdout := make(chan string, 10)
proc.Stream(stdout, nil)

var lines []string
for line := range stdout {
    lines = append(lines, line)
}
// lines == ["line 1", "line 2", "line 3"]
```

### Mock Utilities

```go
// ordered list of commands that were called
mock.CallOrder()

// clears all expectations and history
mock.Reset()

// fails if expected commands weren't called
mock.VerifyExpectations()
```

## Thread Safety

Everything is safe for concurrent use. Use the same
`Commander` from multiple goroutines, run commands
concurrently, attach multiple stream subscribers per process,
use mocks in parallel tests. It all works because the internals
use proper synchronization instead of the stdlib's "fuck you,
figure it out yourself" approach.

## Why This Exists

Because `os/exec` is a hemorrhoid factory. Look at this shit:

### Before (stdlib os/exec)

```go
cmd := exec.CommandContext(
    ctx, "some-command", "arg1", "arg2",
)
stdout, err := cmd.StdoutPipe()
if err != nil { /* ... */ }
stderr, err := cmd.StderrPipe()
if err != nil { /* ... */ }
err = cmd.Start()
if err != nil { /* ... */ }
// now read from pipes in goroutines
// handle timeouts manually
// figure out why your process is hanging
// write your own mocks
// question your life choices
// cry
```

### After (Commander)

```go
cmd := commander.New()
stdout, stderr, err := cmd.Output(
    ctx,
    "some-command",
    []string{"arg1", "arg2"},
)
// done. go get a fucking coffee.
```

## Dependencies

- `log/slog` — debug logging (configure however you want)
- `github.com/psyb0t/ctxerrors` — error wrapping with context
- `github.com/psyb0t/common-go` — common error types

## License

MIT — do whatever you want with it.
