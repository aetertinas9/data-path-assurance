package tests_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/agent"
)

// ---------------------------------------------------------------------------
// Fake nvidia-smi executables (GFO-027 seam).
// ---------------------------------------------------------------------------

// gfoScript writes an executable /bin/sh script into dir and returns its
// absolute, clean path. Every data path is embedded in the script text.
func gfoScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// gfoDataFile writes data into dir/name and returns the path.
func gfoDataFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// gfoQuoteSh single-quotes a path for /bin/sh.
func gfoQuoteSh(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// gfoKillLater kills every PID listed in pidfile at cleanup (descendant
// cleanup is not contracted by GFO-024; the test cleans up after itself).
func gfoKillLater(t *testing.T, pidfile string) {
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidfile)
		for _, f := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

// gfoRow returns one valid CSV row padded with spaces to exactly n bytes
// including the final newline (the GFO-072 line grammar allows [ \t]*).
func gfoRow(t *testing.T, uuid, bdf string, n int) []byte {
	t.Helper()
	row := uuid + ", " + bdf
	if n < len(row)+1 {
		t.Fatalf("test bug: row %q does not fit in %d bytes", row, n)
	}
	return []byte(row + strings.Repeat(" ", n-len(row)-1) + "\n")
}

func gfoRows(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "GPU-%08x-0000-0000-0000-000000000000, 0000:%02x:%02x.0\n", i, i/32, i%32)
	}
	return []byte(b.String())
}

var gfoAllNVIDIAErrs = []error{
	agent.ErrInvalidArgument, agent.ErrNVIDIAStart, agent.ErrNVIDIATimeout, agent.ErrNVIDIAOutputLimit,
	agent.ErrNVIDIAExit, agent.ErrNVIDIAEmpty, agent.ErrNVIDIAMalformed, agent.ErrNVIDIADuplicate,
}

// gfoWantErr asserts entries == nil and errors.Is(err, want) and that err
// matches no other GFO-020 sentinel.
func gfoWantErr(t *testing.T, entries []agent.GPUInventoryEntry, err, want error, clause string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want errors.Is(err, %v)", clause, err, want)
	}
	for _, other := range gfoAllNVIDIAErrs {
		if other != want && errors.Is(err, other) {
			t.Errorf("%s: err = %v also matches %v", clause, err, other)
		}
	}
	if entries != nil {
		t.Errorf("%s: entries = %v on error, want nil", clause, entries)
	}
}

func gfoWantEntries(t *testing.T, entries []agent.GPUInventoryEntry, err error, want []agent.GPUInventoryEntry, clause string) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: unexpected error %v", clause, err)
		return
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("%s: entries %v, want %v", clause, entries, want)
	}
}

// ---------------------------------------------------------------------------
// GFO-020 .. GFO-027: public surface and bounded runner.
// ---------------------------------------------------------------------------

// GFO-020: constants, distinct sentinels, default limits, fresh argument
// slices and no panics.
func TestGFO020_PublicSurface(t *testing.T) {
	var d time.Duration = agent.NVIDIAQueryTimeout
	if d != 5*time.Second || agent.NVIDIAMaxStdoutBytes != 65536 || agent.NVIDIAMaxStderrBytes != 4096 || agent.NVIDIAMaxRows != 256 {
		t.Errorf("GFO-020: constants %v/%d/%d/%d, want 5s/65536/4096/256", d, agent.NVIDIAMaxStdoutBytes, agent.NVIDIAMaxStderrBytes, agent.NVIDIAMaxRows)
	}
	for i, a := range gfoAllNVIDIAErrs {
		if a == nil {
			t.Fatalf("GFO-020: sentinel %d is nil", i)
		}
		for j, b := range gfoAllNVIDIAErrs {
			if i != j && errors.Is(a, b) {
				t.Errorf("GFO-020: sentinels %d (%v) and %d (%v) are not distinct", i, a, j, b)
			}
		}
	}
	want := agent.NVIDIALimits{Timeout: 5 * time.Second, MaxStdoutBytes: 65536, MaxStderrBytes: 4096}
	if got := agent.DefaultNVIDIALimits(); got != want {
		t.Errorf("GFO-020: DefaultNVIDIALimits() = %+v, want %+v", got, want)
	}
	wantArgs := []string{"--query-gpu=uuid,pci.bus_id", "--format=csv,noheader,nounits"}
	a1 := agent.NVIDIAQueryArgs()
	a2 := agent.NVIDIAQueryArgs()
	if !reflect.DeepEqual(a1, wantArgs) || !reflect.DeepEqual(a2, wantArgs) {
		t.Fatalf("GFO-020: NVIDIAQueryArgs() = %q, want %q", a1, wantArgs)
	}
	a1[0] = "--mutated"
	_ = append(a1[:1], "--appended")
	if got := agent.NVIDIAQueryArgs(); !reflect.DeepEqual(got, wantArgs) || !reflect.DeepEqual(a2, wantArgs) {
		t.Errorf("GFO-020: NVIDIAQueryArgs must return a new slice each call; got %q after mutation", got)
	}
	_ = agent.GPUInventoryEntry{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}

	var nilCtx context.Context
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("GFO-020: public function panicked: %v", r)
			}
		}()
		_, _ = agent.ParseNVIDIAQueryOutput(nil)
		_, _ = agent.ParseNVIDIAQueryOutput([]byte{0xff, 0x00, '\n', ','})
		_, _ = agent.RunNVIDIAQuery(nilCtx, "", agent.NVIDIALimits{})
		_, _ = agent.RunNVIDIAQuery(context.Background(), "\x00", agent.DefaultNVIDIALimits())
	}()
}

