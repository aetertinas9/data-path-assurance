package tests_test

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gfoLiveFlags are the GFL-096 live flags listed by GFO-010 (v1.4 adds
// --boot-id-file, GFL-096 v1.2).
var gfoLiveFlags = []string{
	"controller", "cluster-id", "node-name", "node-uid", "sysfs-root",
	"ca-file", "cert-file", "key-file", "nvidia-smi", "pod-resources-socket", "boot-id-file",
}

// gfoLiveRequired are the eight required live flags in a fixed order (GLI-080).
var gfoLiveRequired = []string{"controller", "cluster-id", "node-name", "node-uid", "sysfs-root", "ca-file", "cert-file", "key-file"}

// gfoLiveArgs returns the eight required live flags with values that satisfy
// every GLI-080 value rule (the paths do not exist), the overrides applied by
// flag name, then the extra arguments. An override with the value "\x00"
// drops the flag.
func gfoLiveArgs(over map[string]string, extra ...string) []string {
	defaults := map[string]string{
		"controller": "localhost:1", "cluster-id": gfoCluster, "node-name": gfoNodeName, "node-uid": gfoNodeUID,
		"sysfs-root": "/nonexistent/sysfs-root", "ca-file": "/nonexistent/ca-file", "cert-file": "/nonexistent/cert-file", "key-file": "/nonexistent/key-file",
	}
	var args []string
	for _, fl := range gfoLiveRequired {
		v := defaults[fl]
		if o, ok := over[fl]; ok {
			v = o
		}
		if v == "\x00" {
			continue
		}
		args = append(args, "--"+fl, v)
	}
	return append(args, extra...)
}

// gfoFillNames creates n non-canonical regular files in bus/pci/devices of a
// frame (each is one enumerated name for GFO-041/GFO-095).
func gfoFillNames(t *testing.T, f *gfoFx, frame, n int) {
	t.Helper()
	dir := f.P(gfoSys(frame) + "/bus/pci/devices")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("x%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// GFO-001: cmd/path-agent builds and turns a fixture root into an offline
// snapshot artifact on stdout.
func TestGFO001_PathAgentEmitsOfflineArtifact(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	a := gfoNewBasic(t, 1).OK(bin, "GFO-001")
	if len(a.Frames) != 1 || a.Frames[0].Completeness != "COMPLETE" {
		t.Fatalf("GFO-001: basic fixture gave %d frames, first %q; want 1 COMPLETE frame", len(a.Frames), a.Frames[0].Completeness)
	}
}

// GFO-010 (v1.4): the argument table, "--x v" == "--x=v", and the live mode
// priority usage (exit 2) > live-unsupported (exit 5) > live-config (exit 6).
func TestGFO010_ArgumentTable(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	f := gfoNewBasic(t, 1)
	f.save()
	root := f.Root

	spaced := gfoExec(t, bin, gfoRunOpt{}, "--fixture-root", root)
	gfoOK(t, spaced, &f.M, "GFO-010 --fixture-root <dir>")
	equals := gfoExec(t, bin, gfoRunOpt{}, "--fixture-root="+root)
	gfoOK(t, equals, &f.M, "GFO-010 --fixture-root=<dir>")
	if !bytes.Equal(spaced.Stdout, equals.Stdout) {
		t.Errorf("GFO-010: --fixture-root <dir> and --fixture-root=<dir> produced different stdout")
	}

	type tc struct {
		name string
		args []string
		exit int
	}
	cases := []tc{
		{"no_args", nil, 2},
		{"unknown_flag", []string{"--bogus"}, 2},
		{"unknown_flag_with_value", []string{"--bogus=1"}, 2},
		{"positional_only", []string{"fixture"}, 2},
		{"fixture_then_positional", []string{"--fixture-root", root, "extra"}, 2},
		{"positional_then_fixture", []string{"extra", "--fixture-root", root}, 2},
		{"fixture_repeated", []string{"--fixture-root", root, "--fixture-root", root}, 2},
		{"fixture_repeated_equals", []string{"--fixture-root=" + root, "--fixture-root=" + root}, 2},
		{"fixture_missing_value", []string{"--fixture-root"}, 2},
		{"fixture_empty_equals", []string{"--fixture-root="}, 2},
		{"fixture_empty_argument", []string{"--fixture-root", ""}, 2},
		{"fixture_plus_unknown", []string{"--fixture-root", root, "--bogus"}, 2},
		{"help_plus_fixture", []string{"--help", "--fixture-root", root}, 2},
		{"invalid_fixture_plus_live_is_usage", []string{"--fixture-root", filepath.Join(root, "missing"), "--controller", "127.0.0.1:1"}, 2},
		// v1.4 (GFO-010/GLI-080): live flags without --fixture-root are live mode. Every value here is a
		// path-like string, so --controller (host:port) and --cluster-id (pattern) violate the value rules: exit 2.
		{"all_live_flags", nil, 2},
		{"all_live_flags_valid_values_with_pod_resources", gfoLiveArgs(nil, "--pod-resources-socket", "/nonexistent/pod-resources-socket"), 5},
		{"all_live_flags_valid_values_without_pod_resources", gfoLiveArgs(nil), 6},
		{"value_rule_violation_beats_pod_resources", gfoLiveArgs(map[string]string{"cluster-id": "bad id"}, "--pod-resources-socket", "/nonexistent/s"), 2},
		{"missing_required_beats_pod_resources", gfoLiveArgs(map[string]string{"node-uid": "\x00"}, "--pod-resources-socket", "/nonexistent/s"), 2},
		{"pod_resources_beats_live_config", gfoLiveArgs(nil, "--boot-id-file", "/nonexistent/boot-id", "--pod-resources-socket", "/nonexistent/s"), 5},
		{"fixture_plus_boot_id_file_is_usage", []string{"--fixture-root", root, "--boot-id-file", "/nonexistent/boot-id"}, 2},
		{"live_plus_unknown", []string{"--controller", "127.0.0.1:1", "--bogus"}, 2},
		{"live_plus_positional", []string{"--controller", "127.0.0.1:1", "extra"}, 2},
		{"live_repeated", []string{"--controller", "127.0.0.1:1", "--controller", "127.0.0.1:2"}, 2},
		{"live_missing_value", []string{"--cluster-id", "c", "--controller"}, 2},
	}
	for i := range cases {
		if cases[i].name == "all_live_flags" {
			for _, fl := range gfoLiveFlags {
				cases[i].args = append(cases[i].args, "--"+fl, "/nonexistent/"+fl)
			}
		}
	}
	for _, fl := range gfoLiveFlags {
		cases = append(cases,
			tc{"fixture_plus_" + fl, []string{"--fixture-root", root, "--" + fl, "/nonexistent/" + fl}, 2},
			tc{"fixture_plus_" + fl + "_equals", []string{"--fixture-root=" + root, "--" + fl + "=/nonexistent/" + fl}, 2},
			tc{fl + "_before_fixture", []string{"--" + fl, "/nonexistent/" + fl, "--fixture-root", root}, 2},
			// v1.4: a single live flag leaves required flags missing (exit 2, not 5).
			tc{"live_only_" + fl, []string{"--" + fl, "/nonexistent/" + fl}, 2},
			tc{"live_only_" + fl + "_equals", []string{"--" + fl + "=/nonexistent/" + fl}, 2},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gfoFail(t, gfoExec(t, bin, gfoRunOpt{Timeout: 20 * time.Second}, c.args...), c.exit, "GFO-010 "+c.name)
		})
	}
}

// GFO-010: exactly "-h" or "--help" prints usage to stderr, nothing to
// stdout, exit 0.
func TestGFO010_HelpOnly(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	for _, h := range []string{"-h", "--help"} {
		res := gfoExec(t, bin, gfoRunOpt{Timeout: 20 * time.Second}, h)
		if res.Killed || res.Exit != 0 {
			t.Errorf("GFO-010 %s: exit %d (killed %v), want 0; stderr=%q", h, res.Exit, res.Killed, gfoTail(res.Stderr))
		}
		if len(res.Stdout) != 0 {
			t.Errorf("GFO-010/GFO-012 %s: stdout has %d bytes, want 0", h, len(res.Stdout))
		}
		if len(res.Stderr) == 0 {
			t.Errorf("GFO-010 %s: usage must be written to stderr", h)
		}
	}
}

// GFO-011 (v1.4): exit codes 1..6 with their tokens, the fixture-mode priority
// 3 > 4 > 1 (stdout write failure is exit 1 "internal") and the live priority 2 > 5 > 6.
func TestGFO011_ExitCodesTokensAndPriority(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)

	readOnly := func(t *testing.T) *os.File {
		t.Helper()
		p := filepath.Join(t.TempDir(), "stdout")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		ro, err := os.Open(p) // read-only descriptor: every write fails
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ro.Close() })
		return ro
	}

	t.Run("exit1_stdout_write_failure", func(t *testing.T) {
		f := gfoNewBasic(t, 1)
		f.save()
		res := gfoExec(t, bin, gfoRunOpt{Stdout: readOnly(t)}, "--fixture-root", f.Root)
		gfoFail(t, res, 1, "GFO-011 stdout write failure")
	})
	t.Run("exit3_beats_exit1", func(t *testing.T) {
		f := gfoNewBasic(t, 1)
		f.SaveRaw("{")
		res := gfoExec(t, bin, gfoRunOpt{Stdout: readOnly(t)}, "--fixture-root", f.Root)
		gfoFail(t, res, 3, "GFO-011 3>1")
	})
	t.Run("exit4_beats_exit1", func(t *testing.T) {
		f := gfoNewFx(t, 1)
		gfoFillNames(t, f, 0, 8193)
		f.save()
		res := gfoExec(t, bin, gfoRunOpt{Stdout: readOnly(t)}, "--fixture-root", f.Root)
		gfoFail(t, res, 4, "GFO-011 4>1")
	})
	t.Run("exit3_beats_exit4", func(t *testing.T) {
		f := gfoNewFx(t, 2)
		gfoFillNames(t, f, 0, 8193)
		f.Remove("frames/1/sys")
		f.Fail(bin, 3, "GFO-011 3>4")
	})
	t.Run("exit2_usage", func(t *testing.T) {
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, "--bogus"), 2, "GFO-011 usage")
	})
	t.Run("exit5_live", func(t *testing.T) {
		// v1.4: exit 5 is --pod-resources-socket with no usage error (GFO-013); --node-name alone is exit 2.
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, gfoLiveArgs(nil, "--pod-resources-socket", "/nonexistent/s")...), 5, "GFO-011 live")
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, "--node-name", "n"), 2, "GFO-011 incomplete live flags")
	})
	t.Run("exit6_live_config", func(t *testing.T) {
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{Timeout: 20 * time.Second}, gfoLiveArgs(nil)...), 6, "GFO-011 live config")
	})
	t.Run("live_priority_2_5_6", func(t *testing.T) {
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, gfoLiveArgs(map[string]string{"controller": "127.0.0.1:1"}, "--pod-resources-socket", "/nonexistent/s")...), 2, "GFO-011 2>5")
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, gfoLiveArgs(nil, "--pod-resources-socket", "/nonexistent/s")...), 5, "GFO-011 5>6")
	})
}

