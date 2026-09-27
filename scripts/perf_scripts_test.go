package scripts_test

import (
	"os"
	"os/exec"
	"testing"
)

// TestPerfScripts runs the Python tests for the perf suites' recording
// (test_perf.py), which drive perf-lib.sh against a stub docker and
// perf-results.py over old and new rows.
func TestPerfScripts(t *testing.T) {
	for _, tool := range []string{"bash", "git", "jq", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	cmd := exec.Command("python3", "-m", "unittest", "-v", "test_perf")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatalf("python3 -m unittest test_perf: %v", err)
	}
}
