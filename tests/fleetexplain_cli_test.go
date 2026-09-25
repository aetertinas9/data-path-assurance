package tests_test

// pathctl binary contract: arguments, exit codes, stderr, live refusal,
// input file opening and atomic output (GFX-010..015).

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gfxBasicInputs builds S-BASIC with path-agent and writes it with the
// normative fleet file.
func gfxBasicInputs(t *testing.T, bins gfxBins) (gfxInputs, *gfoArtifact) {
	t.Helper()
	a := gfxAgentArtifact(t, bins.Agent, gfxSBasic(t))
	return gfxWriteInputs(t, a.Raw, gfxDefaultFleet().JSON()), a
}

func gfxFifo(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatalf("mkfifo %s: %v", p, err)
	}
	return p
}

// TestGFX010_ArgumentTable covers every row of the GFX-010 table. All usage
// cases name FIFOs as input files: exit 2 must be decided before any file
// access, so a FIFO open would block and fail the case.
func TestGFX010_ArgumentTable(t *testing.T) {
	bins := gfxBuild(t, true)
	in, _ := gfxBasicInputs(t, bins)
	dir := t.TempDir()
	fa := gfxFifo(t, dir, "artifact.fifo")
	ff := gfxFifo(t, dir, "fleet.fifo")
	n := gfxDevName
	long254 := strings.Repeat("a", 254)
	usage := [][]string{
		{},
		{"explain"},
		{"explain", "gpu"},
		{"explain", "gpu", n},
		{"Explain", "gpu", n, "--artifact", fa, "--fleet", ff},
		{"explain", "GPU", n, "--artifact", fa, "--fleet", ff},
		{"explain", "pod", n, "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", "-x", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", "", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", "a b", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", "a\tb", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", "gpü", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", long254, "--artifact", fa, "--fleet", ff},
		{"explain", "node", long254, "--artifact", fa, "--fleet", ff},
		{"explain", "--artifact", fa, "gpu", n, "--fleet", ff},
		{"explain", "gpu", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", n, "extra", "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", n, "-artifact", fa, "--fleet", ff},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "-output=json"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "-"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--"},
		{"explain", "gpu", n, "--"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--zqxflag"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--zqxflag=v"},
		{"explain", "gpu", n, "--Artifact", fa, "--fleet", ff},
		{"explain", "gpu", n, "--artifact", fa, "--artifact", fa, "--fleet", ff},
		{"explain", "gpu", n, "--artifact=" + fa, "--fleet=" + ff, "--fleet=" + ff},
		{"explain", "gpu", n, "--artifact=", "--fleet", ff},
		{"explain", "gpu", n, "--artifact", "", "--fleet", ff},
		{"explain", "gpu", n, "--artifact", fa, "--fleet"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output", "JSON"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output=Text"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output=yaml"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output="},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--output", "json", "--output", "json"},
		{"explain", "gpu", n, "--artifact", fa},
		{"explain", "gpu", n, "--fleet", ff},
		{"explain", "gpu", n, "--output", "json"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--server", "127.0.0.1:1"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--key-file", ff},
		{"explain", "gpu", n, "--artifact", fa, "--server", "127.0.0.1:1"},
		{"explain", "gpu", n, "--fleet", ff, "--cluster-id", "lab-a"},
		{"explain", "gpu", n, "--server", "127.0.0.1:1", "--output", "bogus"},
		{"explain", "gpu", n, "--server", "127.0.0.1:1", "--server", "127.0.0.2:1"},
		{"explain", "gpu", n, "--server="},
		{"explain", "gpu", n, "--ca-file", ""},
		{"explain", "gpu", n, "--server", "127.0.0.1:1", "--zqxflag"},
		{"explain", "gpu", n, "--controller", "127.0.0.1:1"},
		{"-h", "explain"},
		{"explain", "-h"},
		{"--help", "--help"},
		{"-h", "-h"},
		{"explain", "gpu", n, "-h"},
		{"explain", "gpu", n, "--artifact", fa, "--fleet", ff, "--help"},
		{"-H"},
		{"--help=x"},
		{"help"},
	}
	gfxWarm(t, bins.Pathctl)
	for _, args := range usage {
		name := "GFX-010 usage " + strings.Join(args, " ")
		gfxQuickRuns(t, bins.Pathctl, name+" (exit 2 must be decided before any file access)", func(res gfoResult) {
			gfxFail(t, res, 2, name, "zqxflag", fa, ff)
		}, args...)
	}

	// Offline forms that pass argument checking.
	ok := [][]string{
		{"explain", "gpu", n, "--artifact", in.Artifact, "--fleet", in.Fleet},
		{"explain", "gpu", n, "--artifact=" + in.Artifact, "--fleet=" + in.Fleet, "--output=json"},
		{"explain", "gpu", n, "--output", "text", "--fleet", in.Fleet, "--artifact", in.Artifact},
		{"explain", "node", gfoNodeName, "--fleet=" + in.Fleet, "--output", "json", "--artifact", in.Artifact},
	}
	for _, args := range ok {
		gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, args...), "GFX-010 "+strings.Join(args[3:], " "))
	}

	// Flag values are taken verbatim, even when they start with '-' or
	// contain '='; '-' is an ordinary path.
	art, _ := os.ReadFile(in.Artifact)
	fl, _ := os.ReadFile(in.Fleet)
	cwd := t.TempDir()
	gfxWriteFile(t, filepath.Join(cwd, "-h"), art)
	gfxWriteFile(t, filepath.Join(cwd, "--output"), fl)
	gfxWriteFile(t, filepath.Join(cwd, "-"), art)
	gfxWriteFile(t, filepath.Join(cwd, "x=y.json"), fl)
	values := []struct {
		args []string
		json bool
	}{
		{[]string{"explain", "gpu", n, "--artifact", "-h", "--fleet", "--output"}, false},
		{[]string{"explain", "gpu", n, "--artifact=-", "--fleet=x=y.json"}, false},
		{[]string{"explain", "gpu", n, "--artifact", "-", "--fleet", "x=y.json", "--output", "json"}, true},
	}
	text := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, "explain", "gpu", n, "--artifact", in.Artifact, "--fleet", in.Fleet), "GFX-010 reference")
	js := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, "explain", "gpu", n, "--artifact", in.Artifact, "--fleet", in.Fleet, "--output", "json"), "GFX-010 reference json")
	for _, v := range values {
		out := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{Dir: cwd}, v.args...), "GFX-010 verbatim values "+strings.Join(v.args[3:], " "))
		want := text
		if v.json {
			want = js
		}
		if !bytes.Equal(out, want) {
			t.Errorf("GFX-010/GFX-004: %v: output differs from the same inputs given by absolute path", v.args)
		}
	}

	// A syntactically valid 253-byte name reaches target selection (exit 4).
	name253 := strings.Repeat("a", 253)
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, "explain", "gpu", name253, "--artifact", in.Artifact, "--fleet", in.Fleet), 4, "GFX-010/GFX-024 253-byte gpu name", name253)
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, "explain", "node", name253, "--artifact", in.Artifact, "--fleet", in.Fleet), 4, "GFX-010/GFX-024 253-byte node name", name253)
}

