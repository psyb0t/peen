package workflowerrors

import (
	"bytes"
	"fmt"
	"runtime"
)

const MaxStackDepth = 50

func stack(skip int) string {
	// get stack
	stack := make([]uintptr, MaxStackDepth)
	length := runtime.Callers(2+skip, stack[:])

	// trim
	stack = stack[:length]

	buf := bytes.Buffer{}
	frames := runtime.CallersFrames(stack)
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&buf, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}

	return buf.String()
}
