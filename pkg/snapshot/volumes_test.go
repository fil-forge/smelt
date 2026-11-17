package snapshot

import (
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/manifest"
)

// A snapshot that saved the databases but not the vaults would restore
// tenants nobody can sign for and objects nobody can read, because each hilt
// keeps its tenant keys and each gateway its region KEK in a vault of its
// own. The set has to follow the manifest.
func TestResolveVolumesCoversEveryInstance(t *testing.T) {
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

	vols, err := resolveVolumes(m)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, v := range vols {
		have[v] = true
	}

	for _, want := range []string{
		// The databases, shared by every instance of a kind.
		"hilt-postgres-data", "ingot-postgres-data",
		// Each hilt's own vault, and the unseal share restored with it.
		"hilt-vault-data", "hilt-vault-init",
		"hilt-1-vault-data", "hilt-1-vault-init",
		// Each gateway's data and its region KEK.
		"ingot-data", "ingot-openbao-data", "ingot-openbao-init",
		"ingot-1-data", "ingot-1-openbao-data", "ingot-1-openbao-init",
		"ingot-2-data", "ingot-2-openbao-data", "ingot-2-openbao-init",
	} {
		if !have[want] {
			t.Errorf("snapshot would not save %q; it saves %s", want, strings.Join(vols, " "))
		}
	}
}

// The default manifest must keep saving exactly what it always did.
func TestResolveVolumesDefaultTopology(t *testing.T) {
	m, err := manifest.ParseBytes([]byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	vols, err := resolveVolumes(m)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, v := range vols {
		have[v] = true
	}
	for _, want := range []string{
		"hilt-postgres-data", "hilt-vault-data", "hilt-vault-init",
		"ingot-data", "ingot-postgres-data", "ingot-openbao-data", "ingot-openbao-init",
	} {
		if !have[want] {
			t.Errorf("default snapshot would not save %q", want)
		}
	}
}
