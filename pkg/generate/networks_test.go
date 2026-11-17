package generate

import (
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/manifest"
	"gopkg.in/yaml.v3"
)

func parseCompose(t *testing.T, data []byte) ComposeFile {
	t.Helper()
	var c ComposeFile
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func twoNetworks(t *testing.T) ([]manifest.ResolvedHilt, []manifest.ResolvedIngot) {
	t.Helper()
	m, err := manifest.ParseBytes([]byte(`
version: 1
hilt:
  nodes:
    - name: hilt
    - name: hilt-1
ingot:
  nodes:
    - name: ingot
      region: us-east-9
    - name: ingot-1
      region: eu-central-9
    - name: ingot-2
      region: us-west-9
      hilt: hilt-1
`))
	if err != nil {
		t.Fatal(err)
	}
	hilts, ingots, err := m.ResolveNetworks()
	if err != nil {
		t.Fatal(err)
	}
	return hilts, ingots
}

// The default manifest emits the services a single-network stack has always
// run, under the names the rest of the repo and its importers use.
func TestGenerateDefaultNetworkKeepsNames(t *testing.T) {
	hilts, ingots := manifest.DefaultNetworks()

	hiltYAML, err := GenerateHiltCompose(hilts)
	if err != nil {
		t.Fatal(err)
	}
	hilt := parseCompose(t, hiltYAML)
	for _, name := range []string{"hilt", "hilt-init", "hilt-vault", "hilt-vault-init", "hilt-postgres"} {
		if _, ok := hilt.Services[name]; !ok {
			t.Errorf("hilt compose has no %q service", name)
		}
	}
	// One hilt needs no database sidecar: POSTGRES_DB creates its database.
	if _, ok := hilt.Services["hilt-postgres-init"]; ok {
		t.Error("a single hilt should need no postgres-init sidecar")
	}

	ingotYAML, err := GenerateIngotCompose(ingots, nil)
	if err != nil {
		t.Fatal(err)
	}
	ingot := parseCompose(t, ingotYAML)
	for _, name := range []string{"ingot", "ingot-openbao", "ingot-openbao-init", "ingot-postgres"} {
		if _, ok := ingot.Services[name]; !ok {
			t.Errorf("ingot compose has no %q service", name)
		}
	}
	if _, ok := ingot.Services["ingot-postgres-init"]; ok {
		t.Error("a single ingot should need no postgres-init sidecar")
	}

	// The gateway's region still comes from the shell, as it did when the
	// compose file was static.
	if !hasEnv(ingot.Services["ingot"], "INGOT_REGION=${INGOT_REGION:-us-west-1}") {
		t.Errorf("ingot region env = %v", ingot.Services["ingot"].Environment)
	}
}

func TestGenerateTwoNetworks(t *testing.T) {
	hilts, ingots := twoNetworks(t)

	hiltYAML, err := GenerateHiltCompose(hilts)
	if err != nil {
		t.Fatal(err)
	}
	hilt := parseCompose(t, hiltYAML)
	// Each hilt gets its own identity, product, proof, database and vault,
	// so two networks never write over each other.
	if !hasEnv(hilt.Services["hilt-1"], "HILT_IDENTITY_SERVICE_ID=did:web:hilt-1") {
		t.Errorf("hilt-1 identity = %v", hilt.Services["hilt-1"].Environment)
	}
	if !hasEnv(hilt.Services["hilt-1"], "HILT_UPLOAD_PRODUCT_ID=did:web:hilt-1") {
		t.Error("hilt-1 should register its customers under its own product")
	}
	if !hasEnv(hilt.Services["hilt-1"], "HILT_UPLOAD_PROOFS=/proofs/hilt-1-customer-add-proof.txt") {
		t.Error("hilt-1 should present its own customer-add proof")
	}
	if !hasEnvPrefix(hilt.Services["hilt-1"], "HILT_STORAGE_POSTGRES_DSN=postgres://hilt:hilt@hilt-postgres:5432/hilt_1?") {
		t.Errorf("hilt-1 dsn = %v", hilt.Services["hilt-1"].Environment)
	}
	if !hasEnv(hilt.Services["hilt-1"], "HILT_VAULT_OPENBAO_ADDRESS=http://hilt-1-vault:8200") {
		t.Error("hilt-1 should hold its keys in its own vault")
	}
	if _, ok := hilt.Services["hilt-postgres-init"]; !ok {
		t.Error("a second hilt needs the sidecar that creates its database")
	}
	// Each hilt registers only its own gateways.
	if !hasEnv(hilt.Services["hilt-1-init"], "SMELT_HILT_NAME=hilt-1") {
		t.Errorf("hilt-1-init = %v", hilt.Services["hilt-1-init"].Environment)
	}

	ingotYAML, err := GenerateIngotCompose(ingots, nil)
	if err != nil {
		t.Fatal(err)
	}
	ingot := parseCompose(t, ingotYAML)
	if !hasEnv(ingot.Services["ingot-1"], "INGOT_REGION=eu-central-9") {
		t.Errorf("ingot-1 region = %v", ingot.Services["ingot-1"].Environment)
	}
	// A gateway waits for the hilt that authorizes it, and for that hilt's
	// registrar: hilt refuses /s3/* from an unregistered provider.
	if _, ok := ingot.Services["ingot-2"].DependsOn["hilt-1"]; !ok {
		t.Error("ingot-2 should wait for hilt-1")
	}
	if _, ok := ingot.Services["ingot-2"].DependsOn["hilt-1-init"]; !ok {
		t.Error("ingot-2 should wait for hilt-1's provider registration")
	}
	if _, ok := ingot.Services["ingot"].DependsOn["hilt-1"]; ok {
		t.Error("ingot should not wait for a hilt it is not registered against")
	}
}

func TestGenerateIngotConfigPerGateway(t *testing.T) {
	_, ingots := twoNetworks(t)
	const tmpl = `region: {{.Region}}
service_id: {{.DID}}
dsn: {{.Database}}
auth_url: http://{{.Hilt}}:80
auth_did: {{.HiltDID}}
proofs: /proofs/{{.S3ProofTxt}}
vault: http://{{.Vault}}:8200
`
	got, err := GenerateIngotConfig(tmpl, ingots[2])
	if err != nil {
		t.Fatal(err)
	}
	want := `region: us-west-9
service_id: did:web:ingot-2
dsn: ingot_2
auth_url: http://hilt-1:80
auth_did: did:web:hilt-1
proofs: /proofs/hilt-1-ingot-2-s3-proof.txt
vault: http://ingot-2-openbao:8200
`
	if string(got) != want {
		t.Errorf("rendered config:\n%s\nwant:\n%s", got, want)
	}
}

func TestGenerateVaultConfigNamesItsOwnNode(t *testing.T) {
	// Raft refuses to start two nodes under one id, so each vault's
	// rendered config names itself.
	got, err := GenerateVaultConfig("node_id = \"{{.Vault}}\"\n", "hilt-1-vault")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "node_id = \"hilt-1-vault\"\n" {
		t.Errorf("rendered config = %q", got)
	}
}

func hasEnv(svc ComposeService, entry string) bool {
	for _, e := range svc.Environment {
		if e == entry {
			return true
		}
	}
	return false
}

func hasEnvPrefix(svc ComposeService, prefix string) bool {
	for _, e := range svc.Environment {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
