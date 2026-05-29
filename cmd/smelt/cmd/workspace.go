package cmd

import (
	"fmt"
	"os"
	"path/filepath"

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
every service is rebuilt (a published binary would still link published libforge).`,
	RunE: runWorkspaceBuild,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceBuildCmd)
	workspaceBuildCmd.Flags().StringP("project-dir", "d", ".", "project root directory")
}

func runWorkspaceBuild(cmd *cobra.Command, args []string) error {
	projectDir, _ := cmd.Flags().GetString("project-dir")

	root, services, err := workspace.Detect()
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

	binDir := filepath.Join(projectDir, "generated", "bin")
	binaries := make(map[string]string, len(services))
	for _, svc := range services {
		path, err := workspace.BuildBinary(root, svc, binDir)
		if err != nil {
			return err
		}
		binaries[svc] = path
		fmt.Printf("  built %s\n", svc)
	}

	nodeNames, err := resolvePiriNodeNames(projectDir)
	if err != nil {
		return err
	}

	data, err := workspace.RenderOverride(binaries, nodeNames)
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
