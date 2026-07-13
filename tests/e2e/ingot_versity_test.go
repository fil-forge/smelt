//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	ingottest "github.com/fil-forge/ingot/testing"
	"github.com/versity/versitygw/tests/integration"
	"golang.org/x/sync/errgroup"

	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/stack"
)

// TestIngotVersityConformance runs ingot's versitygw S3 integration suite
// against a forge-mode ingot deployed in the smelt stack, and asserts it
// matches the in-process (in-memory) ingot baseline — i.e. running over
// the live Forge network (Postgres registry + blob/add shipping to sprue)
// is S3-conformant to the same degree as the reference in-memory build.
//
// Runs against the published ingot image; set SMELT_WORKSPACE=1 (with ingot
// in the parent go.work) to rebuild ingot from local source instead:
//
//	go test -tags e2e ./tests/e2e -run TestIngotVersityConformance -v -timeout 900s
func TestIngotVersityConformance(t *testing.T) {
	ctx := t.Context()
	const email = "test@example.com"

	// The same upstream group universe as ingot's own smoke_test.go partition
	// (Smoke plus CopyObject and DeleteObjects), plus Multipart so the gate also
	// covers the multipart upload path over real forge (part ship + accept at
	// Complete). Out-of-scope groups (ACL/tagging cases, UploadPartCopy,
	// ListParts, ListMultipartUploads) fail in the in-process baseline too, so
	// the per-case comparison tolerates them; only forge-vs-baseline divergence
	// fails the gate.
	conformanceSuite := append(append(ingottest.Suite{}, ingottest.Smoke...),
		integration.TestCopyObject,
		integration.TestDeleteObjects,
	)
	conformanceSuite = append(conformanceSuite, ingottest.Multipart...)

	// Cold-boot a fresh stack: this generates ingot.pem (ingot is in
	// nonPiriServiceKeys) and brings up ingot + ingot-postgres alongside
	// the Forge services. SMELT_WORKSPACE=1 rebuilds ingot from local
	// source; otherwise the published ingot image is used.
	opts := []stack.Option{stack.WithPiriNodes(stack.PiriNodeConfig{})}
	if os.Getenv("SMELT_WORKSPACE") != "" {
		opts = append(opts, stack.WithWorkspaceBinaries())
	}
	s := stack.MustNewStack(t, opts...)

	// --- 1. In-process baseline: the same suite against a clean in-memory
	// ingot, so the assertion is self-calibrating rather than a magic number.
	// The baseline build comes from the ingot module pinned in go.mod; keep that
	// pin roughly in step with the published image the stack runs (both track
	// ingot main), or divergence found here may be version skew, not a bug.
	h, err := ingottest.StartHarness(ctx)
	if err != nil {
		t.Fatalf("start in-process harness: %v", err)
	}
	t.Cleanup(func() { _ = h.Stop(context.Background()) })

	baseline := ingottest.Run(ctx, ingottest.Config{
		Endpoint:  h.Endpoint,
		AccessKey: h.AccessKey,
		SecretKey: h.SecretKey,
		Region:    h.Region,
	}, conformanceSuite)
	t.Logf("in-process (in-memory) baseline: ran=%d passed=%d failed=%d",
		baseline.Ran, baseline.Passed, baseline.Failed)
	if baseline.Passed == 0 {
		t.Fatalf("in-process baseline produced 0 passes — harness/suite broken")
	}

	// --- 2. Wait for the smelt-deployed ingot to serve.
	ingotEndpoint := s.IngotEndpoint()
	waitHTTPOK(t, ingotEndpoint+"/health", 2*time.Minute)
	t.Logf("smelt ingot S3 endpoint: %s", ingotEndpoint)

	// --- 3. Provision ingot's space on sprue so blob/add succeeds.
	// Drive the guppy CLI directly (not the smelt ContainerClient, which
	// prefixes --output=json — unsupported by some published guppy images)
	// while smelt's email-clicker confirms the access-request email.
	guppyLoginViaEmail(t, ctx, s, email)

	ingotSpace := ingotSpaceDID(t, ctx, s)
	t.Logf("provisioning ingot space %s to %s", ingotSpace, email)

	provStdout, provStderr, err := s.Exec(ctx, "guppy", "guppy", "space", "provision", ingotSpace, email)
	if err != nil {
		t.Fatalf("guppy space provision: %v (stdout=%s stderr=%s)", err, provStdout, provStderr)
	}
	if !strings.Contains(provStdout+provStderr, "provisioned") {
		t.Fatalf("guppy space provision: unexpected output stdout=%q stderr=%q", provStdout, provStderr)
	}

	// --- 4. Run the SAME versity suite against the forge-mode ingot.
	res := ingottest.Run(ctx, ingottest.Config{
		Endpoint:  ingotEndpoint,
		AccessKey: "ingot",
		SecretKey: "ingotsecret",
		Region:    "us-east-1",
	}, conformanceSuite)
	t.Logf("smelt ingot (forge mode) over %s: ran=%d passed=%d failed=%d",
		ingotEndpoint, res.Ran, res.Passed, res.Failed)

	// --- 5. Assert parity per-case, both directions — the forge-mode listener
	// must fail exactly the cases the in-process baseline fails (the baseline
	// encodes ingot's curated expected-pass/expected-fail partition, which
	// ingot's own smoke_test.go CI enforces):
	//
	//   - a case the baseline passes but forge fails is a forge-specific
	//     regression (a blob-upload or shipping bug) and fails the gate by name;
	//   - a case the baseline fails but forge passes is a behavioral divergence
	//     between the two builds and fails the gate too — mirroring
	//     smoke_test.go's "unexpectedly passed; promote it" ratchet.
	//
	// Both builds share the same out-of-scope failures (ACL, tagging,
	// object-lock, bucket-policy), so comparing failure SETS — rather than a
	// count — keeps those self-calibrated out without a magic number.
	if res.Ran != baseline.Ran {
		t.Errorf("case count mismatch: forge-mode ran=%d, in-process baseline ran=%d", res.Ran, baseline.Ran)
	}
	// Guard against a silently-broken per-case capture turning the set
	// comparison below vacuous (empty vs empty would pass while hiding every
	// regression).
	if (baseline.Failed > 0 && len(baseline.FailedCases) == 0) || (res.Failed > 0 && len(res.FailedCases) == 0) {
		t.Fatalf("per-case capture returned no names (baseline failed=%d names=%d, forge failed=%d names=%d) — gate cannot run",
			baseline.Failed, len(baseline.FailedCases), res.Failed, len(res.FailedCases))
	}
	// timingSensitive names upstream cases whose outcome depends on host load
	// rather than S3 semantics, so baseline-vs-forge divergence on them is
	// noise, not signal. CompleteMultipartUpload_racey_success races ten
	// concurrent 25 MiB multipart uploads of one key under a 30s client
	// deadline — on a host simultaneously running the smelt stack it can time
	// out on either side of the comparison. Divergence on these is logged,
	// never fatal (mirrors ingot's own special-casing of the order-sensitive
	// ListBuckets_truncated in smoke_test.go).
	timingSensitive := map[string]bool{
		"CompleteMultipartUpload_racey_success": true,
	}
	baselineFailed := make(map[string]bool, len(baseline.FailedCases))
	for _, name := range baseline.FailedCases {
		baselineFailed[name] = true
	}
	forgeFailed := make(map[string]bool, len(res.FailedCases))
	for _, name := range res.FailedCases {
		forgeFailed[name] = true
	}
	var regressions []string
	for _, name := range res.FailedCases {
		if timingSensitive[name] {
			if !baselineFailed[name] {
				t.Logf("timing-sensitive case %s failed on forge but passed in-process (not gated)", name)
			}
			continue
		}
		if !baselineFailed[name] {
			regressions = append(regressions, name)
		}
	}
	if len(regressions) > 0 {
		sort.Strings(regressions)
		t.Errorf("forge-mode ingot has %d forge-specific failure(s) the in-process baseline does not have: %v",
			len(regressions), regressions)
	}
	var divergentPasses []string
	for _, name := range baseline.FailedCases {
		if timingSensitive[name] {
			if !forgeFailed[name] {
				t.Logf("timing-sensitive case %s passed on forge but failed in-process (not gated)", name)
			}
			continue
		}
		if !forgeFailed[name] {
			divergentPasses = append(divergentPasses, name)
		}
	}
	if len(divergentPasses) > 0 {
		sort.Strings(divergentPasses)
		t.Errorf("forge-mode ingot passes %d case(s) the in-process baseline fails: %v — "+
			"the two builds have diverged; if the forge behavior is the correct one, fix the "+
			"in-process build (and promote the case in ingot's smoke_test.go) rather than relaxing this gate",
			len(divergentPasses), divergentPasses)
	}
	if res.Passed == 0 {
		t.Fatalf("forge-mode ingot passed 0 versity cases")
	}
}

