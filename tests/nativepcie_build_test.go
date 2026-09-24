package tests_test

// Build, make, documentation and dependency-boundary checks that need no
// native backend (NPO-062, NPO-063, NPO-064, NPO-066, NPO-071). These read
// only repository metadata and run the go tool; they never read source.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// npoRequireNativeToolchain skips when the GIVEN "native artifacts can be
// generated" cannot be established on this host (no make or no C compiler).
// It returns the absolute path of the C compiler.
func npoRequireNativeToolchain(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("NPO-064: make not available on this host (%v); cannot generate native artifacts", err)
	}
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	path, err := exec.LookPath(cc)
	if err != nil {
		t.Skipf("NPO-064: C compiler %q not available on this host (%v); cannot generate native artifacts", cc, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Skipf("NPO-064: cannot resolve C compiler path: %v", err)
	}
	return abs
}

// npoReadStamps returns the concatenated content of the stamp headers.
func npoReadStamps(t *testing.T, repoRoot string, stamps []string) string {
	t.Helper()
	var sb strings.Builder
	for _, p := range stamps {
		b, err := os.ReadFile(filepath.Join(repoRoot, p))
		if err != nil {
			t.Fatalf("NPO-064: cannot read stamp %s: %v", p, err)
		}
		sb.WriteString(p)
		sb.WriteString(":\n")
		sb.Write(b)
		sb.WriteString("\n")
	}
	return sb.String()
}

type npoArchiveState struct {
	content []byte
	mtime   time.Time
}

