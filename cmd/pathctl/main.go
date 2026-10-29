// Command pathctl shows the data path of every GPU on a host and checks that
// it can run at full speed.
//
//	pathctl path    [--sysfs /sys | --fixture DIR] [--nvidia-smi PATH] [--gpu ID] [--output text|json|mermaid]
//	pathctl check   same flags; exits 3 when any check fails
//	pathctl watch   same flags plus --interval and --count; prints check changes, exits 3 if any FAIL was seen
//	pathctl capture --out DIR [--sysfs /sys] [--nvidia-smi PATH]
//
// A fixture directory holds sys/ (as written by capture) and nvidia/ with
// query.csv, nvlink.txt and topo.txt. Exit codes: 0 done, 1 internal error,
// 2 usage, 3 a check failed (check only), 4 the GPU was not found.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/aetertinas9/data-path-assurance/internal/datapath"
	"github.com/aetertinas9/data-path-assurance/internal/nvidia"
	"github.com/aetertinas9/data-path-assurance/internal/render"
	"github.com/aetertinas9/data-path-assurance/internal/sysfs"
)

const usage = `usage:
  pathctl path    [--sysfs DIR | --fixture DIR] [--nvidia-smi PATH] [--devices FILE]
                  [--locality PIX|PXB|PHB|NODE|SYS] [--gpu BDF|UUID|INDEX] [--output text|json|mermaid]
  pathctl check   (same flags as path; exit 3 when a check fails)
  pathctl watch   (same flags as path) [--interval 15s] [--count N]
                  re-reads the host and prints every check that changes; exit 3 if any FAIL was seen
  pathctl capture --out DIR [--sysfs DIR] [--nvidia-smi PATH]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type common struct {
	sysfsDir, fixture, smi, devices, locality, gpu, output string
}

func (c *common) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.sysfsDir, "sysfs", "/sys", "sysfs root of the live host")
	fs.StringVar(&c.fixture, "fixture", "", "fixture directory (sys/ and nvidia/) instead of the live host")
	fs.StringVar(&c.smi, "nvidia-smi", "", "nvidia-smi binary (default: found on PATH; ignored with --fixture)")
	fs.StringVar(&c.devices, "devices", "", "JSON file with extra PCI device IDs")
	fs.StringVar(&c.locality, "locality", "PXB", "worst acceptable GPU-to-NIC locality")
	fs.StringVar(&c.gpu, "gpu", "", "show one GPU: PCI address, UUID or nvidia-smi index")
	fs.StringVar(&c.output, "output", "text", "text, json or mermaid")
}

// options turns the shared flags into datapath options; a non-zero code is
// the exit code of a usage error already written to stderr.
func (c *common) options(stderr io.Writer) (datapath.Options, int) {
	opts := datapath.DefaultOptions()
	loc, ok := datapath.ParseLocality(strings.ToUpper(c.locality))
	if !ok {
		fmt.Fprintf(stderr, "pathctl: --locality must be PIX, PXB, PHB, NODE or SYS\n")
		return opts, 2
	}
	opts.LocalityTarget = loc
	if c.devices != "" {
		if err := opts.Table.LoadExtra(c.devices); err != nil {
			fmt.Fprintf(stderr, "pathctl: --devices: %v\n", err)
			return opts, 2
		}
	}
	return opts, 0
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "path", "check":
		return runPath(ctx, args[0], args[1:], stdout, stderr)
	case "watch":
		return runWatch(ctx, args[1:], stdout, stderr)
	case "capture":
		return runCapture(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "pathctl: unknown command %q\n%s", args[0], usage)
	return 2
}

func runPath(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	c.flags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "pathctl %s: unexpected argument %q\n", cmd, fs.Arg(0))
		return 2
	}
	opts, code := c.options(stderr)
	if code != 0 {
		return code
	}
	if c.output != "text" && c.output != "json" && c.output != "mermaid" {
		fmt.Fprintf(stderr, "pathctl: --output must be text, json or mermaid\n")
		return 2
	}

	report, err := collect(ctx, c, opts)
	if err != nil {
		fmt.Fprintf(stderr, "pathctl: %v\n", err)
		return 1
	}
	if c.gpu != "" {
		g := findGPU(report, c.gpu)
		if g == nil {
			fmt.Fprintf(stderr, "pathctl: GPU %q not found\n", c.gpu)
			return 4
		}
		report.GPUs = []*datapath.GPU{g}
	}

	var out []byte
	switch c.output {
	case "json":
		out, err = render.JSON(report)
		if err != nil {
			fmt.Fprintf(stderr, "pathctl: %v\n", err)
			return 1
		}
	case "mermaid":
		out = render.Mermaid(report)
	default:
		if cmd == "check" && c.gpu == "" {
			out = checkText(report)
		} else if c.gpu != "" {
			out = render.GPUText(report, report.GPUs[0])
			out = append(out, limitations(report)...)
		} else {
			out = render.Text(report)
		}
	}
	if _, err := stdout.Write(out); err != nil {
		return 1
	}
	if cmd == "check" && report.Overall() == datapath.Fail {
		return 3
	}
	return 0
}

func checkText(r *datapath.Report) []byte {
	var b strings.Builder
	for _, g := range r.GPUs {
		fmt.Fprintf(&b, "GPU %s  %s\n", g.Dev.BDF, g.Name)
		for _, c := range g.Checks {
			fmt.Fprintf(&b, "  %-16s %-8s %s\n", c.Name, c.Status, c.Detail)
		}
	}
	b.Write(limitations(r))
	fmt.Fprintf(&b, "RESULT %s\n", r.Overall())
	return []byte(b.String())
}

func limitations(r *datapath.Report) []byte {
	if len(r.Limitations) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("\nLIMITATIONS\n")
	for _, l := range r.Limitations {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	return []byte(b.String())
}

func findGPU(r *datapath.Report, key string) *datapath.GPU {
	for _, g := range r.GPUs {
		if g.Dev.BDF == strings.ToLower(key) || g.UUID == key {
			return g
		}
		if bdf, ok := nvidia.CanonicalBDF(key); ok && g.Dev.BDF == bdf {
			return g
		}
		if n, err := strconv.Atoi(key); err == nil && g.Index == n {
			return g
		}
	}
	return nil
}

// collect reads the host or the fixture and builds the report.
func collect(ctx context.Context, c common, opts datapath.Options) (*datapath.Report, error) {
	if c.fixture != "" {
		root, err := openDir(c.fixture)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		sysRoot, err := root.OpenRoot("sys")
		if err != nil {
			return nil, fmt.Errorf("fixture %s has no sys directory", c.fixture)
		}
		defer sysRoot.Close()
		host := sysfs.Read(sysRoot)
		nv := nvidia.FromDir(root, "nvidia")
		return datapath.Build("fixture:"+filepath.Clean(c.fixture), host, nv, opts), nil
	}
	root, err := openDir(c.sysfsDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	host := sysfs.Read(root)
	nv := nvidia.Outputs{Errors: map[string]error{}}
	smi, err := findSMI(c.smi)
	if err != nil {
		for name := range nvidia.Commands() {
			nv.Errors[name] = err
		}
	} else {
		nv = nvidia.Run(ctx, smi)
	}
	return datapath.Build("sysfs:"+filepath.Clean(c.sysfsDir), host, nv, opts), nil
}

func findSMI(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	p, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return "", errors.New("nvidia-smi not found on PATH (use --nvidia-smi)")
	}
	return filepath.Abs(p)
}

func openDir(dir string) (*os.Root, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return os.OpenRoot(dir)
}

func runCapture(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var out, sysfsDir, smi string
	fs.StringVar(&out, "out", "", "fixture directory to write (required)")
	fs.StringVar(&sysfsDir, "sysfs", "/sys", "sysfs root of the live host")
	fs.StringVar(&smi, "nvidia-smi", "", "nvidia-smi binary (default: found on PATH)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if out == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	root, err := openDir(sysfsDir)
	if err != nil {
		fmt.Fprintf(stderr, "pathctl: %v\n", err)
		return 1
	}
	defer root.Close()
	if err := sysfs.Capture(root, filepath.Join(out, "sys")); err != nil {
		fmt.Fprintf(stderr, "pathctl: capture: %v\n", err)
		return 1
	}
	nvDir := filepath.Join(out, "nvidia")
	if err := os.MkdirAll(nvDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "pathctl: capture: %v\n", err)
		return 1
	}
	path, err := findSMI(smi)
	if err != nil {
		fmt.Fprintf(stderr, "pathctl: capture: %v; nvidia outputs not captured\n", err)
	} else {
		nv := nvidia.Run(ctx, path)
		for name, data := range map[string][]byte{nvidia.QueryFile: nv.Query, nvidia.NVLinkFile: nv.NVLink, nvidia.TopoFile: nv.Topo} {
			if data == nil {
				fmt.Fprintf(stderr, "pathctl: capture: %s: %v\n", name, nv.Errors[name])
				continue
			}
			if err := os.WriteFile(filepath.Join(nvDir, name), data, 0o644); err != nil {
				fmt.Fprintf(stderr, "pathctl: capture: %v\n", err)
				return 1
			}
		}
	}
	fmt.Fprintf(stdout, "captured %s into %s\n", sysfsDir, out)
	return 0
}