// GFO-021: invalid arguments are rejected with ErrInvalidArgument before any
// process is created.
func TestGFO021_RunnerArgumentValidation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	data := gfoDataFile(t, dir, "rows", gfoRow(t, gfoUUIDA, "0000:3b:00.0", 64))
	fake := gfoScript(t, dir, "nvidia-smi", "touch "+gfoQuoteSh(marker)+"\ncat "+gfoQuoteSh(data)+"\n")
	valid := agent.DefaultNVIDIALimits()
	with := func(mut func(l *agent.NVIDIALimits)) agent.NVIDIALimits {
		l := valid
		mut(&l)
		return l
	}
	var nilCtx context.Context
	ctx := context.Background()
	cases := []struct {
		name string
		ctx  context.Context
		exe  string
		lim  agent.NVIDIALimits
	}{
		{"nil_ctx", nilCtx, fake, valid},
		{"empty_executable", ctx, "", valid},
		{"relative", ctx, "nvidia-smi", valid},
		{"dot_relative", ctx, "./nvidia-smi", valid},
		{"not_clean_dotdot", ctx, dir + "/../" + filepath.Base(dir) + "/nvidia-smi", valid},
		{"not_clean_dot", ctx, dir + "/./nvidia-smi", valid},
		{"not_clean_double_slash", ctx, strings.Replace(fake, "/", "//", 1), valid},
		{"not_clean_trailing_slash", ctx, fake + "/", valid},
		{"timeout_zero", ctx, fake, with(func(l *agent.NVIDIALimits) { l.Timeout = 0 })},
		{"timeout_negative", ctx, fake, with(func(l *agent.NVIDIALimits) { l.Timeout = -time.Second })},
		{"timeout_5s_plus_1ns", ctx, fake, with(func(l *agent.NVIDIALimits) { l.Timeout = 5*time.Second + 1 })},
		{"stdout_zero", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStdoutBytes = 0 })},
		{"stdout_negative", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStdoutBytes = -1 })},
		{"stdout_65537", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStdoutBytes = 65537 })},
		{"stderr_zero", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStderrBytes = 0 })},
		{"stderr_negative", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStderrBytes = -1 })},
		{"stderr_4097", ctx, fake, with(func(l *agent.NVIDIALimits) { l.MaxStderrBytes = 4097 })},
		{"zero_limits", ctx, fake, agent.NVIDIALimits{}},
		{"invalid_limits_beat_start_failure", ctx, filepath.Join(dir, "missing"), agent.NVIDIALimits{}},
	}
	for _, c := range cases {
		entries, err := agent.RunNVIDIAQuery(c.ctx, c.exe, c.lim)
		gfoWantErr(t, entries, err, agent.ErrInvalidArgument, "GFO-021 "+c.name)
		if _, serr := os.Stat(marker); serr == nil {
			t.Fatalf("GFO-021 %s: a process was created for invalid arguments", c.name)
		}
	}

	// Exact bounds and narrower limits are valid arguments.
	entries, err := agent.RunNVIDIAQuery(ctx, fake, valid)
	gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}}, "GFO-021 exact default bounds")
	_, err = agent.RunNVIDIAQuery(ctx, fake, agent.NVIDIALimits{Timeout: 5 * time.Second, MaxStdoutBytes: 1, MaxStderrBytes: 1})
	gfoWantErr(t, nil, err, agent.ErrNVIDIAOutputLimit, "GFO-021/GFO-023 narrowed stdout bound")
}