var spaceDIDRe = regexp.MustCompile(`"space_did":"(did:key:[^"]+)"`)

// ingotSpaceDID extracts ingot's space DID from its startup log line
// (`ingot space loaded` ... "space_did":"did:key:..."). Reading the log
// is more robust than capturing `ingot space ls` stdout through the
// testcontainers exec multiplexer, which returns it empty.
func ingotSpaceDID(t *testing.T, ctx context.Context, s *stack.Stack) string {
	t.Helper()
	logs, err := s.Logs(ctx, "ingot")
	if err != nil {
		t.Fatalf("ingot logs: %v", err)
	}
	m := spaceDIDRe.FindStringSubmatch(logs)
	if m == nil {
		t.Fatalf("space_did not found in ingot logs")
	}
	return m[1]
}

// guppyLoginViaEmail logs the guppy agent in as email by racing the
// (blocking) `guppy login` CLI command against smelt's SMTP4Dev
// email-clicker, which fetches the access-request email and POSTs its
// in-network validation link from inside the guppy container.
func guppyLoginViaEmail(t *testing.T, ctx context.Context, s *stack.Stack, email string) {
	t.Helper()
	validator, err := guppy.NewSMTP4DevLoginValidator(
		s.EmailEndpoint(),
		guppy.WithSMTP4DevLoginValidatorClicker(&guppy.ExecDoer{Stack: s, Service: "guppy"}),
	)
	if err != nil {
		t.Fatalf("email validator: %v", err)
	}

	lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	g, gctx := errgroup.WithContext(lctx)
	g.Go(func() error {
		if _, stderr, err := s.Exec(gctx, "guppy", "guppy", "login", email); err != nil {
			return fmt.Errorf("guppy login command: %v (stderr=%s)", err, stderr)
		}
		return nil
	})
	g.Go(func() error {
		return validator.ValidateEmailLogin(gctx, email)
	})
	if err := g.Wait(); err != nil {
		t.Fatalf("guppy login via email: %v", err)
	}
}

// waitHTTPOK polls url until it returns 2xx or the timeout elapses.
func waitHTTPOK(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s to return 2xx", url)
}
