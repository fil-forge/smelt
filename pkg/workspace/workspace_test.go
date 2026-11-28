package workspace

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveTargetArch(t *testing.T) {
	dockerSays := func(arch string) func() (string, error) {
		return func() (string, error) { return arch, nil }
	}
	dockerFails := func() (string, error) { return "", errors.New("no daemon") }

	cases := []struct {
		name       string
		override   string
		docker     func() (string, error)
		host       string
		wantArch   string
		wantSource string
	}{
		{"SMELT_GOARCH wins over everything", "amd64", dockerSays("arm64"), "arm64", "amd64", "SMELT_GOARCH"},
		{"SMELT_GOARCH is normalized", " ARM64\n", dockerSays("amd64"), "amd64", "arm64", "SMELT_GOARCH"},
		{"docker server arch wins over the host", "", dockerSays("arm64"), "amd64", "arm64", "docker server"},
		{"host arch when docker is unreachable", "", dockerFails, "amd64", "amd64", "host (docker server arch unavailable)"},
		{"host arch when docker reports an unsupported arch", "", dockerSays("s390x"), "arm64", "arm64", "host (docker server arch unavailable)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arch, source, err := resolveTargetArch(tc.override, tc.docker, tc.host)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if arch != tc.wantArch || source != tc.wantSource {
				t.Errorf("got (%q, %q), want (%q, %q)", arch, source, tc.wantArch, tc.wantSource)
			}
		})
	}

	unsupported := []string{"s390x", "x86_64", "aarch64"}
	for _, override := range unsupported {
		t.Run("SMELT_GOARCH="+override+" is rejected", func(t *testing.T) {
			_, _, err := resolveTargetArch(override, dockerSays("amd64"), "amd64")
			if err == nil || !strings.Contains(err.Error(), override) {
				t.Errorf("want error naming %q, got %v", override, err)
			}
		})
	}
}

func TestRenderOverrideBinariesAndConfigs(t *testing.T) {
	data, err := RenderOverride(
		map[string]string{"ingot": "/host/bin/ingot", "piri": "/host/bin/piri"},
		map[string]string{"ingot": "/host/cfg/config.yaml"},
		Instances{Piri: []string{"piri-0", "piri-1"}, Ingots: []string{"ingot"}},
	)
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}
	out := string(data)

	for _, want := range []string{
		"/host/bin/ingot:/usr/bin/ingot:ro",
		"/host/cfg/config.yaml:/etc/ingot/config.yaml:ro",
		"piri-0:",
		"piri-1:",
		"/host/bin/piri:/usr/bin/piri:ro",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("override missing %q:\n%s", want, out)
		}
	}
}

func TestRenderOverrideRegistrarFanOut(t *testing.T) {
	// upload and hilt binaries must also be mounted into their one-shot
	// registrar services, which run the same image's CLI — otherwise a
	// workspace build would test a local server against the published CLI.
	data, err := RenderOverride(
		map[string]string{"upload": "/host/bin/sprue", "hilt": "/host/bin/hilt"},
		nil, Instances{Hilts: []string{"hilt"}},
	)
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}
	out := string(data)

	for _, want := range []string{
		"upload-init:",
		"hilt-init:",
		"/host/bin/sprue:/usr/bin/sprue:ro",
		"/host/bin/hilt:/usr/bin/hilt:ro",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("override missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "/host/bin/sprue:/usr/bin/sprue:ro"); got != 2 {
		t.Errorf("sprue binary mounted %d time(s), want 2 (upload + upload-init):\n%s", got, out)
	}
}

func TestRenderOverrideUnknownService(t *testing.T) {
	if _, err := RenderOverride(map[string]string{"nope": "/x"}, nil, Instances{}); err == nil {
		t.Fatal("expected error for unknown binary service")
	}
	if _, err := RenderOverride(nil, map[string]string{"nope": "/x"}, Instances{}); err == nil {
		t.Fatal("expected error for unknown config service")
	}
}

// A hilt config override reaches every hilt server and none of their
// registrars, which run the image's CLI.
func TestRenderOverrideHiltConfig(t *testing.T) {
	data, err := RenderOverride(nil,
		map[string]string{"hilt": "/host/cfg/hilt.yaml"},
		Instances{Hilts: []string{"hilt", "hilt-1"}},
	)
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}
	var doc struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing override: %v", err)
	}
	want := "/host/cfg/hilt.yaml:/etc/hilt/config.yaml:ro"
	for _, name := range []string{"hilt", "hilt-1"} {
		if got := doc.Services[name].Volumes; len(got) != 1 || got[0] != want {
			t.Errorf("%s volumes = %v, want [%s]", name, got, want)
		}
	}
	for _, name := range []string{"hilt-init", "hilt-1-init"} {
		if _, ok := doc.Services[name]; ok {
			t.Errorf("%s got a config mount:\n%s", name, data)
		}
	}
}

