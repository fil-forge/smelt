package generate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fil-forge/smelt/pkg/manifest"
)

// Options controls the generation process.
type Options struct {
	// ManifestPath is an optional explicit manifest override. When empty
	// Generate falls back to manifest.ResolveManifestPath which picks the
	// session manifest (from a prior snapshot load) when present and the
	// project manifest otherwise.
	ManifestPath string
	ProjectDir   string // Project root directory
	Force        bool   // Overwrite existing keys
}

// Result contains the paths to all generated artifacts.
type Result struct {
	PiriComposePath  string
	HiltComposePath  string
	IngotComposePath string
	KeysDir          string
	NodeCount        int
	HiltCount        int
	IngotCount       int
	// ManifestPath is the path Generate actually read from.
	ManifestPath string
}

// Generate reads the manifest, generates keys, and produces Docker Compose files.
func Generate(opts Options) (*Result, error) {
	manifestPath := opts.ManifestPath
	if manifestPath == "" {
		var source manifest.ManifestSource
		manifestPath, source = manifest.ResolveManifestPath(opts.ProjectDir)
		if source != manifest.SourceProject {
			fmt.Printf("Using manifest from %s: %s\n", source, manifestPath)
		}
	}

	m, err := manifest.Parse(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	nodes, err := m.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve manifest: %w", err)
	}

	hilts, ingots, err := m.ResolveNetworks()
	if err != nil {
		return nil, fmt.Errorf("resolve manifest: %w", err)
	}

	keysDir := filepath.Join(opts.ProjectDir, "generated", "keys")
	composeDir := filepath.Join(opts.ProjectDir, "generated", "compose")

	if err := os.MkdirAll(composeDir, 0755); err != nil {
		return nil, fmt.Errorf("create compose dir: %w", err)
	}

	// Generate keys.
	if err := GenerateKeys(keysDir, nodes, hilts, ingots, opts.Force); err != nil {
		return nil, fmt.Errorf("generate keys: %w", err)
	}

	// Generate piri compose.
	piriYAML, err := GeneratePiriCompose(nodes)
	if err != nil {
		return nil, fmt.Errorf("generate piri compose: %w", err)
	}
	piriPath := filepath.Join(composeDir, "piri.yml")
	if err := os.WriteFile(piriPath, piriYAML, 0644); err != nil {
		return nil, fmt.Errorf("write piri compose: %w", err)
	}

	hiltPath, ingotPath, err := GenerateNetworks(opts.ProjectDir, hilts, ingots, nodes)
	if err != nil {
		return nil, err
	}

	return &Result{
		PiriComposePath:  piriPath,
		HiltComposePath:  hiltPath,
		IngotComposePath: ingotPath,
		KeysDir:          keysDir,
		NodeCount:        len(nodes),
		HiltCount:        len(hilts),
		IngotCount:       len(ingots),
		ManifestPath:     manifestPath,
	}, nil
}

// GenerateNetworks writes the hilt and ingot compose files, one rendered
// config per gateway, and one rendered server config per vault. It returns
// the two compose paths.
//
// The per-instance files are rendered rather than mounted straight from
// systems/, because each carries its own identity, database and peer names
// (see systems/ingot/config/config.yaml.tmpl).
func GenerateNetworks(projectDir string, hilts []manifest.ResolvedHilt, ingots []manifest.ResolvedIngot, piri []manifest.ResolvedPiriNode) (string, string, error) {
	composeDir := filepath.Join(projectDir, "generated", "compose")
	ingotDir := filepath.Join(projectDir, "generated", "ingot")
	vaultDir := filepath.Join(projectDir, "generated", "openbao")
	for _, dir := range []string{composeDir, ingotDir, vaultDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", "", fmt.Errorf("create %s: %w", dir, err)
		}
	}

	hiltYAML, err := GenerateHiltCompose(hilts)
	if err != nil {
		return "", "", fmt.Errorf("generate hilt compose: %w", err)
	}
	hiltPath := filepath.Join(composeDir, "hilt.yml")
	if err := os.WriteFile(hiltPath, hiltYAML, 0644); err != nil {
		return "", "", fmt.Errorf("write hilt compose: %w", err)
	}

	ingotYAML, err := GenerateIngotCompose(ingots, piri)
	if err != nil {
		return "", "", fmt.Errorf("generate ingot compose: %w", err)
	}
	ingotPath := filepath.Join(composeDir, "ingot.yml")
	if err := os.WriteFile(ingotPath, ingotYAML, 0644); err != nil {
		return "", "", fmt.Errorf("write ingot compose: %w", err)
	}

	ingotTmpl, err := os.ReadFile(filepath.Join(projectDir, "systems", "ingot", "config", "config.yaml.tmpl"))
	if err != nil {
		return "", "", fmt.Errorf("read ingot config template: %w", err)
	}
	for _, i := range ingots {
		rendered, err := GenerateIngotConfig(string(ingotTmpl), i)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", i.Name, err)
		}
		if err := os.WriteFile(filepath.Join(ingotDir, i.Name+".yaml"), rendered, 0644); err != nil {
			return "", "", fmt.Errorf("write %s config: %w", i.Name, err)
		}
	}

	vaults := make(map[string]string, len(hilts)+len(ingots))
	for _, h := range hilts {
		vaults[h.Vault()] = filepath.Join(projectDir, "systems", "hilt", "openbao", "config.hcl.tmpl")
	}
	for _, i := range ingots {
		vaults[i.Vault()] = filepath.Join(projectDir, "systems", "ingot", "openbao", "config.hcl.tmpl")
	}
	for vault, tmplPath := range vaults {
		tmpl, err := os.ReadFile(tmplPath)
		if err != nil {
			return "", "", fmt.Errorf("read vault config template: %w", err)
		}
		rendered, err := GenerateVaultConfig(string(tmpl), vault)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", vault, err)
		}
		if err := os.WriteFile(filepath.Join(vaultDir, VaultConfigName(vault)), rendered, 0644); err != nil {
			return "", "", fmt.Errorf("write %s config: %w", vault, err)
		}
	}

	return hiltPath, ingotPath, nil
}