// GFO-022: direct execution with exact argv, immediate stdin EOF, no shell
// interpretation of the path, and start failures.
func TestGFO022_DirectExecution(t *testing.T) {
	ctx := context.Background()
	lim := agent.DefaultNVIDIALimits()

	t.Run("argv_and_stdin", func(t *testing.T) {
		dir := t.TempDir()
		argv, stdin := filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
		rows := gfoDataFile(t, dir, "rows", []byte(gfoUUIDA+", 0000:3b:00.0\n"+gfoUUIDB+", 00000000:3C:00.1\n"))
		exe := gfoScript(t, dir, "nvidia-smi", "printf '%s\\n' \"$0\" \"$#\" \"$@\" > "+gfoQuoteSh(argv)+
			"\ncat > "+gfoQuoteSh(stdin)+"\ncat "+gfoQuoteSh(rows)+"\n")
		entries, err := agent.RunNVIDIAQuery(ctx, exe, lim)
		gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}, {UUID: gfoUUIDB, BDF: "0000:3c:00.1"}}, "GFO-022")
		got, _ := os.ReadFile(argv)
		want := exe + "\n2\n--query-gpu=uuid,pci.bus_id\n--format=csv,noheader,nounits\n"
		if string(got) != want {
			t.Errorf("GFO-022: child saw $0/argc/argv %q, want %q", got, want)
		}
		if in, err := os.ReadFile(stdin); err != nil || len(in) != 0 {
			t.Errorf("GFO-022: child stdin = %q (%v), want immediate EOF", in, err)
		}
	})

	t.Run("path_not_shell_interpreted", func(t *testing.T) {
		base := t.TempDir()
		inj1, inj2 := filepath.Join(base, "INJECTED1"), filepath.Join(base, "INJECTED2")
		dir := filepath.Join(base, "sp ace;touch "+inj1+";x", "$(touch "+inj2+")", "$HOME`id`")
		exe := gfoScript(t, dir, "nvidia smi;$x", "cat "+gfoQuoteSh(gfoDataFile(t, base, "rows", []byte(gfoUUIDA+",0000:3b:00.0")))+"\n")
		if filepath.Clean(exe) != exe || !filepath.IsAbs(exe) {
			t.Fatalf("test bug: %q is not absolute and clean", exe)
		}
		entries, err := agent.RunNVIDIAQuery(ctx, exe, lim)
		gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}}, "GFO-022 special path")
		for _, p := range []string{inj1, inj2} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("GFO-022: executable path was interpreted by a shell (%s exists)", p)
			}
		}
	})

	t.Run("start_failures", func(t *testing.T) {
		dir := t.TempDir()
		noExec := filepath.Join(dir, "no-exec")
		if err := os.WriteFile(noExec, []byte("#!/bin/sh\necho x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		badFormat := filepath.Join(dir, "bad-format")
		if err := os.WriteFile(badFormat, []byte("this is not an executable format\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		badInterp := gfoScript(t, dir, "bad-interp", "")
		if err := os.WriteFile(badInterp, []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "a-directory")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, exe := range map[string]string{
			"missing": filepath.Join(dir, "missing"), "not_executable": noExec, "directory": sub,
			"format_error": badFormat, "missing_interpreter": badInterp,
		} {
			entries, err := agent.RunNVIDIAQuery(ctx, exe, lim)
			gfoWantErr(t, entries, err, agent.ErrNVIDIAStart, "GFO-022 start failure "+name)
		}
	})
}

// GFO-023/GFO-027: output bounds; exactly the bound is accepted, one byte more
// terminates the process with ErrNVIDIAOutputLimit.
func TestGFO023_GFO027_OutputBounds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	narrow := agent.NVIDIALimits{Timeout: 5 * time.Second, MaxStdoutBytes: 64, MaxStderrBytes: 16}
	def := agent.DefaultNVIDIALimits()
	cases := []struct {
		name      string
		lim       agent.NVIDIALimits
		stdoutLen int
		stderrLen int
		ok        bool
	}{
		{"narrow_stdout_exact", narrow, 64, 0, true},
		{"narrow_stdout_plus_1", narrow, 65, 0, false},
		{"narrow_stderr_exact", narrow, 64, 16, true},
		{"narrow_stderr_plus_1", narrow, 64, 17, false},
		{"default_stdout_65536", def, 65536, 0, true},
		{"default_stdout_65537", def, 65537, 0, false},
		{"default_stderr_4096", def, 100, 4096, true},
		{"default_stderr_4097", def, 100, 4097, false},
	}
	for i, c := range cases {
		out := gfoDataFile(t, dir, fmt.Sprintf("out%d", i), gfoRow(t, gfoUUIDA, "0000:3b:00.0", c.stdoutLen))
		errf := gfoDataFile(t, dir, fmt.Sprintf("err%d", i), []byte(strings.Repeat("e", c.stderrLen)))
		exe := gfoScript(t, dir, fmt.Sprintf("fake%d", i), "cat "+gfoQuoteSh(errf)+" >&2\ncat "+gfoQuoteSh(out)+"\n")
		entries, err := agent.RunNVIDIAQuery(ctx, exe, c.lim)
		if c.ok {
			gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}}, "GFO-023 "+c.name)
		} else {
			gfoWantErr(t, entries, err, agent.ErrNVIDIAOutputLimit, "GFO-023 "+c.name)
		}
	}

	// Exceeding the bound terminates the process instead of waiting for it.
	for _, stream := range []string{"stdout", "stderr"} {
		big := gfoDataFile(t, dir, "big-"+stream, []byte(strings.Repeat("x", 70000)))
		redirect := ""
		if stream == "stderr" {
			redirect = " >&2"
		}
		exe := gfoScript(t, dir, "flood-"+stream, "cat "+gfoQuoteSh(big)+redirect+"\nexec sleep 30\n")
		start := time.Now()
		entries, err := agent.RunNVIDIAQuery(ctx, exe, def)
		gfoWantErr(t, entries, err, agent.ErrNVIDIAOutputLimit, "GFO-023 flood "+stream)
		if el := time.Since(start); el >= def.Timeout {
			t.Errorf("GFO-023: %s flood returned after %v; the process must be terminated on exceeding the bound, not at the timeout", stream, el)
		}
	}
}

