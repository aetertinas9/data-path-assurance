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

// PCIE-003: exercise the repository's checker, not a second implementation of
// its dependency rules. All package text below is synthetic test data. The
// checker and go build run only inside isolated temporary modules.
func TestPCIE_003_ArchitectureBoundaries(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	const module = "example.com/pcie-architecture-fixture"
	const leaf = "package fixture\n"
	imports := func(paths ...string) string {
		text := "package fixture\nimport (\n"
		for _, path := range paths {
			text += "_ \"" + path + "\"\n"
		}
		return text + ")\n"
	}
	cases := []struct {
		name               string
		files              map[string]string
		forbidden          string
		moduleRequirements string
	}{
		{
			name:  "no_domains_package",
			files: map[string]string{"pkg/model/fixture.go": leaf},
		},
		{
			name: "allowed_stdlib_model_evidence",
			files: map[string]string{
				"pkg/model/fixture.go":             imports("strings"),
				"internal/evidence/fixture.go":     imports(module+"/pkg/model", "time"),
				"internal/domains/pcie/fixture.go": imports(module+"/pkg/model", module+"/internal/evidence", "sort"),
			},
		},
		{
			name: "root_domains_allowed_model_evidence",
			files: map[string]string{
				"pkg/model/fixture.go":         leaf,
				"internal/evidence/fixture.go": imports(module + "/pkg/model"),
				"internal/domains/fixture.go":  imports(module+"/pkg/model", module+"/internal/evidence"),
			},
		},
		{
			name: "root_domains_graph_rejected",
			files: map[string]string{
				"internal/domains/fixture.go": imports(module + "/internal/graph"),
				"internal/graph/fixture.go":   leaf,
			},
			forbidden: module + "/internal/graph",
		},
		{
			name: "direct_graph_rejected",
			files: map[string]string{
				"internal/domains/pcie/fixture.go": imports(module + "/internal/graph"),
				"internal/graph/fixture.go":        leaf,
			},
			forbidden: module + "/internal/graph",
		},
		{
			name: "direct_identity_rejected",
			files: map[string]string{
				"internal/domains/pcie/fixture.go": imports(module + "/internal/identity"),
				"internal/identity/fixture.go":     leaf,
			},
			forbidden: module + "/internal/identity",
		},
		{
			name: "transitive_helper_rejected",
			files: map[string]string{
				"internal/domains/pcie/fixture.go": imports(module + "/internal/evidence"),
				"internal/evidence/fixture.go":     imports(module + "/internal/helper"),
				"internal/helper/fixture.go":       leaf,
			},
			forbidden: module + "/internal/helper",
		},
		{
			name: "domain_http_rejected",
			files: map[string]string{
				"internal/domains/pcie/fixture.go": imports("net/http"),
			},
			forbidden: "net/http",
		},
		{
			name: "model_nonstandard_dependency_rejected",
			files: map[string]string{
				"pkg/model/fixture.go":       imports(module + "/internal/helper"),
				"internal/helper/fixture.go": leaf,
			},
			forbidden: module + "/internal/helper",
		},
	}
	// Use a core package outside domains so these cases specifically require
	// the existing global forbidden-import gate, not the domain allowlist.
	for _, sdk := range []struct{ name, path string }{{"kubernetes", "k8s.io/api"}, {"grpc", "google.golang.org/grpc"}} {
		for _, transitive := range []bool{false, true} {
			name := sdk.name + "_direct_from_app"
			files := map[string]string{
				"internal/app/fixture.go": imports(sdk.path),
				"stubs/sdk/go.mod":        "module " + sdk.path + "\n\ngo 1.26.0\n",
				"stubs/sdk/fixture.go":    leaf,
			}
			if transitive {
				name = sdk.name + "_transitive_from_app"
				files["internal/app/fixture.go"] = imports(module + "/internal/helper")
				files["internal/helper/fixture.go"] = imports(sdk.path)
			}
			cases = append(cases, struct {
				name               string
				files              map[string]string
				forbidden          string
				moduleRequirements string
			}{name: name, files: files, forbidden: sdk.path,
				moduleRequirements: "\nrequire " + sdk.path + " v0.0.0\nreplace " + sdk.path + " => ./stubs/sdk\n"})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pcieArchWrite(t, root, "go.mod", []byte("module "+module+"\n\ngo 1.26.0\n"+tc.moduleRequirements))
			pcieArchWrite(t, root, "Makefile", makefile)
			for path, source := range tc.files {
				pcieArchWrite(t, root, path, []byte(source))
			}
			// Invalid Go fixtures must never count as a successful rejection.
			if output, err := pcieArchRun(t, root, "go", "build", "./..."); err != nil {
				t.Fatalf("fixture build failed: %v\n%s", err, output)
			}
			output, err := pcieArchRun(t, root, "make", "--no-print-directory", "arch-check")
			if tc.forbidden == "" {
				if err != nil || !strings.Contains(output, "arch-check: OK") || strings.Contains(output, "arch-check: FAIL") {
					t.Fatalf("allowed fixture rejected: %v\n%s", err, output)
				}
				return
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() <= 0 {
				t.Fatalf("expected checker rejection, got %v\n%s", err, output)
			}
			if !strings.Contains(output, "arch-check: FAIL") || !strings.Contains(output, tc.forbidden) || strings.Contains(output, "arch-check: OK") {
				t.Fatalf("missing dependency rejection for %s:\n%s", tc.forbidden, output)
			}
			for _, diagnostic := range []string{"cannot determine module path", "go list ./... failed", "arch-check: go list -deps", "no required module provides package", "syntax error", "build constraints exclude all Go files"} {
				if strings.Contains(output, diagnostic) {
					t.Fatalf("tool/fixture failure is not an architecture rejection:\n%s", output)
				}
			}
		})
	}
}

func pcieArchWrite(t *testing.T, root, path string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pcieArchRun(t *testing.T, root, command string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = root
	cmd.WaitDelay = 2 * time.Second
	// Disable workspace inheritance, network module resolution, toolchain
	// downloads, and ambient command flags without modifying the user's env.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GO111MODULE=on", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off", "GOFLAGS=", "MAKEFLAGS=", "MFLAGS=")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("%s %v timed out or retained subprocess pipes: %v / %v\n%s", command, args, ctx.Err(), err, output)
	}
	return string(output), err
}
