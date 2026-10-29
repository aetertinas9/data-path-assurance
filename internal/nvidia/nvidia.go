// Package nvidia reads what the NVIDIA driver knows about the GPUs of a host:
// the inventory query, the NVLink link states and the topology matrix of
// nvidia-smi. The three outputs come either from a bounded run of nvidia-smi
// (absolute path, fixed arguments, time and size limits, no shell) or from
// canned files in a fixture directory, and are parsed the same way.
package nvidia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bounds of one nvidia-smi run.
const (
	Timeout        = 5 * time.Second
	MaxOutputBytes = 1 << 20
)

// Canned output file names inside a fixture's nvidia directory.
const (
	QueryFile  = "query.csv"
	NVLinkFile = "nvlink.txt"
	TopoFile   = "topo.txt"
)

// QueryFields is the --query-gpu field list; the CSV columns follow it.
var QueryFields = []string{
	"index", "name", "uuid", "pci.bus_id",
	"pcie.link.gen.current", "pcie.link.gen.max",
	"pcie.link.width.current", "pcie.link.width.max",
}

// Commands returns the three argument lists, excluding argv[0].
func Commands() map[string][]string {
	return map[string][]string{
		QueryFile:  {"--query-gpu=" + strings.Join(QueryFields, ","), "--format=csv,noheader,nounits"},
		NVLinkFile: {"nvlink", "-s"},
		TopoFile:   {"topo", "-m"},
	}
}

// Outputs are the raw bytes of each command, or the error that produced none.
type Outputs struct {
	Query, NVLink, Topo []byte
	Errors              map[string]error
}

// Run executes the three commands with the binary at path.
func Run(ctx context.Context, path string) Outputs {
	out := Outputs{Errors: map[string]error{}}
	if !filepath.IsAbs(path) {
		err := errors.New("nvidia-smi path is not absolute")
		for name := range Commands() {
			out.Errors[name] = err
		}
		return out
	}
	for name, args := range Commands() {
		data, err := run(ctx, path, args)
		if err != nil {
			out.Errors[name] = err
			continue
		}
		switch name {
		case QueryFile:
			out.Query = data
		case NVLinkFile:
			out.NVLink = data
		case TopoFile:
			out.Topo = data
		}
	}
	return out
}

func run(ctx context.Context, path string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: MaxOutputBytes}
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	cmd.WaitDelay = 200 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nvidia-smi %s: %w", args[0], err)
	}
	if stdout.Len() > MaxOutputBytes {
		return nil, fmt.Errorf("nvidia-smi %s: output exceeds %d bytes", args[0], MaxOutputBytes)
	}
	return stdout.Bytes(), nil
}

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len() <= l.n {
		room := l.n + 1 - l.w.Len()
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}

// FromDir reads the canned outputs from a fixture directory. A missing file is
// an error for that command only.
func FromDir(root *os.Root, dir string) Outputs {
	out := Outputs{Errors: map[string]error{}}
	read := func(name string) []byte {
		p := dir + "/" + name
		info, err := root.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxOutputBytes {
			out.Errors[name] = fmt.Errorf("%s: absent or not a regular file under %d bytes", p, MaxOutputBytes)
			return nil
		}
		data, err := root.ReadFile(p)
		if err != nil {
			out.Errors[name] = fmt.Errorf("%s: %w", p, err)
			return nil
		}
		return data
	}
	out.Query = read(QueryFile)
	out.NVLink = read(NVLinkFile)
	out.Topo = read(TopoFile)
	return out
}

// GPU is one row of the inventory query.
type GPU struct {
	Index               int
	Name, UUID, BDF     string
	LinkGen, LinkGenMax int // 0 when not reported
	LinkWidth, WidthMax int
}

