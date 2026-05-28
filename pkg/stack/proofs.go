package stack

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fil-forge/libforge/commands/claim"
	"github.com/fil-forge/libforge/commands/space/egress"
	"github.com/fil-forge/libforge/identity"
	"github.com/fil-forge/ucantone/did"
	"github.com/fil-forge/ucantone/principal"
	"github.com/fil-forge/ucantone/principal/signer"
	"github.com/fil-forge/ucantone/ucan"
	"github.com/fil-forge/ucantone/ucan/delegation"

	"github.com/fil-forge/smelt/pkg/manifest"
)

// delegateFn matches the method value of a libforge bound command's Delegate
// (e.g. claim.Cache.Delegate, egress.Track.Delegate).
type delegateFn func(issuer ucan.Signer, audience, subject did.DID, opts ...delegation.Option) (ucan.Delegation, error)

// generateProofs writes the UCAN delegation proofs the delegator consumes at
// startup. It mirrors delegator/cmd/gen.go — the same generator the compose
// stack invokes via generate-proofs.sh — using libforge command primitives and
// ucantone envelope encoding, so the proofs parse with the delegator's
// ucantone delegation.Decode.
//
// As of ucan1.0 there are exactly two: indexer → delegator (/claim/cache) and
// etracker → delegator (/space/egress/track). Per-node piri → upload proofs are
// no longer generated — provider registration stopped consuming proof files
// (see systems/upload/post_start.sh).
func generateProofs(tempDir string, nodes []manifest.ResolvedPiriNode) error {
	keysDir := filepath.Join(tempDir, "generated", "keys")
	proofsDir := filepath.Join(tempDir, "generated", "proofs")
	if err := os.MkdirAll(proofsDir, 0755); err != nil {
		return fmt.Errorf("create proofs dir: %w", err)
	}

	if err := writeDelegation(keysDir, proofsDir,
		"indexer", "did:web:indexer", "did:web:delegator",
		"indexing-service-proof.txt", claim.Cache.Delegate); err != nil {
		return err
	}
	if err := writeDelegation(keysDir, proofsDir,
		"etracker", "did:web:etracker", "did:web:delegator",
		"egress-tracking-proof.txt", egress.Track.Delegate); err != nil {
		return err
	}

	_ = nodes // piri → upload proofs are obsolete as of ucan1.0
	return nil
}

// writeDelegation creates one delegation from issuerKeyName's key (optionally
// wrapped as issuerDidWeb) to audienceDID, using the given libforge command,
// and writes the ucantone-encoded envelope to proofsDir/outputFile.
func writeDelegation(keysDir, proofsDir, issuerKeyName, issuerDidWeb, audienceDID, outputFile string, delegate delegateFn) error {
	pemData, err := os.ReadFile(filepath.Join(keysDir, issuerKeyName+".pem"))
	if err != nil {
		return fmt.Errorf("read issuer key %s: %w", issuerKeyName, err)
	}

	var issuer principal.Signer
	issuer, err = identity.DecodeEd25519SignerFromPEM(pemData)
	if err != nil {
		return fmt.Errorf("decode issuer key %s: %w", issuerKeyName, err)
	}

	if issuerDidWeb != "" {
		web, err := did.Parse(issuerDidWeb)
		if err != nil {
			return fmt.Errorf("parse issuer did:web %s: %w", issuerDidWeb, err)
		}
		issuer, err = signer.Wrap(issuer, web)
		if err != nil {
			return fmt.Errorf("wrap issuer %s: %w", issuerDidWeb, err)
		}
	}

	audience, err := did.Parse(audienceDID)
	if err != nil {
		return fmt.Errorf("parse audience %s: %w", audienceDID, err)
	}

	// Subject is the issuer's own DID — it delegates authority over its own
	// resources to the audience (mirrors delegator/cmd/gen.go).
	dlg, err := delegate(issuer, audience, issuer.DID(), delegation.WithNoExpiration())
	if err != nil {
		return fmt.Errorf("create delegation (%s): %w", outputFile, err)
	}

	out, err := delegation.Encode(dlg)
	if err != nil {
		return fmt.Errorf("encode delegation (%s): %w", outputFile, err)
	}

	if err := os.WriteFile(filepath.Join(proofsDir, outputFile), out, 0644); err != nil {
		return fmt.Errorf("write proof file %s: %w", outputFile, err)
	}
	return nil
}
