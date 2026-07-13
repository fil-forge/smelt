// Package workspace builds smelt service binaries from local sibling checkouts
// and renders a docker-compose override that mounts each binary over the
// published image's binary.
//
// It is the mechanism behind SMELT_WORKSPACE=1 (the make flow) and
// stack.WithWorkspaceBinaries() (the Go test stack). The active Go workspace
// (go.work) is the single source of truth for "what am I editing": every
// service whose module appears in the use-list is rebuilt from local source.
// Because the shared libforge library is resolved purely through the workspace
// (the siblings carry no replace directives), libforge appearing in the
// use-list forces every service to be rebuilt — otherwise a published binary
// would still link the published libforge pseudo-version.
//
// Binaries are compiled on the host, where go.work resolves local cross-module
// edits, then bind-mounted into otherwise-published containers. This needs no
// Dockerfiles and no go.work inside Docker.
package workspace

import (
	"bytes"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// serviceBuild describes how to build one smelt service from its sibling module.
type serviceBuild struct {
	moduleDir   string // go.work use-dir basename, e.g. "piri"
	buildTarget string // `go build` package arg, e.g. "./cmd"
	binPath     string // absolute path of the binary inside the container image
}

// Services maps smelt service name -> build descriptor. The map key doubles as
// the compose service name for every service except "piri", which fans out to
// the generated piri-N nodes. Verified against each sibling's Dockerfile — note
// delegator installs its binary as /usr/bin/registrar (binary name != module).
var Services = map[string]serviceBuild{
	"piri":            {moduleDir: "piri", buildTarget: "./cmd", binPath: "/usr/bin/piri"},
	"upload":          {moduleDir: "sprue", buildTarget: "./cmd/main.go", binPath: "/usr/bin/sprue"},
	"signing-service": {moduleDir: "piri-signing-service", buildTarget: ".", binPath: "/usr/bin/signer"},
	"indexer":         {moduleDir: "indexing-service", buildTarget: "./cmd", binPath: "/usr/bin/indexer"},
	"delegator":       {moduleDir: "delegator", buildTarget: ".", binPath: "/usr/bin/registrar"},
	"guppy":           {moduleDir: "guppy", buildTarget: ".", binPath: "/usr/bin/guppy"},
	"ingot":           {moduleDir: "ingot", buildTarget: "./cmd/ingot", binPath: "/usr/bin/ingot"},
}

// libforgeDir is the workspace dir of the shared library. Its presence in the
// use-list forces a rebuild of every service (see package doc).
const libforgeDir = "libforge"

// Detect inspects the active go.work and returns the workspace root directory
// (the dir containing go.work) and the sorted set of smelt services to build
// from local source. Returns an error when no workspace is active.
func Detect() (root string, services []string, err error) {
	gowork, err := goEnv("GOWORK")
	if err != nil {
		return "", nil, err
	}
	if gowork == "" || gowork == "off" {
		return "", nil, fmt.Errorf("no active go.work; create one at the fil-forge parent dir, e.g. `go work init ./smelt ./libforge ./piri` (see CLAUDE.md)")
	}
	root = filepath.Dir(gowork)

	dirs, err := parseUseDirs(gowork)
	if err != nil {
		return "", nil, fmt.Errorf("parse %s: %w", gowork, err)
	}
	inWorkspace := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		inWorkspace[filepath.Base(d)] = true
	}

	selected := map[string]bool{}
	if inWorkspace[libforgeDir] {
		for name := range Services {
			selected[name] = true
		}
	} else {
		for name, spec := range Services {
			if inWorkspace[spec.moduleDir] {
				selected[name] = true
			}
		}
	}
	for name := range selected {
		services = append(services, name)
	}
	sort.Strings(services)
	return root, services, nil
}

