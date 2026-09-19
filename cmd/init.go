package main

// This file is yours - it never gets replaced by framework updates.
// Use this init() to set up custom slog handlers, global config,
// or anything else that needs to run before the app starts.
//
// Example - add a custom slog handler:
//
//	func init() {
//		slogconf.AddSink(myCustomHandler)
//	}

// productName is what this binary calls itself: its cobra Use and Short, its
// own --help, and the "binary" field on every log line.
//
// main.go declares appName with the framework's own name as its default and
// expects a build to override it through -ldflags. A plain `go build ./cmd`,
// `go run ./cmd`, or `go install` sets no ldflags, so the binary would
// introduce itself as Servicepack to anyone who did not build it through the
// Makefile. Setting it here fixes the name for every build. A build that does
// pass -ldflags still wins, because the linker writes appName before any init
// runs and the assignment below only replaces the framework default.
const productName = "peen"

// servicepackDefaultName is the value main.go starts appName at. Overwriting
// only this exact value keeps an -ldflags build authoritative.
const servicepackDefaultName = "servicepack"

//nolint:gochecknoinits
func init() {
	if appName == servicepackDefaultName {
		appName = productName
	}
}
