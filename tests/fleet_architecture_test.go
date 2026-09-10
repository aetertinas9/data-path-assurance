package tests_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GFL-102: internal/fleet is domain core. The repository architecture checker
// must accept its domain dependencies and reject forbidden dependencies reached
// either directly or transitively.
func TestGFL_102_FleetArchitectureBoundaries(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}

	const module = "example.com/fleet-architecture-fixture"
	const leaf = "package fixture\n"
	imports := func(paths ...string) string {
		text := "package fixture\nimport (\n"
		for _, path := range paths {
			text += "_ \"" + path + "\"\n"
		}
		return text + ")\n"
	}

	cases := []struct {
		name      string
		fleet     string
		helper    string
		forbidden string
		extraMod  string
	}{
		{
			name:  "allowed_model_graph_evidence",
			fleet: imports(module+"/pkg/model", module+"/internal/graph", module+"/internal/evidence", "sort", "time"),
		},
		{
			name:      "direct_kubernetes_rejected",
			fleet:     imports("k8s.io/api"),
			forbidden: "k8s.io/api",
			extraMod:  "\nrequire k8s.io/api v0.0.0\nreplace k8s.io/api => ./stubs/k8s\n",
		},
		{
			name:      "transitive_grpc_rejected",
			fleet:     imports(module + "/internal/fleethelper"),
			helper:    imports("google.golang.org/grpc"),
			forbidden: "google.golang.org/grpc",
			extraMod:  "\nrequire google.golang.org/grpc v0.0.0\nreplace google.golang.org/grpc => ./stubs/grpc\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fleetArchWrite(t, root, "go.mod", "module "+module+"\n\ngo 1.26.0\n"+tc.extraMod)
			fleetArchWrite(t, root, "Makefile", string(makefile))
			fleetArchWrite(t, root, "pkg/model/fixture.go", leaf)
			fleetArchWrite(t, root, "internal/graph/fixture.go", imports(module+"/pkg/model"))
			fleetArchWrite(t, root, "internal/evidence/fixture.go", imports(module+"/pkg/model"))
			fleetArchWrite(t, root, "internal/fleet/fixture.go", tc.fleet)
			if tc.helper != "" {
				fleetArchWrite(t, root, "internal/fleethelper/fixture.go", tc.helper)
			}
			if strings.Contains(tc.extraMod, "k8s.io/api") {
				fleetArchWrite(t, root, "stubs/k8s/go.mod", "module k8s.io/api\n\ngo 1.26.0\n")
				fleetArchWrite(t, root, "stubs/k8s/fixture.go", leaf)
			}
			if strings.Contains(tc.extraMod, "google.golang.org/grpc") {
				fleetArchWrite(t, root, "stubs/grpc/go.mod", "module google.golang.org/grpc\n\ngo 1.26.0\n")
				fleetArchWrite(t, root, "stubs/grpc/fixture.go", leaf)
			}

			if output, err := fleetArchRun(t, root, "go", "build", "./..."); err != nil {
				t.Fatalf("fixture build failed: %v\n%s", err, output)
			}
			output, err := fleetArchRun(t, root, "make", "--no-print-directory", "arch-check")
			if tc.forbidden == "" {
				if err != nil || !strings.Contains(output, "arch-check: OK") || strings.Contains(output, "arch-check: FAIL") {
					t.Fatalf("allowed fleet dependency rejected: %v\n%s", err, output)
				}
				return
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() <= 0 {
				t.Fatalf("expected architecture rejection, got %v\n%s", err, output)
			}
			if !strings.Contains(output, "arch-check: FAIL") || !strings.Contains(output, tc.forbidden) || strings.Contains(output, "arch-check: OK") {
				t.Fatalf("missing rejection for %s:\n%s", tc.forbidden, output)
			}
		})
	}
}

func fleetArchWrite(t *testing.T, root, path, data string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fleetArchRun(t *testing.T, root, command string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = root
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "GOWORK=off", "GO111MODULE=on", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off", "GOFLAGS=", "MAKEFLAGS=", "MFLAGS=")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("%s %v timed out or retained subprocess pipes: %v / %v\n%s", command, args, ctx.Err(), err, output)
	}
	return string(output), err
}
