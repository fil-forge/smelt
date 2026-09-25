package smelt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// stubPiri stands in for /usr/bin/piri: it reports a fixed DID, copies the
// base config `piri init` receives to $STUB_OUT, writes a minimal initialized
// config, and exits 0 on `serve`.
const stubPiri = `#!/bin/sh
case "$1" in
identity) echo "did:key:z6MkStubStubStub" ;;
init)
    for a in "$@"; do
        case "$a" in
            --base-config=*) cp "${a#--base-config=}" "$STUB_OUT/base-config.toml" ;;
            --data-dir=*) data="${a#--data-dir=}" ;;
        esac
    done
    echo 'proof_set = 1' > "$data/piri-config.toml"
    ;;
serve) exit 0 ;;
esac
`

// entrypointRun is one execution of systems/piri/entrypoint.sh against a
// temporary root that mirrors the container's paths.
type entrypointRun struct {
	root   string
	out    string
	output string
	err    error
}

func newEntrypointRoot(t *testing.T) *entrypointRun {
	t.Helper()
	root := t.TempDir()
	r := &entrypointRun{root: root, out: filepath.Join(root, "out")}
	for _, d := range []string{"keys", "config", "data/piri", "tmp/piri", "usr/bin", "scripts", "out"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"piri-base-config.toml", "piri-indexing.toml", "piri-overrides.toml"} {
		copyFile(t, filepath.Join("systems/piri/config", f), filepath.Join(root, "config", f))
	}
	writeFile(t, filepath.Join(root, "usr/bin/piri"), stubPiri, 0o755)
	writeFile(t, filepath.Join(root, "scripts/register-did.sh"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFile(t, filepath.Join(root, "keys/piri.pem"), "stub", 0o644)

	src, err := os.ReadFile("systems/piri/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	rewrite := strings.NewReplacer(
		"/keys/", root+"/keys/",
		"/config/", root+"/config/",
		"/data/piri", root+"/data/piri",
		"/tmp/piri", root+"/tmp/piri",
		"/usr/bin/piri", root+"/usr/bin/piri",
		"/scripts/", root+"/scripts/",
	)
	writeFile(t, filepath.Join(root, "entrypoint.sh"), rewrite.Replace(string(src)), 0o755)
	return r
}

func (r *entrypointRun) run(t *testing.T, env ...string) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(r.root, "entrypoint.sh"))
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "STUB_OUT=" + r.out}, env...)
	out, err := cmd.CombinedOutput()
	r.output, r.err = string(out), err
}

func (r *entrypointRun) baseConfig(t *testing.T) map[string]any {
	t.Helper()
	return parseTOML(t, filepath.Join(r.out, "base-config.toml"))
}

func parseTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, string(data), 0o644)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// testdata/piri-base-config-indexer-on.toml is the single base config smelt
// shipped before the indexer tables moved to piri-indexing.toml.
func TestPiriEntrypointIndexerDefaultMatchesSingleFile(t *testing.T) {
	want := parseTOML(t, "testdata/piri-base-config-indexer-on.toml")
	for _, env := range [][]string{nil, {"PIRI_INDEXER=on"}} {
		r := newEntrypointRoot(t)
		r.run(t, env...)
		if r.err != nil {
			t.Fatalf("env %v: entrypoint failed: %v\n%s", env, r.err, r.output)
		}
		if got := r.baseConfig(t); !reflect.DeepEqual(got, want) {
			t.Errorf("env %v: assembled base config\n got %v\nwant %v", env, got, want)
		}
	}
}

func TestPiriEntrypointIndexerOff(t *testing.T) {
	r := newEntrypointRoot(t)
	r.run(t, "PIRI_INDEXER=off")
	if r.err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", r.err, r.output)
	}
	got := r.baseConfig(t)

	want := parseTOML(t, "testdata/piri-base-config-indexer-on.toml")
	services := want["ucan"].(map[string]any)["services"].(map[string]any)
	delete(services, "indexer")
	delete(services, "publisher")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembled base config\n got %v\nwant %v", got, want)
	}
}

func TestPiriEntrypointIndexerRejectsOtherValues(t *testing.T) {
	r := newEntrypointRoot(t)
	r.run(t, "PIRI_INDEXER=false")
	if r.err == nil {
		t.Fatalf("entrypoint accepted PIRI_INDEXER=false\n%s", r.output)
	}
	if !strings.Contains(r.output, "PIRI_INDEXER must be on or off") {
		t.Errorf("missing error message:\n%s", r.output)
	}
	if _, err := os.Stat(filepath.Join(r.out, "base-config.toml")); err == nil {
		t.Error("piri init ran despite the invalid value")
	}
}

func TestPiriEntrypointIndexerOffWarnsOnInitializedConfig(t *testing.T) {
	initialized := "proof_set = 1\n\n[ucan]\n  [ucan.services]\n    [ucan.services.indexer]\n      did = \"did:web:indexer\"\n      url = \"http://indexer:80/claims\"\n"
	tests := []struct {
		name     string
		config   string
		env      string
		wantWarn bool
	}{
		{"off with indexer", initialized, "PIRI_INDEXER=off", true},
		{"on with indexer", initialized, "PIRI_INDEXER=on", false},
		{"off without indexer", "proof_set = 1\n\n[ucan.services.upload]\nurl = \"http://upload:80\"\n", "PIRI_INDEXER=off", false},
		{"off with proof only", "proof_set = 1\n\n[ucan.services.indexer]\nproof = \"x\"\n\n[ucan.services.upload]\nurl = \"http://upload:80\"\n", "PIRI_INDEXER=off", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newEntrypointRoot(t)
			writeFile(t, filepath.Join(r.root, "data/piri/piri-config.toml"), tt.config, 0o644)
			r.run(t, tt.env)
			if r.err != nil {
				t.Fatalf("entrypoint failed: %v\n%s", r.err, r.output)
			}
			if !strings.Contains(r.output, "Config exists, skipping init") {
				t.Fatalf("init was not skipped:\n%s", r.output)
			}
			if got := strings.Contains(r.output, "WARNING: PIRI_INDEXER=off"); got != tt.wantWarn {
				t.Errorf("warning printed = %v, want %v\n%s", got, tt.wantWarn, r.output)
			}
		})
	}
}
