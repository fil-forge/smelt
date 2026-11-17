package generate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fil-forge/libforge/identity"
	"github.com/fil-forge/smelt/pkg/manifest"
	"github.com/fil-forge/ucantone/multikey"
)

// fixedServiceKeys is the list of singleton services that need Ed25519 keys.
// Piri, hilt and ingot are declared in the manifest and keyed per instance.
var fixedServiceKeys = []string{
	"upload",
	"indexer",
	"delegator",
	"signing-service",
	"etracker",
	"guppy",
	"swarf",
}

// GenerateKeys generates all cryptographic keys for the stack.
// Idempotent: existing keys are skipped unless force is true.
func GenerateKeys(keysDir string, nodes []manifest.ResolvedPiriNode, hilts []manifest.ResolvedHilt, ingots []manifest.ResolvedIngot, force bool) error {
	if err := os.MkdirAll(keysDir, 0755); err != nil {
		return fmt.Errorf("create keys dir: %w", err)
	}

	// Generate Ed25519 keys for the singleton services.
	for _, svc := range fixedServiceKeys {
		if err := generateEd25519Key(keysDir, svc, force); err != nil {
			return fmt.Errorf("generate key for %s: %w", svc, err)
		}
	}

	// One identity per declared hilt and ingot. The first of each is named
	// "hilt" and "ingot", so a single-network stack keeps the key files it
	// already has on disk.
	for _, h := range hilts {
		if err := generateEd25519Key(keysDir, h.Name, force); err != nil {
			return fmt.Errorf("generate key for %s: %w", h.Name, err)
		}
	}
	for _, i := range ingots {
		if err := generateEd25519Key(keysDir, i.Name, force); err != nil {
			return fmt.Errorf("generate key for %s: %w", i.Name, err)
		}
	}

	// Generate Ed25519 keys and EVM wallets for each piri node.
	for _, node := range nodes {
		if err := generateEd25519Key(keysDir, node.Name, force); err != nil {
			return fmt.Errorf("generate key for %s: %w", node.Name, err)
		}

		acctIdx := PiriAccountIndex(node.Index)
		if acctIdx >= len(AnvilAccounts) {
			return fmt.Errorf("piri node %s: account index %d exceeds available Anvil accounts", node.Name, acctIdx)
		}
		walletName := node.Name + "-wallet"
		if err := generatePiriWallet(keysDir, walletName, AnvilAccounts[acctIdx].PrivateKey, force); err != nil {
			return fmt.Errorf("generate wallet for %s: %w", node.Name, err)
		}
	}

	// Generate payer key (signing-service) from Anvil account 1.
	if err := generatePayerKey(keysDir, force); err != nil {
		return fmt.Errorf("generate payer key: %w", err)
	}

	if err := writePiriNodesFile(keysDir, nodes); err != nil {
		return fmt.Errorf("write piri nodes file: %w", err)
	}

	if err := writeNetworksFile(keysDir, ingots); err != nil {
		return fmt.Errorf("write networks file: %w", err)
	}

	return nil
}

// NetworksFile is the name of the file in the keys directory describing the
// declared topology: one tab-separated `<hilt> <ingot> <region>` line per
// gateway, in manifest order. It holds no secrets, so the shell steps that
// need the shape of the stack but none of its keys — proof generation and
// each hilt's provider registration — read it directly.
const NetworksFile = "networks.tsv"

// writeNetworksFile rewrites [NetworksFile] so it always reflects the
// current manifest. A gateway that leaves its region to INGOT_REGION writes
// an empty third field, which the readers substitute at run time.
func writeNetworksFile(keysDir string, ingots []manifest.ResolvedIngot) error {
	var b strings.Builder
	for _, i := range ingots {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", i.Hilt, i.Name, i.Region)
	}
	return os.WriteFile(filepath.Join(keysDir, NetworksFile), []byte(b.String()), 0644)
}

