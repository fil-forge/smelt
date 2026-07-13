//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"testing"
	"time"

	ingottest "github.com/fil-forge/ingot/testing"
	"github.com/fil-forge/smelt/pkg/stack"
)

// TestIngotForgeReadAfterEviction proves the appliance read tier: after the local
// spool is wiped, a GET must re-fetch the object's body blobs from piri by
// resolving their location from the local blob_locations table
// (registry.LocalLocator) and issuing a /content/retrieve — not from
// read-after-write. Body blobs live only in the spool; the manifest/MST live in
// the catalog log and survive the wipe, so only the body read exercises the
// network tier.
//
//	go test -tags e2e ./tests/e2e -run TestIngotForgeReadAfterEviction -v -timeout 900s
func TestIngotForgeReadAfterEviction(t *testing.T) {
	ctx := t.Context()
	const email = "test@example.com"

	opts := []stack.Option{stack.WithPiriNodes(stack.PiriNodeConfig{})}
	if os.Getenv("SMELT_WORKSPACE") != "" {
		opts = append(opts, stack.WithWorkspaceBinaries())
	}
	s := stack.MustNewStack(t, opts...)

	ingotEndpoint := s.IngotEndpoint()
	waitHTTPOK(t, ingotEndpoint+"/health", 2*time.Minute)

	// ingot self-provisions its space on sprue (guppy-free) so uploads succeed.
	ingotLoginViaEmail(t, ctx, s, email)
	if out, errOut, err := s.Exec(ctx, "ingot",
		"ingot", "--config", ingotConfigPath, "space", "generate", "--provision-to", email); err != nil {
		t.Fatalf("ingot space generate: %v (stdout=%s stderr=%s)", err, out, errOut)
	}

	cfg := ingottest.Config{Endpoint: ingotEndpoint, AccessKey: "ingot", SecretKey: "ingotsecret", Region: "us-east-1"}
	const bucket, key = "evict-bucket", "obj"

	// A deterministic body large enough to be a real blob shipped to piri.
	data := make([]byte, 512*1024)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}

	if err := ingottest.CreateBucket(ctx, cfg, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := ingottest.PutBytes(ctx, cfg, bucket, key, data); err != nil {
		t.Fatalf("put object: %v", err)
	}

	// Wipe the local spool so the next GET cannot read-after-write — its body
	// blobs must be re-fetched from piri. (Alpine image → sh/rm available.)
	if out, errOut, err := s.Exec(ctx, "ingot", "sh", "-c", "rm -rf /data/spool"); err != nil {
		t.Fatalf("evict spool: %v (stdout=%s stderr=%s)", err, out, errOut)
	}

	got, err := ingottest.GetBytes(ctx, cfg, bucket, key)
	if err != nil {
		t.Fatalf("get after eviction (forge read tier): %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read-after-eviction mismatch: got %d bytes, want %d", len(got), len(data))
	}
	t.Logf("read-after-eviction OK: %d bytes re-fetched from piri via the local locator", len(got))
}
