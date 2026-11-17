package smelt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fil-forge/smelt/pkg/generate"
	"github.com/fil-forge/smelt/pkg/manifest"
)

// generatedComposePaths writes the hilt and ingot compose files for the
// default topology into a temp dir and returns their paths. The two systems
// have no compose.yml in the tree: `smelt generate` writes one of each from
// the manifest, so a test that asserts on their contents generates them the
// same way `make up` does.
func generatedComposePaths(t *testing.T) (hiltPath, ingotPath string) {
	t.Helper()
	dir := t.TempDir()
	hilts, ingots := manifest.DefaultNetworks()
	piri := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: manifest.DBPostgres, Blob: manifest.BlobS3}},
	}

	hiltYAML, err := generate.GenerateHiltCompose(hilts)
	if err != nil {
		t.Fatal(err)
	}
	hiltPath = filepath.Join(dir, "hilt.yml")
	if err := os.WriteFile(hiltPath, hiltYAML, 0644); err != nil {
		t.Fatal(err)
	}

	ingotYAML, err := generate.GenerateIngotCompose(ingots, piri)
	if err != nil {
		t.Fatal(err)
	}
	ingotPath = filepath.Join(dir, "ingot.yml")
	if err := os.WriteFile(ingotPath, ingotYAML, 0644); err != nil {
		t.Fatal(err)
	}
	return hiltPath, ingotPath
}
