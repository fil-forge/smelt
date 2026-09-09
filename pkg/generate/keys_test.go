package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/manifest"
)

func TestGenerateEd25519KeyBackfillsDID(t *testing.T) {
	dir := t.TempDir()
	if err := generateEd25519Key(dir, "piri-0", false); err != nil {
		t.Fatalf("generate: %v", err)
	}
	didPath := filepath.Join(dir, "piri-0.did")
	want, err := os.ReadFile(didPath)
	if err != nil {
		t.Fatalf("read did: %v", err)
	}

	// A keys directory from before DID files existed has the key but no .did.
	if err := os.Remove(didPath); err != nil {
		t.Fatalf("remove did: %v", err)
	}
	if err := generateEd25519Key(dir, "piri-0", false); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	got, err := os.ReadFile(didPath)
	if err != nil {
		t.Fatalf("read backfilled did: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("backfilled DID %q does not match the key's DID %q", got, want)
	}
}

func TestGenerateEd25519KeyKeepsExistingKey(t *testing.T) {
	dir := t.TempDir()
	if err := generateEd25519Key(dir, "piri-0", false); err != nil {
		t.Fatalf("generate: %v", err)
	}
	pemPath := filepath.Join(dir, "piri-0.pem")
	before, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if err := generateEd25519Key(dir, "piri-0", false); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	after, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read key after regenerate: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("existing key was overwritten without force")
	}
}

func TestGenerateKeysWritesPiriNodesFile(t *testing.T) {
	keysDir := t.TempDir()
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0},
		{Name: "piri-1", Index: 1},
	}
	if err := GenerateKeys(keysDir, nodes, false); err != nil {
		t.Fatalf("generate: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(keysDir, PiriNodesFile))
	if err != nil {
		t.Fatalf("read piri nodes file: %v", err)
	}
	var want strings.Builder
	for _, node := range nodes {
		id, err := os.ReadFile(filepath.Join(keysDir, node.Name+".did"))
		if err != nil {
			t.Fatalf("read did for %s: %v", node.Name, err)
		}
		want.Write(id)
	}
	if string(got) != want.String() {
		t.Fatalf("piri nodes file %q does not list the node DIDs %q", got, want.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(string(got)), "\n") {
		if !strings.HasPrefix(line, "did:key:") {
			t.Fatalf("piri nodes file line %q is not a did:key", line)
		}
	}
}
