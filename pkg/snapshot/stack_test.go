package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectName(t *testing.T) {
	testCases := []struct {
		desc        string
		envName     string
		composeYAML string // "" means no compose.yml
		want        string
	}{
		{
			desc:        "compose.yml pins a name",
			composeYAML: "name: smelt\nservices: {}\n",
			want:        "smelt",
		},
		{
			desc:        "COMPOSE_PROJECT_NAME is set",
			envName:     "override",
			composeYAML: "name: smelt\n",
			want:        "override",
		},
		{
			desc:        "compose.yml has no name",
			composeYAML: "services: {}\n",
			want:        "my-worktree-1a2b3c",
		},
		{
			desc: "there is no compose.yml",
			want: "my-worktree-1a2b3c",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Setenv("COMPOSE_PROJECT_NAME", tc.envName)
			projectDir := projectDirWithCompose(t, tc.composeYAML)

			got, err := projectName(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("projectName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectNameRejectsUnusableComposeFile(t *testing.T) {
	testCases := map[string]string{
		"compose.yml is not valid YAML":     "name: [smelt\n",
		"compose.yml interpolates the name": "name: ${PROJECT:-smelt}\n",
	}
	for desc, composeYAML := range testCases {
		t.Run(desc, func(t *testing.T) {
			t.Setenv("COMPOSE_PROJECT_NAME", "")
			projectDir := projectDirWithCompose(t, composeYAML)

			if _, err := projectName(projectDir); err == nil {
				t.Error("projectName() returned no error")
			}
		})
	}
}

// The repo's own compose.yml must resolve to the name its volumes carry, so a
// checkout in a folder named anything else still finds `smelt_<vol>`.
func TestProjectNameOfThisRepo(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	got, err := projectName(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got != "smelt" {
		t.Errorf("projectName(%s) = %q, want %q", repoRoot, got, "smelt")
	}
}

// projectDirWithCompose creates a project directory whose basename differs
// from any compose project name, and writes composeYAML as its compose.yml
// unless composeYAML is empty.
func projectDirWithCompose(t *testing.T, composeYAML string) string {
	t.Helper()
	projectDir := filepath.Join(t.TempDir(), "My-Worktree-1a2b3c")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if composeYAML != "" {
		if err := os.WriteFile(filepath.Join(projectDir, composeFile), []byte(composeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir
}
