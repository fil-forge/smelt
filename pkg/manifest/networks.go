package manifest

import (
	"fmt"
	"regexp"
)

const (
	// MaxHilts and MaxIngots are limited by the host ports reserved for
	// each in the 15XXX map: 15110-15119 for hilt and its vaults,
	// 15130-15139 for ingot and its vaults. The first instance of each
	// keeps the ports it has always had. A fifth of either would put its
	// vault on 15120 or 15140, which plc and swarf hold.
	MaxHilts  = 4
	MaxIngots = 4

	// FirstHiltName and FirstIngotName are the names the leading instance
	// of each takes when the manifest does not name it. They carry no
	// index so that the service, its DID and its host ports are the ones
	// every script, document and importer of pkg/stack already knows.
	FirstHiltName  = "hilt"
	FirstIngotName = "ingot"
)

// serviceNameRE is the compose service name a hilt or ingot entry may take.
// It is also the did:web label and a postgres database name, so it stays
// lowercase alphanumeric with dashes.
var serviceNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// HiltSpec describes the tenant-management services to run, one per storage
// network. Use either Count (N identically configured hilts) or Nodes.
type HiltSpec struct {
	Count int            `yaml:"count,omitempty"`
	Nodes []HiltNodeSpec `yaml:"nodes,omitempty"`
}

// HiltNodeSpec describes a single hilt.
type HiltNodeSpec struct {
	Name  string `yaml:"name,omitempty"`
	Image string `yaml:"image,omitempty"`
}

// IngotSpec describes the S3 gateways to run, one per region. Use either
// Count (N gateways on the first hilt) or Nodes.
type IngotSpec struct {
	Count int             `yaml:"count,omitempty"`
	Nodes []IngotNodeSpec `yaml:"nodes,omitempty"`
}

// IngotNodeSpec describes a single ingot: the region it serves and the hilt
// that authorizes it.
type IngotNodeSpec struct {
	Name  string `yaml:"name,omitempty"`
	Image string `yaml:"image,omitempty"`
	// Region is the S3 region name this gateway serves. Left empty the
	// generated compose keeps reading INGOT_REGION from the shell, which
	// is how a single-region stack has always been pointed at a region.
	Region string `yaml:"region,omitempty"`
	// Hilt names the hilt that holds this region's tenants and authorizes
	// its S3 requests. Empty means the first hilt, so a single-network
	// manifest never has to say it.
	Hilt string `yaml:"hilt,omitempty"`
}

// ResolvedHilt is one fully resolved tenant-management service.
type ResolvedHilt struct {
	Name  string
	Index int
	Image string
	// Ingots are the gateways registered against this hilt, in manifest
	// order. Every one of them serves a different region.
	Ingots []string
}

// ResolvedIngot is one fully resolved S3 gateway.
type ResolvedIngot struct {
	Name  string
	Index int
	Image string
	// Region is the literal region name, or empty when the stack takes it
	// from INGOT_REGION at compose time.
	Region string
	// Hilt is the name of the hilt serving this gateway.
	Hilt string
}

// DID is the did:web identity a service is known by across the stack. It is
// the service's compose name, which is also its DNS name on forge-network,
// so peers resolve the document from the container itself.
func (h ResolvedHilt) DID() string { return "did:web:" + h.Name }

// DID is the gateway's did:web identity. See [ResolvedHilt.DID].
func (i ResolvedIngot) DID() string { return "did:web:" + i.Name }

// Database is the postgres database this hilt's tenant store lives in. The
// first keeps the plain name its server was created with.
func (h ResolvedHilt) Database() string { return indexedName("hilt", h.Index) }

// Database is the postgres database this gateway's registry lives in. See
// [ResolvedHilt.Database].
func (i ResolvedIngot) Database() string { return indexedName("ingot", i.Index) }

// Vault is the compose name of the OpenBao holding this hilt's tenant and
// access-key private keys.
func (h ResolvedHilt) Vault() string { return suffixed(h.Name, "vault") }

// Vault is the compose name of the OpenBao holding this region's KEK. One
// per gateway: the key belongs to the region, not the network.
func (i ResolvedIngot) Vault() string { return suffixed(i.Name, "openbao") }

// Port is the host port the hilt serves on.
func (h ResolvedHilt) Port() int { return indexedPort(15110, 15113, h.Index) }

// VaultPort is the host port this hilt's OpenBao serves on.
func (h ResolvedHilt) VaultPort() int { return indexedPort(15112, 15114, h.Index) }

// Port is the host port the gateway serves S3 on.
func (i ResolvedIngot) Port() int { return indexedPort(15130, 15133, i.Index) }

// VaultPort is the host port this region's OpenBao serves on.
func (i ResolvedIngot) VaultPort() int { return indexedPort(15132, 15134, i.Index) }

