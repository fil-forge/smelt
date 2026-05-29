//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fil-forge/smelt/pkg/clients/guppy"
	"github.com/fil-forge/smelt/pkg/clients/randdir"
	"github.com/fil-forge/smelt/pkg/stack"
)

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
			ctx := t.Context()

			opts := []stack.Option{
				stack.WithPiriNodes(stack.PiriNodeConfig{
					S3:       tt.useS3,
					Postgres: tt.usePostgres,
				}),
			}
			if os.Getenv("SMELT_WORKSPACE") != "" {
				opts = append(opts, stack.WithWorkspaceBinaries())
			}

			s := stack.MustNewStack(t, opts...)
			gup, err := guppy.NewContainerClient(s)
			if err != nil {
				t.Fatal(err)
			}

			const email = "test@example.com"

			// Login: a fresh stack means a real login (not already-logged-in),
			// and the result should carry the resolved account DID.
			login, err := gup.Login(ctx, email)
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			if !login.LoggedIn {
				t.Fatalf("login: LoggedIn=false, result=%+v", login)
			}
			if login.AlreadyLoggedIn {
				t.Fatalf("login: expected a fresh login, got AlreadyLoggedIn=true")
			}
			if !strings.HasPrefix(login.Account, "did:mailto:") {
				t.Fatalf("login: Account=%q, want a did:mailto: DID", login.Account)
			}
			if !strings.Contains(login.Account, "example.com") {
				t.Fatalf("login: Account=%q, want it derived from %q", login.Account, email)
			}
			t.Logf("logged in as %s (claimed %d delegations)", login.Account, login.ClaimedDelegations)

			// whoami: the local agent identity is a did:key.
			who, err := gup.Whoami(ctx)
			if err != nil {
				t.Fatalf("whoami: %v", err)
			}
			if !strings.HasPrefix(who.DID, "did:key:") {
				t.Fatalf("whoami: DID=%q, want a did:key: DID", who.DID)
			}

			// account list should now include the account we logged in as.
			accounts, err := gup.Account().List(ctx)
			if err != nil {
				t.Fatalf("account list: %v", err)
			}
			if !slices.ContainsFunc(accounts, func(a guppy.AccountItem) bool { return a.ID == login.Account }) {
				t.Fatalf("account list: logged-in account %s not present; got %+v", login.Account, accounts)
			}

			// Generate a space; its DID is a did:key.
			gen, err := gup.Space().Generate(ctx)
			if err != nil {
				t.Fatalf("space generate: %v", err)
			}
			spaceDID := gen.DID
			if !strings.HasPrefix(spaceDID, "did:key:") {
				t.Fatalf("space generate: DID=%q, want a did:key: DID", spaceDID)
			}
			t.Logf("created space: %s", spaceDID)

			// space list should now include the space we just generated.
			spaces, err := gup.Space().List(ctx)
			if err != nil {
				t.Fatalf("space list: %v", err)
			}
			if !slices.ContainsFunc(spaces, func(sp guppy.SpaceItem) bool { return sp.ID == spaceDID }) {
				t.Fatalf("space list: generated space %s not present; got %+v", spaceDID, spaces)
			}

			dataPath, err := randdir.NewContainerClient(s).Generate(ctx, "10MB")
			if err != nil {
				t.Fatalf("generate test data: %v", err)
			}

			// Add the source; the result echoes the space/path and assigns an ID.
			add, err := gup.Upload().Source().Add(ctx, spaceDID, dataPath)
			if err != nil {
				t.Fatalf("add source: %v", err)
			}
			if !add.OK {
				t.Fatalf("add source: OK=false, result=%+v", add)
			}
			if add.Space != spaceDID {
				t.Fatalf("add source: Space=%q, want %q", add.Space, spaceDID)
			}
			if add.Path != dataPath {
				t.Fatalf("add source: Path=%q, want %q", add.Path, dataPath)
			}
			if add.SourceID == "" {
				t.Fatalf("add source: empty SourceID, result=%+v", add)
			}

			// source list should report exactly the one source, with the same ID.
			sources, err := gup.Upload().Source().List(ctx, spaceDID)
			if err != nil {
				t.Fatalf("source list: %v", err)
			}
			if len(sources) != 1 {
				t.Fatalf("source list: got %d sources, want 1: %+v", len(sources), sources)
			}
			if sources[0].SourceID != add.SourceID {
				t.Fatalf("source list: SourceID=%q, want %q", sources[0].SourceID, add.SourceID)
			}
			if sources[0].Path != dataPath {
				t.Fatalf("source list: Path=%q, want %q", sources[0].Path, dataPath)
			}

			// Upload the single source: exactly one completed, none failed, and
			// the completed entry must tie back to the source we added.
			up, err := gup.Upload().Run(ctx, spaceDID, guppy.WithReplicas(1))
			if err != nil {
				t.Fatalf("upload: %v", err)
			}
			if len(up.Failed) != 0 {
				t.Fatalf("upload: expected no failures, got %d: %+v", len(up.Failed), up.Failed)
			}
			if len(up.Completed) != 1 {
				t.Fatalf("upload: got %d completed, want 1: %+v", len(up.Completed), up.Completed)
			}
			done := up.Completed[0]
			if !strings.HasPrefix(done.RootCID, "bafy") {
				t.Fatalf("upload: RootCID=%q, want a bafy... CID", done.RootCID)
			}
			if done.SourceID != add.SourceID {
				t.Fatalf("upload: SourceID=%q, want %q (the source we added)", done.SourceID, add.SourceID)
			}
			if done.UploadID == "" {
				t.Fatalf("upload: empty UploadID, entry=%+v", done)
			}
			if done.Attempts < 1 {
				t.Fatalf("upload: Attempts=%d, want >=1", done.Attempts)
			}
			t.Logf("uploaded root %s (upload %s, %d attempt(s))", done.RootCID, done.UploadID, done.Attempts)

			// ls should list the uploaded root.
			uploads, err := gup.Ls(ctx, spaceDID)
			if err != nil {
				t.Fatalf("ls: %v", err)
			}
			if !slices.ContainsFunc(uploads, func(u guppy.UploadListItem) bool { return u.Root == done.RootCID }) {
				t.Fatalf("ls: uploaded root %s not listed; got %+v", done.RootCID, uploads)
			}

			// A successful upload means the space is provisioned to a provider, so
			// space info must report at least one.
			info, err := gup.Space().Info(ctx, spaceDID)
			if err != nil {
				t.Fatalf("space info: %v", err)
			}
			if info.Space != spaceDID {
				t.Fatalf("space info: Space=%q, want %q", info.Space, spaceDID)
			}
			if len(info.Providers) == 0 {
				t.Fatalf("space info: expected at least one provider for a provisioned space, got none")
			}

			// Retrieve the root; randdir produces a directory of files, so the
			// retrieved content is a directory and the result echoes our request.
			dstPath := fmt.Sprintf("/tmp/testdata-download-%d", time.Now().UnixNano())
			ret, err := gup.Retrieve(ctx, spaceDID, done.RootCID, dstPath)
			if err != nil {
				t.Fatalf("retrieve: %v", err)
			}
			if !ret.OK {
				t.Fatalf("retrieve: OK=false, result=%+v", ret)
			}
			if ret.CID != done.RootCID {
				t.Fatalf("retrieve: CID=%q, want %q", ret.CID, done.RootCID)
			}
			if ret.Space != spaceDID {
				t.Fatalf("retrieve: Space=%q, want %q", ret.Space, spaceDID)
			}
			if ret.OutputPath != dstPath {
				t.Fatalf("retrieve: OutputPath=%q, want %q", ret.OutputPath, dstPath)
			}
			if !ret.Directory {
				t.Fatalf("retrieve: Directory=false, expected a directory for randdir output")
			}
		})
	}
}
