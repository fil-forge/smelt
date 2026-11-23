package smelt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubUcantool stands in for ucantool: it appends its arguments to
// $STUB_LOG, one invocation per line, and prints a proof.
const stubUcantool = `#!/bin/sh
echo "$*" >> "$STUB_LOG"
echo proof
`

// proofsRun is a copy of generated/generate-proofs.sh in a temporary
// directory, with the keys a one-node stack has and a stub ucantool.
type proofsRun struct {
	t   *testing.T
	dir string
	log string
}

func newProofsRun(t *testing.T) *proofsRun {
	t.Helper()
	dir := t.TempDir()
	script, err := os.ReadFile(filepath.Join("generated", "generate-proofs.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("generate-proofs.sh", string(script), 0o755)
	write("ucantool", stubUcantool, 0o755)
	for _, key := range []string{"indexer", "delegator", "etracker", "upload", "hilt", "piri-0"} {
		write(filepath.Join("keys", key+".pem"), "key", 0o600)
	}
	write(filepath.Join("keys", "networks.tsv"), "hilt\tingot\tsmelt-1\n", 0o644)
	write(filepath.Join("keys", "piri-0.did"), "did:web:piri-0%3A3000\n", 0o644)
	return &proofsRun{t: t, dir: dir, log: filepath.Join(dir, "ucantool.log")}
}

// run executes the script and returns the ucantool invocations it made for
// piri-0's key.
func (r *proofsRun) run(args ...string) []string {
	r.t.Helper()
	_ = os.Remove(r.log)
	cmd := exec.Command(filepath.Join(r.dir, "generate-proofs.sh"), args...)
	cmd.Env = append(os.Environ(),
		"UCANTOOL="+filepath.Join(r.dir, "ucantool"),
		"STUB_LOG="+r.log,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("generate-proofs.sh: %v\n%s", err, out)
	}
	log, err := os.ReadFile(r.log)
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(string(log), "\n") {
		if strings.Contains(line, "piri-0.pem") {
			calls = append(calls, line)
		}
	}
	return calls
}

// A node is registered under the did:web of its public URL, so its
// delegation to the upload service has to be issued as that DID.
func TestGenerateProofsIssuesANodeProofAsItsDIDWeb(t *testing.T) {
	r := newProofsRun(t)

	calls := r.run()
	if len(calls) != 1 {
		t.Fatalf("got %d delegations for piri-0, want 1: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "--issuer-did-web did:web:piri-0%3A3000") {
		t.Errorf("piri-0's delegation is not issued as its did:web: %s", calls[0])
	}
	if !strings.Contains(calls[0], "--audience did:web:upload") {
		t.Errorf("piri-0's delegation is not addressed to the upload service: %s", calls[0])
	}

	if calls := r.run(); len(calls) != 0 {
		t.Errorf("a second run reissued piri-0's proof: %v", calls)
	}
}

// A proofs directory from before nodes were did:webs holds a proof issued as
// the key's did:key, with no record of its issuer. It is replaced, where a
// proof that merely exists would be skipped.
func TestGenerateProofsReplacesANodeProofIssuedAsAnotherDID(t *testing.T) {
	r := newProofsRun(t)
	proofs := filepath.Join(r.dir, "proofs")
	if err := os.MkdirAll(proofs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proofs, "piri-0-proof.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if calls := r.run(); len(calls) != 1 {
		t.Fatalf("got %d delegations for piri-0, want the stale proof replaced: %v", len(calls), calls)
	}

	// A node that moves to another URL is another DID.
	if err := os.WriteFile(filepath.Join(r.dir, "keys", "piri-0.did"), []byte("did:web:piri-0%3A4000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := r.run()
	if len(calls) != 1 || !strings.Contains(calls[0], "--issuer-did-web did:web:piri-0%3A4000") {
		t.Errorf("want piri-0's proof reissued as its new DID, got %v", calls)
	}
}