// GFO-024/GFO-027: timeout and ctx end the run within 1s even when the child
// ignores SIGTERM or descendants keep stdout/stderr open.
func TestGFO024_GFO027_TimeBound(t *testing.T) {
	type tc struct {
		name    string
		body    func(pidfile string) string
		lim     agent.NVIDIALimits
		ctx     func() (context.Context, context.CancelFunc)
		want    error // sentinel, or context error
		trigger time.Duration
	}
	short := agent.NVIDIALimits{Timeout: 300 * time.Millisecond, MaxStdoutBytes: 65536, MaxStderrBytes: 4096}
	bg := func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
	sleepBody := func(string) string { return "exec sleep 30\n" }
	ignoreTerm := func(pidfile string) string {
		return "trap '' TERM\necho $$ > " + gfoQuoteSh(pidfile) + "\nsleep 30 &\necho $! >> " + gfoQuoteSh(pidfile) + "\nwait\n"
	}
	holdPipes := func(pidfile string) string {
		return "sleep 30 &\necho $! $$ > " + gfoQuoteSh(pidfile) + "\nexec sleep 30\n"
	}
	cases := []tc{
		{"timeout", sleepBody, short, bg, agent.ErrNVIDIATimeout, 300 * time.Millisecond},
		{"timeout_sigterm_ignored", ignoreTerm, short, bg, agent.ErrNVIDIATimeout, 300 * time.Millisecond},
		{"timeout_descendant_holds_pipes", holdPipes, short, bg, agent.ErrNVIDIATimeout, 300 * time.Millisecond},
		{"default_timeout_5s", sleepBody, agent.DefaultNVIDIALimits(), bg, agent.ErrNVIDIATimeout, 5 * time.Second},
		{"ctx_cancel", sleepBody, agent.DefaultNVIDIALimits(), func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(200*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled, 200 * time.Millisecond},
		{"ctx_deadline", sleepBody, agent.DefaultNVIDIALimits(), func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 200*time.Millisecond)
		}, context.DeadlineExceeded, 200 * time.Millisecond},
		{"ctx_deadline_descendant_holds_pipes", holdPipes, agent.DefaultNVIDIALimits(), func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 200*time.Millisecond)
		}, context.DeadlineExceeded, 200 * time.Millisecond},
		{"ctx_cancel_sigterm_ignored", ignoreTerm, agent.DefaultNVIDIALimits(), func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(200*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled, 200 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "pids")
			gfoKillLater(t, pidfile)
			exe := gfoScript(t, dir, "nvidia-smi", c.body(pidfile))
			ctx, cancel := c.ctx()
			defer cancel()
			start := time.Now()
			entries, err := agent.RunNVIDIAQuery(ctx, exe, c.lim)
			el := time.Since(start)
			if entries != nil || !errors.Is(err, c.want) {
				t.Errorf("GFO-024 %s: entries %v err %v, want nil and errors.Is(err, %v)", c.name, entries, err, c.want)
			}
			if el > c.trigger+time.Second {
				t.Errorf("GFO-024 %s: returned after %v, want within 1s of %v", c.name, el, c.trigger)
			}
			if c.want == agent.ErrNVIDIATimeout && el < c.trigger {
				t.Errorf("GFO-024 %s: timed out after %v, before the %v Timeout", c.name, el, c.trigger)
			}
		})
	}

	t.Run("default_timeout_not_early", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		rows := gfoDataFile(t, dir, "rows", []byte(gfoUUIDA+", 0000:3b:00.0\n"))
		// A 2s child keeps >= 2s of margin under the 5s default even with the
		// first-exec delay of a fresh script.
		exe := gfoScript(t, dir, "nvidia-smi", "sleep 2\ncat "+gfoQuoteSh(rows)+"\n")
		entries, err := agent.RunNVIDIAQuery(context.Background(), exe, agent.DefaultNVIDIALimits())
		gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}}, "GFO-024/GFO-027 2s child under the 5s default")
	})
}

