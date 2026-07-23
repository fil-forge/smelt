package staging

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// generateProofs issues the UCAN delegation proofs the staging stack needs,
// signing them with the identity keys in keysDir and writing them to proofsDir
// (committed to git). Mirrors generated/generate-proofs.sh but uses the staging
// did:web audiences. A proof already on disk is skipped unless one of the keys
// it depends on was freshly generated this run (fresh) — so a re-run against a
// fully-populated 1Password item leaves the committed proofs untouched.
//
// Five proofs, two of which exist only to satisfy the delegator's startup
// validation (it requires an indexing- and egress-service delegation even though
// neither service runs in staging):
//   - indexing-service-proof:  indexer  -> delegator, /claim/cache
//   - egress-tracking-proof:   etracker -> delegator, /egress/track
//   - piri-0-proof:            piri-0   -> sprue,     blob/* + pdp/info
//   - hilt-customer-add-proof: sprue    -> hilt,      /customer/add
//   - hilt-ingot-s3-proof:     hilt     -> ingot,     /s3/request/authorize + /s3/bucket/*
//
// ingotDID is ingot's did:key (derived from ingot.pem) — ingot has no did:web,
// so the hilt->ingot proof is addressed to the key directly.
func generateProofs(ucantool, keysDir, proofsDir, ingotDID string, fresh map[string]bool) ([]string, error) {
	if _, err := exec.LookPath(ucantool); err != nil {
		return nil, fmt.Errorf("%q not found in PATH (install: go install github.com/fil-forge/ucantool@latest): %w", ucantool, err)
	}

	type proof struct {
		out      string
		issuer   string // key file basename (without .pem)
		issuerDW string // issuer did:web
		audience string
		subject  string // empty -> omit --subject
		commands []string
		gzip     bool     // emit base64+gzip container (required wherever the consumer parses a UCAN *container*)
		deps     []string // 1Password key fields whose fresh regeneration invalidates this proof
	}
	proofs := []proof{
		{
			out:      "indexing-service-proof.txt",
			issuer:   "indexer",
			issuerDW: DIDIndexer,
			audience: DIDDelegator,
			subject:  DIDIndexer,
			commands: []string{"/claim/cache"},
			deps:     []string{"indexer-key"},
		},
		{
			out:      "egress-tracking-proof.txt",
			issuer:   "etracker",
			issuerDW: DIDEtracker,
			audience: DIDDelegator,
			subject:  DIDEtracker,
			commands: []string{"/egress/track"},
			deps:     []string{"etracker-key"},
		},
		{
			out:      "piri-0-proof.txt",
			issuer:   "piri-0",
			audience: DIDSprue,
			commands: []string{"/blob/allocate", "/blob/accept", "/blob/replica/allocate", "/pdp/info"},
			gzip:     true,
			deps:     []string{"piri-0-key"},
		},
		// Hilt presents this to sprue when registering tenants as customers.
		// hilt's upload.proofs loader parses a UCAN *container* (hilt
		// pkg/fx/upload.go), hence gzip. The audience is a DID string, so a
		// fresh hilt key does not invalidate it — only the issuer key matters.
		{
			out:      "hilt-customer-add-proof.txt",
			issuer:   "sprue",
			issuerDW: DIDSprue,
			audience: DIDHilt,
			subject:  DIDSprue,
			commands: []string{"/customer/add"},
			gzip:     true,
			deps:     []string{"sprue-key"},
		},
		// Ingot presents these when calling hilt's UCAN RPC API. The audience is
		// ingot's did:key, so this proof depends on BOTH keys.
		{
			out:      "hilt-ingot-s3-proof.txt",
			issuer:   "hilt",
			issuerDW: DIDHilt,
			audience: ingotDID,
			subject:  DIDHilt,
			commands: []string{"/s3/request/authorize", "/s3/bucket/create", "/s3/bucket/delete", "/s3/bucket/info", "/s3/bucket/list"},
			gzip:     true,
			deps:     []string{"hilt-key", "ingot-key"},
		},
	}

	var written []string
	for _, p := range proofs {
		outPath := filepath.Join(proofsDir, p.out)
		if fileExists(outPath) && !anyFresh(fresh, p.deps) {
			continue
		}

		args := []string{
			"delegate",
			"--issuer-private-key-file", filepath.Join(keysDir, p.issuer+".pem"),
			"--audience", p.audience,
		}
		if p.issuerDW != "" {
			args = append(args, "--issuer-did-web", p.issuerDW)
		}
		if p.subject != "" {
			args = append(args, "--subject", p.subject)
		}
		for _, c := range p.commands {
			args = append(args, "--command", c)
		}
		if p.gzip {
			args = append(args, "--container", "base64+gzip")
		}

		out, err := os.Create(outPath)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", p.out, err)
		}
		cmd := exec.Command(ucantool, args...)
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		closeErr := out.Close()
		// A failed (or partially-written) proof must not linger on disk: it would be
		// unusable and risks getting committed. Remove it before returning the error.
		if runErr != nil || closeErr != nil {
			os.Remove(outPath)
			if runErr != nil {
				return nil, fmt.Errorf("ucantool delegate for %s: %w", p.out, runErr)
			}
			return nil, fmt.Errorf("writing %s: %w", p.out, closeErr)
		}
		written = append(written, outPath)
	}
	return written, nil
}

func anyFresh(fresh map[string]bool, deps []string) bool {
	for _, d := range deps {
		if fresh[d] {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
