package snapshot

import "testing"

func TestCompatibilityWarnings(t *testing.T) {
	cases := []struct {
		name    string
		volumes []string
		want    int
	}{
		{"vault persisted alongside tenants", []string{"hilt-postgres-data", "hilt-vault-data", "hilt-vault-init"}, 0},
		{"tenants without their vault", []string{"hilt-postgres-data", "ingot-openbao-data"}, 1},
		{"no hilt at all", []string{"minio-data", "piri-0-data"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Descriptor{Volumes: tc.volumes}
			if got := len(d.CompatibilityWarnings()); got != tc.want {
				t.Errorf("got %d warning(s), want %d: %v", got, tc.want, d.CompatibilityWarnings())
			}
		})
	}
}
