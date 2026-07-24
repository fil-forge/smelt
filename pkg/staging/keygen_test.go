package staging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpsertEnvVar(t *testing.T) {
	cases := map[string]struct {
		initial string // "" means the file does not exist yet
		key     string
		value   string
		want    string
	}{
		"creates file when missing": {
			initial: "",
			key:     "PAYER_ADDRESS",
			value:   "0xabc",
			want:    "PAYER_ADDRESS=0xabc\n",
		},
		"appends to file ending in newline without a blank line": {
			initial: "A=1\n",
			key:     "B",
			value:   "2",
			want:    "A=1\nB=2\n",
		},
		"appends and adds EOL when input lacks trailing newline": {
			initial: "A=1",
			key:     "B",
			value:   "2",
			want:    "A=1\nB=2\n",
		},
		"replaces existing key in place": {
			initial: "A=1\nB=2\n",
			key:     "A",
			value:   "9",
			want:    "A=9\nB=2\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.env")
			if tc.initial != "" {
				if err := os.WriteFile(path, []byte(tc.initial), 0o644); err != nil {
					t.Fatalf("write initial file: %v", err)
				}
			}
			if err := upsertEnvVar(path, tc.key, tc.value); err != nil {
				t.Fatalf("upsertEnvVar: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read file: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", string(got), tc.want)
			}
		})
	}
}
