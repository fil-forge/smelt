package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/manifest"
	"gopkg.in/yaml.v3"
)

func externalS3(prefix string) *manifest.S3Spec {
	return &manifest.S3Spec{Endpoint: "s3.us-east-2.amazonaws.com", BucketPrefix: prefix}
}

func generateCompose(t *testing.T, nodes []manifest.ResolvedPiriNode) (string, ComposeFile) {
	t.Helper()
	data, err := GeneratePiriCompose(nodes)
	if err != nil {
		t.Fatal(err)
	}
	var compose ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("unmarshal generated compose: %v", err)
	}
	return string(data), compose
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// assertNetworksDeclared checks that every network a service joins is either
// declared in the file or the external forge-network.
func assertNetworksDeclared(t *testing.T, compose ComposeFile) {
	t.Helper()
	for name, svc := range compose.Services {
		for _, n := range svc.Networks {
			if n == "forge-network" {
				continue
			}
			if _, ok := compose.Networks[n]; !ok {
				t.Errorf("service %s joins undeclared network %s", name, n)
			}
		}
	}
}

func TestGeneratePiriComposeExternalS3(t *testing.T) {
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: "postgres", Blob: "s3", S3: externalS3("example-box-")}},
	}
	out, compose := generateCompose(t, nodes)

	if strings.Contains(out, "piri-minio") || strings.Contains(out, "minioadmin") {
		t.Error("an external-S3 stack should not reference piri-minio or its root login")
	}
	svc := compose.Services["piri-0"]
	for _, want := range []string{
		"PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com",
		"PIRI_S3_BUCKET_PREFIX=example-box-piri-0-",
		"PIRI_S3_ACCESS_KEY_ID=${SMELT_PIRI_S3_ACCESS_KEY_ID:-}",
		"PIRI_S3_SECRET_ACCESS_KEY=${SMELT_PIRI_S3_SECRET_ACCESS_KEY:-}",
		"PIRI_S3_INSECURE=false",
	} {
		if !hasString(svc.Environment, want) {
			t.Errorf("piri-0 environment lacks %q", want)
		}
	}
	// Postgres still needs the storage network.
	if !hasString(svc.Networks, "piri-storage-net") {
		t.Error("a postgres node should join piri-storage-net")
	}
	assertNetworksDeclared(t, compose)
}

func TestGeneratePiriComposeExternalS3Insecure(t *testing.T) {
	s3 := externalS3("")
	s3.Endpoint = "minio.example.internal:9000"
	insecure := true
	s3.Insecure = &insecure
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: "postgres", Blob: "s3", S3: s3}},
	}
	_, compose := generateCompose(t, nodes)
	env := compose.Services["piri-0"].Environment
	for _, want := range []string{"PIRI_S3_INSECURE=true", "PIRI_S3_BUCKET_PREFIX=piri-0-", "PIRI_S3_ENDPOINT=minio.example.internal:9000"} {
		if !hasString(env, want) {
			t.Errorf("piri-0 environment lacks %q", want)
		}
	}
}

func TestGeneratePiriComposeMixedS3(t *testing.T) {
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: "postgres", Blob: "s3", S3: externalS3("example-box-")}},
		{Name: "piri-1", Index: 1, Storage: manifest.StorageSpec{DB: "sqlite", Blob: "s3"}},
	}
	_, compose := generateCompose(t, nodes)

	if _, ok := compose.Services["piri-minio"]; !ok {
		t.Fatal("piri-1 uses the stack's MinIO, so piri-minio should be present")
	}
	if _, ok := compose.Services["piri-0"].DependsOn["piri-minio"]; ok {
		t.Error("the external node should not depend on piri-minio")
	}
	if _, ok := compose.Services["piri-1"].DependsOn["piri-minio"]; !ok {
		t.Error("the in-stack node should depend on piri-minio")
	}
	if !hasString(compose.Services["piri-0"].Environment, "PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com") {
		t.Error("piri-0 should point at the external endpoint")
	}
	for _, want := range []string{"PIRI_S3_ENDPOINT=piri-minio:9000", "PIRI_S3_ACCESS_KEY_ID=minioadmin", "PIRI_S3_INSECURE=true", "PIRI_S3_BUCKET_PREFIX=piri-1-"} {
		if !hasString(compose.Services["piri-1"].Environment, want) {
			t.Errorf("piri-1 environment lacks %q", want)
		}
	}
	if !hasString(compose.Services["piri-1"].Networks, "piri-storage-net") {
		t.Error("the in-stack S3 node should join piri-storage-net")
	}
	assertNetworksDeclared(t, compose)
}

func TestGeneratePiriComposeSQLiteExternalS3(t *testing.T) {
	nodes := []manifest.ResolvedPiriNode{
		{Name: "piri-0", Index: 0, Storage: manifest.StorageSpec{DB: "sqlite", Blob: "s3", S3: externalS3("example-box-")}},
	}
	out, compose := generateCompose(t, nodes)

	if strings.Contains(out, "piri-storage-net") {
		t.Error("sqlite with external S3 needs no storage network")
	}
	if strings.Contains(out, "piri-minio") || strings.Contains(out, "piri-postgres") {
		t.Error("sqlite with external S3 needs no storage services")
	}
	assertNetworksDeclared(t, compose)
}

// TestPiriEntrypointRefusesEmptyExternalCredentials runs the entrypoint's
// credential check. It exits before touching piri or the filesystem when
// the check fails; when the check passes the script goes on and fails
// later for lack of a container, so only the check's message is asserted.
func TestPiriEntrypointRefusesEmptyExternalCredentials(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "systems", "piri", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const refusal = "is outside the stack"

	tests := []struct {
		name       string
		env        []string
		wantRefuse bool
	}{
		{"external, both empty", []string{"PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com", "PIRI_S3_ACCESS_KEY_ID=", "PIRI_S3_SECRET_ACCESS_KEY="}, true},
		{"external, secret empty", []string{"PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com", "PIRI_S3_ACCESS_KEY_ID=AKIDEXAMPLE", "PIRI_S3_SECRET_ACCESS_KEY="}, true},
		{"external, unset", []string{"PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com"}, true},
		{"external, both set", []string{"PIRI_S3_ENDPOINT=s3.us-east-2.amazonaws.com", "PIRI_S3_ACCESS_KEY_ID=AKIDEXAMPLE", "PIRI_S3_SECRET_ACCESS_KEY=example"}, false},
		{"in-stack minio, defaults", []string{"PIRI_S3_ENDPOINT=piri-minio:9000"}, false},
		{"endpoint unset", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A scratch dir as cwd and a PATH without piri keep the passing
			// cases from doing anything beyond failing on the missing binary.
			cmd := exec.Command(sh, script)
			cmd.Dir = t.TempDir()
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "PIRI_BLOB_BACKEND=s3"}, tt.env...)
			out, err := cmd.CombinedOutput()
			refused := strings.Contains(string(out), refusal)
			if refused != tt.wantRefuse {
				t.Errorf("refused=%v, want %v; output:\n%s", refused, tt.wantRefuse, out)
			}
			if tt.wantRefuse && err == nil {
				t.Error("expected a non-zero exit")
			}
		})
	}
}
