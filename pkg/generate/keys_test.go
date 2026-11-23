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
	hilts, ingots := manifest.DefaultNetworks()
	if err := GenerateKeys(keysDir, nodes, hilts, ingots, false); err != nil {
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
	if want := "did:web:piri-0%3A3000\ndid:web:piri-1%3A3000\n"; string(got) != want {
		t.Fatalf("piri nodes file %q does not list each node's did:web, want %q", got, want)
	}
}

// A keys directory written when a node's .did held its key's did:key is
// corrected by a plain generate, without replacing the key.
func TestGenerateKeysReplacesANodesKeyDID(t *testing.T) {
	keysDir := t.TempDir()
	node := manifest.ResolvedPiriNode{Name: "piri-0", Index: 0}
	if err := generateEd25519Key(keysDir, node.Name, false); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemPath := filepath.Join(keysDir, "piri-0.pem")
	key, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	if err := GenerateKeys(keysDir, []manifest.ResolvedPiriNode{node}, nil, nil, false); err != nil {
		t.Fatalf("generate: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(keysDir, "piri-0.did"))
	if err != nil {
		t.Fatalf("read did: %v", err)
	}
	if strings.TrimSpace(string(got)) != node.DID() {
		t.Fatalf("node DID file holds %q, want %q", got, node.DID())
	}
	after, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read key after generate: %v", err)
	}
	if string(after) != string(key) {
		t.Fatal("existing key was replaced")
	}
}
