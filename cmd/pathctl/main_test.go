package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../testdata/fixtures/two-socket-rtx"

func TestPathAndCheck(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"path", "--fixture", fixture}, &out, &errb); code != 0 {
		t.Fatalf("path exit %d: %s", code, errb.String())
	}
	golden, err := os.ReadFile("../../testdata/golden/two-socket-rtx.txt")
	if err != nil {
		t.Fatal(err)
	}
	// The first line names the fixture path as given on the command line.
	if got, want := afterFirstLine(out.String()), afterFirstLine(string(golden)); got != want {
		t.Errorf("path output differs from golden:\n%s", out.String())
	}

	out.Reset()
	if code := run([]string{"check", "--fixture", fixture}, &out, &errb); code != 3 {
		t.Errorf("check exit %d, want 3 (a check fails)", code)
	}
	if !strings.Contains(out.String(), "RESULT FAIL") || !strings.Contains(out.String(), "p2p-acs          FAIL") {
		t.Errorf("check output:\n%s", out.String())
	}

	out.Reset()
	if code := run([]string{"path", "--fixture", fixture, "--gpu", "1", "--output", "json"}, &out, &errb); code != 0 {
		t.Fatalf("json exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"bdf": "0000:1d:00.0"`) || strings.Contains(out.String(), `"bdf": "0000:9b:00.0",\n    "index"`) {
		t.Errorf("gpu selection by index:\n%s", out.String())
	}
	out.Reset()
	if code := run([]string{"path", "--fixture", fixture, "--gpu", "GPU-33333333-3333-3333-3333-333333333333"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "GPU 0000:9b:00.0") {
		t.Errorf("gpu selection by uuid: %d\n%s", code, out.String())
	}
	if code := run([]string{"path", "--fixture", fixture, "--gpu", "nope"}, &out, &errb); code != 4 {
		t.Errorf("missing gpu exit %d, want 4", code)
	}
	if code := run([]string{"path", "--fixture", fixture, "--locality", "near"}, &out, &errb); code != 2 {
		t.Errorf("bad locality exit %d, want 2", code)
	}
	out.Reset()
	if code := run([]string{"path", "--fixture", fixture, "--output", "mermaid"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "flowchart TB") {
		t.Errorf("mermaid: %d\n%s", code, out.String())
	}
}

// TestCaptureRoundTrip captures the fixture's sysfs as if it were a live host
// and checks that the capture reads back identically.
func TestCaptureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	// Without nvidia-smi the capture still writes the sysfs read set.
	code := run([]string{"capture", "--out", dir, "--sysfs", filepath.Join(fixture, "sys"), "--nvidia-smi", "/nonexistent/nvidia-smi"}, &out, &errb)
	if code != 0 {
		t.Fatalf("capture exit %d: %s", code, errb.String())
	}
	for _, name := range []string{"query.csv", "nvlink.txt", "topo.txt"} {
		src := filepath.Join(fixture, "nvidia", name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nvidia", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var a, b bytes.Buffer
	if code := run([]string{"path", "--fixture", fixture, "--output", "json"}, &a, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if code := run([]string{"path", "--fixture", dir, "--output", "json"}, &b, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	norm := func(s string) string { return strings.SplitN(s, "\n", 3)[2] } // drop "{" and the source line
	if norm(a.String()) != norm(b.String()) {
		t.Errorf("capture differs:\n%s\n----\n%s", a.String(), b.String())
	}
}

func afterFirstLine(s string) string {
	_, rest, _ := strings.Cut(s, "\n")
	return rest
}

func TestWatch(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"watch", "--fixture", fixture, "--interval", "10ms", "--count", "2", "--gpu", "0000:9b:00.0"}, &out, &errb)
	if code != 3 {
		t.Fatalf("watch exit %d, want 3 (a FAIL was seen): %s", code, errb.String())
	}
	s := out.String()
	if strings.Count(s, "result=FAIL gpus=1") != 2 {
		t.Errorf("expected two reads:\n%s", s)
	}
	// Every check is printed on the first read and, the fixture being
	// static, nothing on the second.
	if strings.Count(s, "0000:9b:00.0 gpu-link FAIL") != 1 || strings.Contains(s, "->") {
		t.Errorf("change lines:\n%s", s)
	}
	out.Reset()
	if code := run([]string{"watch", "--fixture", "../../testdata/fixtures/nvlink-pair", "--interval", "10ms", "--count", "1", "--gpu", "0"}, &out, &errb); code != 0 {
		t.Errorf("healthy GPU watch exit %d: %s", code, out.String())
	}
	if code := run([]string{"watch", "--fixture", fixture, "--interval", "0s"}, &out, &errb); code != 2 {
		t.Errorf("zero interval exit %d, want 2", code)
	}
}
