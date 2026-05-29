// Package randdir drives the `randdir` test-data generator.
//
// randdir is NOT part of guppy — it is a standalone testing utility that
// happens to be bundled in the guppy container image. It gets its own client
// (rather than a method on the guppy client) to keep that distinction explicit:
// it produces random files on disk for upload tests and emits no structured
// output, so it does not go through guppy's JSON CLI at all.
package randdir

import (
	"context"
	"fmt"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/cliexec"
	"github.com/fil-forge/smelt/pkg/stack"
)

// defaultService is the container that ships the randdir binary.
const defaultService = "guppy"

type config struct {
	seed        string
	minFileSize string
	maxFileSize string
	output      string
}

// Option configures a Generate call.
type Option func(*config)

// WithSeed makes generation deterministic for the given seed.
func WithSeed(seed string) Option {
	return func(c *config) { c.seed = seed }
}

// WithMinFileSize sets randdir's --min-file-size (e.g. "256KB").
func WithMinFileSize(size string) Option {
	return func(c *config) { c.minFileSize = size }
}

// WithMaxFileSize sets randdir's --max-file-size (e.g. "32MB").
func WithMaxFileSize(size string) Option {
	return func(c *config) { c.maxFileSize = size }
}

// WithOutput overrides the (otherwise auto-generated) output directory inside
// the container.
func WithOutput(path string) Option {
	return func(c *config) { c.output = path }
}

// Client drives the randdir binary inside a container.
type Client struct {
	runner cliexec.Runner
}

// NewContainerClient returns a randdir client that runs in the guppy container
// (where the randdir binary lives).
func NewContainerClient(s *stack.Stack) *Client {
	return &Client{runner: cliexec.StackRunner{Stack: s, Service: defaultService}}
}

// NewWithRunner returns a randdir client backed by an explicit runner (useful
// for tests or when randdir lives in a different container).
func NewWithRunner(r cliexec.Runner) *Client {
	return &Client{runner: r}
}

// Generate creates random test data of the given total size and returns the
// path to the generated directory inside the container.
func (c *Client) Generate(ctx context.Context, size string, options ...Option) (string, error) {
	cfg := &config{}
	for _, option := range options {
		option(cfg)
	}

	path := cfg.output
	if path == "" {
		path = fmt.Sprintf("/tmp/testdata-%d", time.Now().UnixNano())
	}

	args := []string{"randdir", "--size", size, "--output", path}
	if cfg.seed != "" {
		args = append(args, "--seed", cfg.seed)
	}
	if cfg.minFileSize != "" {
		args = append(args, "--min-file-size", cfg.minFileSize)
	}
	if cfg.maxFileSize != "" {
		args = append(args, "--max-file-size", cfg.maxFileSize)
	}

	if _, _, err := c.runner.Run(ctx, args...); err != nil {
		return "", fmt.Errorf("randdir: %w", err)
	}
	return path, nil
}
