//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/stack"
)

// Each storage permutation boots a full stack (~20 containers, including
// several postgres, vault, and the JVM-based dynamodb-local). Booting
// them all at once saturates a 4-vCPU CI runner and the slow-starting
// JVM services miss their healthcheck windows, so concurrency is bounded
// via `go test -parallel N` (see e2e.yml, defaulting to 1). The -parallel
// token is held until each subtest fully completes — including the stack
// teardown registered by MustNewStack via t.Cleanup — so it caps live
// stacks correctly.
//
// piri's db backend is always postgres: piri:main's curio PDP pipeline
// refuses to start with sqlite ("curio PDP pipeline requires Postgres"),
// so only the blob backend (filesystem vs s3) still varies.
func TestUploadAndRetrieve(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("skipping on darwin (docker-in-docker flakiness)")
	}

	tests := []struct {
		name  string
		useS3 bool
	}{
		{name: "filesystem"},
		{name: "s3", useS3: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			opts := []stack.Option{
				stack.WithPiriNodes(stack.PiriNodeConfig{
					S3:       tt.useS3,
					Postgres: true,
				}),
			}
			if img := os.Getenv("PIRI_IMAGE"); img != "" {
				opts = append(opts, stack.WithPiriImage(img))
			}
			if img := os.Getenv("GUPPY_IMAGE"); img != "" {
				opts = append(opts, stack.WithGuppyImage(img))
			}
			if os.Getenv("SMELT_WORKSPACE") != "" {
				opts = append(opts, stack.WithWorkspaceBinaries())
			}

			s := stack.MustNewStack(t, opts...)

			// Smelt owns ingot's *system definition* (topology, config,
			// keys); ingot's behavior tests live in the ingot repo
			// (testing/forge_*_test.go), which imports this stack SDK.
			// Here we only assert the definition boots to healthy.
			waitHTTPOK(t, s.IngotEndpoint()+"/health", 2*time.Minute)

			// Same for swarf (no /health route; GET / is the server-info
			// endpoint).
			waitHTTPOK(t, s.SwarfEndpoint()+"/", 2*time.Minute)

			gup, err := guppy.NewContainerClient(s)
			if err != nil {
				t.Fatal(err)
			}

			if err := gup.Login(ctx, "test@example.com"); err != nil {
				t.Fatalf("failed to login: %v", err)
			}

			spaceDID, err := gup.GenerateSpace(ctx)
			if err != nil {
				t.Fatalf("failed to generate space: %v", err)
			}
			t.Logf("created space: %s", spaceDID)

			dataPath, err := gup.GenerateTestData(ctx, "10MB")
			if err != nil {
				t.Fatalf("failed to generate test data: %v", err)
			}

			if err := gup.AddSource(ctx, spaceDID, dataPath); err != nil {
				t.Fatalf("failed to add source: %v", err)
			}

			cids, err := gup.Upload(ctx, spaceDID, guppy.WithReplicas(1))
			if err != nil {
				t.Fatalf("failed to upload: %v", err)
			}
			if len(cids) == 0 {
				t.Fatal("expected at least one CID from upload")
			}
			t.Logf("uploaded CIDs: %v", cids)

			dstPath := fmt.Sprintf("/tmp/testdata-download-%d", time.Now().UnixNano())
			if err := gup.Retrieve(ctx, spaceDID, cids[len(cids)-1], dstPath); err != nil {
				t.Fatalf("failed to retrieve: %v", err)
			}
		})
	}
}
