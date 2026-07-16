//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/stack"
)

// TestCurioPiriValidation is Track V of the landing plan (see
// fil-forge/LANDING.md): boot the smelt stack with a piri binary built from
// frrist/hello-again-curio (Curio-based PDP core, harmonydb/Postgres) and
// validate the protocol surface end to end.
//
//	ITEST_PIRI_BIN=/path/to/piri-curio go test -tags e2e ./tests/e2e -run TestCurioPiriValidation -v -timeout 1800s
//
// The Curio PDP core requires Postgres (harmonytask uses FOR UPDATE SKIP
// LOCKED), so the piri node runs with the postgres storage backend.
func TestCurioPiriValidation(t *testing.T) {
	piriBin := os.Getenv("ITEST_PIRI_BIN")
	if piriBin == "" {
		t.Skip("set ITEST_PIRI_BIN to a piri binary built from the curio branch (skiff nosupraseal tags)")
	}

	opts := []stack.Option{
		stack.WithPiriNodes(stack.PiriNodeConfig{Postgres: true}),
		stack.WithServiceBinary("piri", piriBin),
	}
	// The published filecoin-localdev:main image emits nil-Ticket mock
	// tipsets that lotus ≥1.36.0 clients reject, silently starving
	// chainsched (see LANDING.md Track V). Point this at an image carrying
	// the mockrpc Ticket fix (e.g. filecoin-localdev:curio-local) until the
	// published image is rebuilt.
	if img := os.Getenv("ITEST_BLOCKCHAIN_IMAGE"); img != "" {
		opts = append(opts, stack.WithBlockchainImage(img))
	}

	ctx := t.Context()
	s := stack.MustNewStack(t, opts...)

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
	dataPath, err := gup.GenerateTestData(ctx, "10MB")
	if err != nil {
		t.Fatalf("failed to generate test data: %v", err)
	}
	if err := gup.AddSource(ctx, spaceDID, dataPath); err != nil {
		t.Fatalf("failed to add source: %v", err)
	}
	cids, err := gup.Upload(ctx, spaceDID)
	if err != nil {
		t.Fatalf("failed to upload: %v", err)
	}
	if len(cids) == 0 {
		t.Fatal("expected at least one CID from upload")
	}
	t.Logf("uploaded CIDs: %v", cids)

	dstPath := fmt.Sprintf("/tmp/curio-validation-download-%d", time.Now().UnixNano())
	if err := gup.Retrieve(ctx, spaceDID, cids[len(cids)-1], dstPath); err != nil {
		t.Fatalf("failed to retrieve: %v", err)
	}
	t.Log("upload/retrieve round-trip OK against the Curio-based piri")
}