func npoArchive(t *testing.T, repoRoot string) npoArchiveState {
	t.Helper()
	p := npoArchivePath(repoRoot)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("NPO-061: static archive missing after make native: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return npoArchiveState{content: b, mtime: st.ModTime()}
}

func (a npoArchiveState) rebuiltFrom(b npoArchiveState) bool {
	return !bytes.Equal(a.content, b.content) || !a.mtime.Equal(b.mtime)
}

// NPO-064 first GIVEN: a change of compiler identity between consecutive
// `make native` runs (within the same second, no sleep) must change the
// generated build stamp and regenerate the archive; restoring the input must
// restore the stamp so Go's build cache cannot reuse an obsolete archive.
// Runs on a private repository copy; the candidate tree is untouched.
func TestNPO064_BuildStampTracksCompilerIdentity(t *testing.T) {
	ccAbs := npoRequireNativeToolchain(t)
	repo := npoRepoCopy(t)
	env := npoIsolatedEnv(t)

	// Step 1: default inputs; discover the stamp as the header make creates.
	stamps := npoDiscoverStamp(t, repo, env)
	s1 := npoReadStamps(t, repo, stamps)
	a1 := npoArchive(t, repo)

	// Step 2, immediately: same compiler under a different identity string.
	npoMustRun(t, "NPO-064", repo, env, "make", "native", "CC="+ccAbs)
	s2 := npoReadStamps(t, repo, stamps)
	a2 := npoArchive(t, repo)
	if s2 == s1 {
		t.Errorf("NPO-064: build stamp %v unchanged after make native CC=%s (compiler identity changed within the same second)", stamps, ccAbs)
	}
	if !a2.rebuiltFrom(a1) {
		t.Errorf("NPO-064: static archive not regenerated after compiler identity change (content and mtime identical)")
	}

	// Step 3, immediately: back to the default inputs.
	npoMustRun(t, "NPO-064", repo, env, "make", "native")
	s3 := npoReadStamps(t, repo, stamps)
	a3 := npoArchive(t, repo)
	if s3 != s1 {
		t.Errorf("NPO-064: build stamp after restoring default inputs differs from the original stamp:\n--- step 1\n%s\n--- step 3\n%s", s1, s3)
	}
	if s3 == s2 && s2 != s1 {
		t.Errorf("NPO-064: build stamp still reflects the previous compiler identity after restoring the default")
	}
	if !a3.rebuiltFrom(a2) {
		t.Errorf("NPO-064: static archive not regenerated after restoring the default compiler identity")
	}
}

// npoGoTestProbe runs one existing, cheap NPO test in the copy through the
// real cgo bridge. The output tells whether Go relinked or reused a cached
// test binary.
func npoGoTestProbe(t *testing.T, repo string, env []string) string {
	t.Helper()
	return npoMustRun(t, "NPO-064", repo, env, "go", "test", "-run", "^TestNPO043_AvailableOnSupportedTarget$", "./tests/")
}

// NPO-064 first GIVEN: when native source or the public header changes and
// `make native` regenerates the archive, the build stamp must change so a
// following `go test` (no -count) relinks instead of reporting "(cached)".
// Runs on a private copy with an isolated GOCACHE; only appends a comment.
func TestNPO064_ChangedNativeSourceInvalidatesGoCache(t *testing.T) {
	npoRequireNativeToolchain(t)
	repo := npoRepoCopy(t)
	env := npoIsolatedEnv(t, "CGO_ENABLED=1")

	stamps := npoDiscoverStamp(t, repo, env)
	s1 := npoReadStamps(t, repo, stamps)
	first := npoGoTestProbe(t, repo, env)
	if strings.Contains(first, "(cached)") {
		t.Fatalf("fixture: first go test in a fresh GOCACHE reported (cached):\n%s", first)
	}

	sources, err := filepath.Glob(filepath.Join(repo, "internal", "nativepcie", "csrc", "*.c"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("NPO-001: no canonical C source under internal/nativepcie/csrc (%v)", err)
	}
	sort.Strings(sources)
	npoAppendLine(t, sources[0], "\n/* npo: native source change probe */\n")
	a1 := npoArchive(t, repo)
	npoMustRun(t, "NPO-064", repo, env, "make", "native")
	a2 := npoArchive(t, repo)
	s2 := npoReadStamps(t, repo, stamps)
	if !a2.rebuiltFrom(a1) {
		t.Errorf("NPO-064: static archive not regenerated after native source changed")
	}
	if s2 == s1 {
		t.Errorf("NPO-064: build stamp %v unchanged after native source %s changed", stamps, filepath.Base(sources[0]))
	}
	second := npoGoTestProbe(t, repo, env)
	if strings.Contains(second, "(cached)") {
		t.Errorf("NPO-064: go test reused a cached binary after native source changed and make native ran:\n%s", second)
	}

	header := filepath.Join(repo, "internal", "nativepcie", "csrc", "dpa_pcie.h")
	npoAppendLine(t, header, "\n/* npo: public header change probe */\n")
	npoMustRun(t, "NPO-064", repo, env, "make", "native")
	s3 := npoReadStamps(t, repo, stamps)
	if s3 == s2 {
		t.Errorf("NPO-064: build stamp %v unchanged after public header dpa_pcie.h changed", stamps)
	}
	third := npoGoTestProbe(t, repo, env)
	if strings.Contains(third, "(cached)") {
		t.Errorf("NPO-064: go test reused a cached binary after the public header changed and make native ran:\n%s", third)
	}
}

// NPO-065: `make clean-native` removes exactly the native outputs and stamp
// this feature generated and leaves unrelated files alone. Runs on a copy.
func TestNPO065_CleanNativeRemovesOnlyNativeOutputs(t *testing.T) {
	npoRequireNativeToolchain(t)
	repo := npoRepoCopy(t)
	env := npoIsolatedEnv(t)

	before := npoFilesUnder(t, repo)
	npoMustRun(t, "NPO-065", repo, env, "make", "native", "native-example")
	if out, err := npoRun(repo, env, "make", "native-asan"); err != nil {
		t.Logf("NPO-072: make native-asan failed (%v); sanitizer archive not generated:\n%s", err, out)
	}
	generated := npoNewEntries(before, npoFilesUnder(t, repo))
	if len(generated) == 0 {
		t.Fatalf("NPO-065: make native generated nothing to clean")
	}
	if _, err := os.Stat(npoArchivePath(repo)); err != nil {
		t.Fatalf("NPO-061: archive missing after make native: %v", err)
	}

	unrelated := []string{
		filepath.Join("build", "unrelated.txt"),
		filepath.Join("build", "native", "unrelated-note.txt"),
		"npo-unrelated-root-file.txt",
	}
	for _, rel := range unrelated {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("unrelated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	npoMustRun(t, "NPO-065", repo, env, "make", "clean-native")
	after := npoFilesUnder(t, repo)
	for _, g := range generated {
		// A VCS ignore marker written into build/ is not a native artifact
		// (NPO-065 covers artifacts and stamp paths); it may stay.
		if filepath.Base(g) == ".gitignore" {
			continue
		}
		if after[g] {
			t.Errorf("NPO-065: make clean-native left generated native output %s", g)
		}
	}
	for _, rel := range unrelated {
		if !after[rel] {
			t.Errorf("NPO-065: make clean-native removed unrelated file %s", rel)
		}
	}
	for f := range before {
		if !after[f] {
			t.Errorf("NPO-065: make clean-native removed pre-existing file %s", f)
		}
	}
}

// npoMakefileText returns the top-level Makefile plus any files it includes.
func npoMakefileText(t *testing.T, repoRoot string) string {
	t.Helper()
	main, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("NPO-063: cannot read Makefile: %v", err)
	}
	text := string(main)
	inc := regexp.MustCompile(`(?m)^-?include\s+(.+)$`)
	for _, m := range inc.FindAllStringSubmatch(text, -1) {
		for _, name := range strings.Fields(m[1]) {
			if strings.ContainsAny(name, "$*") {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(repoRoot, name)); err == nil {
				text += "\n" + string(b)
			}
		}
	}
	return text
}

func TestNPO063_MakeTargetsExist(t *testing.T) {
	text := npoMakefileText(t, npoRepoRoot(t))
	for _, target := range []string{"native", "native-asan", "native-example", "native-test", "native-test-sanitize", "clean-native"} {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `\s*:`)
		if !re.MatchString(text) {
			t.Errorf("NPO-063: Makefile lacks target %q", target)
		}
	}
}

// NPO-062: inspect the commands `make native` actually executes (dry run of
// the full graph), not Makefile text: every C compile carries C11, PIC and
// the warning set; the shared library uses the platform mode; non-public
// symbols are hidden; only make, a C compiler and ar are invoked.
func TestNPO062_NativeCompileFlags(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	out := npoMakeDryRun(t, repoRoot, "native")
	var compiles, shared, archives int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// Classify by what the command produces, not by which file names
		// appear anywhere in the line (make also prints bookkeeping shell
		// lines that mention artifact names).
		target := npoOutputTarget(fields)
		hasC := false
		for _, f := range fields {
			if f == "-c" {
				hasC = true
			}
		}
		switch {
		case hasC && strings.HasSuffix(target, ".o"):
			compiles++
			for _, flag := range []string{"-std=c11", "-fPIC", "-Wall", "-Wextra", "-Werror", "-fvisibility=hidden"} {
				if !npoHasField(fields, flag) {
					t.Errorf("NPO-062: native compile command lacks %q:\n%s", flag, line)
				}
			}
		case strings.HasSuffix(target, "libdpa_pcie.dylib") || strings.HasSuffix(target, "libdpa_pcie.so"):
			shared++
			mode := "-shared"
			if runtime.GOOS == "darwin" {
				mode = "-dynamiclib"
			}
			if !npoHasField(fields, mode) {
				t.Errorf("NPO-062: shared library link command lacks platform mode %q:\n%s", mode, line)
			}
		case filepath.Base(fields[0]) == "ar" && npoHasSuffixField(fields, "libdpa_pcie.a"):
			archives++
		}
		for _, tool := range []string{"cmake", "meson", "pkg-config", "python", "cargo", "cmake3"} {
			if regexp.MustCompile(`(^|\s|/)` + tool + `(\s|$)`).MatchString(line) {
				t.Errorf("NPO-062: native build invokes %q; only make, a C compiler and ar are allowed:\n%s", tool, line)
			}
		}
	}
	if compiles == 0 {
		t.Errorf("NPO-062: make -n -B native shows no C compile command:\n%s", out)
	}
	if shared == 0 {
		t.Errorf("NPO-062: make -n -B native shows no shared library command:\n%s", out)
	}
	if archives == 0 {
		t.Errorf("NPO-062: make -n -B native shows no ar archive command:\n%s", out)
	}
}

// npoOutputTarget returns the `-o` operand of a compiler/linker command line
// ("-o file" or "-ofile"), or "" when there is none.
func npoOutputTarget(fields []string) string {
	for i, f := range fields {
		if f == "-o" && i+1 < len(fields) {
			return fields[i+1]
		}
		if strings.HasPrefix(f, "-o") && len(f) > 2 && !strings.HasPrefix(f, "-op") {
			return f[2:]
		}
	}
	return ""
}

func npoHasField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func npoHasSuffixField(fields []string, suffix string) bool {
	for _, f := range fields {
		if strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

// npoPathWithoutGo returns PATH with every directory that contains a `go`
// executable removed.
func npoPathWithoutGo(t *testing.T) string {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, "go")); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			continue
		}
		kept = append(kept, dir)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// NPO-062: the native build requires only make, a C compiler and ar — on a
// fresh repository copy (no artifacts) it must succeed with `go` absent from
// PATH and create the archive under build/native/<GOOS>-<GOARCH>/.
func TestNPO062_NativeBuildNeedsOnlyMakeCcAr(t *testing.T) {
	npoRequireNativeToolchain(t)
	repo := npoRepoCopy(t)
	path := npoPathWithoutGo(t)
	t.Setenv("PATH", path)
	for _, tool := range []string{"make", "cc", "ar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("NPO-062: %s not found on PATH without go (%v)", tool, err)
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		t.Fatalf("fixture: go still reachable at %s after filtering PATH", p)
	}
	archive := npoArchivePath(repo)
	if _, err := os.Stat(archive); err == nil {
		t.Fatalf("fixture: %s already exists in a fresh copy", archive)
	}

	out, err := npoRun(repo, npoIsolatedEnv(t, "PATH="+path), "make", "native")
	if err != nil {
		head := out
		if len(head) > 1500 {
			head = head[:1500] + "\n..."
		}
		t.Fatalf("NPO-062: make native fails without go on PATH (%v):\n%s", err, head)
	}
	st, err := os.Stat(archive)
	if err != nil || st.Size() == 0 {
		t.Fatalf("NPO-062/NPO-061: make native without go did not create %s (%v)", archive, err)
	}
}

// NPO-060: on a platform outside the supported set, a cgo-enabled build must
// still succeed through the unavailable stub. darwin/amd64 is such a target
// from a darwin/arm64 host; other hosts try linux/386. If the cross C
// toolchain is missing the check is skipped with the reason.
func TestNPO060_UnsupportedPlatformWithCgoUsesStub(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	goos, goarch := "linux", "386"
	if runtime.GOOS == "darwin" {
		goos, goarch = "darwin", "amd64"
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		t.Skipf("NPO-060: host %s/%s coincides with the probe target", goos, goarch)
	}
	cmd := exec.Command("go", "build", "./internal/nativepcie/...")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS="+goos, "GOARCH="+goarch)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	text := string(out)
	toolchain := regexp.MustCompile(`(?i)(C compiler|cgo: .*not found|cannot find|no such file|unsupported (target|arch|platform)|unknown target|ld: |linker)`)
	if toolchain.MatchString(text) {
		t.Skipf("NPO-060: cross C toolchain for %s/%s unavailable (%v):\n%s", goos, goarch, err, text)
	}
	t.Errorf("NPO-060: CGO_ENABLED=1 GOOS=%s GOARCH=%s go build ./internal/nativepcie/... failed (%v):\n%s", goos, goarch, err, text)
}

// npoMakeDryRun returns every recipe line the dependency graph of <target>
// contains. -B treats all prerequisites as out of date so recipes are printed
// even when native artifacts are already current; nothing is built or removed.
func npoMakeDryRun(t *testing.T, repoRoot, target string) string {
	t.Helper()
	cmd := exec.Command("make", "-n", "-B", target)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("NPO-063: make -n -B %s failed: %v\n%s", target, err, out)
	}
	return string(out)
}

