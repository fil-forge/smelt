package smelt_test

import (
	"os"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/template"
	"gopkg.in/yaml.v3"
)

// The services that export traces, and the service name each runs under in
// its compose file.
var tracedServices = map[string]string{
	"systems/ingot/compose.yml":  "ingot",
	"systems/upload/compose.yml": "upload",
	"systems/hilt/compose.yml":   "hilt",
}

type composeEnv struct {
	Services map[string]struct {
		Environment []string `yaml:"environment"`
	} `yaml:"services"`
}

// otelEntries returns a service's OTEL_* environment entries in file order.
func otelEntries(t *testing.T, path, svc string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c composeEnv
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	s, ok := c.Services[svc]
	if !ok {
		t.Fatalf("%s: service %q not found", path, svc)
	}
	var out []string
	for _, e := range s.Environment {
		if strings.HasPrefix(e, "OTEL_") {
			out = append(out, e)
		}
	}
	return out
}

// Each traced service takes its collector from OTEL_ENDPOINT or, failing
// that, OTEL_EXPORTER_OTLP_ENDPOINT, and takes the sampling ratio and
// resource attributes bare, so an unset shell leaves them out of the
// container.
func TestTracedServicesOTELEnvironment(t *testing.T) {
	want := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_ENDPOINT:-${OTEL_EXPORTER_OTLP_ENDPOINT:-}}",
		"OTEL_TRACES_SAMPLER_ARG",
		"OTEL_RESOURCE_ATTRIBUTES",
	}
	for path, svc := range tracedServices {
		got := otelEntries(t, path, svc)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: %s OTEL entries\n got %q\nwant %q", path, svc, got, want)
		}
	}
}

// The endpoint expression, interpolated the way compose does it: unset gives
// the empty value the services read as tracing off, and OTEL_ENDPOINT (the
// name smelt documents) wins over OTEL_EXPORTER_OTLP_ENDPOINT.
func TestTraceEndpointInterpolation(t *testing.T) {
	const expr = "${OTEL_ENDPOINT:-${OTEL_EXPORTER_OTLP_ENDPOINT:-}}"
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unset", nil, ""},
		{"both empty", map[string]string{"OTEL_ENDPOINT": "", "OTEL_EXPORTER_OTLP_ENDPOINT": ""}, ""},
		{"smelt name", map[string]string{"OTEL_ENDPOINT": "http://a:4318"}, "http://a:4318"},
		{"standard name", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://b:4318"}, "http://b:4318"},
		{"both", map[string]string{"OTEL_ENDPOINT": "http://a:4318", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://b:4318"}, "http://a:4318"},
		{"smelt name empty", map[string]string{"OTEL_ENDPOINT": "", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://b:4318"}, "http://b:4318"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := template.Substitute(expr, func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
