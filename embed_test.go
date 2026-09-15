package smelt_test

import (
	"path"
	"strings"
	"testing"

	"github.com/fil-forge/smelt"
	"github.com/fil-forge/smelt/pkg/generate"
	"github.com/fil-forge/smelt/pkg/manifest"
	"gopkg.in/yaml.v3"
)

// pkg/stack extracts EmbeddedFiles into a temp directory and runs compose from
// there, so every bind-mount source under systems/ has to be embedded. A
// source that is missing does not fail the boot: Docker materializes it as an
// empty directory, and a service whose entrypoint is `sh <that path>` exits 0
// without doing its job, which surfaces much later as whatever the job was
// supposed to set up.
func TestGeneratedPiriComposeBindSourcesAreEmbedded(t *testing.T) {
	// Every backend combination, so the postgres and S3 services are emitted.
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: "sqlite", Blob: "filesystem"}},
		{Name: "piri-1", Index: 1, Storage: manifest.StorageSpec{DB: "postgres", Blob: "s3"}},
	}

	data, err := generate.GeneratePiriCompose(nodes)
	if err != nil {
		t.Fatal(err)
	}

	var compose generate.ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatal(err)
	}

	checked := 0
	for name, svc := range compose.Services {
		for _, vol := range svc.Volumes {
			src, _, ok := strings.Cut(vol, ":")
			if !ok {
				continue
			}
			// Generated compose lives in generated/compose/, so its relative
			// sources resolve from there. Named volumes have no path at all.
			if !strings.Contains(src, "/") {
				continue
			}
			resolved := path.Clean(path.Join("generated", "compose", src))
			// generated/keys and generated/proofs are written at runtime.
			if !strings.HasPrefix(resolved, "systems/") {
				continue
			}
			if _, err := smelt.EmbeddedFiles.ReadFile(resolved); err != nil {
				t.Errorf("service %s mounts %s, which is not in EmbeddedFiles: add a //go:embed line for it in embed.go", name, resolved)
			}
			checked++
		}
	}

	if checked == 0 {
		t.Fatal("no systems/ bind sources found to check; the test is no longer exercising anything")
	}
}