// GFO-012: success writes only artifact + one newline; failures write only a
// bounded stderr that never carries fixture contents or secret flag values.
func TestGFO012_OutputStreamsAndSecrecy(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)

	t.Run("success_streams", func(t *testing.T) {
		f := gfoNewBasic(t, 1)
		res := f.Run(bin)
		gfoOK(t, res, &f.M, "GFO-012")
		if n := bytes.Count(res.Stdout, []byte("\n")); n != 1 || !bytes.HasSuffix(res.Stdout, []byte("}\n")) {
			t.Errorf("GFO-012: stdout has %d newlines / suffix %q, want exactly one final newline after the object", n, res.Stdout[len(res.Stdout)-2:])
		}
	})
	t.Run("manifest_content_not_echoed", func(t *testing.T) {
		const canary = "CANARY-MANIFEST-7f3a9c"
		for i, raw := range []string{
			strings.Replace(gfoDefaultManifest(1).JSON(), gfoSchemaIn, canary, 1),
			`{"clusterID":"` + canary,
			strings.Replace(gfoDefaultManifest(1).JSON(), `"clusterID":"lab-a"`, `"clusterID":"lab a `+canary+`"`, 1),
		} {
			f := gfoNewBasic(t, 1)
			f.SaveRaw(raw)
			res := f.Run(bin)
			gfoFail(t, res, 3, fmt.Sprintf("GFO-012 manifest case %d", i))
			if bytes.Contains(res.Stderr, []byte(canary)) {
				t.Errorf("GFO-012: manifest case %d: stderr contains manifest content %q", i, canary)
			}
		}
	})
	t.Run("canned_csv_content_not_echoed", func(t *testing.T) {
		const canary = "CANARY-CSV-51d0e2"
		f := gfoNewBasic(t, 1)
		f.Write(gfoCSV(0), canary+", 0000:03:00.0\n")
		res := f.Run(bin)
		a := gfoOK(t, res, &f.M, "GFO-012 csv")
		if bytes.Contains(res.Stdout, []byte(canary)) || bytes.Contains(res.Stderr, []byte(canary)) {
			t.Errorf("GFO-012/GFO-051: canned CSV content leaked into output")
		}
		if !a.Frames[0].HasDiag("nvidia_malformed", gfoCSV(0)) {
			t.Errorf("GFO-074: want nvidia_malformed(%s), got %s", gfoCSV(0), a.Frames[0].DiagString())
		}
	})
	t.Run("secret_flag_values_not_echoed", func(t *testing.T) {
		f := gfoNewBasic(t, 1)
		f.save()
		for _, fl := range []string{"ca-file", "cert-file", "key-file"} {
			secret := "SECRET-" + strings.ToUpper(fl) + "-e1b2c3"
			for _, c := range []struct {
				args []string
				exit int
			}{
				{[]string{"--fixture-root", f.Root, "--" + fl, secret}, 2},
				{[]string{"--fixture-root", f.Root, "--" + fl + "=" + secret}, 2},
				{[]string{"--" + fl, secret}, 2},
				{[]string{"--" + fl + "=" + secret}, 2},
				// v1.4: with every required flag valid the secret-valued flag reaches exit 5 / exit 6, never stderr.
				{gfoLiveArgs(map[string]string{fl: secret}, "--pod-resources-socket", "/nonexistent/s"), 5},
				{gfoLiveArgs(map[string]string{fl: secret}), 6},
				{[]string{"--" + fl, secret, "--" + fl, secret}, 2},
				{[]string{"--" + fl, secret, "--bogus"}, 2},
				{[]string{"--" + fl, secret, "extra"}, 2},
			} {
				res := gfoExec(t, bin, gfoRunOpt{}, c.args...)
				gfoFail(t, res, c.exit, "GFO-012 "+strings.Join(c.args, " "))
				if bytes.Contains(res.Stderr, []byte(secret)) {
					t.Errorf("GFO-012: stderr for %q contains the %s value", c.args, fl)
				}
			}
		}
	})
	t.Run("stderr_bounded", func(t *testing.T) {
		huge := strings.Repeat("a", 20000)
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, "--"+huge), 2, "GFO-012 huge flag name")
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, huge), 2, "GFO-012 huge positional")
		// v1.4 (GLI-080): --controller must be host:port with a DNS host, so a 20000-byte value is a usage error.
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, "--controller", huge), 2, "GFO-012 huge live value")
		gfoFail(t, gfoExec(t, bin, gfoRunOpt{}, "--fixture-root", filepath.Join(t.TempDir(), huge)), 3, "GFO-012 huge fixture path")
	})
}

