// Package guppy provides a test-harness client for the guppy CLI. Its surface
// mirrors guppy's command tree (sub-clients for space/upload/account/blob) and
// it consumes guppy's `--output json` mode, so results are decoded from
// structured output rather than scraped from human-readable text.
package guppy

import (
	"context"
	"time"
)

type loginConfig struct {
	timeout time.Duration
}

// LoginOption configures Login.
type LoginOption func(*loginConfig)

// WithLoginTimeout bounds how long Login waits for email validation.
func WithLoginTimeout(timeout time.Duration) LoginOption {
	return func(c *loginConfig) {
		c.timeout = timeout
	}
}

type spaceGenerateConfig struct {
	name string
}

// SpaceGenerateOption configures Space().Generate.
type SpaceGenerateOption func(*spaceGenerateConfig)

// WithSpaceName sets the optional name for the generated space.
func WithSpaceName(name string) SpaceGenerateOption {
	return func(c *spaceGenerateConfig) {
		c.name = name
	}
}

type uploadConfig struct {
	replicas int
}

// UploadOption configures Upload().Run.
type UploadOption func(*uploadConfig)

// WithReplicas requests the given number of replicas per shard.
func WithReplicas(replicas int) UploadOption {
	return func(c *uploadConfig) {
		c.replicas = replicas
	}
}

type lsConfig struct {
	shards bool
}

// LsOption configures Ls.
type LsOption func(*lsConfig)

// WithShards includes shard CIDs under each upload root.
func WithShards() LsOption {
	return func(c *lsConfig) {
		c.shards = true
	}
}

// Client is the guppy CLI surface exposed to tests. Sub-clients mirror guppy's
// command groups (`guppy space ...`, `guppy upload ...`, etc.).
type Client interface {
	// Login authenticates the agent with the given email, driving the email
	// validation flow to completion.
	Login(ctx context.Context, email string, options ...LoginOption) (LoginResult, error)

	// Whoami returns the local agent's DID.
	Whoami(ctx context.Context) (WhoamiResult, error)

	// Version returns build information for the guppy binary.
	Version(ctx context.Context) (VersionResult, error)

	// Retrieve downloads content by CID to a destination path.
	Retrieve(ctx context.Context, spaceDID, cid, destPath string) (RetrieveResult, error)

	// Ls lists uploads in a space.
	Ls(ctx context.Context, spaceDID string, options ...LsOption) ([]UploadListItem, error)

	// Space exposes the `guppy space ...` command group.
	Space() *SpaceClient
	// Upload exposes the `guppy upload ...` command group.
	Upload() *UploadClient
	// Account exposes the `guppy account ...` command group.
	Account() *AccountClient
	// Blob exposes the `guppy blob ...` command group.
	Blob() *BlobClient
}
