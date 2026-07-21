//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/stack"
)

// stackSem bounds how many full stacks are alive at once. Each storage
// permutation spins up ~20 containers (blockchain, three postgres, vault,
// the JVM-based dynamodb-local, piri, ...). Letting all four permutations
// boot in parallel saturates a 4-vCPU CI runner, and the slow-starting
// JVM services (dynamodb-local, vault) then miss their Docker healthcheck
// windows — compose gives up with "dependency failed to start ...
// is unhealthy" and the subtest flakes. Capping concurrency keeps peak
// load in check while still overlapping the expensive boots. Override with
// SMELT_E2E_MAX_PARALLEL on beefier machines.
var stackSem = make(chan struct{}, maxParallelStacks())

func maxParallelStacks() int {
	if v := os.Getenv("SMELT_E2E_MAX_PARALLEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func TestUploadAndRetrieve(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("skipping on darwin (docker-in-docker flakiness)")
	}

	tests := []struct {
		name        string
		useS3       bool
		usePostgres bool
	}{
		{name: "default"},
		{name: "s3", useS3: true},
		{name: "postgres", usePostgres: true},
		{name: "s3_and_postgres", useS3: true, usePostgres: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Bound concurrent stack boots — see stackSem.
			stackSem <- struct{}{}
			defer func() { <-stackSem }()

			ctx := t.Context()

			opts := []stack.Option{
				stack.WithPiriNodes(stack.PiriNodeConfig{
					S3:       tt.useS3,
					Postgres: tt.usePostgres,
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