// GFO-025: result precedence ErrInvalidArgument > ErrNVIDIAStart > (first
// observed output limit / timeout / ctx) > ErrNVIDIAExit > parse error.
func TestGFO025_ResultPrecedence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	valid := gfoDataFile(t, dir, "valid", []byte(gfoUUIDA+", 0000:3b:00.0\n"+gfoUUIDB+", 0000:3c:00.0\n"))
	malformed := gfoDataFile(t, dir, "malformed", []byte("not,a,row\n"))
	dup := gfoDataFile(t, dir, "dup", []byte(gfoUUIDA+", 0000:3b:00.0\n"+gfoUUIDA+", 0000:3c:00.0\n"))
	many := gfoDataFile(t, dir, "many", gfoRows(257))
	big := gfoDataFile(t, dir, "big", []byte(strings.Repeat("x", 70000)))
	bigErr := gfoDataFile(t, dir, "bigerr", []byte(strings.Repeat("e", 5000)))
	short := agent.NVIDIALimits{Timeout: 300 * time.Millisecond, MaxStdoutBytes: 65536, MaxStderrBytes: 4096}
	cases := []struct {
		name string
		body string
		lim  agent.NVIDIALimits
		want error
	}{
		{"exit_1_valid_stdout", "cat " + gfoQuoteSh(valid) + "\nexit 1\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"exit_3_empty_stdout", "exit 3\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"exit_1_malformed_stdout", "cat " + gfoQuoteSh(malformed) + "\nexit 1\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"exit_255", "cat " + gfoQuoteSh(valid) + "\nexit 255\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"signal_kill", "cat " + gfoQuoteSh(valid) + "\nkill -KILL $$\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"signal_term", "cat " + gfoQuoteSh(valid) + "\nkill -TERM $$\nsleep 5\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAExit},
		{"exit_0_empty", "exit 0\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAEmpty},
		{"exit_0_malformed", "cat " + gfoQuoteSh(malformed) + "\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAMalformed},
		{"exit_0_duplicate", "cat " + gfoQuoteSh(dup) + "\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIADuplicate},
		{"exit_0_257_rows", "cat " + gfoQuoteSh(many) + "\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAOutputLimit},
		{"stdout_limit_beats_exit", "cat " + gfoQuoteSh(big) + "\nexit 1\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAOutputLimit},
		{"stderr_limit_beats_exit", "cat " + gfoQuoteSh(bigErr) + " >&2\ncat " + gfoQuoteSh(valid) + "\nexit 1\n", agent.DefaultNVIDIALimits(), agent.ErrNVIDIAOutputLimit},
		{"timeout_beats_exit", "cat " + gfoQuoteSh(valid) + "\nsleep 3\nexit 1\n", short, agent.ErrNVIDIATimeout},
	}
	for i, c := range cases {
		exe := gfoScript(t, dir, fmt.Sprintf("fake%d", i), c.body)
		entries, err := agent.RunNVIDIAQuery(ctx, exe, c.lim)
		gfoWantErr(t, entries, err, c.want, "GFO-025 "+c.name)
	}

	ok := gfoScript(t, dir, "ok", "echo 'warning: noise on stderr' >&2\ncat "+gfoQuoteSh(valid)+"\n")
	entries, err := agent.RunNVIDIAQuery(ctx, ok, agent.DefaultNVIDIALimits())
	gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}, {UUID: gfoUUIDB, BDF: "0000:3c:00.0"}}, "GFO-025 success with stderr noise")
}

// GFO-026: error strings never contain child stdout/stderr bytes; no
// substitute inventory is built from partial data.
func TestGFO026_ErrorHygiene(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const outMark, errMark = "LEAK-STDOUT-7f3a", "LEAK-STDERR-9b2c"
	markOut := gfoDataFile(t, dir, "markout", []byte(outMark+"\n"))
	rows := gfoDataFile(t, dir, "rows", []byte(gfoUUIDA+", 0000:3b:00.0\n"+gfoUUIDA+", 0000:3c:00.0\n"))
	bigErr := gfoDataFile(t, dir, "bigerr", []byte(strings.Repeat(errMark, 400)))
	short := agent.NVIDIALimits{Timeout: 300 * time.Millisecond, MaxStdoutBytes: 65536, MaxStderrBytes: 4096}
	cases := []struct {
		name, body string
		lim        agent.NVIDIALimits
	}{
		{"exit", "cat " + gfoQuoteSh(markOut) + "\necho " + errMark + " >&2\nexit 2\n", agent.DefaultNVIDIALimits()},
		{"malformed", "cat " + gfoQuoteSh(markOut) + "\necho " + errMark + " >&2\n", agent.DefaultNVIDIALimits()},
		{"duplicate", "cat " + gfoQuoteSh(rows) + "\necho " + errMark + " >&2\n", agent.DefaultNVIDIALimits()},
		{"stderr_limit", "cat " + gfoQuoteSh(bigErr) + " >&2\n", agent.DefaultNVIDIALimits()},
		{"timeout", "cat " + gfoQuoteSh(markOut) + "\necho " + errMark + " >&2\nexec sleep 30\n", short},
		{"index_serial_only", "echo '0, 0000:3b:00.0'\necho 'SERIAL-1324, 0000:3c:00.0'\n", agent.DefaultNVIDIALimits()},
	}
	for i, c := range cases {
		exe := gfoScript(t, dir, fmt.Sprintf("fake%d", i), c.body)
		entries, err := agent.RunNVIDIAQuery(ctx, exe, c.lim)
		if err == nil || entries != nil {
			t.Errorf("GFO-026 %s: entries %v err %v, want an error and no inventory", c.name, entries, err)
			continue
		}
		for _, leak := range []string{outMark, errMark, gfoUUIDA, "0000:3b:00.0", "0000:3c:00.0", "SERIAL-1324"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("GFO-026 %s: error string %q contains child output %q", c.name, err.Error(), leak)
			}
		}
	}
	for _, in := range []string{outMark + "\n", gfoUUIDA + ", 0000:3b:00.0\n" + gfoUUIDA + ", 0000:3c:00.0\n", "GPU-X, " + outMark} {
		_, err := agent.ParseNVIDIAQueryOutput([]byte(in))
		if err == nil || strings.Contains(err.Error(), outMark) || strings.Contains(err.Error(), gfoUUIDA) {
			t.Errorf("GFO-026: ParseNVIDIAQueryOutput(%q) error %v must exist and not echo input bytes", in, err)
		}
	}
}