// GFO-013 (v1.4, GLI-080): in live mode --pod-resources-socket without a
// usage error ends with exit 5 within 1s without opening flag paths,
// connecting or executing anything (the FIFO --pod-resources-socket and
// --key-file are never opened); an incomplete or invalid live flag set is exit
// 2; a complete valid set without --pod-resources-socket is exit 6 (start-up
// file validation) within 1s without waiting on a FIFO, connecting or running
// anything; secret values never reach stderr.
func TestGFO013_LiveModeUnsupportedWithoutSideEffects(t *testing.T) {
	bin := gfoBuildAgent(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "secret.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	notDir := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(notDir, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "nvidia-smi-ran")
	script := filepath.Join(dir, "fake-nvidia-smi")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctl := "localhost:" + port // GLI-022: the controller host is a DNS name, never an IP literal

	type tc struct {
		name string
		args []string
		exit int
	}
	cases := []tc{
		// usage (exit 2): incomplete flag sets and the IP-literal controller, even next to the socket flag (2 > 5).
		{"incomplete_key_file", []string{"--key-file", fifo}, 2},
		{"incomplete_ca_file", []string{"--ca-file", fifo}, 2},
		{"incomplete_cert_file", []string{"--cert-file", fifo}, 2},
		{"incomplete_sysfs_root", []string{"--sysfs-root", fifo}, 2},
		{"incomplete_pod_resources_socket", []string{"--pod-resources-socket", fifo}, 2},
		{"incomplete_nvidia_smi", []string{"--nvidia-smi", script}, 2},
		{"incomplete_boot_id_file", []string{"--boot-id-file", fifo}, 2},
		{"ip_literal_controller_alone", []string{"--controller", ln.Addr().String()}, 2},
		{"unroutable_ip_controller_alone", []string{"--controller", "10.255.255.1:65000"}, 2},
		{"ip_literal_controller_with_everything", gfoLiveArgs(map[string]string{"controller": ln.Addr().String(), "sysfs-root": fifo, "ca-file": fifo, "cert-file": fifo, "key-file": fifo},
			"--nvidia-smi", script, "--pod-resources-socket", fifo, "--boot-id-file", fifo), 2},
		// live-unsupported (exit 5): a valid flag set with --pod-resources-socket; nothing is opened, run or dialed.
		{"pod_resources_socket_fifo", gfoLiveArgs(map[string]string{"controller": ctl}, "--pod-resources-socket", fifo), 5},
		{"everything_fifo_with_pod_resources_socket", gfoLiveArgs(map[string]string{"controller": ctl, "sysfs-root": fifo, "ca-file": fifo, "cert-file": fifo, "key-file": fifo},
			"--nvidia-smi", script, "--pod-resources-socket", fifo, "--boot-id-file", fifo), 5},
		// live-config (exit 6): no --pod-resources-socket; the start-up validation fails without waiting on a FIFO.
		{"key_file_fifo", gfoLiveArgs(map[string]string{"controller": ctl, "key-file": fifo}, "--nvidia-smi", script), 6},
		{"ca_file_fifo", gfoLiveArgs(map[string]string{"controller": ctl, "ca-file": fifo}, "--nvidia-smi", script), 6},
		{"cert_file_fifo", gfoLiveArgs(map[string]string{"controller": ctl, "cert-file": fifo}, "--nvidia-smi", script), 6},
		{"boot_id_file_fifo", gfoLiveArgs(map[string]string{"controller": ctl}, "--nvidia-smi", script, "--boot-id-file", fifo), 6},
		{"all_tls_files_fifo", gfoLiveArgs(map[string]string{"controller": ctl, "ca-file": fifo, "cert-file": fifo, "key-file": fifo}, "--nvidia-smi", script, "--boot-id-file", fifo), 6},
		{"sysfs_root_is_not_a_directory", gfoLiveArgs(map[string]string{"controller": ctl, "sysfs-root": notDir}, "--nvidia-smi", script), 6},
		{"nothing_exists", gfoLiveArgs(map[string]string{"controller": ctl}, "--nvidia-smi", script), 6},
	}
	for _, c := range cases {
		res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, c.args...)
		name := "GFO-013 " + c.name
		gfoFail(t, res, c.exit, name)
		if res.Elapsed > time.Second {
			t.Errorf("%s: took %v, want <= 1s", name, res.Elapsed)
		}
		if bytes.Contains(res.Stderr, []byte(fifo)) || bytes.Contains(res.Stderr, []byte(notDir)) {
			t.Errorf("%s: stderr contains a flag value path", name)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("GFO-013: --nvidia-smi executable was run before start-up validation ended")
	}
	select {
	case <-accepted:
		t.Errorf("GFO-013: path-agent connected to the --controller address")
	case <-time.After(200 * time.Millisecond):
	}
}
