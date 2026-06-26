package staging

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// storeInOnePassword writes every secret into a single 1Password item via the
// `op` CLI. Each field value is read from a file with op's `@path` syntax, so
// multi-line PEMs and special characters pass through verbatim and no secret
// value is ever placed on a command line or in this process's logs.
//
// fields maps the 1Password field label to the temp file holding its value.
// Returns the sorted list of field labels written (names only — never values).
//
// The item is created if absent, edited if present. Requires an authenticated
// `op` session (run `op signin` first).
func storeInOnePassword(vault, item string, fields map[string]string) ([]string, error) {
	if vault == "" || item == "" {
		return nil, fmt.Errorf("1Password vault and item must be set")
	}
	if _, err := exec.LookPath("op"); err != nil {
		return nil, fmt.Errorf("1Password CLI %q not found in PATH: %w", "op", err)
	}

	names := make([]string, 0, len(fields))
	for label := range fields {
		names = append(names, label)
	}
	sort.Strings(names)

	// Build "<label>[password]=@<file>" assignments in stable order.
	assignments := make([]string, 0, len(names))
	for _, label := range names {
		assignments = append(assignments, fmt.Sprintf("%s[password]=@%s", label, fields[label]))
	}

	verb := "edit"
	if !itemExists(vault, item) {
		verb = "create"
	}

	var args []string
	if verb == "create" {
		args = append(args, "item", "create",
			"--category", "Secure Note",
			"--title", item,
			"--vault", vault)
	} else {
		args = append(args, "item", "edit", item, "--vault", vault)
	}
	args = append(args, assignments...)

	cmd := exec.Command("op", args...)
	cmd.Stderr = os.Stderr // op writes the item summary (no secret values) to stdout/stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("op item %s failed: %w", verb, err)
	}
	return names, nil
}

func itemExists(vault, item string) bool {
	cmd := exec.Command("op", "item", "get", item, "--vault", vault)
	return cmd.Run() == nil
}