// ---------------------------------------------------------------------------
// GFO-070 .. GFO-072: parser grammar.
// ---------------------------------------------------------------------------

// GFO-070 [OD-11]: only lower-case GPU-<8-4-4-4-12> UUIDs, preserved verbatim.
func TestGFO070_UUIDGrammar(t *testing.T) {
	valid := []string{gfoUUIDA, "GPU-00000000-0000-0000-0000-000000000000", "GPU-ffffffff-ffff-ffff-ffff-ffffffffffff"}
	for _, u := range valid {
		entries, err := agent.ParseNVIDIAQueryOutput([]byte(" " + u + " ,0000:3b:00.0\n"))
		gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: u, BDF: "0000:3b:00.0"}}, "GFO-070 "+u)
	}
	invalid := []string{
		"GPU-0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9", "GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8fA",
		"gpu-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", "Gpu-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", "MIG-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"MIG-GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9/1/0", "{GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9}",
		"GPU-{0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9}", "GPU-0a1b2c3d4e5f60718293a4b5c6d7e8f9",
		"GPU-0a1b2c3-4e5f-6071-8293-a4b5c6d7e8f9a", "GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9-0",
		"GPU-0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8g9", "GPU-0a1b 2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		"GPU-", "0", "",
	}
	for _, u := range invalid {
		entries, err := agent.ParseNVIDIAQueryOutput([]byte(u + ", 0000:3b:00.0\n"))
		gfoWantErr(t, entries, err, agent.ErrNVIDIAMalformed, "GFO-070 "+strconv.Quote(u))
	}
}

