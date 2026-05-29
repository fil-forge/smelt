package guppy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fil-forge/smelt/pkg/clients/cliexec"
	"github.com/fil-forge/smelt/pkg/stack"
	"golang.org/x/sync/errgroup"
)

var errAlreadyLoggedIn = fmt.Errorf("already logged in")

// guppyJSONPrefix is prepended to every guppy invocation: the binary plus the
// persistent flag that selects machine-readable output. A fixed position keeps
// invocations deterministic.
var guppyJSONPrefix = []string{"guppy", "--output=json"}

type LoginValidator interface {
	// ValidateEmailLogin waits for a validation email to be sent to the given
	// address, extracts the validation link, and clicks it. Use the passed
	// context to stop waiting.
	ValidateEmailLogin(ctx context.Context, email string) error
}

type Option func(*ContainerClient)

func WithLoginValidator(validator LoginValidator) Option {
	return func(c *ContainerClient) {
		c.validator = validator
	}
}

// Compile-time check that ContainerClient implements guppy.Client.
var _ Client = (*ContainerClient)(nil)

// ContainerClient implements guppy.Client by executing guppy inside its
// container and decoding the JSON it emits under `--output json`.
type ContainerClient struct {
	runner    cliexec.Runner
	validator LoginValidator
}

func MustNewContainerClient(t *testing.T, stack *stack.Stack, options ...Option) *ContainerClient {
	c, err := NewContainerClient(stack, options...)
	if err != nil {
		t.Fatalf("failed to create guppy client: %v", err)
	}
	return c
}

func NewContainerClient(stack *stack.Stack, options ...Option) (*ContainerClient, error) {
	c := &ContainerClient{
		runner: cliexec.StackRunner{Stack: stack, Service: "guppy"},
	}
	for _, option := range options {
		option(c)
	}
	if c.validator == nil {
		// Fetch emails from smtp4dev over its host-mapped API port (fine with
		// ephemeral ports — MappedPort resolves it), but POST the validation
		// link from inside the Docker network. Sprue's public_url points at
		// the `upload` DNS name in test mode, so the host can't reach it.
		clicker := &ExecDoer{Stack: stack, Service: "guppy"}
		validator, err := NewSMTP4DevLoginValidator(
			stack.EmailEndpoint(),
			WithSMTP4DevLoginValidatorClicker(clicker),
		)
		if err != nil {
			return nil, err
		}
		c.validator = validator
	}
	return c, nil
}