func TestNPO063_NativeArtifactsPrecedeGoInDependencyGraph(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	// A native compile/archive step: the C compiler or ar acting on the
	// library, or the library artifact itself being produced.
	native := regexp.MustCompile(`(?m)^.*(libdpa_pcie|-std=c11|\bar\s+[a-z]*r[a-z]*\s).*$`)
	for _, c := range []struct{ target, goCmd string }{{"test", "go test"}, {"build", "go build"}, {"all", "go build"}} {
		out := npoMakeDryRun(t, repoRoot, c.target)
		goIdx := strings.Index(out, c.goCmd)
		natIdx := native.FindStringIndex(out)
		if goIdx < 0 {
			t.Errorf("NPO-063: make -n %s does not run %q:\n%s", c.target, c.goCmd, out)
			continue
		}
		if natIdx == nil {
			t.Errorf("NPO-063: make -n %s prepares no native artifact before %q:\n%s", c.target, c.goCmd, out)
			continue
		}
		if natIdx[0] > goIdx {
			t.Errorf("NPO-063: make -n %s runs %q before native preparation:\n%s", c.target, c.goCmd, out)
		}
	}
}

// npoFreshCopyWithoutArtifacts returns a repository copy and verifies the
// precondition "no native artifacts, no build stamp" actually holds there.
func npoFreshCopyWithoutArtifacts(t *testing.T) string {
	t.Helper()
	repo := npoRepoCopy(t)
	if _, err := os.Stat(filepath.Join(repo, "build")); err == nil {
		t.Fatalf("fixture: build/ present in a fresh copy")
	}
	return repo
}

