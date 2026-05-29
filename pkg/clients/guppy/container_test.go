package guppy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/cliexec"
)

// newTestClient builds a ContainerClient backed by a fake runner so methods can
// be exercised without a live stack.
func newTestClient(run cliexec.RunnerFunc) *ContainerClient {
	return &ContainerClient{runner: run}
}

// recordingRunner captures the args it was called with and returns canned output.
func recordingRunner(stdout, stderr string, err error, gotArgs *[]string) cliexec.RunnerFunc {
	return func(_ context.Context, args ...string) (string, string, error) {
		if gotArgs != nil {
			*gotArgs = args
		}
		return stdout, stderr, err
	}
}

func TestWhoami(t *testing.T) {
	var args []string
	c := newTestClient(recordingRunner(`{"did":"did:key:zABC"}`, "", nil, &args))

	got, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if got.DID != "did:key:zABC" {
		t.Fatalf("DID = %q", got.DID)
	}
	want := []string{"guppy", "--output=json", "whoami"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestSpaceGenerate(t *testing.T) {
	var args []string
	c := newTestClient(recordingRunner(`{"did":"did:key:zSPACE"}`, "", nil, &args))

	got, err := c.Space().Generate(context.Background(), WithSpaceName("demo"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.DID != "did:key:zSPACE" {
		t.Fatalf("DID = %q", got.DID)
	}
	if strings.Join(args, " ") != "guppy --output=json space generate --name demo" {
		t.Fatalf("args = %v", args)
	}
}

func TestSpaceList(t *testing.T) {
	c := newTestClient(recordingRunner(`[{"id":"did:key:z1","names":["a"]},{"id":"did:key:z2"}]`, "", nil, nil))
	got, err := c.Space().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Names[0] != "a" || got[1].ID != "did:key:z2" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestSourceAdd(t *testing.T) {
	var args []string
	c := newTestClient(recordingRunner(`{"ok":true,"space":"did:key:zS","source_id":"abc","name":"/data","path":"/data"}`, "", nil, &args))
	got, err := c.Upload().Source().Add(context.Background(), "did:key:zS", "/data")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !got.OK || got.SourceID != "abc" {
		t.Fatalf("decoded = %+v", got)
	}
	if strings.Join(args, " ") != "guppy --output=json upload source add did:key:zS /data" {
		t.Fatalf("args = %v", args)
	}
}

func TestUploadRun(t *testing.T) {
	var args []string
	out := `{"completed":[{"root_cid":"bafyROOT","upload_id":"u1","source_id":"s1","attempts":1}],"failed":[]}`
	c := newTestClient(recordingRunner(out, "", nil, &args))

	got, err := c.Upload().Run(context.Background(), "did:key:zS", WithReplicas(2))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got.Completed) != 1 || got.Completed[0].RootCID != "bafyROOT" {
		t.Fatalf("decoded = %+v", got)
	}
	if len(got.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", got.Failed)
	}
	if strings.Join(args, " ") != "guppy --output=json upload --replicas 2 did:key:zS" {
		t.Fatalf("args = %v", args)
	}
}

func TestLsWithShards(t *testing.T) {
	var args []string
	c := newTestClient(recordingRunner(`[{"root":"bafyR","shards":["bafyS1","bafyS2"]}]`, "", nil, &args))
	got, err := c.Ls(context.Background(), "did:key:zS", WithShards())
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	if len(got) != 1 || len(got[0].Shards) != 2 {
		t.Fatalf("decoded = %+v", got)
	}
	if strings.Join(args, " ") != "guppy --output=json ls --shards did:key:zS" {
		t.Fatalf("args = %v", args)
	}
}

func TestBlobLs(t *testing.T) {
	c := newTestClient(recordingRunner(`[{"digest":"zQmA","size":1024}]`, "", nil, nil))
	got, err := c.Blob().Ls(context.Background(), "did:key:zS")
	if err != nil {
		t.Fatalf("Blob Ls: %v", err)
	}
	if len(got) != 1 || got[0].Size != 1024 {
		t.Fatalf("decoded = %+v", got)
	}
}

// stubValidator records whether validation ran and whether it observed
// cancellation due to an already-logged-in result.
type stubValidator struct {
	ran          bool
	canceledWith error
}

func (v *stubValidator) ValidateEmailLogin(ctx context.Context, email string) error {
	v.ran = true
	select {
	case <-ctx.Done():
		v.canceledWith = context.Cause(ctx)
		return ctx.Err()
	case <-time.After(50 * time.Millisecond):
		// Simulate the email link being clicked successfully.
		return nil
	}
}

func TestLoginSuccess(t *testing.T) {
	v := &stubValidator{}
	c := &ContainerClient{
		runner:    recordingRunner(`{"account":"did:mailto:test","logged_in":true,"already_logged_in":false,"claimed_delegations":2}`, "", nil, nil),
		validator: v,
	}

	got, err := c.Login(context.Background(), "test@example.com")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !got.LoggedIn || got.ClaimedDelegations != 2 {
		t.Fatalf("result = %+v", got)
	}
	if !v.ran {
		t.Fatal("validator should run on a normal login")
	}
}

func TestLoginAlreadyLoggedIn(t *testing.T) {
	v := &stubValidator{}
	// The login command returns quickly with already_logged_in=true and no email
	// is sent; the validator must be canceled rather than blocking.
	c := &ContainerClient{
		runner: cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
			return `{"account":"did:mailto:test","logged_in":true,"already_logged_in":true,"claimed_delegations":0}`, "", nil
		}),
		validator: v,
	}

	got, err := c.Login(context.Background(), "test@example.com")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !got.AlreadyLoggedIn {
		t.Fatalf("expected already_logged_in, got %+v", got)
	}
	if v.ran && v.canceledWith != nil && !errors.Is(v.canceledWith, errAlreadyLoggedIn) {
		t.Fatalf("validator canceled with unexpected cause: %v", v.canceledWith)
	}
}

func TestLoginExecError(t *testing.T) {
	v := &stubValidator{}
	c := &ContainerClient{
		runner:    recordingRunner("", "boom", errors.New("exit 1"), nil),
		validator: v,
	}
	if _, err := c.Login(context.Background(), "test@example.com"); err == nil {
		t.Fatal("expected login error to propagate")
	}
}
