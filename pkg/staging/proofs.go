package staging

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// generateProofs issues the UCAN delegation proofs the staging stack needs,
// signing them with the freshly-generated identity keys in keysDir and writing
// them to proofsDir (committed to git). Mirrors generated/generate-proofs.sh but
// uses the staging did:web audiences.
//
// Three proofs, two of which exist only to satisfy the delegator's startup
// validation (it requires an indexing- and egress-service delegation even though
// neither service runs in staging):
//   - indexing-service-proof: indexer  -> delegator, /claim/cache
//   - egress-tracking-proof:  etracker -> delegator, /egress/track
//   - piri-0-proof:           piri-0   -> upload,    blob/* + pdp/info
func generateProofs(ucantool, keysDir, proofsDir string) ([]string, error) {
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
		gzip     bool // emit base64+gzip container (piri proofs)
	}
	proofs := []proof{
		{
			out:      "indexing-service-proof.txt",
			issuer:   "indexer",
			issuerDW: DIDIndexer,
			audience: DIDDelegator,
			subject:  DIDIndexer,
			commands: []string{"/claim/cache"},
		},
		{
			out:      "egress-tracking-proof.txt",
			issuer:   "etracker",
			issuerDW: DIDEtracker,
			audience: DIDDelegator,
			subject:  DIDEtracker,
			commands: []string{"/egress/track"},
		},
		{
			out:      "piri-0-proof.txt",
			issuer:   "piri-0",
			audience: DIDSprue,
			commands: []string{"/blob/allocate", "/blob/accept", "/blob/replica/allocate", "/pdp/info"},
			gzip:     true,
		},
	}

	var written []string
	for _, p := range proofs {
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

		outPath := filepath.Join(proofsDir, p.out)
		out, err := os.Create(outPath)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", p.out, err)
		}
		cmd := exec.Command(ucantool, args...)
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		out.Close()
		if runErr != nil {
			return nil, fmt.Errorf("ucantool delegate for %s: %w", p.out, runErr)
		}
		written = append(written, outPath)
	}
	return written, nil
}
