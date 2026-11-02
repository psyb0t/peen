// The eval tasks hold Go source for their own task containers. This module
// file keeps them out of Peen's module, so Peen's builds, tests, and lint never
// compile them.
module peen.local/bench-eval

go 1.25