// NPO-063 third GIVEN / NPO-060: with native artifacts and the build stamp
// absent (fresh copy, isolated GOCACHE), CGO_ENABLED=0 go build ./... must
// succeed through the unavailable stub.
func TestNPO063_NoCgoBuildSucceedsWithoutNativeArtifacts(t *testing.T) {
	repo := npoFreshCopyWithoutArtifacts(t)
	env := npoIsolatedEnv(t, "CGO_ENABLED=0")
	if out, err := npoRun(repo, env, "go", "build", "./..."); err != nil {
		t.Fatalf("NPO-060/NPO-063: CGO_ENABLED=0 go build ./... failed without native artifacts: %v\n%s", err, out)
	}
	if out, err := npoRun(repo, env, "go", "vet", "./internal/nativepcie/..."); err != nil {
		t.Errorf("NPO-060: CGO_ENABLED=0 go vet ./internal/nativepcie/... failed without native artifacts: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "build")); err == nil {
		t.Errorf("NPO-063: CGO_ENABLED=0 go build created build/ (native artifacts must not be required or produced)")
	}
}

// NPO-060 / NPO-063: every other platform, and CGO_ENABLED=0 anywhere, must
// build the whole repository through the unavailable stub without native
// artifacts (fresh copy). Cross-compiles run in parallel as subtests.
func TestNPO060_UnavailableStubBuildsOnEveryTarget(t *testing.T) {
	repo := npoFreshCopyWithoutArtifacts(t)
	env := npoIsolatedEnv(t)
	targets := []struct{ goos, goarch string }{
		{"windows", "amd64"}, {"windows", "arm64"}, {"plan9", "amd64"},
		{"js", "wasm"}, {"wasip1", "wasm"}, {"freebsd", "amd64"},
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"},
	}
	for _, tg := range targets {
		name := tg.goos + "/" + tg.goarch
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("go", "build", "./...")
			cmd.Dir = repo
			cmd.Env = append(append([]string{}, env...), "CGO_ENABLED=0", "GOOS="+tg.goos, "GOARCH="+tg.goarch)
			out, err := cmd.CombinedOutput()
			if err != nil {
				head := string(out)
				if len(head) > 1200 {
					head = head[:1200] + "\n..."
				}
				t.Errorf("NPO-060/NPO-063: CGO_ENABLED=0 GOOS=%s GOARCH=%s go build ./... failed (%v):\n%s", tg.goos, tg.goarch, err, head)
			}
		})
	}
}