// GFO-071: pci.bus_id grammar and canonicalisation to GFO-040.
func TestGFO071_BusIDGrammar(t *testing.T) {
	valid := map[string]string{
		"0000:3b:00.0": "0000:3b:00.0", "0000:3B:00.0": "0000:3b:00.0", "00000000:3B:00.0": "0000:3b:00.0",
		"00010000:01:00.0": "10000:01:00.0", "0001:01:00.0": "0001:01:00.0", "0000ABCD:01:00.0": "abcd:01:00.0",
		"000ABCDE:01:00.0": "abcde:01:00.0", "FFFFFFFF:FF:1F.7": "ffffffff:ff:1f.7", "0000:00:1f.0": "0000:00:1f.0",
		"00000000:00:00.0": "0000:00:00.0", "ABCD:EF:1A.3": "abcd:ef:1a.3",
	}
	for in, want := range valid {
		entries, err := agent.ParseNVIDIAQueryOutput([]byte(gfoUUIDA + ", " + in + "\n"))
		gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{{UUID: gfoUUIDA, BDF: want}}, "GFO-071 "+in)
	}
	invalid := []string{
		"000:3b:00.0", "00000:3b:00.0", "000000:3b:00.0", "0000000:3b:00.0", "000000000:3b:00.0",
		"0000:3b:20.0", "0000:3b:ff.0", "0000:3b:00.8", "0000:3b:0.0", "0000:3:00.0", "3b:00.0",
		"0000:3b:00", "0000-3b-00.0", "0000:3b:00.0.0", "0000:3g:00.0", "0000:3b:00.0x", "",
	}
	for _, in := range invalid {
		entries, err := agent.ParseNVIDIAQueryOutput([]byte(gfoUUIDA + ", " + in + "\n"))
		gfoWantErr(t, entries, err, agent.ErrNVIDIAMalformed, "GFO-071 "+strconv.Quote(in))
	}
}

