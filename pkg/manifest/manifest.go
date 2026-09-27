// Package manifest defines the smelt.yml schema and resolves it into
// concrete node configurations ready for compose generation.
package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// MaxPiriNodes is limited by the number of Anvil pre-funded accounts.
	// Account 0 = piri-0 wallet, account 1 = payer (signing-service),
	// accounts 2-9 = piri-1 through piri-8.
	MaxPiriNodes = 9

	DBSQLite   = "sqlite"
	DBPostgres = "postgres"
	BlobFS     = "filesystem"
	BlobS3     = "s3"

	// maxBucketNameLen is the S3 limit on bucket name length.
	maxBucketNameLen = 63
	// longestPiriBucket is the longest of the store names piri appends to
	// its bucket prefix (allocations, acceptances, claims, receipts, pdp,
	// consolidation).
	longestPiriBucket = "consolidation"
)

var bucketPrefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// Manifest is the top-level smelt.yml schema.
type Manifest struct {
	Version int      `yaml:"version"`
	Piri    PiriSpec `yaml:"piri"`
}

// PiriSpec describes the desired piri node topology.
// Use either Count (shorthand for N identical nodes) or Nodes (explicit per-node config).
type PiriSpec struct {
	Count    int            `yaml:"count,omitempty"`
	Defaults PiriDefaults   `yaml:"defaults,omitempty"`
	Nodes    []PiriNodeSpec `yaml:"nodes,omitempty"`
}

// PiriDefaults are inherited by every node unless overridden.
type PiriDefaults struct {
	Image   string      `yaml:"image,omitempty"`
	Storage StorageSpec `yaml:"storage,omitempty"`
}

// PiriNodeSpec describes a single piri node.
type PiriNodeSpec struct {
	Name    string      `yaml:"name,omitempty"`
	Image   string      `yaml:"image,omitempty"`
	Storage StorageSpec `yaml:"storage,omitempty"`
}

// StorageSpec controls piri's database and blob backends.
type StorageSpec struct {
	DB   string  `yaml:"db,omitempty"`
	Blob string  `yaml:"blob,omitempty"`
	S3   *S3Spec `yaml:"s3,omitempty"`
}

// S3Spec points an S3-backed node at a store outside the stack. Unset, or
// with an empty endpoint, the node uses the stack's piri-minio. Credentials
// never appear here: the generated compose reads them from the shell as
// SMELT_PIRI_S3_ACCESS_KEY_ID and SMELT_PIRI_S3_SECRET_ACCESS_KEY.
type S3Spec struct {
	// Endpoint is host[:port] with no scheme. For AWS use the regional
	// host (s3.<region>.amazonaws.com): piri passes no region, and its S3
	// client derives the signing region from the hostname.
	Endpoint string `yaml:"endpoint,omitempty"`
	// BucketPrefix is prepended to "<node>-"; piri appends each store name.
	BucketPrefix string `yaml:"bucket_prefix,omitempty"`
	// Insecure selects plain HTTP. Defaults to false (TLS).
	Insecure *bool `yaml:"insecure,omitempty"`
}

// External reports whether the spec names an endpoint outside the stack.
func (s *S3Spec) External() bool {
	return s != nil && s.Endpoint != ""
}

// ResolvedPiriNode is a fully resolved node ready for compose generation.
type ResolvedPiriNode struct {
	Name    string
	Index   int
	Image   string
	Storage StorageSpec
}