// TestGFX010_HelpOnly: exactly one -h or --help prints usage to stderr only.
func TestGFX010_HelpOnly(t *testing.T) {
	bins := gfxBuild(t, false)
	for _, arg := range []string{"-h", "--help"} {
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, arg)
		if res.Exit != 0 || len(res.Stdout) != 0 {
			t.Errorf("GFX-010 %s: exit %d stdout %d bytes, want exit 0 and 0 stdout bytes", arg, res.Exit, len(res.Stdout))
		}
		if len(res.Stderr) == 0 || len(res.Stderr) > 8192 || res.Stderr[len(res.Stderr)-1] != '\n' {
			t.Errorf("GFX-010/GFX-012 %s: usage on stderr is %d bytes, want 1..8192 ending in a newline", arg, len(res.Stderr))
		}
		for i, c := range res.Stderr {
			if (c < 0x20 || c > 0x7e) && c != '\n' {
				t.Errorf("GFX-012 %s: usage byte %d is 0x%02x", arg, i, c)
				break
			}
		}
	}
}

// TestGFX011_ProcessingOrder: the first failing step decides the exit code,
// including the three examples of GFX-011, and the fleet file is not opened
// before the artifact is fully accepted.
func TestGFX011_ProcessingOrder(t *testing.T) {
	bins := gfxBuild(t, true)
	in, _ := gfxBasicInputs(t, bins)
	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	gfxWriteFile(t, badJSON, []byte("{"))
	bigFleet := filepath.Join(dir, "big-fleet.json")
	gfxWriteFile(t, bigFleet, bytes.Repeat([]byte(" "), gfxFleetMax+1))
	bigArtifact := filepath.Join(dir, "big-artifact.bin")
	gfxWriteFile(t, bigArtifact, bytes.Repeat([]byte("x"), gfxArtifactMax+1))
	fifoFleet := gfxFifo(t, dir, "fleet.fifo")
	fifoArtifact := gfxFifo(t, dir, "artifact.fifo")
	missing := filepath.Join(dir, "missing.json")
	badFleet := filepath.Join(dir, "bad-fleet.json")
	gfxWriteFile(t, badFleet, []byte("{}"))
	other := gfxDefaultFleet()
	other.ClusterID = "lab-b"
	otherFleet := filepath.Join(dir, "lab-b.json")
	gfxWriteFile(t, otherFleet, []byte(other.JSON()))

	cases := []struct {
		name, kind, target, artifact, fleet string
		want                                int
	}{
		{"GFX-011 example: invalid JSON artifact + oversized fleet", "gpu", gfxDevName, badJSON, bigFleet, 6},
		{"GFX-011 example: oversized non-JSON artifact", "gpu", gfxDevName, bigArtifact, in.Fleet, 8},
		{"GFX-011 example: valid artifact + FIFO fleet", "gpu", gfxDevName, in.Artifact, fifoFleet, 7},
		{"artifact missing + fleet missing", "gpu", gfxDevName, missing, missing, 6},
		{"FIFO artifact + FIFO fleet", "gpu", gfxDevName, fifoArtifact, fifoFleet, 6},
		{"invalid artifact + FIFO fleet (fleet not opened)", "gpu", gfxDevName, badJSON, fifoFleet, 6},
		{"oversized artifact + missing fleet", "gpu", gfxDevName, bigArtifact, missing, 8},
		{"oversized artifact + FIFO fleet", "gpu", gfxDevName, bigArtifact, fifoFleet, 8},
		{"valid artifact + oversized fleet", "gpu", gfxDevName, in.Artifact, bigFleet, 8},
		{"valid artifact + invalid fleet + unknown name", "gpu", "zqx-missing", in.Artifact, badFleet, 7},
		{"valid inputs that disagree + unknown name", "gpu", "zqx-missing", in.Artifact, otherFleet, 7},
		{"valid inputs + unknown gpu", "gpu", "zqx-missing", in.Artifact, in.Fleet, 4},
		{"valid inputs + unknown node", "node", "zqx-missing", in.Artifact, in.Fleet, 4},
	}
	gfxWarm(t, bins.Pathctl)
	for _, c := range cases {
		args := []string{"explain", c.kind, c.target, "--artifact", c.artifact, "--fleet", c.fleet}
		check := func(res gfoResult) { gfxFail(t, res, c.want, c.name, "zqx-missing", dir) }
		if strings.Contains(c.name, "FIFO") {
			gfxQuickRuns(t, bins.Pathctl, c.name+" (FIFO must not be waited on)", check, args...)
			continue
		}
		check(gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, args...))
	}
	// Exit 3 is reserved for S3b and is never produced; exit 5 needs no files.
	res := gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, "explain", "gpu", gfxDevName, "--server", "127.0.0.1:1")
	gfxFail(t, res, 5, "GFX-011 live refusal")
}

