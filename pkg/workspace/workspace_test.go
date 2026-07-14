package workspace

import (
	"strings"
	"testing"
)

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