// Login logs in with the given email, racing the (blocking) login command
// against the email-validation flow. The login result's explicit
// already_logged_in / logged_in fields drive the control flow that previously
// relied on scraping stdout.
func (c *ContainerClient) Login(ctx context.Context, email string, options ...LoginOption) (LoginResult, error) {
	config := &loginConfig{}
	for _, option := range options {
		option(config)
	}
	if config.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.timeout)
		defer cancel()
	}

	g, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var result LoginResult
	g.Go(func() error {
		res, err := cliexec.JSON[LoginResult](ctx, c.runner, guppyJSONPrefix, "login", email)
		if err != nil {
			return err
		}
		result = res
		if res.AlreadyLoggedIn {
			// No email is sent in this case; stop the validator so it doesn't
			// wait forever.
			cancel(errAlreadyLoggedIn)
			return nil
		}
		if !res.LoggedIn {
			return fmt.Errorf("login did not report success: %+v", res)
		}
		return nil
	})

	g.Go(func() error {
		err := c.validator.ValidateEmailLogin(ctx, email)
		if err != nil {
			if !errors.Is(context.Cause(ctx), errAlreadyLoggedIn) {
				return err
			}
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return LoginResult{}, err
	}
	return result, nil
}

// Whoami returns the local agent's DID.
func (c *ContainerClient) Whoami(ctx context.Context) (WhoamiResult, error) {
	return cliexec.JSON[WhoamiResult](ctx, c.runner, guppyJSONPrefix, "whoami")
}

// Version returns build information for the guppy binary.
func (c *ContainerClient) Version(ctx context.Context) (VersionResult, error) {
	return cliexec.JSON[VersionResult](ctx, c.runner, guppyJSONPrefix, "version")
}

// Retrieve downloads content by CID to a destination path.
func (c *ContainerClient) Retrieve(ctx context.Context, spaceDID, cid, destPath string) (RetrieveResult, error) {
	return cliexec.JSON[RetrieveResult](ctx, c.runner, guppyJSONPrefix, "retrieve", spaceDID, cid, destPath)
}

// Ls lists uploads in a space.
func (c *ContainerClient) Ls(ctx context.Context, spaceDID string, options ...LsOption) ([]UploadListItem, error) {
	config := &lsConfig{}
	for _, option := range options {
		option(config)
	}
	args := []string{"ls"}
	if config.shards {
		args = append(args, "--shards")
	}
	args = append(args, spaceDID)
	return cliexec.JSON[[]UploadListItem](ctx, c.runner, guppyJSONPrefix, args...)
}

// Space exposes the `guppy space ...` command group.
func (c *ContainerClient) Space() *SpaceClient { return &SpaceClient{c: c} }

// Upload exposes the `guppy upload ...` command group.
func (c *ContainerClient) Upload() *UploadClient { return &UploadClient{c: c} }

// Account exposes the `guppy account ...` command group.
func (c *ContainerClient) Account() *AccountClient { return &AccountClient{c: c} }

// Blob exposes the `guppy blob ...` command group.
func (c *ContainerClient) Blob() *BlobClient { return &BlobClient{c: c} }

// SpaceClient drives `guppy space ...`.
type SpaceClient struct{ c *ContainerClient }

// Generate creates a new space and returns its DID.
func (s *SpaceClient) Generate(ctx context.Context, options ...SpaceGenerateOption) (SpaceGenerateResult, error) {
	config := &spaceGenerateConfig{}
	for _, option := range options {
		option(config)
	}
	args := []string{"space", "generate"}
	if config.name != "" {
		args = append(args, "--name", config.name)
	}
	return cliexec.JSON[SpaceGenerateResult](ctx, s.c.runner, guppyJSONPrefix, args...)
}

// List lists all spaces in the local store.
func (s *SpaceClient) List(ctx context.Context) ([]SpaceItem, error) {
	return cliexec.JSON[[]SpaceItem](ctx, s.c.runner, guppyJSONPrefix, "space", "list")
}

// Info returns provider information for a space.
func (s *SpaceClient) Info(ctx context.Context, spaceDID string) (SpaceInfoResult, error) {
	return cliexec.JSON[SpaceInfoResult](ctx, s.c.runner, guppyJSONPrefix, "space", "info", spaceDID)
}

// UploadClient drives `guppy upload ...`.
type UploadClient struct{ c *ContainerClient }

// Run uploads all sources in a space and returns the per-upload results.
func (u *UploadClient) Run(ctx context.Context, spaceDID string, options ...UploadOption) (UploadResult, error) {
	config := &uploadConfig{}
	for _, option := range options {
		option(config)
	}
	args := []string{"upload"}
	if config.replicas > 0 {
		args = append(args, "--replicas", fmt.Sprintf("%d", config.replicas))
	}
	args = append(args, spaceDID)
	return cliexec.JSON[UploadResult](ctx, u.c.runner, guppyJSONPrefix, args...)
}

// Source exposes the `guppy upload source ...` command group.
func (u *UploadClient) Source() *SourceClient { return &SourceClient{c: u.c} }

// SourceClient drives `guppy upload source ...`.
type SourceClient struct{ c *ContainerClient }

// Add adds a source directory to a space.
func (s *SourceClient) Add(ctx context.Context, spaceDID, path string) (SourceAddResult, error) {
	return cliexec.JSON[SourceAddResult](ctx, s.c.runner, guppyJSONPrefix, "upload", "source", "add", spaceDID, path)
}

// List lists the sources added to a space.
func (s *SourceClient) List(ctx context.Context, spaceDID string) ([]SourceItem, error) {
	return cliexec.JSON[[]SourceItem](ctx, s.c.runner, guppyJSONPrefix, "upload", "source", "list", spaceDID)
}

// AccountClient drives `guppy account ...`.
type AccountClient struct{ c *ContainerClient }

// List lists logged-in accounts.
func (a *AccountClient) List(ctx context.Context) ([]AccountItem, error) {
	return cliexec.JSON[[]AccountItem](ctx, a.c.runner, guppyJSONPrefix, "account", "list")
}

// BlobClient drives `guppy blob ...`.
type BlobClient struct{ c *ContainerClient }

// Ls lists blobs in a space.
func (b *BlobClient) Ls(ctx context.Context, spaceDID string) ([]BlobItem, error) {
	return cliexec.JSON[[]BlobItem](ctx, b.c.runner, guppyJSONPrefix, "blob", "ls", spaceDID)
}
