// Package staging implements the one-time secret-generation ceremony for the
// Forge staging deployment. Unlike the local dev stack (pkg/generate), which
// regenerates throwaway keys on every `make up` and derives EVM wallets from
// Anvil's public deterministic accounts, staging keys are generated ONCE, stored
// in 1Password, and never rotated per-deploy.
//
// What this produces:
//   - Ed25519 service identity keys (PEM) for every service identity.
//   - Real, random secp256k1 EVM wallets for the chain-transacting roles
//     (signing-service payer, delegator transactor, piri owner). Their addresses
//     are printed so the operator can fund them from a Calibnet faucet.
//   - Random connection secrets (Postgres password, S3 access/secret keys).
//   - UCAN delegation proofs (committed to git) signed with the freshly-generated
//     identity keys, using the staging did:web identities.
//
// Private key material is written only to a temp dir, copied into 1Password, then
// wiped. Only public EVM addresses are ever printed. Nothing secret is logged or
// committed.
package staging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fil-forge/smelt/pkg/generate"
)

// Staging did:web identities. The upload service is published as "sprue".
const (
	DIDSprue          = "did:web:sprue.staging.fil.one"
	DIDSigningService = "did:web:signing-service.staging.fil.one"
	DIDDelegator      = "did:web:delegator.staging.fil.one"
	DIDIndexer        = "did:web:indexer.staging.fil.one"  // not deployed; identity used only to sign the proof the delegator requires
	DIDEtracker       = "did:web:etracker.staging.fil.one" // not deployed; identity used only to sign the proof the delegator requires
)

// serviceIdentityKeys are the Ed25519 identities generated for staging. indexer
// and etracker are NOT run as containers, but the delegator validates an
// indexing- and egress-service delegation at startup, so we still need their
// keys to issue those proofs.
var serviceIdentityKeys = []string{
	"sprue",
	"signing-service",
	"delegator",
	"indexer",
	"etracker",
	"piri-0",
	"guppy",
}

// Options controls the keygen ceremony.
type Options struct {
	ProjectDir string // repo root; proofs are written under environments/staging/proofs
	OPVault    string // 1Password vault (e.g. "Fil One")
	OPItem     string // 1Password item title (e.g. "FilOne Forge Staging")
	Store      bool   // store generated secrets into 1Password via the op CLI
	Proofs     bool   // generate UCAN delegation proofs (requires ucantool)
	Ucantool   string // ucantool binary name/path
}

// Result reports the non-secret outcome of the ceremony.
type Result struct {
	// FundAddresses maps a role to the 0x EVM address that must be funded.
	FundAddresses map[string]string
	ProofsWritten []string
	OPFields      []string // field names written to 1Password (names only, never values)
	// WalletsEnvPath is the wallets.env file the wallet addresses were written
	// into (empty if not written).
	WalletsEnvPath string
}

