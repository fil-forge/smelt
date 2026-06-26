package staging

import (
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

func TestGenerateEVMWalletAddressFormat(t *testing.T) {
	w, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	if !addressRE.MatchString(w.Address) {
		t.Fatalf("address %q is not a 0x-prefixed 20-byte hex string", w.Address)
	}
}

func TestRawHexIs32Bytes(t *testing.T) {
	w, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	b, err := hex.DecodeString(w.RawHex())
	if err != nil {
		t.Fatalf("RawHex is not valid hex: %v", err)
	}
	if len(b) != 32 {
		t.Fatalf("RawHex decodes to %d bytes, want 32", len(b))
	}
}

func TestHex0xPrefix(t *testing.T) {
	w, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	if !strings.HasPrefix(w.Hex0x(), "0x") {
		t.Fatalf("Hex0x %q missing 0x prefix", w.Hex0x())
	}
}

func TestPiriWalletHexDecodesToDelegatedJSON(t *testing.T) {
	w, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	raw, err := hex.DecodeString(w.PiriWalletHex())
	if err != nil {
		t.Fatalf("PiriWalletHex is not valid hex: %v", err)
	}
	var parsed struct {
		Type       string `json:"Type"`
		PrivateKey string `json:"PrivateKey"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decoded wallet is not valid JSON: %v", err)
	}
	if parsed.Type != "delegated" {
		t.Fatalf("wallet Type = %q, want %q", parsed.Type, "delegated")
	}
}

func TestDistinctWalletsHaveDistinctAddresses(t *testing.T) {
	a, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	b, err := GenerateEVMWallet()
	if err != nil {
		t.Fatalf("GenerateEVMWallet: %v", err)
	}
	if a.Address == b.Address {
		t.Fatalf("two generated wallets share an address %q", a.Address)
	}
}
