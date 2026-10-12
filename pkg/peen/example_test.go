package peen_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing/fstest"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
)

func exampleOptions() (peen.Options, func(), error) {
	directory, err := os.MkdirTemp("", "peen-example-")
	if err != nil {
		return peen.Options{}, nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(directory); err != nil {
			fmt.Println(err) //nolint:forbidigo // Cleanup errors fail the example output contract.
		}
	}

	return peen.Options{
		ConfigDirectory: directory,
		RootAgent:       "core:general-purpose",
		InMemoryStore:   true,
		Models: map[string]peen.ModelClient{
			"example/model": {
				Client: elelem.New(elelemtest.NewScriptedDriver(
					elelemtest.Text("example reply"),
				)),
				Model: elelem.Model{ID: "model"},
			},
		},
		DefaultModel: "example/model",
	}, cleanup, nil
}

func closeExample(runtime *peen.Runtime) {
	if err := runtime.Close(context.Background()); err != nil {
		fmt.Println(err) //nolint:forbidigo // Cleanup errors fail the example output contract.
	}
}

func ExampleRuntime_Close() {
	options, cleanup, err := exampleOptions()
	if err != nil {
		fmt.Println(err)

		return
	}
	defer cleanup()
	runtime, err := peen.New(options)
	if err != nil {
		fmt.Println(err)

		return
	}
	closeExample(runtime)
	_, err = runtime.Message(context.Background(), peen.MessageRequest{
		Message: "after cleanup",
	})
	fmt.Println(errors.Is(err, peen.ErrClosed))
	// Output: true
}

func ExampleRuntime_OpenSession() {
	options, cleanup, err := exampleOptions()
	if err != nil {
		fmt.Println(err)

		return
	}
	defer cleanup()
	files, err := peen.FromIOFS(fstest.MapFS{})
	if err != nil {
		fmt.Println(err)

		return
	}
	options.Filesystem = files
	options.WorkspaceID = "initial-workspace"
	runtime, err := peen.New(options)
	if err != nil {
		fmt.Println(err)

		return
	}
	defer closeExample(runtime)
	session, err := runtime.OpenSession(context.Background(), peen.OpenSessionOptions{
		Workspace: "another-workspace",
	})
	if err != nil {
		fmt.Println(err)

		return
	}
	result, err := session.Send(context.Background(), peen.MessageRequest{
		Message: "inspect",
	}, nil)
	if err != nil {
		fmt.Println(err)

		return
	}
	fmt.Println(result.Message)
	// Output: example reply
}

func ExampleTool() {
	options, cleanup, err := exampleOptions()
	if err != nil {
		fmt.Println(err)

		return
	}
	defer cleanup()
	options.Tools = []peen.Tool{{
		Name:            "projectInfo",
		Description:     "Returns project metadata.",
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
			return peen.ToolResult{Content: "metadata"}, nil
		},
	}}
	options.Hooks = map[string]peen.HookHandler{
		"preToolUse": func(context.Context, peen.HookInvocation) (peen.HookOutcome, error) {
			return peen.HookOutcome{}, nil
		},
	}
	options.Harness = []peen.HarnessLayer{{Files: map[string]string{
		"AGENTS.md": "Check project metadata before editing.",
	}}}
	runtime, err := peen.New(options)
	if err != nil {
		fmt.Println(err)

		return
	}
	defer closeExample(runtime)
	fmt.Println("extensions configured")
	// Output: extensions configured
}

func ExampleFromIOFS() {
	files, err := peen.FromIOFS(fstest.MapFS{
		"README.md": {Data: []byte("virtual workspace")},
	})
	if err != nil {
		fmt.Println(err)

		return
	}
	data, err := files.ReadFile("README.md")
	if err != nil {
		fmt.Println(err)

		return
	}
	fmt.Println(string(data))
	// Output: virtual workspace
}
