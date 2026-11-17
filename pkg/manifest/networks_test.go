package manifest

import "testing"

func TestResolveNetworksDefault(t *testing.T) {
	m, err := ParseBytes([]byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	hilts, ingots, err := m.ResolveNetworks()
	if err != nil {
		t.Fatal(err)
	}
	if len(hilts) != 1 || len(ingots) != 1 {
		t.Fatalf("expected one hilt and one ingot, got %d and %d", len(hilts), len(ingots))
	}
	// The names, DIDs, databases and ports a single-network stack has
	// always had: scripts, docs and importers of pkg/stack all use them.
	h, i := hilts[0], ingots[0]
	if h.Name != "hilt" || h.DID() != "did:web:hilt" || h.Database() != "hilt" {
		t.Errorf("hilt = %q/%q/%q", h.Name, h.DID(), h.Database())
	}
	if h.Port() != 15110 || h.VaultPort() != 15112 || h.Vault() != "hilt-vault" {
		t.Errorf("hilt ports = %d/%d, vault %q", h.Port(), h.VaultPort(), h.Vault())
	}
	if i.Name != "ingot" || i.DID() != "did:web:ingot" || i.Database() != "ingot" {
		t.Errorf("ingot = %q/%q/%q", i.Name, i.DID(), i.Database())
	}
	if i.Port() != 15130 || i.VaultPort() != 15132 || i.Vault() != "ingot-openbao" {
		t.Errorf("ingot ports = %d/%d, vault %q", i.Port(), i.VaultPort(), i.Vault())
	}
	if i.Hilt != "hilt" {
		t.Errorf("ingot hilt = %q, want hilt", i.Hilt)
	}
	// Left to INGOT_REGION, which is how a single-region stack is pointed
	// at a region today.
	if i.Region != "" {
		t.Errorf("ingot region = %q, want empty", i.Region)
	}
	if len(h.Ingots) != 1 || h.Ingots[0] != "ingot" {
		t.Errorf("hilt ingots = %v", h.Ingots)
	}
}

// The topology the Fil One console exercises: one network over two regions
// and a second over one.
func TestResolveNetworksTwoNetworks(t *testing.T) {
	m, err := ParseBytes([]byte(`
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
	if len(hilts) != 2 || len(ingots) != 3 {
		t.Fatalf("expected 2 hilts and 3 ingots, got %d and %d", len(hilts), len(ingots))
	}

	if got := hilts[0].Ingots; len(got) != 2 || got[0] != "ingot" || got[1] != "ingot-1" {
		t.Errorf("hilt ingots = %v, want [ingot ingot-1]", got)
	}
	if got := hilts[1].Ingots; len(got) != 1 || got[0] != "ingot-2" {
		t.Errorf("hilt-1 ingots = %v, want [ingot-2]", got)
	}

	// The second hilt takes the next free ports and its own database; the
	// first keeps what it had.
	if hilts[1].Port() != 15113 || hilts[1].VaultPort() != 15114 {
		t.Errorf("hilt-1 ports = %d/%d, want 15113/15114", hilts[1].Port(), hilts[1].VaultPort())
	}
	if hilts[1].Database() != "hilt_1" {
		t.Errorf("hilt-1 database = %q, want hilt_1", hilts[1].Database())
	}
	if ingots[1].Port() != 15133 || ingots[2].Port() != 15135 {
		t.Errorf("ingot ports = %d/%d, want 15133/15135", ingots[1].Port(), ingots[2].Port())
	}
	if ingots[2].Hilt != "hilt-1" || ingots[2].Database() != "ingot_2" {
		t.Errorf("ingot-2 = hilt %q, database %q", ingots[2].Hilt, ingots[2].Database())
	}
	// An ingot that does not name a hilt lands on the first one.
	if ingots[1].Hilt != "hilt" {
		t.Errorf("ingot-1 hilt = %q, want hilt", ingots[1].Hilt)
	}
}

func TestResolveNetworksCountForm(t *testing.T) {
	m, err := ParseBytes([]byte(`
version: 1
hilt:
  count: 2
ingot:
  nodes:
    - region: us-east-9
    - region: us-west-9
      hilt: hilt-1
`))
	if err != nil {
		t.Fatal(err)
	}
	hilts, ingots, err := m.ResolveNetworks()
	if err != nil {
		t.Fatal(err)
	}
	if len(hilts) != 2 || hilts[0].Name != "hilt" || hilts[1].Name != "hilt-1" {
		t.Fatalf("hilt names = %v", []string{hilts[0].Name, hilts[1].Name})
	}
	if ingots[0].Name != "ingot" || ingots[1].Name != "ingot-1" {
		t.Errorf("ingot names = %v", []string{ingots[0].Name, ingots[1].Name})
	}
}

func TestResolveNetworksRejects(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"two gateways on one region",
			"version: 1\ningot:\n  nodes:\n    - region: us-east-9\n    - region: us-east-9\n",
			"both serve region",
		},
		{
			"gateway on an undeclared hilt",
			"version: 1\ningot:\n  nodes:\n    - region: us-east-9\n      hilt: hilt-9\n",
			"which the manifest does not declare",
		},
		{
			"hilt with no gateway",
			"version: 1\nhilt:\n  count: 2\ningot:\n  nodes:\n    - region: us-east-9\n",
			"has no ingot",
		},
		{
			"several gateways without regions",
			"version: 1\ningot:\n  count: 2\n",
			"only a single-ingot stack",
		},
		{
			"count and nodes together",
			"version: 1\nhilt:\n  count: 2\n  nodes:\n    - name: hilt\n",
			"cannot specify both",
		},
		{
			"duplicate name",
			"version: 1\ningot:\n  nodes:\n    - name: ingot\n      region: us-east-9\n    - name: ingot\n      region: us-west-9\n",
			"duplicate ingot name",
		},
		{
			"name that is not a compose service",
			"version: 1\nhilt:\n  nodes:\n    - name: Hilt_One\n",
			"must match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseBytes([]byte(tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = m.ResolveNetworks()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestMaxInstances(t *testing.T) {
	m, err := ParseBytes([]byte("version: 1\nhilt:\n  count: 6\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ResolveNetworks(); err == nil {
		t.Fatal("expected 6 hilts to exceed the maximum")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