// PiriNodesFile is the name of the file in the keys directory listing the DID
// of every piri node declared in the manifest, one per line. It holds no
// secrets, so containers that only need the node identities (e.g. hilt-init)
// mount it alone rather than the whole keys directory.
const PiriNodesFile = "piri-nodes.did"

// writePiriNodesFile rewrites [PiriNodesFile] from the per-node .did files, so
// it always reflects the current manifest.
func writePiriNodesFile(keysDir string, nodes []manifest.ResolvedPiriNode) error {
	var b strings.Builder
	for _, node := range nodes {
		id, err := os.ReadFile(filepath.Join(keysDir, node.Name+".did"))
		if err != nil {
			return fmt.Errorf("read DID for %s: %w", node.Name, err)
		}
		b.WriteString(strings.TrimSpace(string(id)))
		b.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(keysDir, PiriNodesFile), []byte(b.String()), 0644)
}

// generateEd25519Key generates an Ed25519 key pair in PEM format, plus a
// <name>.did file holding the key's did:key identifier (consumed by shell
// scripts that have no DID tooling, e.g. systems/hilt/register-provider.sh).
// An existing key is kept unless force is true; its <name>.did is written if
// missing, so a keys directory created before DID files existed is completed
// by a plain `smelt generate`.
func generateEd25519Key(keysDir, name string, force bool) error {
	privPath := filepath.Join(keysDir, name+".pem")
	if !force && fileExists(privPath) {
		if fileExists(filepath.Join(keysDir, name+".did")) {
			return nil
		}
		privPEM, err := os.ReadFile(privPath)
		if err != nil {
			return fmt.Errorf("read existing private key: %w", err)
		}
		return writeDIDFile(keysDir, name, privPEM)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	pubPath := filepath.Join(keysDir, name+".pub")
	if err := os.WriteFile(pubPath, pubPEM, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return writeDIDFile(keysDir, name, privPEM)
}

// writeDIDFile writes <name>.did holding the did:key identifier derived from
// the PEM-encoded private key.
func writeDIDFile(keysDir, name string, privPEM []byte) error {
	signer, err := identity.DecodeSignerFromPEM(privPEM)
	if err != nil {
		return fmt.Errorf("decode private key for DID derivation: %w", err)
	}
	id := multikey.KeyIssuer(signer).DID().String()
	didPath := filepath.Join(keysDir, name+".did")
	if err := os.WriteFile(didPath, []byte(id+"\n"), 0644); err != nil {
		return fmt.Errorf("write DID file: %w", err)
	}
	return nil
}

// generatePiriWallet creates a piri-format wallet file from an Anvil private key.
// Format: hex-encoded JSON {"Type":"delegated","PrivateKey":"<base64>"}
func generatePiriWallet(keysDir, name, privateKeyHex string, force bool) error {
	walletPath := filepath.Join(keysDir, name+".hex")
	if !force && fileExists(walletPath) {
		return nil
	}

	keyHex := strings.TrimPrefix(privateKeyHex, "0x")
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("decode private key hex: %w", err)
	}

	walletJSON := fmt.Sprintf(`{"Type":"delegated","PrivateKey":"%s"}`,
		base64.StdEncoding.EncodeToString(keyBytes))

	walletHexEncoded := hex.EncodeToString([]byte(walletJSON))
	if err := os.WriteFile(walletPath, []byte(walletHexEncoded), 0600); err != nil {
		return fmt.Errorf("write wallet: %w", err)
	}

	return nil
}

// generatePayerKey writes the payer-key.hex file (raw hex, no 0x prefix).
func generatePayerKey(keysDir string, force bool) error {
	payerPath := filepath.Join(keysDir, "payer-key.hex")
	if !force && fileExists(payerPath) {
		return nil
	}

	payerKey := strings.TrimPrefix(AnvilAccounts[1].PrivateKey, "0x")
	if err := os.WriteFile(payerPath, []byte(payerKey), 0600); err != nil {
		return fmt.Errorf("write payer key: %w", err)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