func TestRenderOverrideNoConfigPath(t *testing.T) {
	// guppy has no registered configPath — config override must error, not
	// silently mount nowhere.
	if _, err := RenderOverride(nil, map[string]string{"guppy": "/x"}, Instances{}); err == nil {
		t.Fatal("expected error for service without a config path")
	}
}

func TestContainers(t *testing.T) {
	got, err := Containers([]string{"piri", "upload", "ingot", "delegator"}, Instances{Piri: []string{"piri-0", "piri-1"}, Ingots: []string{"ingot"}})
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	want := []string{"delegator", "delegator-init", "ingot", "piri-0", "piri-1", "upload", "upload-init"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestContainersUnknownService(t *testing.T) {
	if _, err := Containers([]string{"nope"}, Instances{}); err == nil {
		t.Fatal("expected error for unknown service")
	}
}

// A manifest declaring several networks runs several hilts and gateways. A
// build that reached only the leading pair would leave the rest on the
// published image, which reads as the change not taking effect.
func TestRenderOverrideReachesEveryInstance(t *testing.T) {
	data, err := RenderOverride(
		map[string]string{"hilt": "/host/bin/hilt", "ingot": "/host/bin/ingot"},
		nil,
		Instances{Hilts: []string{"hilt", "hilt-1"}, Ingots: []string{"ingot", "ingot-1", "ingot-2"}},
	)
	if err != nil {
		t.Fatalf("RenderOverride: %v", err)
	}
	out := string(data)

	// Each hilt and its own registrar, and every gateway. Anchored to a
	// service key: the mount paths hold these names too.
	for _, want := range []string{"hilt", "hilt-init", "hilt-1", "hilt-1-init", "ingot", "ingot-1", "ingot-2"} {
		key := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(want) + `:$`)
		if !key.MatchString(out) {
			t.Errorf("override is missing service %q:\n%s", want, out)
		}
	}
}

func TestContainersReachesEveryInstance(t *testing.T) {
	got, err := Containers(
		[]string{"hilt", "ingot"},
		Instances{Hilts: []string{"hilt", "hilt-1"}, Ingots: []string{"ingot", "ingot-1"}},
	)
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	want := []string{"hilt", "hilt-1", "hilt-1-init", "hilt-init", "ingot", "ingot-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An environment override reaches every instance running the service, a
// hilt's servers and not its registrars, and passes a "$" through literally.
func TestRenderEnvOverride(t *testing.T) {
	data, err := RenderEnvOverride(map[string]map[string]string{
		"hilt":  {"HILT_LOG_LEVEL": "debug", "HILT_PASSWORD": "a$b"},
		"piri":  {"PIRI_LOG_LEVEL": "warn"},
		"swarf": {"SWARF_LOG_LEVEL": "error"},
	}, Instances{Hilts: []string{"hilt", "hilt-1"}, Piri: []string{"piri-0", "piri-1"}})
	if err != nil {
		t.Fatalf("RenderEnvOverride: %v", err)
	}
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing override: %v", err)
	}
	for svc, want := range map[string]map[string]string{
		"hilt":   {"HILT_LOG_LEVEL": "debug", "HILT_PASSWORD": "a$$b"},
		"hilt-1": {"HILT_LOG_LEVEL": "debug", "HILT_PASSWORD": "a$$b"},
		"piri-0": {"PIRI_LOG_LEVEL": "warn"},
		"piri-1": {"PIRI_LOG_LEVEL": "warn"},
		"swarf":  {"SWARF_LOG_LEVEL": "error"},
	} {
		got := doc.Services[svc].Environment
		if len(got) != len(want) {
			t.Errorf("%s environment = %v, want %v", svc, got, want)
			continue
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s %s = %q, want %q", svc, k, got[k], v)
			}
		}
	}
	for _, name := range []string{"hilt-init", "hilt-1-init"} {
		if _, ok := doc.Services[name]; ok {
			t.Errorf("%s got environment:\n%s", name, data)
		}
	}
}

func TestRenderEnvOverrideRejectsBadInput(t *testing.T) {
	for _, name := range []string{"", "A=B", "A B"} {
		if _, err := RenderEnvOverride(map[string]map[string]string{"swarf": {name: "x"}}, Instances{}); err == nil {
			t.Errorf("expected an error for variable name %q", name)
		}
	}
	// A manifest with no hilt leaves a hilt override nowhere to go.
	if _, err := RenderEnvOverride(map[string]map[string]string{"hilt": {"HILT_X": "1"}}, Instances{}); err == nil {
		t.Error("expected an error for a service with no instances")
	}
}
