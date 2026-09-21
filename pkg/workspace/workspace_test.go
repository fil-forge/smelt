package workspace

import (
	"errors"
	"strings"
	"testing"
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
		[]string{"piri-0", "piri-1"},
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
		nil, nil,
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
	if _, err := RenderOverride(map[string]string{"nope": "/x"}, nil, nil); err == nil {
		t.Fatal("expected error for unknown binary service")
	}
	if _, err := RenderOverride(nil, map[string]string{"nope": "/x"}, nil); err == nil {
		t.Fatal("expected error for unknown config service")
	}
}

func TestRenderOverrideNoConfigPath(t *testing.T) {
	// guppy has no registered configPath — config override must error, not
	// silently mount nowhere.
	if _, err := RenderOverride(nil, map[string]string{"guppy": "/x"}, nil); err == nil {
		t.Fatal("expected error for service without a config path")
	}
}

func TestContainers(t *testing.T) {
	got, err := Containers([]string{"piri", "upload", "ingot"}, []string{"piri-0", "piri-1"})
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	want := []string{"ingot", "piri-0", "piri-1", "upload", "upload-init"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestContainersUnknownService(t *testing.T) {
	if _, err := Containers([]string{"nope"}, nil); err == nil {
		t.Fatal("expected error for unknown service")
	}
}
