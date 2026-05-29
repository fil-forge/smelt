// Package cliexec provides the shared mechanism for harness clients that drive
// a service's CLI inside its container and parse JSON output.
//
// It is the exec-world analog of smtp4dev's HTTP jsonRequest helper: a Runner
// seam (so tests can swap a live stack for a fake) plus a generic JSON decoder.
// Each service client (guppy, and later piri/sprue/indexer) supplies its own
// binary/flag prefix and result types; the exec + decode + error-wrapping
// plumbing lives here once.
package cliexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fil-forge/smelt/pkg/stack"
)

// Runner executes a command inside a service container, returning stdout and
// stderr separately. It is the seam between a live stack and a test fake.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr string, err error)
}

// StackRunner runs commands in a named service container on a stack.
type StackRunner struct {
	Stack   *stack.Stack
	Service string
}

// Run implements Runner against a live stack.
func (r StackRunner) Run(ctx context.Context, args ...string) (string, string, error) {
	return r.Stack.Exec(ctx, r.Service, args...)
}

// RunnerFunc adapts a plain function to a Runner (handy for tests).
type RunnerFunc func(ctx context.Context, args ...string) (string, string, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, args ...string) (string, string, error) {
	return f(ctx, args...)
}

// Run executes prefix+args via the runner and returns raw stdout. On a non-zero
// exit (or runner error) it returns an error annotated with the command and any
// stderr, so callers get actionable diagnostics rather than a bare exit code.
func Run(ctx context.Context, r Runner, prefix []string, args ...string) (string, error) {
	full := make([]string, 0, len(prefix)+len(args))
	full = append(full, prefix...)
	full = append(full, args...)

	stdout, stderr, err := r.Run(ctx, full...)
	if err != nil {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return stdout, fmt.Errorf("%s: %w (stderr: %s)", strings.Join(full, " "), err, msg)
		}
		return stdout, fmt.Errorf("%s: %w", strings.Join(full, " "), err)
	}
	return stdout, nil
}

// JSON executes prefix+args and decodes the command's stdout into T.
//
// prefix is the fixed binary + output-format tokens (e.g. {"guppy",
// "--output=json"}); args is the subcommand and its arguments. An empty stdout
// or malformed JSON is reported as an error that includes the offending output.
func JSON[T any](ctx context.Context, r Runner, prefix []string, args ...string) (T, error) {
	var zero T

	stdout, err := Run(ctx, r, prefix, args...)
	if err != nil {
		return zero, err
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return zero, fmt.Errorf("%s: empty output", strings.Join(args, " "))
	}

	var out T
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return zero, fmt.Errorf("decoding json output of %q: %w (output: %s)", strings.Join(args, " "), err, trimmed)
	}
	return out, nil
}
