package manifest

import (
	"strings"
	"testing"
)

func resolveYAML(t *testing.T, src string) ([]ResolvedPiriNode, error) {
	t.Helper()
	m, err := ParseBytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return m.Resolve()
}

func TestS3ExternalEndpoint(t *testing.T) {
	nodes, err := resolveYAML(t, `
version: 1
piri:
  nodes:
    - storage:
        db: postgres
        blob: s3
        s3:
          endpoint: s3.us-east-2.amazonaws.com
          bucket_prefix: example-box-
`)
	if err != nil {
		t.Fatal(err)
	}
	s3 := nodes[0].Storage.S3
	if !s3.External() {
		t.Fatal("expected an external S3 spec")
	}
	if s3.Endpoint != "s3.us-east-2.amazonaws.com" || s3.BucketPrefix != "example-box-" {
		t.Errorf("unexpected spec %+v", *s3)
	}
	if s3.Insecure != nil {
		t.Errorf("insecure should stay unset, got %v", *s3.Insecure)
	}
}

func TestS3UnsetKeepsNil(t *testing.T) {
	nodes, err := resolveYAML(t, `
version: 1
piri:
  nodes:
    - storage:
        blob: s3
`)
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Storage.S3 != nil || nodes[0].Storage.S3.External() {
		t.Errorf("expected no S3 spec, got %+v", nodes[0].Storage.S3)
	}
}

func TestS3DefaultsMergeFieldByField(t *testing.T) {
	nodes, err := resolveYAML(t, `
version: 1
piri:
  defaults:
    storage:
      blob: s3
      s3:
        endpoint: s3.us-east-2.amazonaws.com
        bucket_prefix: shared-
        insecure: false
  nodes:
    - {}
    - storage:
        s3:
          bucket_prefix: other-
          insecure: true
    - storage:
        blob: filesystem
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodes[0].Storage.S3; got.Endpoint != "s3.us-east-2.amazonaws.com" || got.BucketPrefix != "shared-" || *got.Insecure {
		t.Errorf("node 0 should inherit the defaults, got %+v", *got)
	}
	if got := nodes[1].Storage.S3; got.Endpoint != "s3.us-east-2.amazonaws.com" || got.BucketPrefix != "other-" || !*got.Insecure {
		t.Errorf("node 1 should override prefix and insecure only, got %+v", *got)
	}
	if nodes[2].Storage.S3 != nil {
		t.Errorf("a filesystem node should not inherit the defaults' s3 block, got %+v", *nodes[2].Storage.S3)
	}
}

func TestS3ValidationErrors(t *testing.T) {
	tests := []struct {
		name, storage, want string
	}{
		{"needs blob s3", "blob: filesystem\n        s3: {endpoint: s3.us-east-2.amazonaws.com}", "requires blob"},
		{"scheme in endpoint", "blob: s3\n        s3: {endpoint: 'https://s3.us-east-2.amazonaws.com'}", "without a scheme"},
		{"uppercase prefix", "blob: s3\n        s3: {endpoint: s3.example.com, bucket_prefix: Box-}", "must match"},
		{"prefix starts with dash", "blob: s3\n        s3: {endpoint: s3.example.com, bucket_prefix: -box-}", "must match"},
		{"prefix with underscore", "blob: s3\n        s3: {endpoint: s3.example.com, bucket_prefix: my_box-}", "must match"},
		// 43 + len("piri-0-consolidation") = 63 is the limit; 44 is one over.
		{"bucket too long", "blob: s3\n        s3: {endpoint: s3.example.com, bucket_prefix: " + strings.Repeat("a", 44) + "}", "longer than 63"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveYAML(t, "version: 1\npiri:\n  nodes:\n    - storage:\n        "+tt.storage+"\n")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestS3BucketNameAtLimit(t *testing.T) {
	_, err := resolveYAML(t, "version: 1\npiri:\n  nodes:\n    - storage:\n        blob: s3\n        s3: {endpoint: s3.example.com, bucket_prefix: "+strings.Repeat("a", 43)+"}\n")
	if err != nil {
		t.Fatalf("a 63-character bucket name should pass: %v", err)
	}
}
