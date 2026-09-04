//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
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

// TestCurioPiriProvingLoop validates the part no smelt e2e has ever
// asserted: the full PDP proving loop on the local devnet. It uploads
// enough data to cross the 128MiB-padded aggregation threshold, then
// follows the pdpv0 milestones through piri's logs — pieces added on-chain,
// proving period initialized, proof submitted — and finally confirms the
// provePossession transaction succeeded on-chain (the PDPVerifier contract
// reverts on an invalid proof, so a status-1 receipt IS verification).
//
//	ITEST_PIRI_BIN=… ITEST_BLOCKCHAIN_IMAGE=… go test -tags e2e ./tests/e2e -run TestCurioPiriProvingLoop -v -timeout 2400s
func TestCurioPiriProvingLoop(t *testing.T) {
	piriBin := os.Getenv("ITEST_PIRI_BIN")
	if piriBin == "" {
		t.Skip("set ITEST_PIRI_BIN to a piri binary built from the curio branch")
	}

	opts := []stack.Option{
		stack.WithPiriNodes(stack.PiriNodeConfig{Postgres: true}),
		stack.WithServiceBinary("piri", piriBin),
	}
	if img := os.Getenv("ITEST_BLOCKCHAIN_IMAGE"); img != "" {
		opts = append(opts, stack.WithBlockchainImage(img))
	}

	ctx := t.Context()
	s := stack.MustNewStack(t, opts...)

	gup, err := guppy.NewContainerClient(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := gup.Login(ctx, "prover@example.com"); err != nil {
		t.Fatalf("failed to login: %v", err)
	}
	spaceDID, err := gup.GenerateSpace(ctx)
	if err != nil {
		t.Fatalf("failed to generate space: %v", err)
	}
	// ~100MB raw ≈ ~200MB padded — comfortably past the 128MiB-padded
	// aggregation threshold, so the aggregate + AddRoots fire promptly.
	dataPath, err := gup.GenerateTestData(ctx, "100MB")
	if err != nil {
		t.Fatalf("failed to generate test data: %v", err)
	}
	if err := gup.AddSource(ctx, spaceDID, dataPath); err != nil {
		t.Fatalf("failed to add source: %v", err)
	}
	if _, err := gup.Upload(ctx, spaceDID); err != nil {
		t.Fatalf("failed to upload: %v", err)
	}
	t.Log("100MB uploaded; waiting for the pdpv0 pipeline milestones")

	// Milestone 1: proving period initialized (requires the dataset to have
	// on-chain pieces, i.e. AddRoots confirmed and the piece-add watcher ran).
	waitForPiriLogContains(t, ctx, s, 8*time.Minute, "Initial challenge window scheduled")
	t.Log("milestone: proving period initialized (pieces are on-chain)")

	// Milestone 2: a proof was built and submitted.
	proveLine := waitForPiriLogContains(t, ctx, s, 10*time.Minute, "PDP Prove Task: transaction sent")
	txHash := hexHashRe.FindString(proveLine)
	if txHash == "" {
		t.Fatalf("no tx hash in prove log line: %q", proveLine)
	}
	t.Logf("milestone: provePossession submitted (%s)", txHash)

	// Milestone 3: the proof transaction succeeded on-chain.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		stdout, _, err := s.Exec(ctx, "blockchain", "cast", "receipt", txHash, "--rpc-url", "http://localhost:8545")
		if err == nil && strings.Contains(stdout, "status") && strings.Contains(stdout, "1 (success)") {
			t.Log("milestone: provePossession receipt status 1 — proof verified by PDPVerifier")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("provePossession %s never confirmed successfully; last cast output: %v %s", txHash, err, stdout)
		}
		time.Sleep(5 * time.Second)
	}
}

var hexHashRe = regexp.MustCompile(`0x[0-9a-fA-F]{64}`)

// waitForPiriLogContains polls piri-0's logs until a line contains substr,
// returning that line.
func waitForPiriLogContains(t *testing.T, ctx context.Context, s *stack.Stack, timeout time.Duration, substr string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		logs, err := s.Logs(ctx, "piri-0")
		if err != nil {
			t.Fatalf("piri-0 logs: %v", err)
		}
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, substr) {
				return line
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context done waiting for piri log %q: %v", substr, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	t.Fatalf("piri-0 logs never contained %q within %s", substr, timeout)
	return ""
}
