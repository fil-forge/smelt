// Package staging implements the secret-generation ceremony for the Forge
// staging deployment. Unlike the local dev stack (pkg/generate), which
// regenerates throwaway keys on every `make up` and derives EVM wallets from
// Anvil's public deterministic accounts, staging secrets are long-lived: they
// are stored in 1Password and never rotated per-deploy.
//
// The ceremony has "ensure" semantics and is safe to re-run: every secret the
// 1Password item already holds is reused byte-for-byte (funded wallets and
// registered DIDs survive), and only missing fields are generated and added.
// To rotate a specific secret, delete its field from the 1Password item (and
// any proof files signed with it) and re-run.
//
// What this produces:
//   - Ed25519 service identity keys (PEM) for every service identity.
//   - Real, random secp256k1 EVM wallets for the chain-transacting roles
//     (signing-service payer, delegator transactor, piri owner). Their addresses
//     are printed so the operator can fund them from a Calibnet faucet.
//   - Random connection secrets (Postgres passwords, S3 access/secret keys,
//     hilt partner key, vault token, ingot root S3 credentials).
//   - UCAN delegation proofs (committed to git) signed with the identity keys,
//     using the staging did:web identities. A proof is re-issued only when
//     missing or when a key it depends on was freshly generated.
//
// Private key material is written only to a temp dir, copied into 1Password,
// then wiped. Only public EVM addresses are ever printed. Nothing secret is
// logged or committed.
package staging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fil-forge/libforge/identity"
	"github.com/fil-forge/smelt/pkg/generate"
	"github.com/fil-forge/ucantone/multikey"
)

// Staging did:web identities. The upload service is published as "sprue".
const (
	DIDSprue          = "did:web:sprue.staging.fil.one"
	DIDSigningService = "did:web:signing-service.staging.fil.one"
	DIDDelegator      = "did:web:delegator.staging.fil.one"
	DIDIndexer        = "did:web:indexer.staging.fil.one"  // not deployed; identity used only to sign the proof the delegator requires
	DIDEtracker       = "did:web:etracker.staging.fil.one" // not deployed; identity used only to sign the proof the delegator requires
	DIDHilt           = "did:web:hilt.staging.fil.one"
	// ingot has no did:web — it acts under its did:key (derived from ingot.pem).
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
	"hilt",
	"ingot",
}

// connSecretFields are the random connection secrets for the dependency
// containers and service-to-service auth. Each becomes one CONCEALED 1Password
// field holding a randomHex value (hex-only by construction — several land
// inside JSON env values and Postgres DSNs, where quoting is not an option).
var connSecretFields = []string{
	// core bundle
	"core-postgres-admin-password",
	"sprue-postgres-password",
	"hilt-postgres-password",
	"plc-postgres-password",
	"hilt-partner-key",
	"hilt-vault-token",
	"minio-access-key",
	"minio-secret-key",
	// piri bundle
	"piri-postgres-admin-password",
	"piri-0-postgres-password",
	"ingot-postgres-password",
	"ingot-root-access-key",
	"ingot-root-secret-key",
}

// Options controls the keygen ceremony.
type Options struct {
	ProjectDir string // repo root; proofs are written under environments/staging/proofs
	OPVault    string // 1Password vault (e.g. "Fil One")
	OPItem     string // 1Password item title (e.g. "FilOne Forge Staging")
	Store      bool   // reuse/store secrets in 1Password via the op CLI
	Proofs     bool   // generate UCAN delegation proofs (requires ucantool)
	Ucantool   string // ucantool binary name/path
}

// Result reports the non-secret outcome of the ceremony.
type Result struct {
	// FundAddresses maps a role to the 0x EVM address that must be funded.
	FundAddresses map[string]string
	ProofsWritten []string
	OPFields      []string // field names written to 1Password (names only, never values)
	// ReusedFields / GeneratedFields split the field names by whether the value
	// was read back from the existing 1Password item or freshly generated this
	// run. An all-reused run means nothing was rotated.
	ReusedFields    []string
	GeneratedFields []string
	// WalletsEnvPath is the wallets.env file the wallet addresses were written
	// into (empty if not written).
	WalletsEnvPath string
}