// indexedPort gives instance 0 the port it has always had and lays the rest
// out in pairs from the first free one, so a service and its vault stay
// adjacent.
func indexedPort(first, rest, index int) int {
	if index == 0 {
		return first
	}
	return rest + (index-1)*2
}

// indexedName keeps the leading instance on the bare name and suffixes the
// rest, matching how [FirstHiltName] keeps the leading service name.
func indexedName(base string, index int) string {
	if index == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, index)
}

func suffixed(name, part string) string { return name + "-" + part }

// ResolveNetworks normalizes the hilt and ingot sections into concrete
// instances. The default is what smelt has always run: one hilt and one
// ingot, the gateway registered against the hilt, its region left to
// INGOT_REGION.
func (m *Manifest) ResolveNetworks() ([]ResolvedHilt, []ResolvedIngot, error) {
	hilts, err := resolveHilts(&m.Hilt)
	if err != nil {
		return nil, nil, err
	}
	ingots, err := resolveIngots(&m.Ingot, hilts)
	if err != nil {
		return nil, nil, err
	}
	for i := range ingots {
		for j := range hilts {
			if hilts[j].Name == ingots[i].Hilt {
				hilts[j].Ingots = append(hilts[j].Ingots, ingots[i].Name)
			}
		}
	}
	for _, h := range hilts {
		if len(h.Ingots) == 0 {
			return nil, nil, fmt.Errorf("manifest: hilt %q has no ingot, so its network serves no region", h.Name)
		}
	}

	piri, err := m.Resolve()
	if err != nil {
		return nil, nil, err
	}
	if err := assertNamesAreFree(hilts, ingots, piri); err != nil {
		return nil, nil, err
	}
	if err := assertProofsAreDistinct(ingots); err != nil {
		return nil, nil, err
	}
	return hilts, ingots, nil
}

// sharedServices are the services generation emits once however many
// instances are declared, so an instance may not take one of their names.
var sharedServices = []string{
	"hilt-postgres", "hilt-postgres-init",
	"ingot-postgres", "ingot-postgres-init",
}

// assertNamesAreFree refuses a manifest whose instances would write over each
// other's services. Every instance brings derived names with it — a hilt also
// emits a registrar and a vault, a gateway a vault — so a hilt called
// "hilt-init" is not a new service but the first hilt's registrar, and the
// stack would wait on a server that never completes.
func assertNamesAreFree(hilts []ResolvedHilt, ingots []ResolvedIngot, piri []ResolvedPiriNode) error {
	owner := map[string]string{}
	claim := func(service, by string) error {
		if held, taken := owner[service]; taken {
			return fmt.Errorf("manifest: %s and %s would both be the service %q", held, by, service)
		}
		owner[service] = by
		return nil
	}

	for _, name := range sharedServices {
		owner[name] = "generation itself"
	}
	for _, n := range piri {
		if err := claim(n.Name, fmt.Sprintf("piri node %q", n.Name)); err != nil {
			return err
		}
	}
	for _, h := range hilts {
		by := fmt.Sprintf("hilt %q", h.Name)
		for _, service := range []string{h.Name, h.Name + "-init", h.Vault(), h.Vault() + "-init"} {
			if err := claim(service, by); err != nil {
				return err
			}
		}
	}
	for _, i := range ingots {
		by := fmt.Sprintf("ingot %q", i.Name)
		for _, service := range []string{i.Name, i.Vault(), i.Vault() + "-init"} {
			if err := claim(service, by); err != nil {
				return err
			}
		}
	}
	return nil
}

// assertProofsAreDistinct refuses a manifest whose gateways would share a
// proof file. The name joins the pair with a dash and a name may hold one, so
// ("a-b", "c") and ("a", "b-c") both read as "a-b-c": one gateway would be
// handed a delegation issued by the wrong hilt to the wrong audience, and
// could authorize nothing.
func assertProofsAreDistinct(ingots []ResolvedIngot) error {
	owner := map[string]string{}
	for _, i := range ingots {
		pair := fmt.Sprintf("%s -> %s", i.Hilt, i.Name)
		name := S3ProofName(i.Hilt, i.Name)
		if held, taken := owner[name]; taken {
			return fmt.Errorf("manifest: %s and %s would share the proof file %q", held, pair, name)
		}
		owner[name] = pair
	}
	return nil
}

// S3ProofName is the file holding the delegation a hilt issues to one of its
// gateways for the /s3/* commands. [assertProofsAreDistinct] keeps two pairs
// from landing on one name.
func S3ProofName(hilt, ingot string) string {
	return hilt + "-" + ingot + "-s3-proof.txt"
}

