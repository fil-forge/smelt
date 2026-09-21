package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SessionManifestPath is the path of the scratch-dir session manifest,
// relative to the project root. A snapshot load writes the loaded
// snapshot's smelt.yml here so subsequent generate/save calls drive off
// the snapshot's topology without touching the tracked project manifest.
const SessionManifestPath = "generated/snapshot-scratch/smelt.yml"

// ProjectManifestPath is the tracked manifest at the project root.
const ProjectManifestPath = "smelt.yml"

// ManifestEnvVar names the environment variable that overrides the manifest
// path. `SMELT_MANIFEST=manifests/piri-1-postgres-filesystem.yml make up`
// drives the whole stack off that file without editing the tracked smelt.yml.
// A relative value is resolved against the project directory.
const ManifestEnvVar = "SMELT_MANIFEST"

// ManifestSource says where ResolveManifestPath found the manifest.
type ManifestSource string

const (
	// SourceEnv: the path came from SMELT_MANIFEST.
	SourceEnv ManifestSource = "SMELT_MANIFEST"
	// SourceSession: a snapshot session is active and its manifest is used.
	SourceSession ManifestSource = "snapshot session"
	// SourceProject: the tracked smelt.yml at the project root.
	SourceProject ManifestSource = "project"
)

// ResolveManifestPath returns the manifest smelt should drive off for the
// given project, and where it came from. Precedence: SMELT_MANIFEST, then the
// session manifest of an active snapshot session, then the tracked project
// manifest. Every code path that needs the topology (generate, workspace
// build, snapshot save) goes through here, so one variable steers them all.
func ResolveManifestPath(projectDir string) (string, ManifestSource) {
	if env := os.Getenv(ManifestEnvVar); env != "" {
		if filepath.IsAbs(env) {
			return env, SourceEnv
		}
		return filepath.Join(projectDir, env), SourceEnv
	}
	session := filepath.Join(projectDir, SessionManifestPath)
	if _, err := os.Stat(session); err == nil {
		return session, SourceSession
	}
	return filepath.Join(projectDir, ProjectManifestPath), SourceProject
}

// Parse reads a smelt.yml manifest from the given path.
func Parse(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return ParseBytes(data)
}

// ParseBytes parses a smelt.yml manifest from raw bytes.
func ParseBytes(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}
