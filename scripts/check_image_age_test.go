package scripts_test

import (
	"os"
	"os/exec"
	"testing"
)

// TestCheckImageAge runs the Python tests for the image-age warning
// (test_check_image_age.py): which tags count as moving, the shapes compose
// prints its image list in, and the timestamp docker stamps, which carries
// more precision than older pythons parse.
func TestCheckImageAge(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	cmd := exec.Command("python3", "-m", "unittest", "-v", "test_check_image_age")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatalf("python3 -m unittest test_check_image_age: %v", err)
	}
}