func resolveHilts(spec *HiltSpec) ([]ResolvedHilt, error) {
	nodes, err := expand(spec.Count, len(spec.Nodes), "hilt")
	if err != nil {
		return nil, err
	}
	if nodes == 0 {
		nodes = len(spec.Nodes)
	}
	if nodes > MaxHilts {
		return nil, fmt.Errorf("manifest: %d hilts exceeds maximum of %d (host ports 15110-15119)", nodes, MaxHilts)
	}

	resolved := make([]ResolvedHilt, nodes)
	seen := make(map[string]bool)
	for i := range resolved {
		var n HiltNodeSpec
		if i < len(spec.Nodes) {
			n = spec.Nodes[i]
		}
		name := n.Name
		if name == "" {
			name = autoName(FirstHiltName, i)
		}
		if err := validateServiceName("hilt", name, seen); err != nil {
			return nil, err
		}
		resolved[i] = ResolvedHilt{Name: name, Index: i, Image: n.Image}
	}
	return resolved, nil
}

func resolveIngots(spec *IngotSpec, hilts []ResolvedHilt) ([]ResolvedIngot, error) {
	nodes, err := expand(spec.Count, len(spec.Nodes), "ingot")
	if err != nil {
		return nil, err
	}
	if nodes == 0 {
		nodes = len(spec.Nodes)
	}
	if nodes > MaxIngots {
		return nil, fmt.Errorf("manifest: %d ingots exceeds maximum of %d (host ports 15130-15139)", nodes, MaxIngots)
	}

	byName := make(map[string]bool, len(hilts))
	for _, h := range hilts {
		byName[h.Name] = true
	}

	resolved := make([]ResolvedIngot, nodes)
	seen := make(map[string]bool)
	regions := make(map[string]string)
	for i := range resolved {
		var n IngotNodeSpec
		if i < len(spec.Nodes) {
			n = spec.Nodes[i]
		}
		name := n.Name
		if name == "" {
			name = autoName(FirstIngotName, i)
		}
		if err := validateServiceName("ingot", name, seen); err != nil {
			return nil, err
		}

		hilt := n.Hilt
		if hilt == "" {
			hilt = hilts[0].Name
		}
		if !byName[hilt] {
			return nil, fmt.Errorf("manifest: ingot %q names hilt %q, which the manifest does not declare", name, hilt)
		}

		// A region is served by exactly one gateway: hilt resolves the
		// provider for a request from the region it is signed for, so a
		// second claim on the same region would make registration lose
		// whichever ran first.
		if n.Region != "" {
			if other, dup := regions[n.Region]; dup {
				return nil, fmt.Errorf("manifest: ingots %q and %q both serve region %q", other, name, n.Region)
			}
			regions[n.Region] = name
		} else if len(resolved) > 1 {
			return nil, fmt.Errorf("manifest: ingot %q has no region; only a single-ingot stack may leave it to INGOT_REGION", name)
		}

		resolved[i] = ResolvedIngot{Name: name, Index: i, Image: n.Image, Region: n.Region, Hilt: hilt}
	}
	return resolved, nil
}

// expand reports how many instances a count/nodes pair asks for, refusing
// the two together. A zero result means "take it from nodes", which the
// caller does; both zero means the single default instance.
func expand(count, nodes int, kind string) (int, error) {
	if count > 0 && nodes > 0 {
		return 0, fmt.Errorf("manifest: %s: cannot specify both 'count' and 'nodes'", kind)
	}
	if count > 0 {
		return count, nil
	}
	if nodes == 0 {
		return 1, nil
	}
	return 0, nil
}

func autoName(first string, index int) string {
	if index == 0 {
		return first
	}
	return fmt.Sprintf("%s-%d", first, index)
}

func validateServiceName(kind, name string, seen map[string]bool) error {
	if !serviceNameRE.MatchString(name) {
		return fmt.Errorf("manifest: %s name %q must match %s", kind, name, serviceNameRE)
	}
	if seen[name] {
		return fmt.Errorf("manifest: duplicate %s name %q", kind, name)
	}
	seen[name] = true
	return nil
}

// DefaultNetworks is the single network smelt runs when nothing declares
// otherwise: one hilt and one ingot registered against it, the gateway's
// region left to INGOT_REGION. Callers that build a stack without a manifest
// (the Go test SDK) use it so they get the same names, ports and DIDs a
// default `make up` does.
func DefaultNetworks() ([]ResolvedHilt, []ResolvedIngot) {
	m := &Manifest{}
	hilts, ingots, err := m.ResolveNetworks()
	if err != nil {
		// Unreachable: the zero manifest is the default topology, which
		// resolveHilts and resolveIngots accept by construction.
		panic("manifest: default networks do not resolve: " + err.Error())
	}
	return hilts, ingots
}