// GFO-072: output grammar and its check order: size > empty > line grammar >
// row count > duplicates; input order is preserved.
func TestGFO072_OutputGrammarAndOrder(t *testing.T) {
	row := func(u, b string) string { return u + ", " + b }
	a := row(gfoUUIDA, "0000:3b:00.0")
	b := row(gfoUUIDB, "0000:3c:00.0")
	ea := agent.GPUInventoryEntry{UUID: gfoUUIDA, BDF: "0000:3b:00.0"}
	eb := agent.GPUInventoryEntry{UUID: gfoUUIDB, BDF: "0000:3c:00.0"}
	ok := []struct {
		name string
		in   string
		want []agent.GPUInventoryEntry
	}{
		{"no_final_newline", a, []agent.GPUInventoryEntry{ea}},
		{"final_newline", a + "\n", []agent.GPUInventoryEntry{ea}},
		{"crlf", a + "\r\n" + b + "\r\n", []agent.GPUInventoryEntry{ea, eb}},
		{"order_preserved", b + "\n" + a + "\n", []agent.GPUInventoryEntry{eb, ea}},
		{"spaces_and_tabs", " \t" + gfoUUIDA + " \t,\t 0000:3b:00.0 \t\n", []agent.GPUInventoryEntry{ea}},
		{"no_spaces", gfoUUIDA + ",0000:3b:00.0", []agent.GPUInventoryEntry{ea}},
		{"double_cr_before_final_newline", a + "\r\r\n", []agent.GPUInventoryEntry{ea}},
	}
	for _, c := range ok {
		entries, err := agent.ParseNVIDIAQueryOutput([]byte(c.in))
		gfoWantEntries(t, entries, err, c.want, "GFO-072 "+c.name)
	}
	big := string(gfoRow(t, gfoUUIDA, "0000:3b:00.0", 65536))
	entries, err := agent.ParseNVIDIAQueryOutput([]byte(big))
	gfoWantEntries(t, entries, err, []agent.GPUInventoryEntry{ea}, "GFO-072 exactly 65536 bytes")
	entries, err = agent.ParseNVIDIAQueryOutput(gfoRows(256))
	if err != nil || len(entries) != 256 || entries[255].BDF != "0000:07:1f.0" {
		t.Errorf("GFO-072: 256 rows -> %d entries, %v", len(entries), err)
	}

	bad := []struct {
		name string
		in   []byte
		want error
	}{
		{"nil", nil, agent.ErrNVIDIAEmpty},
		{"empty", []byte(""), agent.ErrNVIDIAEmpty},
		{"lf", []byte("\n"), agent.ErrNVIDIAEmpty},
		{"crlf", []byte("\r\n"), agent.ErrNVIDIAEmpty},
		{"cr", []byte("\r"), agent.ErrNVIDIAMalformed},
		{"two_lf", []byte("\n\n"), agent.ErrNVIDIAMalformed},
		{"space_lf", []byte(" \n"), agent.ErrNVIDIAMalformed},
		{"crlf_crlf", []byte("\r\n\r\n"), agent.ErrNVIDIAMalformed},
		{"trailing_empty_line", []byte(a + "\n\n"), agent.ErrNVIDIAMalformed},
		{"leading_empty_line", []byte("\n" + a), agent.ErrNVIDIAMalformed},
		{"inner_empty_line", []byte(a + "\n\n" + b), agent.ErrNVIDIAMalformed},
		{"triple_cr", []byte(a + "\r\r\r\n"), agent.ErrNVIDIAMalformed},
		{"cr_inside", []byte(gfoUUIDA + ",\r0000:3b:00.0\n"), agent.ErrNVIDIAMalformed},
		{"quoted_uuid", []byte(`"` + gfoUUIDA + `", 0000:3b:00.0`), agent.ErrNVIDIAMalformed},
		{"three_fields", []byte(a + ", extra"), agent.ErrNVIDIAMalformed},
		{"one_field", []byte(gfoUUIDA), agent.ErrNVIDIAMalformed},
		{"empty_fields", []byte(","), agent.ErrNVIDIAMalformed},
		{"header", []byte("uuid, pci.bus_id\n" + a), agent.ErrNVIDIAMalformed},
		{"nul", []byte(a + "\x00\n"), agent.ErrNVIDIAMalformed},
		{"non_ascii", []byte(a + "\u00a0\n"), agent.ErrNVIDIAMalformed},
		{"vertical_tab_padding", []byte(a + "\v\n"), agent.ErrNVIDIAMalformed},
		{"one_bad_row_among_valid", []byte(a + "\n" + "bad\n" + b + "\n"), agent.ErrNVIDIAMalformed},
		{"size_65537_valid_row", gfoRow(t, gfoUUIDA, "0000:3b:00.0", 65537), agent.ErrNVIDIAOutputLimit},
		{"size_65537_garbage", []byte(strings.Repeat("\x00", 65537)), agent.ErrNVIDIAOutputLimit},
		{"size_65537_newlines", []byte(strings.Repeat("\n", 65537)), agent.ErrNVIDIAOutputLimit},
		{"rows_257", gfoRows(257), agent.ErrNVIDIAOutputLimit},
		{"rows_257_with_malformed", append(gfoRows(256), []byte("bad\n")...), agent.ErrNVIDIAMalformed},
		{"rows_257_with_duplicate", append(gfoRows(256), gfoRows(1)...), agent.ErrNVIDIAOutputLimit},
		{"rows_256_with_duplicate", append(gfoRows(255), gfoRows(1)...), agent.ErrNVIDIADuplicate},
		{"duplicate_uuid", []byte(a + "\n" + row(gfoUUIDA, "0000:3c:00.0")), agent.ErrNVIDIADuplicate},
		{"duplicate_bdf", []byte(a + "\n" + row(gfoUUIDB, "0000:3b:00.0")), agent.ErrNVIDIADuplicate},
		{"duplicate_bdf_other_notation", []byte(a + "\n" + row(gfoUUIDB, "00000000:3B:00.0")), agent.ErrNVIDIADuplicate},
		{"malformed_beats_duplicate", []byte(a + "\n" + a + "\nbad\n"), agent.ErrNVIDIAMalformed},
	}
	for _, c := range bad {
		entries, err := agent.ParseNVIDIAQueryOutput(c.in)
		gfoWantErr(t, entries, err, c.want, "GFO-072 "+c.name)
	}
}