// Keygen runs the one-time staging key/secret/proof generation. It is NOT
// idempotent against 1Password by design — re-running mints fresh keys and would
// overwrite the stored item, which would orphan already-funded wallets. Callers
// must treat it as a deliberate one-shot.
func Keygen(opts Options) (*Result, error) {
	if opts.Ucantool == "" {
		opts.Ucantool = "ucantool"
	}
	proofsDir := filepath.Join(opts.ProjectDir, "environments", "staging", "proofs")
	if err := os.MkdirAll(proofsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create proofs dir: %w", err)
	}

	// All private material lands in a temp dir we wipe at the end. It is never
	// written under the repo, so nothing secret can be accidentally committed.
	tmp, err := os.MkdirTemp("", "smelt-staging-keygen-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	res := &Result{FundAddresses: map[string]string{}}

	// 1. Ed25519 service identities (reuses the exact PKCS8 PEM format the
	//    services expect — see pkg/generate.GenerateEd25519Key).
	for _, name := range serviceIdentityKeys {
		if err := generate.GenerateEd25519Key(tmp, name, true); err != nil {
			return nil, fmt.Errorf("generate %s identity: %w", name, err)
		}
	}

	// 2. Real random EVM wallets for the chain-transacting roles. Each role gets
	//    its own wallet so the services never collide on transaction nonces.
	walletFiles := map[string]string{} // op field name -> temp file path
	wallets := []struct {
		role     string // human label for funding
		opField  string // 1Password field name
		file     string // temp file basename
		addrVar  string // wallets.env variable holding the public address
		contents func(*EVMWallet) string
	}{
		{"signing-service payer", "payer-key", "payer-key.hex", "PAYER_ADDRESS", (*EVMWallet).RawHex},
		{"delegator transactor", "delegator-transactor-key", "delegator-transactor-key.hex", "DELEGATOR_TRANSACTOR_ADDRESS", (*EVMWallet).Hex0x},
		{"piri-0 owner", "piri-0-wallet", "piri-0-wallet.hex", "PIRI_0_OWNER_ADDRESS", (*EVMWallet).PiriWalletHex},
	}
	// Wallet addresses are public; record them in the committed wallets.env so they
	// are easy to find for periodic balance top-ups. (Private keys go to 1Password.)
	walletsEnv := filepath.Join(opts.ProjectDir, "environments", "staging", "wallets.env")
	for _, w := range wallets {
		wallet, err := GenerateEVMWallet()
		if err != nil {
			return nil, fmt.Errorf("generate %s wallet: %w", w.role, err)
		}
		path := filepath.Join(tmp, w.file)
		if err := os.WriteFile(path, []byte(w.contents(wallet)), 0o600); err != nil {
			return nil, fmt.Errorf("write %s wallet: %w", w.role, err)
		}
		walletFiles[w.opField] = path
		res.FundAddresses[w.role] = wallet.Address
		if err := upsertEnvVar(walletsEnv, w.addrVar, wallet.Address); err != nil {
			return nil, fmt.Errorf("write %s to %s: %w", w.addrVar, walletsEnv, err)
		}
	}
	res.WalletsEnvPath = walletsEnv

	// 3. Connection secrets for the dependency containers we run (Postgres, MinIO).
	connSecrets := map[string]string{
		"sprue-postgres-password": "",
		"minio-access-key":        "",
		"minio-secret-key":        "",
	}
	connFiles := map[string]string{}
	for field := range connSecrets {
		secret, err := randomHex(24)
		if err != nil {
			return nil, fmt.Errorf("generate %s: %w", field, err)
		}
		path := filepath.Join(tmp, field)
		if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", field, err)
		}
		connFiles[field] = path
	}

	// 4. UCAN delegation proofs (committed to git), signed with the staging
	//    identities and addressed to the staging did:web audiences.
	if opts.Proofs {
		written, err := generateProofs(opts.Ucantool, tmp, proofsDir)
		if err != nil {
			return nil, fmt.Errorf("generate proofs: %w", err)
		}
		res.ProofsWritten = written
	}

	// 5. Store everything secret in the single 1Password item. Identity PEMs are
	//    stored by file; wallets and connection secrets likewise — all via @file
	//    refs so multi-line PEMs and special characters survive intact.
	if opts.Store {
		fields := map[string]string{}
		for _, name := range serviceIdentityKeys {
			fields[name+"-key"] = filepath.Join(tmp, name+".pem")
		}
		for field, path := range walletFiles {
			fields[field] = path
		}
		for field, path := range connFiles {
			fields[field] = path
		}
		names, err := storeInOnePassword(opts.OPVault, opts.OPItem, fields)
		if err != nil {
			return nil, fmt.Errorf("store secrets in 1Password: %w", err)
		}
		res.OPFields = names
	}

	return res, nil
}

// upsertEnvVar sets KEY=value in a dotenv-style file: it replaces an existing
// `KEY=...` line in place (preserving everything else) or appends one. Creates
// the file if it does not exist.
func upsertEnvVar(path, key, value string) error {
	prefix := key + "="
	line := prefix + value

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(path, []byte(line+"\n"), 0o644)
		}
		return err
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			lines[i] = line
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, line)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// randomHex returns n random bytes hex-encoded (2n chars).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
