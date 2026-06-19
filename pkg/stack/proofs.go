package stack

import (
	"fmt"
	"os"
	"path/filepath"

	blobcmds "github.com/fil-forge/libforge/commands/blob"
	replicacmds "github.com/fil-forge/libforge/commands/blob/replica"
	"github.com/fil-forge/libforge/commands/claim"
	pdpcmds "github.com/fil-forge/libforge/commands/pdp"
	"github.com/fil-forge/libforge/commands/space/egress"
	"github.com/fil-forge/libforge/identity"
	"github.com/fil-forge/ucantone/did"
	"github.com/fil-forge/ucantone/principal"
	"github.com/fil-forge/ucantone/principal/signer"
	"github.com/fil-forge/ucantone/ucan"
	"github.com/fil-forge/ucantone/ucan/container"
	"github.com/fil-forge/ucantone/ucan/delegation"

	"github.com/fil-forge/smelt/pkg/manifest"
)

// piriCommands are delegated from each piri-N node to the upload service
// so upload can register the node as a storage provider and invoke /blob/*
// operations on its behalf.
var piriCommands = []ucan.Command{
	blobcmds.Allocate.Command,
	blobcmds.Accept.Command,
	replicacmds.Allocate.Command,
	pdpcmds.Info.Command,
}

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

	for _, node := range nodes {
		if err := writeProofs(
			keysDir,
			proofsDir,
			node.Name,
			"",
			"did:web:upload",
			node.Name+"-proof.txt",
			piriCommands,
		); err != nil {
			return err
		}
	}

	_ = nodes // piri → upload proofs are obsolete as of ucan1.0
	return nil
}

// writeProofs writes a UCAN container of delegations from issuerKeyName's key
// (optionally wrapped as issuerDidWeb) to audienceDID for each passed command.
func writeProofs(keysDir, proofsDir, issuerKeyName, issuerDidWeb, audienceDID, outputFile string, cmds []ucan.Command) error {
	pemData, err := os.ReadFile(filepath.Join(keysDir, issuerKeyName+".pem"))
	if err != nil {
		return fmt.Errorf("reading issuer key %s: %w", issuerKeyName, err)
	}

	var issuer principal.Signer
	issuer, err = identity.DecodeEd25519SignerFromPEM(pemData)
	if err != nil {
		return fmt.Errorf("decoding issuer key %s: %w", issuerKeyName, err)
	}

	if issuerDidWeb != "" {
		web, err := did.Parse(issuerDidWeb)
		if err != nil {
			return fmt.Errorf("parsing issuer did:web %s: %w", issuerDidWeb, err)
		}
		issuer, err = signer.Wrap(issuer, web)
		if err != nil {
			return fmt.Errorf("wrapping issuer %s: %w", issuerDidWeb, err)
		}
	}

	audience, err := did.Parse(audienceDID)
	if err != nil {
		return fmt.Errorf("parsing audience %s: %w", audienceDID, err)
	}

	dlgs := make([]ucan.Delegation, 0, len(cmds))
	for _, cmd := range cmds {
		dlg, err := delegation.Delegate(issuer, audience, issuer.DID(), cmd, delegation.WithNoExpiration())
		if err != nil {
			return fmt.Errorf("creating delegation for command %s: %w", cmd, err)
		}
		dlgs = append(dlgs, dlg)
	}

	out, err := container.Encode(container.Base64Gzip, container.New(container.WithDelegations(dlgs...)))
	if err != nil {
		return fmt.Errorf("encode delegation (%s): %w", outputFile, err)
	}

	if err := os.WriteFile(filepath.Join(proofsDir, outputFile), out, 0644); err != nil {
		return fmt.Errorf("write proof file %s: %w", outputFile, err)
	}
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
