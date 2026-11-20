package stack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fil-forge/libforge/identity"
	"github.com/fil-forge/ucantone/multikey/ed25519"
	"github.com/fil-forge/ucantone/ucan/delegation"
)

// The delegator refuses to start without the signing service's delegation, and
// extends it to storage nodes, so it has to be addressed to the delegator, be
// over the signing service's own DID, and cover every /pdp/sign/* command.
func TestGenerateProofsWritesTheSigningServiceProof(t *testing.T) {
	tempDir := t.TempDir()
	keysDir := filepath.Join(tempDir, "generated", "keys")
	if err := os.MkdirAll(keysDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"indexer", "etracker", "signing-service"} {
		signer, err := ed25519.Generate()
		if err != nil {
			t.Fatal(err)
		}
		pem, err := identity.EncodeSignerToPEM(signer)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(keysDir, name+".pem"), pem, 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := generateProofs(tempDir, nil, nil, nil); err != nil {
		t.Fatalf("generateProofs: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tempDir, "generated", "proofs", "signing-service-proof.txt"))
	if err != nil {
		t.Fatal(err)
	}
	dlg, err := delegation.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := dlg.Issuer().String(); got != "did:web:signing-service" {
		t.Errorf("issuer = %s", got)
	}
	if got := dlg.Subject().String(); got != "did:web:signing-service" {
		t.Errorf("subject = %s", got)
	}
	if got := dlg.Audience().String(); got != "did:web:delegator" {
		t.Errorf("audience = %s", got)
	}
	if got := dlg.Command().String(); got != "/pdp/sign" {
		t.Errorf("command = %s", got)
	}
	if dlg.Expiration() != nil {
		t.Errorf("expected no expiration, got %d", *dlg.Expiration())
	}
}
