package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fil-forge/smelt/pkg/manifest"
	"github.com/fil-forge/smelt/pkg/workspace"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Build service binaries from local sibling checkouts via go.work",
	Long: `Builds smelt service binaries from the local sibling repos selected by the
active Go workspace (go.work) and writes a docker-compose override that mounts
them over the published images. Used by 'SMELT_WORKSPACE=1 make up'.`,
}

var workspaceBuildCmd = &cobra.Command{
	Use:   "build",
	Short: "Compile workspace-selected service binaries and write the mount override",
	Long: `Reads the active go.work use-list, compiles each selected service from its
sibling checkout into generated/bin/, and writes generated/compose/workspace.override.yml
mounting each binary over the published image. If libforge is in the workspace,
every service is rebuilt (a published binary would still link published libforge).

With --only, just the named services are recompiled; the override still mounts
every selected service, reusing the binaries already in generated/bin/.`,
	RunE: runWorkspaceBuild,
}

var workspaceServicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Print the compose services that run workspace binaries",
	Long: `Prints, space-separated, the compose service names that run a binary built
from the workspace: each selected service, its one-shot registrar siblings
(e.g. upload-init), and every piri-N node when piri is selected. 'make redeploy'
feeds this to 'docker compose up --force-recreate'. With --only, limits the
list to the named services.`,
	RunE: runWorkspaceServices,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceBuildCmd, workspaceServicesCmd)
	for _, c := range []*cobra.Command{workspaceBuildCmd, workspaceServicesCmd} {
		c.Flags().StringP("project-dir", "d", ".", "project root directory")
		c.Flags().StringSlice("only", nil, "restrict to these smelt services (comma-separated)")
	}
}

func runWorkspaceBuild(cmd *cobra.Command, args []string) error {
	projectDir, _ := cmd.Flags().GetString("project-dir")
	only, _ := cmd.Flags().GetStringSlice("only")

	root, services, err := workspace.Detect()
	if err != nil {
		return err
	}
	toBuild, err := restrictServices(services, only)
	if err != nil {
		return err
	}

	overridePath := filepath.Join(projectDir, "generated", "compose", "workspace.override.yml")
	if len(services) == 0 {
		// go.work is active but lists no smelt service modules — nothing to
		// inject. Drop any stale override so compose uses published images.
		_ = os.Remove(overridePath)
		fmt.Println("Workspace active but no service modules selected; using published images.")
		return nil
	}

	arch, archSource, err := workspace.TargetArch()
	if err != nil {
		return err
	}
	fmt.Printf("Building linux/%s binaries (arch from %s)\n", arch, archSource)

	binDir := filepath.Join(projectDir, "generated", "bin")
	binaries := make(map[string]string, len(services))
	for _, svc := range toBuild {
		path, err := workspace.BuildBinary(root, svc, binDir)
		if err != nil {
			return err
		}
		binaries[svc] = path
		fmt.Printf("  built %s\n", svc)
	}
	// The override must keep mounting every selected service, or the ones
	// skipped by --only would silently fall back to the published binary.
	for _, svc := range services {
		if _, built := binaries[svc]; built {
			continue
		}
		path, err := filepath.Abs(filepath.Join(binDir, svc))
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("--only skipped %s but no previous build exists at %s; run `smelt workspace build` without --only first", svc, path)
		}
		binaries[svc] = path
		fmt.Printf("  reusing %s\n", svc)
	}

	nodeNames, err := resolvePiriNodeNames(projectDir)
	if err != nil {
		return err
	}

	data, err := workspace.RenderOverride(binaries, nil, nodeNames)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(overridePath), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(overridePath, data, 0644); err != nil {
		return fmt.Errorf("write override: %w", err)
	}

	fmt.Printf("Wrote %s (%d service(s) from local source)\n", overridePath, len(services))
	return nil
}

func runWorkspaceServices(cmd *cobra.Command, args []string) error {
	projectDir, _ := cmd.Flags().GetString("project-dir")
	only, _ := cmd.Flags().GetStringSlice("only")

	_, services, err := workspace.Detect()
	if err != nil {
		return err
	}
	services, err = restrictServices(services, only)
	if err != nil {
		return err
	}
	nodeNames, err := resolvePiriNodeNames(projectDir)
	if err != nil {
		return err
	}
	containers, err := workspace.Containers(services, nodeNames)
	if err != nil {
		return err
	}
	// An empty list must be an error, never empty output: a caller such as
	// `docker compose up --force-recreate $(smelt workspace services)` would
	// otherwise recreate every container in the stack.
	if len(containers) == 0 {
		return fmt.Errorf("no workspace services selected; add service modules to the go.work use-list")
	}
	fmt.Println(strings.Join(containers, " "))
	return nil
}

// restrictServices narrows the workspace-selected services to the --only
// list, erroring when a requested service is not selected by go.work (its
// module is missing from the use-list, so there is no local source to build).
func restrictServices(selected, only []string) ([]string, error) {
	if len(only) == 0 {
		return selected, nil
	}
	isSelected := make(map[string]bool, len(selected))
	for _, s := range selected {
		isSelected[s] = true
	}
	var out []string
	for _, s := range only {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !isSelected[s] {
			return nil, fmt.Errorf("service %q is not selected by go.work (selected: %s); add its module to the use-list", s, strings.Join(selected, ", "))
		}
		out = append(out, s)
	}
	return out, nil
}

// resolvePiriNodeNames resolves piri-N service names from the active manifest so
// the override can mount the piri binary into every node.
func resolvePiriNodeNames(projectDir string) ([]string, error) {
	manifestPath, _ := manifest.ResolveManifestPath(projectDir)
	m, err := manifest.Parse(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	nodes, err := m.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve manifest: %w", err)
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return names, nil
}
