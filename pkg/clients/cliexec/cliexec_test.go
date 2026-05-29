package cliexec_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fil-forge/smelt/pkg/clients/cliexec"
)

type sample struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func TestRunPrependsPrefix(t *testing.T) {
	var gotArgs []string
	r := cliexec.RunnerFunc(func(_ context.Context, args ...string) (string, string, error) {
		gotArgs = args
		return "ok", "", nil
	})

	if _, err := cliexec.Run(context.Background(), r, []string{"guppy", "--output=json"}, "space", "generate"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"guppy", "--output=json", "space", "generate"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}

func TestRunSurfacesStderr(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return "", "boom on stderr", errors.New("exit 1")
	})
	_, err := cliexec.Run(context.Background(), r, []string{"guppy"}, "whoami")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom on stderr") {
		t.Fatalf("error should surface stderr, got: %v", err)
	}
}

func TestJSONHappy(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return `{"name":"x","n":7}` + "\n", "", nil
	})
	got, err := cliexec.JSON[sample](context.Background(), r, []string{"guppy", "--output=json"}, "thing")
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got != (sample{Name: "x", N: 7}) {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestJSONArray(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return `[{"name":"a","n":1},{"name":"b","n":2}]`, "", nil
	})
	got, err := cliexec.JSON[[]sample](context.Background(), r, []string{"guppy", "--output=json"}, "list")
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if len(got) != 2 || got[1].Name != "b" {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestJSONEmptyOutput(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return "   \n", "", nil
	})
	_, err := cliexec.JSON[sample](context.Background(), r, []string{"guppy"}, "thing")
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty-output error, got: %v", err)
	}
}

func TestJSONMalformed(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return "not json", "", nil
	})
	_, err := cliexec.JSON[sample](context.Background(), r, []string{"guppy"}, "thing")
	if err == nil || !strings.Contains(err.Error(), "decoding json") {
		t.Fatalf("expected decode error, got: %v", err)
	}
}

func TestJSONNonZeroExit(t *testing.T) {
	r := cliexec.RunnerFunc(func(_ context.Context, _ ...string) (string, string, error) {
		return `{"name":"ignored"}`, "kaboom", errors.New("exit 2")
	})
	_, err := cliexec.JSON[sample](context.Background(), r, []string{"guppy"}, "thing")
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("error should surface stderr, got: %v", err)
	}
}