// npoUntracked returns the untracked ("??") paths reported by
// `git status --porcelain -uall`, ignored files excluded.
func npoUntracked(t *testing.T, repoRoot string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain", "-uall")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("NPO-064: git status failed: %v", err)
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "?? ") {
			paths[strings.TrimPrefix(line, "?? ")] = true
		}
	}
	return paths
}

// NPO-064 second GIVEN: once generated native artifacts and the build stamp
// exist, repository status must ignore them. The test establishes the GIVEN
// itself by running the generating make targets, then compares untracked
// paths before and after: nothing produced may surface as untracked. This
// holds whether or not the artifacts already existed, and it never removes
// anything. The stamp path need not be known.
func TestNPO064_GeneratedNativeArtifactsAreIgnored(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	npoRequireNativeToolchain(t)
	before := npoUntracked(t, repoRoot)

	for _, target := range []string{"native", "native-example", "native-asan"} {
		cmd := exec.Command("make", target)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			if target == "native-asan" {
				// NPO-072: an unsupported sanitizer toolchain may fail clearly.
				t.Logf("NPO-064: make native-asan failed (%v); sanitizer archive not generated:\n%s", err, out)
				continue
			}
			t.Fatalf("NPO-061/NPO-064: make %s failed: %v\n%s", target, err, out)
		}
	}

	after := npoUntracked(t, repoRoot)
	for p := range after {
		if !before[p] {
			t.Errorf("NPO-064: generating native artifacts left %q untracked and not ignored", p)
		}
	}
	// build/ is generated output in its entirety: nothing under it may be
	// untracked-but-not-ignored, even if it existed before this test.
	for p := range after {
		if strings.HasPrefix(p, "build/") {
			t.Errorf("NPO-064: %q under build/ is not ignored", p)
		}
	}
	// Nothing under build/ may be tracked.
	cmd := exec.Command("git", "ls-files", "build")
	cmd.Dir = repoRoot
	if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		t.Errorf("NPO-064: tracked files under build/:\n%s", out)
	}
}