var (
	uuidPattern  = regexp.MustCompile(`^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	busIDPattern = regexp.MustCompile(`^([0-9A-Fa-f]{4,8}):([0-9A-Fa-f]{2}):([0-9A-Fa-f]{2})\.([0-7])$`)
)

// ParseQuery parses the inventory CSV. Rows that do not fit the grammar are
// returned as errors; well-formed rows are kept.
func ParseQuery(data []byte) ([]GPU, []error) {
	var gpus []GPU
	var errs []error
	for n, line := range strings.Split(strings.TrimRight(string(data), "\r\n"), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		cols := strings.Split(line, ",")
		if len(cols) != len(QueryFields) {
			errs = append(errs, fmt.Errorf("query line %d: %d columns, want %d", n+1, len(cols), len(QueryFields)))
			continue
		}
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		g := GPU{Name: cols[1], UUID: cols[2]}
		var ok bool
		if g.Index, ok = atoi(cols[0]); !ok {
			errs = append(errs, fmt.Errorf("query line %d: bad index", n+1))
			continue
		}
		if !uuidPattern.MatchString(g.UUID) {
			errs = append(errs, fmt.Errorf("query line %d: bad uuid", n+1))
			continue
		}
		if g.BDF, ok = CanonicalBDF(cols[3]); !ok {
			errs = append(errs, fmt.Errorf("query line %d: bad pci.bus_id", n+1))
			continue
		}
		g.LinkGen, _ = atoi(cols[4])
		g.LinkGenMax, _ = atoi(cols[5])
		g.LinkWidth, _ = atoi(cols[6])
		g.WidthMax, _ = atoi(cols[7])
		gpus = append(gpus, g)
	}
	return gpus, errs
}

func atoi(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	return v, err == nil && v >= 0
}

// CanonicalBDF turns an nvidia-smi bus id (00000000:1B:00.0) into the sysfs
// spelling (0000:1b:00.0).
func CanonicalBDF(s string) (string, bool) {
	m := busIDPattern.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	domain, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return "", false
	}
	dev, _ := strconv.ParseUint(m[3], 16, 8)
	if dev > 0x1f {
		return "", false
	}
	return fmt.Sprintf("%04x:%s:%s.%s", domain, strings.ToLower(m[2]), strings.ToLower(m[3]), m[4]), true
}

// NVLink is the link state of one GPU as `nvidia-smi nvlink -s` prints it.
type NVLink struct {
	Index  int
	UUID   string
	Links  []LinkState
	Active int
}

// LinkState is one NVLink link: active with a bandwidth, or inactive.
type LinkState struct {
	Index  int
	Active bool
	GBps   float64
}

var (
	nvlinkGPUPattern  = regexp.MustCompile(`^GPU (\d+): .*\(UUID: (GPU-[0-9a-f-]+)\)\s*$`)
	nvlinkLinkPattern = regexp.MustCompile(`^\s*Link (\d+): (.*)$`)
)

// ParseNVLink parses the per-GPU link listing. A GPU without NVLink prints
// nothing (or "Unable to retrieve NVLink information"), and is simply absent.
func ParseNVLink(data []byte) ([]NVLink, []error) {
	var out []NVLink
	var errs []error
	var cur *NVLink
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := nvlinkGPUPattern.FindStringSubmatch(line); m != nil {
			idx, _ := atoi(m[1])
			out = append(out, NVLink{Index: idx, UUID: m[2]})
			cur = &out[len(out)-1]
			continue
		}
		m := nvlinkLinkPattern.FindStringSubmatch(line)
		if m == nil {
			// Anything else ("Unable to retrieve NVLink information" on a
			// GPU without NVLink, blank lines) carries no link state.
			continue
		}
		if cur == nil {
			errs = append(errs, fmt.Errorf("nvlink line %d: link before any GPU", n+1))
			continue
		}
		idx, _ := atoi(m[1])
		ls := LinkState{Index: idx}
		text := strings.TrimSpace(m[2])
		if strings.HasSuffix(text, "GB/s") {
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(text, "GB/s")), 64); err == nil && v > 0 {
				ls.Active, ls.GBps = true, v
			}
		}
		if ls.Active {
			cur.Active++
		}
		cur.Links = append(cur.Links, ls)
	}
	return out, errs
}

// Topo is the `nvidia-smi topo -m` matrix: the relation between every pair of
// listed devices, and the NIC legend.
type Topo struct {
	Devices  []string             // header order: GPU0, GPU1, ..., NIC0, ...
	Relation map[[2]string]string // (row, column) -> X, NV#, PIX, PXB, PHB, NODE, SYS
	NICNames map[string]string    // NIC0 -> mlx5_0
	// Affinity holds the trailing columns of each GPU row, by header name.
	Affinity map[string]map[string]string
}

var topoDevPattern = regexp.MustCompile(`^(GPU|NIC)\d+$`)

// ParseTopo parses the matrix. The header names the device columns followed
// by affinity columns; each device row carries one token per device column
// and the affinity values after them.
func ParseTopo(data []byte) (*Topo, error) {
	t := &Topo{Relation: map[[2]string]string{}, NICNames: map[string]string{}, Affinity: map[string]map[string]string{}}
	lines := strings.Split(string(data), "\n")
	var affinityCols []string
	inLegend := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "Legend") {
			inLegend = true
			continue
		}
		if inLegend {
			if k, v, ok := strings.Cut(trim, ":"); ok && topoDevPattern.MatchString(strings.TrimSpace(k)) {
				t.NICNames[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			continue
		}
		fields := strings.Fields(trim)
		if t.Devices == nil {
			if !topoDevPattern.MatchString(fields[0]) {
				continue
			}
			for _, f := range fields {
				if topoDevPattern.MatchString(f) {
					t.Devices = append(t.Devices, f)
				}
			}
			// Affinity column titles may hold spaces ("CPU Affinity"); they
			// are recovered from the header text after the last device.
			rest := trim[strings.LastIndex(trim, t.Devices[len(t.Devices)-1])+len(t.Devices[len(t.Devices)-1]):]
			affinityCols = splitColumns(rest)
			continue
		}
		if !topoDevPattern.MatchString(fields[0]) {
			continue
		}
		row := fields[0]
		if len(fields) < 1+len(t.Devices) {
			return nil, fmt.Errorf("topo row %s: %d relations, want %d", row, len(fields)-1, len(t.Devices))
		}
		for i, col := range t.Devices {
			t.Relation[[2]string{row, col}] = fields[1+i]
		}
		if extra := fields[1+len(t.Devices):]; len(extra) > 0 && len(affinityCols) > 0 {
			t.Affinity[row] = map[string]string{}
			for i, c := range affinityCols {
				if i < len(extra) {
					t.Affinity[row][c] = extra[i]
				}
			}
		}
	}
	if t.Devices == nil {
		return nil, errors.New("topo: no device header found")
	}
	return t, nil
}

// splitColumns splits a header tail at runs of two or more spaces or tabs.
func splitColumns(s string) []string {
	var cols []string
	for _, part := range regexp.MustCompile(`\t+| {2,}`).Split(strings.TrimSpace(s), -1) {
		if p := strings.TrimSpace(part); p != "" {
			cols = append(cols, p)
		}
	}
	return cols
}

// Rel returns the relation between two header names, in either order.
func (t *Topo) Rel(a, b string) (string, bool) {
	if t == nil {
		return "", false
	}
	if r, ok := t.Relation[[2]string{a, b}]; ok {
		return r, true
	}
	r, ok := t.Relation[[2]string{b, a}]
	return r, ok
}