// Resolve normalizes the manifest into a concrete list of resolved nodes.
func (m *Manifest) Resolve() ([]ResolvedPiriNode, error) {
	spec := &m.Piri

	if spec.Count > 0 && len(spec.Nodes) > 0 {
		return nil, fmt.Errorf("manifest: cannot specify both 'count' and 'nodes'")
	}

	// Expand count shorthand into explicit nodes.
	var nodes []PiriNodeSpec
	if spec.Count > 0 {
		nodes = make([]PiriNodeSpec, spec.Count)
	} else if len(spec.Nodes) > 0 {
		nodes = spec.Nodes
	} else {
		// Default: single node.
		nodes = []PiriNodeSpec{{}}
	}

	if len(nodes) > MaxPiriNodes {
		return nil, fmt.Errorf("manifest: %d piri nodes exceeds maximum of %d (limited by Anvil accounts)", len(nodes), MaxPiriNodes)
	}

	// Apply defaults and auto-generate names.
	resolved := make([]ResolvedPiriNode, len(nodes))
	seen := make(map[string]bool)

	for i, n := range nodes {
		r := ResolvedPiriNode{Index: i}

		// Name
		if n.Name != "" {
			r.Name = n.Name
		} else {
			r.Name = fmt.Sprintf("piri-%d", i)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("manifest: duplicate node name %q", r.Name)
		}
		seen[r.Name] = true

		// Image: node override > defaults > empty (uses PIRI_IMAGE env var at runtime)
		r.Image = firstNonEmpty(n.Image, spec.Defaults.Image)

		// Storage: node override > defaults > hardcoded defaults
		r.Storage.DB = firstNonEmpty(n.Storage.DB, spec.Defaults.Storage.DB, DBSQLite)
		r.Storage.Blob = firstNonEmpty(n.Storage.Blob, spec.Defaults.Storage.Blob, BlobFS)
		// A defaults-level s3 block reaches only the nodes that store
		// blobs in S3, so defaults can serve a mixed topology. A node's own
		// s3 block is kept as written and validated below.
		defaultS3 := spec.Defaults.Storage.S3
		if r.Storage.Blob != BlobS3 {
			defaultS3 = nil
		}
		r.Storage.S3 = mergeS3(n.Storage.S3, defaultS3)

		if err := validateStorage(r.Name, r.Storage); err != nil {
			return nil, fmt.Errorf("manifest: node %q: %w", r.Name, err)
		}

		resolved[i] = r
	}

	return resolved, nil
}

// mergeS3 overlays node fields on the defaults, field by field. It returns
// nil when neither sets a block.
func mergeS3(node, defaults *S3Spec) *S3Spec {
	if node == nil && defaults == nil {
		return nil
	}
	var n, d S3Spec
	if node != nil {
		n = *node
	}
	if defaults != nil {
		d = *defaults
	}
	out := &S3Spec{
		Endpoint:     firstNonEmpty(n.Endpoint, d.Endpoint),
		BucketPrefix: firstNonEmpty(n.BucketPrefix, d.BucketPrefix),
		Insecure:     d.Insecure,
	}
	if n.Insecure != nil {
		out.Insecure = n.Insecure
	}
	return out
}

func validateStorage(name string, s StorageSpec) error {
	switch s.DB {
	case DBSQLite, DBPostgres:
	default:
		return fmt.Errorf("invalid db backend %q (must be %q or %q)", s.DB, DBSQLite, DBPostgres)
	}
	switch s.Blob {
	case BlobFS, BlobS3:
	default:
		return fmt.Errorf("invalid blob backend %q (must be %q or %q)", s.Blob, BlobFS, BlobS3)
	}
	if s.S3 == nil {
		return nil
	}
	if s.Blob != BlobS3 {
		return fmt.Errorf("storage.s3 requires blob %q (got %q)", BlobS3, s.Blob)
	}
	if strings.Contains(s.S3.Endpoint, "://") {
		return fmt.Errorf("storage.s3.endpoint %q must be host[:port] without a scheme", s.S3.Endpoint)
	}
	if s.S3.BucketPrefix != "" && !bucketPrefixRE.MatchString(s.S3.BucketPrefix) {
		return fmt.Errorf("storage.s3.bucket_prefix %q must match %s", s.S3.BucketPrefix, bucketPrefixRE)
	}
	if longest := s.S3.BucketPrefix + name + "-" + longestPiriBucket; len(longest) > maxBucketNameLen {
		return fmt.Errorf("storage.s3.bucket_prefix %q makes bucket %q longer than %d characters", s.S3.BucketPrefix, longest, maxBucketNameLen)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
