package generate

import (
	"os"
	"path/filepath"
	"testing"
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
		t.Fatalf("did not backfilled: %v", err)
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
	before, _ := os.ReadFile(pemPath)
	if err := generateEd25519Key(dir, "piri-0", false); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	after, _ := os.ReadFile(pemPath)
	if string(before) != string(after) {
		t.Fatal("existing key was overwritten without force")
	}
}