// Keygen ensures the staging keys, secrets, and proofs exist. It is idempotent
// against 1Password: fields the item already holds are reused unchanged (so
// funded wallets, registered DIDs, and shipped keys survive a re-run), and only
// missing fields are generated and added. With Store disabled it always
// generates fresh values and stores nothing — useful only for dry runs.
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

	// 0. Read back whatever the 1Password item already holds. This must happen
	//    before any generation: minting fresh keys while 1Password holds
	//    different ones would silently rotate the deployed identity set. The
	//    read fails loudly if the op CLI is missing or unauthenticated.
	existing := map[string]string{}
	if opts.Store {
		existing, err = readOnePasswordFields(opts.OPVault, opts.OPItem)
		if err != nil {
			return nil, fmt.Errorf("read existing 1Password item: %w", err)
		}
	}
	fresh := map[string]bool{}
	// reuse writes an existing field value to its temp file; it returns false
	// when the field is not in the item yet (caller generates it instead).
	reuse := func(field, file string, mode os.FileMode) (bool, error) {
		value, ok := existing[field]
		if !ok {
			fresh[field] = true
			res.GeneratedFields = append(res.GeneratedFields, field)
			return false, nil
		}
		if err := os.WriteFile(filepath.Join(tmp, file), []byte(value), mode); err != nil {
			return false, fmt.Errorf("write reused %s: %w", field, err)
		}
		res.ReusedFields = append(res.ReusedFields, field)
		return true, nil
	}

	// 1. Ed25519 service identities (reuses the exact PKCS8 PEM format the
	//    services expect — see pkg/generate.GenerateEd25519Key).
	for _, name := range serviceIdentityKeys {
		reused, err := reuse(name+"-key", name+".pem", 0o600)
		if err != nil {
			return nil, err
		}
		if !reused {
			if err := generate.GenerateEd25519Key(tmp, name, true); err != nil {
				return nil, fmt.Errorf("generate %s identity: %w", name, err)
			}
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
		parse    func(string) (*EVMWallet, error)
	}{
		{"signing-service payer", "payer-key", "payer-key.hex", "PAYER_ADDRESS", (*EVMWallet).RawHex, ParseEVMWalletRawHex},
		{"delegator transactor", "delegator-transactor-key", "delegator-transactor-key.hex", "DELEGATOR_TRANSACTOR_ADDRESS", (*EVMWallet).Hex0x, ParseEVMWalletHex0x},
		{"piri-0 owner", "piri-0-wallet", "piri-0-wallet.hex", "PIRI_0_OWNER_ADDRESS", (*EVMWallet).PiriWalletHex, ParseEVMWalletPiriHex},
	}
	// Wallet addresses are public; record them in the committed wallets.env so they
	// are easy to find for periodic balance top-ups. (Private keys go to 1Password.)
	walletsEnv := filepath.Join(opts.ProjectDir, "environments", "staging", "wallets.env")
	for _, w := range wallets {
		var wallet *EVMWallet
		if value, ok := existing[w.opField]; ok {
			// Reuse the funded wallet; re-derive its public address so a stale
			// or missing wallets.env entry heals itself.
			wallet, err = w.parse(value)
			if err != nil {
				return nil, fmt.Errorf("parse existing %s wallet from 1Password: %w", w.role, err)
			}
			res.ReusedFields = append(res.ReusedFields, w.opField)
		} else {
			wallet, err = GenerateEVMWallet()
			if err != nil {
				return nil, fmt.Errorf("generate %s wallet: %w", w.role, err)
			}
			fresh[w.opField] = true
			res.GeneratedFields = append(res.GeneratedFields, w.opField)
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

	// 3. Connection secrets for the dependency containers we run (Postgres,
	//    MinIO, Vault) and service-to-service auth (hilt partner key, ingot
	//    root S3 credentials).
	connFiles := map[string]string{}
	for _, field := range connSecretFields {
		reused, err := reuse(field, field, 0o600)
		if err != nil {
			return nil, err
		}
		if !reused {
			secret, err := randomHex(24)
			if err != nil {
				return nil, fmt.Errorf("generate %s: %w", field, err)
			}
			if err := os.WriteFile(filepath.Join(tmp, field), []byte(secret), 0o600); err != nil {
				return nil, fmt.Errorf("write %s: %w", field, err)
			}
		}
		connFiles[field] = filepath.Join(tmp, field)
	}

	// 4. UCAN delegation proofs (committed to git), signed with the staging
	//    identities and addressed to the staging did:web audiences (plus ingot's
	//    did:key). Proofs are re-issued only when missing or when a key they
	//    depend on was freshly generated above.
	if opts.Proofs {
		ingotDID, err := deriveDIDFromPEM(filepath.Join(tmp, "ingot.pem"))
		if err != nil {
			return nil, fmt.Errorf("derive ingot did:key: %w", err)
		}
		written, err := generateProofs(opts.Ucantool, tmp, proofsDir, ingotDID, fresh)
		if err != nil {
			return nil, fmt.Errorf("generate proofs: %w", err)
		}
		res.ProofsWritten = written
	}

	// 5. Store everything secret in the single 1Password item. Identity PEMs,
	//    wallets, and connection secrets are read from their temp files and written
	//    into a JSON template (see storeInOnePassword) so multi-line PEMs and
	//    special characters survive intact and no value lands on a command line.
	//    Reused values are re-written byte-for-byte (a no-op edit); fresh values
	//    are added — the item is never partially refreshed.
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

// deriveDIDFromPEM computes the did:key identifier for an Ed25519 private key
// PEM — the same derivation pkg/generate uses for the local <name>.did files.
func deriveDIDFromPEM(pemPath string) (string, error) {
	pemBytes, err := os.ReadFile(pemPath)
	if err != nil {
		return "", err
	}
	signer, err := identity.DecodeSignerFromPEM(pemBytes)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	return multikey.KeyIssuer(signer).DID().String(), nil
}

// upsertEnvVar sets KEY=value in a dotenv-style file: it replaces an existing
// `KEY=...` line in place (preserving everything else) or appends one. Creates
// the file if it does not exist. The output always ends with a single trailing
// newline, regardless of whether the input had one.
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

	// Drop the trailing newline before splitting so a final "\n" doesn't yield an
	// empty trailing element; we re-add exactly one newline when writing back.
	var lines []string
	if content := strings.TrimSuffix(string(data), "\n"); content != "" {
		lines = strings.Split(content, "\n")
	}
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
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// randomHex returns n random bytes hex-encoded (2n chars).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
