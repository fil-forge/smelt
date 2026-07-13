//go:build e2e

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ingottest "github.com/fil-forge/ingot/testing"

	// The guppy package here is smelt's email-login test helper (SMTP4Dev
	// validator + in-network curl ExecDoer) — NOT the guppy CLI. This test
	// deliberately never invokes guppy: ingot logs in and provisions itself.
	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/stack"
)

const ingotConfigPath = "/etc/ingot/config.yaml"

// TestIngotNativeProvision proves ingot needs no guppy: it drives `ingot login`
// and `ingot space generate --provision-to` inside the ingot container (the
// access-request email confirmed via smtp4dev), then runs ingot's versity Smoke
// suite against the forge-mode listener to confirm S3 PutObject/GetObject
// round-trip through sprue/piri/indexer on a space ingot provisioned itself.
//
// Runs against the published ingot image; set SMELT_WORKSPACE=1 (with ingot
// in the parent go.work) to rebuild ingot from local source instead:
//
//	go test -tags e2e ./tests/e2e -run TestIngotNativeProvision -v -timeout 900s
func TestIngotNativeProvision(t *testing.T) {
	ctx := t.Context()
	const email = "test@example.com"

	opts := []stack.Option{stack.WithPiriNodes(stack.PiriNodeConfig{})}
	if os.Getenv("SMELT_WORKSPACE") != "" {
		opts = append(opts, stack.WithWorkspaceBinaries())
	}
	s := stack.MustNewStack(t, opts...)

	// Wait for the smelt-deployed ingot to serve.
	ingotEndpoint := s.IngotEndpoint()
	waitHTTPOK(t, ingotEndpoint+"/health", 2*time.Minute)
	t.Logf("smelt ingot S3 endpoint: %s", ingotEndpoint)

	// 1. ingot logs itself in (no guppy).
	ingotLoginViaEmail(t, ctx, s, email)

	// 2. ingot provisions its own space (no guppy): generate reuses the
	// daemon's /data/space.key, provisions it to the account on sprue, and
	// grants access. Provisioning is server-side, so the running daemon needs
	// no restart.
	out, errOut, err := s.Exec(ctx, "ingot",
		"ingot", "--config", ingotConfigPath, "space", "generate", "--provision-to", email)
	if err != nil {
		t.Fatalf("ingot space generate: %v (stdout=%s stderr=%s)", err, out, errOut)
	}
	if !strings.Contains(out+errOut, "Generated space:") {
		t.Fatalf("ingot space generate: unexpected output stdout=%q stderr=%q", out, errOut)
	}
	t.Logf("ingot space generate ok:\nstdout=%s\nstderr=%s", out, errOut)

	// 3. Run the versity Smoke suite against the forge-mode ingot. Passing
	// cases exercise the full PutObject -> ship -> GetObject path on a
	// natively-provisioned space.
	res := ingottest.Run(ctx, ingottest.Config{
		Endpoint:  ingotEndpoint,
		AccessKey: "ingot",
		SecretKey: "ingotsecret",
		Region:    "us-east-1",
	}, ingottest.Smoke)
	t.Logf("ingot (forge mode, self-provisioned) over %s: ran=%d passed=%d failed=%d",
		ingotEndpoint, res.Ran, res.Passed, res.Failed)
	if res.Passed == 0 {
		t.Fatalf("forge-mode ingot passed 0 versity cases after native provisioning")
	}
}

// ingotLoginViaEmail logs the ingot agent in as email — guppy-free: the
// blocking `ingot login` CLI runs in the ingot container and the validation
// link is clicked from inside that same container.
func ingotLoginViaEmail(t *testing.T, ctx context.Context, s *stack.Stack, email string) {
	t.Helper()
	if err := guppy.LoginViaEmail(ctx, s, "ingot", email,
		"ingot", "--config", ingotConfigPath, "login", email); err != nil {
		t.Fatalf("ingot login via email: %v", err)
	}
}