func TestNPO066_DomainCoreDoesNotDependOnNativeAdapter(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	text := npoMakefileText(t, repoRoot)
	m := regexp.MustCompile(`(?m)^DOMAIN_CORE\s*:?=\s*(.+)$`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("NPO-066: Makefile has no DOMAIN_CORE list")
	}
	list := exec.Command("go", "list", "./...")
	list.Dir = repoRoot
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	module := "github.com/aetertinas9/data-path-assurance"
	core := regexp.MustCompile(`^` + regexp.QuoteMeta(module) + `/(` + strings.TrimSpace(m[1]) + `)(/.*)?$`)
	var pkgs []string
	for _, p := range strings.Fields(string(out)) {
		if core.MatchString(p) {
			pkgs = append(pkgs, p)
		}
	}
	if len(pkgs) == 0 {
		t.Skip("NPO-066: no domain core package present")
	}
	deps := exec.Command("go", append([]string{"list", "-deps", "-f", "{{.ImportPath}}"}, pkgs...)...)
	deps.Dir = repoRoot
	out, err = deps.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, d := range strings.Fields(string(out)) {
		if strings.HasSuffix(d, "/internal/nativepcie") || strings.Contains(d, "/internal/nativepcie/") {
			t.Errorf("NPO-066: domain core reaches the native adapter %s", d)
		}
	}
	// The nativepcie package itself must not be a dependency of the domain
	// packages through cgo either.
	cgo := exec.Command("go", append([]string{"list", "-deps", "-f", "{{if .CgoFiles}}{{.ImportPath}}{{end}}"}, pkgs...)...)
	cgo.Dir = repoRoot
	out, err = cgo.Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		t.Errorf("NPO-066: domain core transitively includes cgo packages:\n%s", out)
	}
}

