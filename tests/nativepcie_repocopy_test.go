package tests_test

// Helpers for build-graph tests that must not touch the candidate tree: they
// work on a private copy of the repository under t.TempDir() with an isolated
// GOCACHE, establish their own preconditions there, and only append to files
// (never read source content).

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// npoIgnoredFiles lists git-ignored files (generated outputs such as the build
// stamp) so a copy of the repository starts without them.
func npoIgnoredFiles(t *testing.T, repoRoot string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "--others", "--ignored", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("NPO-064: git is required to enumerate generated files (%v)", err)
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(out), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			ignored[filepath.FromSlash(p)] = true
		}
	}
	return ignored
}

// npoRepoCopy copies the repository into t.TempDir(), excluding every
// top-level entry whose name starts with a dot (.git included), build/, and
// every git-ignored file. Contents are streamed byte for byte; nothing is
// inspected. Symlinks are recreated as symlinks.
func npoRepoCopy(t *testing.T) string {
	t.Helper()
	repoRoot := npoRepoRoot(t)
	ignored := npoIgnoredFiles(t, repoRoot)
	dst := filepath.Join(t.TempDir(), "repo")
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if rel == top && strings.HasPrefix(top, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && top == "build" {
			return filepath.SkipDir
		}
		if ignored[rel] {
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = in.Close() }()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				_ = out.Close()
				return err
			}
			return out.Close()
		}
	})
	if err != nil {
		t.Fatalf("fixture: cannot copy repository: %v", err)
	}
	return dst
}

// npoIsolatedEnv returns the process environment with a private GOCACHE and
// any overrides appended (later entries win in exec.Cmd.Env).
func npoIsolatedEnv(t *testing.T, overrides ...string) []string {
	t.Helper()
	env := append(os.Environ(), "GOCACHE="+t.TempDir(), "GOFLAGS=")
	return append(env, overrides...)
}

// npoRun runs a command in dir with env and returns its combined output.
func npoRun(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// npoMustRun runs a command and fails the test (tagged with clause) on error.
func npoMustRun(t *testing.T, clause, dir string, env []string, name string, args ...string) string {
	t.Helper()
	out, err := npoRun(dir, env, name, args...)
	if err != nil {
		head := out
		if len(head) > 2000 {
			head = head[:2000] + "\n..."
		}
		t.Fatalf("%s: %s %s failed in %s: %v\n%s", clause, name, strings.Join(args, " "), dir, err, head)
	}
	return out
}

// npoFilesUnder returns every regular file below dir (relative paths),
// skipping .git.
func npoFilesUnder(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files[rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fixture: cannot walk %s: %v", dir, err)
	}
	return files
}

// npoHeadersOutsideBuild returns the .h files below dir excluding build/.
func npoHeadersOutsideBuild(t *testing.T, dir string) map[string]bool {
	t.Helper()
	headers := map[string]bool{}
	for f := range npoFilesUnder(t, dir) {
		if strings.HasSuffix(f, ".h") && !strings.HasPrefix(f, "build"+string(filepath.Separator)) {
			headers[f] = true
		}
	}
	return headers
}

// npoNewEntries returns the sorted keys present in after but not before.
func npoNewEntries(before, after map[string]bool) []string {
	var out []string
	for k := range after {
		if !before[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// npoAppendLine appends text to a file without reading it.
func npoAppendLine(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("fixture: cannot append to %s: %v", path, err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatalf("fixture: cannot append to %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// npoArchivePath is the static archive location fixed by the integration
// contract for the running host.
func npoArchivePath(dir string) string {
	return filepath.Join(dir, "build", "native", runtime.GOOS+"-"+runtime.GOARCH, "libdpa_pcie.a")
}

// npoDiscoverStamp runs `make native` in the copy and returns the generated
// build-stamp header(s): .h files outside build/ that did not exist before.
func npoDiscoverStamp(t *testing.T, copyDir string, env []string) []string {
	t.Helper()
	before := npoHeadersOutsideBuild(t, copyDir)
	npoMustRun(t, "NPO-061/NPO-064", copyDir, env, "make", "native")
	stamps := npoNewEntries(before, npoHeadersOutsideBuild(t, copyDir))
	if len(stamps) == 0 {
		t.Fatalf("NPO-064: make native generated no build-stamp header (no new .h outside build/)")
	}
	return stamps
}
