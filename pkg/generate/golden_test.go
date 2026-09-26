package generate

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/manifest"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden from the current generator")

// goldenManifests lists every tracked manifest (the project smelt.yml, the
// ready-made manifests, the snapshot manifests) plus the test-only ones,
// keyed by the golden file name that holds its generated piri.yml.
func goldenManifests(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{"smelt.yml": filepath.Join("..", "..", "smelt.yml")}
	for _, pattern := range []string{
		filepath.Join("..", "..", "manifests", "*.yml"),
		filepath.Join("..", "..", "snapshots", "*", "smelt.yml"),
		filepath.Join("testdata", "manifests", "*.yml"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			rel := strings.TrimPrefix(filepath.ToSlash(m), "../../")
			out[strings.ReplaceAll(rel, "/", "__")] = m
		}
	}
	return out
}

// TestGeneratePiriComposeGolden pins the generated piri.yml for every
// tracked manifest byte for byte. Run with -update to accept a change.
func TestGeneratePiriComposeGolden(t *testing.T) {
	manifests := goldenManifests(t)
	names := make([]string, 0, len(manifests))
	for name := range manifests {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			m, err := manifest.Parse(manifests[name])
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := m.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			got, err := GeneratePiriCompose(nodes)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden", name)
			if *updateGolden {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run go test -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("generated piri.yml differs from %s:\n--- got ---\n%s", golden, got)
			}
		})
	}
}