// BuildBinary compiles one service's binary from its sibling module under root
// into outDir, returning the absolute output path. It produces a static
// linux/amd64 binary with the workspace active so local cross-module edits
// (e.g. a local libforge) are compiled in.
func BuildBinary(root, service, outDir string) (string, error) {
	spec, ok := Services[service]
	if !ok {
		return "", fmt.Errorf("unknown service %q", service)
	}
	moduleRoot := filepath.Join(root, spec.moduleDir)
	if _, err := os.Stat(moduleRoot); err != nil {
		return "", fmt.Errorf("service %q module dir not found at %s: %w", service, moduleRoot, err)
	}
	// Resolve outDir to an absolute path: cmd.Dir below is the sibling module,
	// so a relative -o would land under the sibling instead of outDir.
	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absOutDir, 0755); err != nil {
		return "", err
	}

	out := filepath.Join(absOutDir, service)
	cmd := exec.Command(goTool(), "build", "-o", out, spec.buildTarget)
	cmd.Dir = moduleRoot
	// Static linux/amd64 build so the binary drops into the published image's
	// base cleanly. GOWORK is pinned explicitly so the build resolves the same
	// workspace regardless of the caller's cwd or environment.
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
		"GOWORK="+gowork(root),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build %s (%s in %s): %w\n%s", service, spec.buildTarget, moduleRoot, err, stderr.String())
	}
	return filepath.Abs(out)
}

// RenderOverride returns a docker-compose override (YAML) that mounts each built
// binary read-only over the published image's binary. binaries maps service
// name -> host binary path; piriNodes are the generated piri-N service names
// that each receive the piri binary mount.
func RenderOverride(binaries map[string]string, piriNodes []string) ([]byte, error) {
	type svc struct {
		Volumes []string `yaml:"volumes"`
	}
	doc := struct {
		Services map[string]svc `yaml:"services"`
	}{Services: map[string]svc{}}

	for service, hostPath := range binaries {
		spec, ok := Services[service]
		if !ok {
			return nil, fmt.Errorf("unknown service %q", service)
		}
		mount := fmt.Sprintf("%s:%s:ro", hostPath, spec.binPath)
		if service == "piri" {
			for _, node := range piriNodes {
				doc.Services[node] = svc{Volumes: []string{mount}}
			}
			continue
		}
		doc.Services[service] = svc{Volumes: []string{mount}}
	}

	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal override: %w", err)
	}
	const header = "# Auto-generated by `smelt workspace build` -- DO NOT EDIT\n" +
		"# Mounts locally-built service binaries over the published images.\n"
	return append([]byte(header), body...), nil
}

func gowork(root string) string {
	return filepath.Join(root, "go.work")
}

// goTool returns the Go binary of the toolchain running this process
// (build.Default.GOROOT/bin/go), so workspace builds use the same Go that runs
// the test/CLI rather than whatever `go` is first on PATH. Some IDE run configs
// (e.g. GoLand) prepend an old system go (/usr/bin/go) that predates
// patch-versioned go directives and fails to parse a modern go.work. Falls back
// to PATH `go` only if GOROOT can't be resolved.
//
// We read GOROOT via go/build's Default context rather than runtime.GOROOT(),
// which is deprecated as of Go 1.24; build.Default.GOROOT resolves to the same
// value without the deprecation.
func goTool() string {
	if root := build.Default.GOROOT; root != "" {
		p := filepath.Join(root, "bin", "go")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "go"
}

func goEnv(key string) (string, error) {
	out, err := exec.Command(goTool(), "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// parseUseDirs extracts the directory paths from a go.work file's use
// directives, handling both the block form `use (\n ./x\n)` and single-line
// `use ./x`.
func parseUseDirs(goworkPath string) ([]string, error) {
	data, err := os.ReadFile(goworkPath)
	if err != nil {
		return nil, err
	}
	var dirs []string
	inBlock := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case inBlock:
			if line == ")" {
				inBlock = false
				continue
			}
			dirs = append(dirs, strings.Trim(line, `"`))
		case line == "use (" || line == "use(":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			dirs = append(dirs, strings.Trim(strings.TrimSpace(line[len("use "):]), `"`))
		}
	}
	return dirs, nil
}
