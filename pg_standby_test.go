package smelt_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// compose.pg-standby.yml is chained after compose.yml, so compose resolves
// its relative paths from the project directory, not from the file's. A
// source that is missing does not fail the boot: Docker creates an empty
// directory in its place, and a service whose entrypoint is `sh <that path>`
// exits without doing its job.
func TestPGStandbyOverlayBindSourcesExist(t *testing.T) {
	data, err := os.ReadFile("compose.pg-standby.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Volumes    []string `yaml:"volumes"`
			Entrypoint []string `yaml:"entrypoint"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for name, svc := range c.Services {
		mounts := map[string]string{} // container path -> host path
		for _, vol := range svc.Volumes {
			src, rest, ok := strings.Cut(vol, ":")
			if !ok || !strings.HasPrefix(src, "./") {
				continue // a named volume
			}
			dst, _, _ := strings.Cut(rest, ":")
			if _, err := os.Stat(src); err != nil {
				t.Errorf("service %s mounts %s, which does not exist relative to the project directory", name, src)
			}
			mounts[dst] = src
			checked++
		}
		// An entrypoint script must be in one of the service's mounts.
		if len(svc.Entrypoint) == 2 && svc.Entrypoint[0] == "sh" {
			script := svc.Entrypoint[1]
			found := false
			for dst, src := range mounts {
				if rel, ok := strings.CutPrefix(script, dst+"/"); ok {
					found = true
					if _, err := os.Stat(src + "/" + rel); err != nil {
						t.Errorf("service %s runs %s, but %s/%s does not exist", name, script, src, rel)
					}
				}
			}
			if !found {
				t.Errorf("service %s runs %s, which none of its mounts provides", name, script)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no bind sources found to check; the test is no longer exercising anything")
	}
}
