package randdir_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/clients/cliexec"
	"github.com/fil-forge/smelt/pkg/clients/randdir"
)

func TestGenerateDefaultArgs(t *testing.T) {
	var got []string
	c := randdir.NewWithRunner(cliexec.RunnerFunc(func(_ context.Context, args ...string) (string, string, error) {
		got = args
		return "", "", nil
	}))

	path, err := c.Generate(context.Background(), "10MB")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(path, "/tmp/testdata-") {
		t.Fatalf("unexpected auto path: %q", path)
	}
	want := []string{"randdir", "--size", "10MB", "--output", path}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestGenerateAllOptions(t *testing.T) {
	var got []string
	c := randdir.NewWithRunner(cliexec.RunnerFunc(func(_ context.Context, args ...string) (string, string, error) {
		got = args
		return "", "", nil
	}))

	path, err := c.Generate(context.Background(), "1GB",
		randdir.WithOutput("/data/out"),
		randdir.WithSeed("42"),
		randdir.WithMinFileSize("1KB"),
		randdir.WithMaxFileSize("4MB"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if path != "/data/out" {
		t.Fatalf("path = %q, want /data/out", path)
	}
	want := "randdir --size 1GB --output /data/out --seed 42 --min-file-size 1KB --max-file-size 4MB"
	if strings.Join(got, " ") != want {
		t.Fatalf("args = %v, want %q", got, want)
	}
}

func TestGenerateError(t *testing.T) {
	c := randdir.NewWithRunner(cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return "", "disk full", errors.New("exit 1")
	}))
	if _, err := c.Generate(context.Background(), "10MB"); err == nil {
		t.Fatal("expected error to propagate")
	}
}
