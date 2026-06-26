package staging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// opItemTemplate is the subset of 1Password's item-create JSON template we emit.
type opItemTemplate struct {
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Fields   []opField `json:"fields"`
}

type opField struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// storeInOnePassword writes every secret into a single 1Password item via the
// `op` CLI, using a JSON item template piped over stdin. The earlier `@<path>`
// assignment form was a bug: `op item create/edit` does NOT expand `@file`
// (that's a curl-ism), so it stored the literal temp-file path as the value.
// A stdin template both fixes that and keeps secret values off the command line
// and out of shell history — op's own docs recommend a template for sensitive
// values, since assignment statements get logged.
//
// fields maps the 1Password field label to the temp file holding its value.
// Returns the sorted list of field labels written (names only — never values).
//
// The item is fully (re)created: keygen owns every field in it and re-running is
// a deliberate overwrite, so an existing item is deleted first to avoid stale
// fields. Requires an authenticated `op` session (run `op signin` first).
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

	// Build the item template, reading each value from its file. Concealed fields
	// stay masked in the 1Password UI; `op read op://vault/item/<label>` resolves
	// them by label at provision time.
	tmpl := opItemTemplate{Title: item, Category: "SECURE_NOTE"}
	for _, label := range names {
		content, err := os.ReadFile(fields[label])
		if err != nil {
			return nil, fmt.Errorf("reading value for field %q: %w", label, err)
		}
		tmpl.Fields = append(tmpl.Fields, opField{ID: label, Type: "CONCEALED", Label: label, Value: string(content)})
	}
	payload, err := json.Marshal(tmpl)
	if err != nil {
		return nil, fmt.Errorf("building 1Password item template: %w", err)
	}

	// Edit in place if the item exists, create it otherwise. We deliberately do
	// NOT delete-then-create: a failed create would otherwise leave the item
	// deleted with nothing to replace it. keygen supplies the complete field set,
	// so editing fully refreshes the values either way. Both forms read the JSON
	// template from stdin (create wants an explicit "-"; edit reads the pipe).
	var args []string
	verb := "create"
	if itemExists(vault, item) {
		verb = "edit"
		args = []string{"item", "edit", item, "--vault", vault}
	} else {
		args = []string{"item", "create", "--vault", vault, "-"}
	}

	cmd := exec.Command("op", args...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = os.Stderr // op writes the item summary (no secret values) to stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("op item %s failed: %w", verb, err)
	}
	return names, nil
}

func itemExists(vault, item string) bool {
	cmd := exec.Command("op", "item", "get", item, "--vault", vault)
	return cmd.Run() == nil
}
