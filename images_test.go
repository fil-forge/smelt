package smelt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Third-party images read a <NAME>_IMAGE variable whose default is the tag
// the stack used before the variable existed, so an unset variable renders
// the same compose config and a set one repins exactly these services.
var thirdPartyImages = map[string]map[string]string{
	"systems/ingot/compose.yml": {
		"ingot-openbao":      "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		"ingot-openbao-init": "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		"ingot-postgres":     "${POSTGRES_IMAGE:-postgres:16-alpine}",
	},
	"systems/upload/compose.yml": {
		"postgres": "${POSTGRES_IMAGE:-postgres:16-alpine}",
	},
	"systems/hilt/compose.yml": {
		"hilt-vault":    "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		"hilt-postgres": "${POSTGRES_IMAGE:-postgres:16-alpine}",
	},
	"systems/swarf/compose.yml": {
		"swarf-postgres": "${POSTGRES_IMAGE:-postgres:16-alpine}",
	},
	"systems/plc/compose.yml": {
		"plc-postgres": "${POSTGRES_IMAGE:-postgres:16-alpine}",
	},
	"systems/common/compose.yml": {
		"dynamodb-local": "${DYNAMODB_LOCAL_IMAGE:-amazon/dynamodb-local:latest}",
		"email":          "${SMTP4DEV_IMAGE:-rnwood/smtp4dev:v3}",
	},
	"systems/indexing/indexer/compose.yml": {
		"redis": "${REDIS_IMAGE:-redis:7-alpine}",
	},
}

type composeImages struct {
	Services map[string]struct {
		Image string `yaml:"image"`
	} `yaml:"services"`
}

func readComposeImages(t *testing.T, path string) composeImages {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c composeImages
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

func TestThirdPartyImagesReadOverrideVariables(t *testing.T) {
	for path, want := range thirdPartyImages {
		c := readComposeImages(t, path)
		for svc, image := range want {
			got, ok := c.Services[svc]
			if !ok {
				t.Errorf("%s: service %q not found", path, svc)
				continue
			}
			if got.Image != image {
				t.Errorf("%s: %s image = %q, want %q", path, svc, got.Image, image)
			}
		}
	}
}

// Every image the root compose file runs is pinnable from the environment.
// The telemetry system is not part of the root stack and keeps its literals.
func TestIncludedComposeImagesAreInterpolated(t *testing.T) {
	root := readIncludes(t, "compose.yml")
	checked := 0
	for _, path := range root {
		if strings.HasPrefix(path, "generated/") {
			continue // covered by the generator's own tests
		}
		for _, p := range append([]string{path}, readIncludes(t, path)...) {
			for svc, s := range readComposeImages(t, p).Services {
				if s.Image == "" {
					continue // built locally
				}
				checked++
				if !strings.HasPrefix(s.Image, "${") {
					t.Errorf("%s: %s image %q has no override variable", p, svc, s.Image)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no images checked")
	}
}

// readIncludes returns the include paths of a compose file, relative to the
// repository root.
func readIncludes(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Include []struct {
			Path string `yaml:"path"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []string
	for _, inc := range c.Include {
		out = append(out, filepath.Join(filepath.Dir(path), inc.Path))
	}
	return out
}