// TestGFX012_StderrDiscipline: failing runs write one stderr line that never
// contains flag values, paths, names, unknown flag names or input content.
func TestGFX012_StderrDiscipline(t *testing.T) {
	bins := gfxBuild(t, true)
	_, a := gfxBasicInputs(t, bins)
	marker := "zqx-secret-dir"
	dir := filepath.Join(t.TempDir(), marker)
	art := filepath.Join(dir, "zqx-artifact-file.json")
	gfxWriteFile(t, art, a.Raw)
	fl := filepath.Join(dir, "zqx-fleet-file.json")
	gfxWriteFile(t, fl, []byte(gfxDefaultFleet().JSON()))
	forbid := []string{marker, "zqx-artifact-file", "zqx-fleet-file", "zqx"}

	run := func(args ...string) gfoResult {
		return gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, args...)
	}
	gfxFail(t, run("explain", "gpu", "zqx-name", "--artifact", art, "--fleet", fl), 4, "GFX-012 unknown gpu name", forbid...)
	gfxFail(t, run("explain", "node", "zqx-node", "--artifact", art, "--fleet", fl), 4, "GFX-012 unknown node name", forbid...)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", art, "--fleet", fl, "--zqxunknown=zqxvalue"), 2, "GFX-012 unknown flag", forbid...)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", art, "--fleet", fl, "--output", "zqxformat"), 2, "GFX-012 bad output value", forbid...)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", filepath.Join(dir, "zqx-missing.json"), "--fleet", fl), 6, "GFX-012 missing artifact", forbid...)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", art, "--fleet", filepath.Join(dir, "zqx-missing.json")), 7, "GFX-012 missing fleet", forbid...)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--key-file", filepath.Join(dir, "zqx-key.pem")), 5, "GFX-012 live", forbid...)

	// Input-derived keys, strings and numbers stay out of stderr.
	good := gfxDefaultFleet().JSON()
	fleetCases := map[string]string{
		"unknown key":       gfxReplace(t, good, `"clusterID":"lab-a"`, `"clusterID":"lab-a","zqxunknownkey":1`),
		"bad clusterID":     gfxReplace(t, good, `"clusterID":"lab-a"`, `"clusterID":"zqx#bad"`),
		"bad freshness":     gfxReplace(t, good, `"freshnessSeconds":60`, `"freshnessSeconds":86401`),
		"bad device name":   gfxReplace(t, good, `"name":"gpu-node-1-gpu0"`, `"name":"ZQX_BAD"`),
		"bad nested key":    gfxReplace(t, good, `"vendor":"NVIDIA"`, `"vendor":"NVIDIA","zqxclaimkey":"x"`),
		"cluster mismatch":  gfxReplace(t, good, `"clusterID":"lab-a"`, `"clusterID":"zqx-other-cluster"`),
		"node uid mismatch": gfxReplace(t, good, `"uid":"7c9e6679-7425-40de-944b-e07fc1f90ae7"`, `"uid":"zqx-other-node"`),
	}
	for name, body := range fleetCases {
		p := filepath.Join(dir, "zqx-case.json")
		gfxWriteFile(t, p, []byte(body))
		gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", art, "--fleet", p), 7, "GFX-012 fleet "+name, append(forbid, "86401", "ZQX")...)
	}
	artCases := map[string][]byte{
		"unknown key": bytes.Replace(a.Raw, []byte(`"mode":"offline"`), []byte(`"mode":"offline","zqxartkey":1`), 1),
		"bad mode":    bytes.Replace(a.Raw, []byte(`"mode":"offline"`), []byte(`"mode":"zqxmode"`), 1),
		"bad cluster": bytes.Replace(a.Raw, []byte(`"clusterID":"lab-a"`), []byte(`"clusterID":"zqx#"`), 1),
	}
	for name, body := range artCases {
		p := filepath.Join(dir, "zqx-art-case.json")
		gfxWriteFile(t, p, body)
		gfxFail(t, run("explain", "gpu", gfxDevName, "--artifact", p, "--fleet", fl), 6, "GFX-012 artifact "+name, forbid...)
	}
	// A huge unknown flag name and a huge name keep stderr within 8 KiB.
	huge := strings.Repeat("z", 100000)
	gfxFail(t, run("explain", "gpu", gfxDevName, "--"+huge), 2, "GFX-012 huge flag", huge[:64])
	gfxFail(t, run("explain", "gpu", huge, "--artifact", art, "--fleet", fl), 2, "GFX-012 huge name", huge[:64])
}

