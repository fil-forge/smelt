package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// An instance named after a service the stack already runs does not add a
// service, it merges with that one, and for a service carrying an identity
// the network principal would reuse its key. The list has to keep up with
// systems/, which this holds it to.
func TestReservedServicesCoverTheComposeFiles(t *testing.T) {
	reserved := map[string]bool{}
	for _, name := range reservedServices {
		reserved[name] = true
	}

	root := filepath.Join("..", "..", "systems")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(path) != "compose.yml" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc struct {
			Services map[string]yaml.Node `yaml:"services"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return err
		}
		for name := range doc.Services {
			if !reserved[name] {
				t.Errorf("%s declares service %q, which a hilt or ingot could be named after; add it to reservedServices", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