// npoSentences splits Markdown into sentence-sized units: blank lines end a
// paragraph, a single line break inside a paragraph is a space (a wrapped
// sentence stays whole), each list item is its own unit, and units are then
// split at sentence terminators.
func npoSentences(body string) []string {
	listItem := regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s+`)
	terminator := regexp.MustCompile(`[.!?]\s|[.!?]$|다\.\s|다\.$`)
	var units []string
	for _, para := range regexp.MustCompile(`\n[ \t]*\n`).Split(body, -1) {
		var current []string
		flush := func() {
			if len(current) > 0 {
				units = append(units, strings.Join(current, " "))
				current = nil
			}
		}
		for _, line := range strings.Split(para, "\n") {
			if listItem.MatchString(line) {
				flush()
			}
			if s := strings.TrimSpace(line); s != "" {
				current = append(current, s)
			}
		}
		flush()
	}
	var sentences []string
	for _, u := range units {
		sentences = append(sentences, terminator.Split(u, -1)...)
	}
	return sentences
}

func TestNPO071_ReadmeShowsInjectedRootAndNoSysDefault(t *testing.T) {
	repoRoot := npoRepoRoot(t)
	var candidates []string
	for _, p := range []string{"README.md", "readme.md", "README"} {
		if _, err := os.Stat(filepath.Join(repoRoot, p)); err == nil {
			candidates = append(candidates, filepath.Join(repoRoot, p))
		}
	}
	_ = filepath.WalkDir(filepath.Join(repoRoot, "docs"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(path), ".md") {
			candidates = append(candidates, path)
		}
		return nil
	})
	var found string
	var body string
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, "nativepcie.ReadDevice") {
			found, body = p, s
			break
		}
	}
	if found == "" {
		t.Fatalf("NPO-071: no README/docs page demonstrates nativepcie.ReadDevice (searched %d files)", len(candidates))
	}
	if !strings.Contains(body, "os.OpenRoot") {
		t.Errorf("NPO-071: %s shows ReadDevice without os.OpenRoot", found)
	}
	if regexp.MustCompile(`OpenRoot\(\s*"/sys`).MatchString(body) {
		t.Errorf("NPO-071: %s presents a hard-coded /sys root", found)
	}
	// NPO-063 second GIVEN: documentation states that the native prerequisite
	// (`make native`) is required before plain go build/test.
	if !regexp.MustCompile("`?make native`?").MatchString(body) {
		t.Errorf("NPO-063: %s does not state that `make native` is a prerequisite for plain go build/test", found)
	}
	// NPO-071 second GIVEN: each of the four capabilities must be mentioned
	// in a context that says this delivery is NOT that. A sentence (or list
	// item) counts when it names the capability and carries a negating or
	// scoping marker; no particular wording is required.
	negation := regexp.MustCompile(`(?i)\b(not|no|never|without|isn't|doesn't|don't|cannot|can't|unvalidated|unverified|deferred|excluded|exclude[sd]?|out of scope|rather than|instead of|only|neither|nor|미|않|아니)\b|미검증|미실행|미지원|아님|않는|않았|않습|제외|범위 밖`)
	capabilities := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"live GPU observation", regexp.MustCompile(`(?i)live[ -]gpu|live (hardware|device|host|sysfs|observation)|실제 gpu|실기기|실장비`)},
		{"a runnable agent", regexp.MustCompile(`(?i)\bagent\b|에이전트`)},
		{"full PCIe qualification", regexp.MustCompile(`(?i)qualification|qualif|검증 전체|자격`)},
		{"production readiness", regexp.MustCompile(`(?i)production|프로덕션|운영 환경|상용`)},
	}
	sentences := npoSentences(body)
	for _, c := range capabilities {
		ok := false
		for _, s := range sentences {
			if c.re.MatchString(s) && negation.MatchString(s) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("NPO-071: %s never states, in context, that this delivery is not %s", found, c.name)
		}
	}
}