// TestGFX013_LiveUnsupported: transport-only arguments end with exit 5
// within the bound, without connecting, opening flag paths or echoing them.
func TestGFX013_LiveUnsupported(t *testing.T) {
	bins := gfxBuild(t, false)
	dir := t.TempDir()
	fifo := gfxFifo(t, dir, "secret.fifo")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	addr := ln.Addr().String()
	n := gfxDevName
	cases := [][]string{
		{"explain", "gpu", n, "--key-file", fifo},
		{"explain", "gpu", n, "--ca-file", fifo},
		{"explain", "gpu", n, "--cert-file", fifo},
		{"explain", "gpu", n, "--server", addr},
		{"explain", "node", gfoNodeName, "--cluster-id", "lab-a"},
		{"explain", "gpu", n, "--server", addr, "--cluster-id", "lab-a", "--ca-file", fifo, "--cert-file", fifo, "--key-file", fifo},
		{"explain", "gpu", n, "--server=" + addr, "--output=json"},
		{"explain", "node", gfoNodeName, "--output", "text", "--server", "-h"},
		{"explain", "gpu", n, "--server", "10.255.255.1:65000"},
	}
	gfxWarm(t, bins.Pathctl)
	for _, args := range cases {
		name := "GFX-013 " + strings.Join(args[3:], " ")
		gfxQuickRuns(t, bins.Pathctl, name, func(res gfoResult) {
			gfxFail(t, res, 5, name, fifo, addr, "10.255.255.1")
		}, args...)
	}
	select {
	case <-accepted:
		t.Errorf("GFX-013: pathctl connected to the --server address")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestGFX014_InputFileOpening: symlinks are followed, non-regular targets are
// rejected without waiting, and the size limits allow exactly the bound.
func TestGFX014_InputFileOpening(t *testing.T) {
	bins := gfxBuild(t, true)
	in, a := gfxBasicInputs(t, bins)
	dir := t.TempDir()
	link := func(name, target string) string {
		p := filepath.Join(dir, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return p
	}
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := gfxFifo(t, dir, "input.fifo")
	nonRegular := map[string]string{
		"directory":            sub,
		"FIFO":                 fifo,
		"symlink to directory": link("to-dir", sub),
		"symlink to FIFO":      link("to-fifo", fifo),
		"dangling symlink":     link("dangling", filepath.Join(dir, "absent")),
		"symlink to device":    link("to-null", os.DevNull),
		"missing":              filepath.Join(dir, "absent.json"),
	}
	if sock, ok := gfxSocket(t, dir); ok {
		nonRegular["socket"] = sock
	}
	gfxWarm(t, bins.Pathctl)
	for name, p := range nonRegular {
		ca, cf := "GFX-014 artifact "+name, "GFX-014 fleet "+name
		gfxQuickRuns(t, bins.Pathctl, ca, func(res gfoResult) { gfxFail(t, res, 6, ca, dir) },
			gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: p, Fleet: in.Fleet}, "")...)
		gfxQuickRuns(t, bins.Pathctl, cf, func(res gfoResult) { gfxFail(t, res, 7, cf, dir) },
			gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: in.Artifact, Fleet: p}, "")...)
	}

	// Symlinks to regular files are followed.
	want := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "json")...), "GFX-014 reference")
	viaLinks := gfxInputs{Artifact: link("art-link", in.Artifact), Fleet: link("fleet-link", in.Fleet)}
	chained := gfxInputs{Artifact: link("art-link2", viaLinks.Artifact), Fleet: link("fleet-link2", viaLinks.Fleet)}
	for _, li := range []gfxInputs{viaLinks, chained} {
		got := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, li, "json")...), "GFX-014 symlinked inputs")
		if !bytes.Equal(got, want) {
			t.Errorf("GFX-014/GFX-004: output through symlinks differs from direct paths")
		}
	}

	// Permission denied (observable only without root privileges).
	if os.Geteuid() == 0 {
		t.Log("GFX-105: permission denial is not observable as root; skipped")
	} else {
		pa := filepath.Join(dir, "noperm-artifact.json")
		gfxWriteFile(t, pa, a.Raw)
		pf := filepath.Join(dir, "noperm-fleet.json")
		gfxWriteFile(t, pf, []byte(gfxDefaultFleet().JSON()))
		for _, p := range []string{pa, pf} {
			if err := os.Chmod(p, 0o000); err != nil {
				t.Fatal(err)
			}
			p := p
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		}
		gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: pa, Fleet: in.Fleet}, "")...), 6, "GFX-014 artifact permission", dir)
		gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: in.Artifact, Fleet: pf}, "")...), 7, "GFX-014 fleet permission", dir)
	}

	// Size limits: exactly the bound is accepted (whitespace after the value
	// is allowed), one byte more is exit 8 even for otherwise valid content.
	pad := func(data []byte, size int) []byte {
		out := append([]byte{}, data...)
		return append(out, bytes.Repeat([]byte(" "), size-len(out))...)
	}
	fleetJSON := []byte(gfxDefaultFleet().JSON())
	exactArt := filepath.Join(dir, "exact-artifact.json")
	gfxWriteFile(t, exactArt, pad(a.Raw, gfxArtifactMax))
	overArt := filepath.Join(dir, "over-artifact.json")
	gfxWriteFile(t, overArt, pad(a.Raw, gfxArtifactMax+1))
	exactFleet := filepath.Join(dir, "exact-fleet.json")
	gfxWriteFile(t, exactFleet, pad(fleetJSON, gfxFleetMax))
	overFleet := filepath.Join(dir, "over-fleet.json")
	gfxWriteFile(t, overFleet, pad(fleetJSON, gfxFleetMax+1))
	got := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: exactArt, Fleet: exactFleet}, "json")...), "GFX-014 inputs of exactly the size bounds")
	if !bytes.Equal(got, want) {
		t.Errorf("GFX-014: padded inputs at the bounds changed the output")
	}
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: overArt, Fleet: in.Fleet}, "")...), 8, "GFX-014 artifact bound + 1")
	gfxFail(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, gfxInputs{Artifact: in.Artifact, Fleet: overFleet}, "")...), 8, "GFX-014 fleet bound + 1")
}

// gfxSocket creates a unix socket file in dir when the platform path limit
// allows it.
func gfxSocket(t *testing.T, dir string) (string, bool) {
	t.Helper()
	p := filepath.Join(dir, "s.sock")
	if len(p) >= 100 {
		t.Logf("GFX-014 socket case skipped: socket path %d bytes exceeds the platform limit", len(p))
		return "", false
	}
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Logf("GFX-014 socket case skipped: %v", err)
		return "", false
	}
	t.Cleanup(func() { _ = l.Close() })
	return p, true
}

// TestGFX015_WriteFailure: a stdout that cannot be written ends with exit 1
// (internal) and the single stderr line; stdout of a success is one JSON
// line or the text form.
func TestGFX015_WriteFailure(t *testing.T) {
	bins := gfxBuild(t, true)
	in, _ := gfxBasicInputs(t, bins)
	ro := filepath.Join(t.TempDir(), "readonly-stdout")
	gfxWriteFile(t, ro, nil)
	f, err := os.Open(ro)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, output := range []string{"json", "text"} {
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{Stdout: f, Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, in, output)...)
		if res.Exit != 1 {
			t.Errorf("GFX-015 %s: stdout write failure (read-only descriptor) exit %d, want 1; stderr=%q", output, res.Exit, gfoTail(res.Stderr))
			continue
		}
		if m := gfxStderrRE.FindSubmatch(res.Stderr); m == nil || string(m[1]) != "internal" {
			t.Errorf("GFX-012/GFX-015 %s: stderr %q, want one `pathctl: error: internal: ...` line", output, gfoTail(res.Stderr))
		}
	}
	if runtime.GOOS == "linux" {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Logf("GFX-015 /dev/full case skipped: %v", err)
			return
		}
		defer full.Close()
		res := gfxRun(t, bins.Pathctl, gfoRunOpt{Stdout: full, Timeout: gfxHang}, gfxArgs("gpu", gfxDevName, in, "json")...)
		if res.Exit != 1 {
			t.Errorf("GFX-015 /dev/full: exit %d, want 1; stderr=%q", res.Exit, gfoTail(res.Stderr))
		}
	}
	// Success writes exactly one JSON line; every failure writes nothing
	// (asserted by gfxFail across the suite).
	out := gfxOK(t, gfxRun(t, bins.Pathctl, gfoRunOpt{}, gfxArgs("gpu", gfxDevName, in, "json")...), "GFX-015 json")
	if bytes.Count(out, []byte("\n")) != 1 || out[len(out)-1] != '\n' {
		t.Errorf("GFX-015: JSON output is not one line plus a newline")
	}
}
