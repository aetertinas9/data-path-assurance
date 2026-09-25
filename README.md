# data-path-assurance

data-path-assurance is a Go project for explaining physical data-path health in
GPU fleets. Its product direction is to connect GPU identity, Kubernetes node
identity, device location, and path evidence so that readiness and lifecycle
decisions can be traced back to observations.

## Current capabilities

The repository currently provides domain libraries and an offline fixture
collector rather than an installable service. The implemented code includes:

- shared domain types in `pkg/model`;
- typed identity handling in `internal/identity`;
- immutable evidence processing in `internal/evidence`;
- an immutable physical graph maintained by a single-writer reducer in
  `internal/graph`;
- deterministic evaluation of persistent PCIe link-width degradation in
  `internal/domains/pcie`;
- pure GPU device lifecycle, qualification, node aggregation, and scheduling-gate
  evaluation in `internal/fleet`;
- a bounded native PCIe observer, `libdpa_pcie`, with its Go bridge in
  `internal/nativepcie` (see below); and
- an offline host collector in `internal/agent`, run by `path-agent` in
  fixture mode (see below).

PCIe evaluation is passive: it compares negotiated and expected link width
from supplied evidence and produces deterministic results. The fleet package
provides `NewPolicy`, `AdmitSnapshot`, `EvaluateDevice`, `AggregateNode`, and
`EvaluateGate`. These functions consume explicitly supplied policy, evidence,
and time and return domain values or actions; gate decisions do not mutate
Kubernetes.

Snapshot admission distinguishes accepted, duplicate, older or out-of-order,
gap, wrong-session, and conflicting input. Device evaluation binds a GPU UUID
to trusted observed node UID, boot identity, and PCI BDF evidence. Unknown
results are not treated as successful evidence.

## Native PCIe observation

`internal/nativepcie/csrc` holds `libdpa_pcie`, a small C11/POSIX library
(ABI version 1) that parses PCIe sysfs attribute text (link width, link
speed, NUMA node, AER counters, BDF addresses) and reads bounded descriptor
content with `pread`. It is read-only: it never opens a path, consults the
environment, allocates memory, starts a thread, handles signals, or logs. The
Go package `internal/nativepcie` calls that ABI through cgo and adds
`ReadDevice`, which observes one device's fixed attribute set through a
caller-supplied `*os.Root`.

The native library is a build prerequisite for the cgo bridge, and plain
`go build` does not bootstrap it:

```sh
make native          # build/native/<GOOS>-<GOARCH>/libdpa_pcie.{a,dylib} + build stamp
make native-example  # build/native/<GOOS>-<GOARCH>/inspect
make build vet arch-check
```

`make native` also generates `internal/nativepcie/dpa_pcie_stamp.h` from a
digest of the native source, header, compiler, flags, and target so the Go
build cache notices native changes. Generated output never reaches git: the
first native rule writes `build/.gitignore` (`*`) so `build/` ignores itself,
and `internal/nativepcie/.gitignore` ignores the stamp header.
`make clean-native` removes the artifacts and the stamp.

The native bridge is compiled only on darwin/arm64 with cgo enabled (the
validated local target) and, as unvalidated source configuration, on
linux/amd64 and linux/arm64. Everywhere else, and with `CGO_ENABLED=0`, the
package builds as an unavailable stub: `Available()` reports false and every
operation returns `ErrUnavailable`.

Device observation always goes through an injected root; there is no `/sys`
default. A caller chooses the root, for example a test fixture:

```go
root, err := os.OpenRoot(fixtureDir) // caller-selected; contains bus/pci/devices/...
if err != nil {
	return err
}
defer root.Close()

obs, err := nativepcie.ReadDevice(root, "0000:41:00.0")
if err != nil {
	return err // nil root, invalid BDF, or the device directory is unusable
}
for _, f := range obs.Fields {
	fmt.Println(obs.BDF, f.Name, f.Status, f.Value, f.NUMA, f.Counters, f.Err)
}
```

`Fields` always lists, in order, `current_link_width`, `max_link_width`,
`current_link_speed`, `max_link_speed`, `numa_node`, `aer_dev_correctable`,
`aer_dev_nonfatal`, and `aer_dev_fatal`, each with a status of `ok`,
`missing`, `unsupported`, `invalid`, or `io` and its own error. Field failures
do not suppress sibling fields.

What this delivers, and what it does not: the library and bridge are built
and exercised locally against fixture files on macOS. That is not live GPU
observation, not a runnable agent, not full PCIe qualification, and not
production readiness. Observations are raw parsed facts; nothing here
discovers devices, resolves GPU identity, infers topology, or feeds readiness,
health, finding, or scheduling decisions. Linux builds, containers, and real
hardware have not been validated.

## Offline fixture collection

`path-agent --fixture-root <dir>` reads a fixture directory and writes one
deterministic JSON snapshot artifact (`dpa.offline-snapshot/v1`) to stdout.
A fixture holds `manifest.json` (`dpa.offline-fixture/v1`: cluster, node, and
boot identity, an `offline:` trust profile with a positive session, the
evidence TTL, 1 to 16 frame times, and optional operator width baselines), one
sysfs tree per frame under `frames/<i>/sys/`, and optionally the canned output
of `nvidia-smi --query-gpu=uuid,pci.bus_id --format=csv,noheader,nounits` in
`frames/<i>/nvidia-smi.csv`.

A fixture directory looks like this (one `frames/<i>/` per frame; entries under
`bus/pci/devices/` are relative symlinks into the `devices/` tree, as on a real
host):

```text
fixture/
├── manifest.json
└── frames/
    └── 0/
        ├── nvidia-smi.csv          optional canned nvidia-smi output
        └── sys/
            ├── bus/pci/devices/0000:03:00.0 -> ../../../devices/pci0000:00/0000:00:01.0/0000:01:00.0/0000:02:08.0/0000:03:00.0
            └── devices/pci0000:00/0000:00:01.0/0000:01:00.0/0000:02:08.0/0000:03:00.0/
                ├── class           e.g. 0x030200
                ├── vendor          e.g. 0x10de
                ├── current_link_width
                └── max_link_width
```

Sample fixtures are not included in the repository; the tests build their own
fixtures in temporary directories. Point `--fixture-root` at your own fixture:

```sh
CGO_ENABLED=0 go build -o bin/path-agent ./cmd/path-agent
bin/path-agent --fixture-root path/to/fixture > snapshot.json
```

The collector opens the fixture through `os.Root` and follows symlinks only
while they stay inside it. It reads `class`, `vendor`, `current_link_width`,
and `max_link_width`, and checks whether `physfn` exists; writable attributes
such as `numa_node`, `enable`, `remove`, `reset`, and `resource*` are never
opened. Fixture mode starts no process and reads no clock, so the same fixture
yields the same bytes. Each frame carries the snapshot envelope (node UID,
boot ID, session, sequence, completeness, observed time, payload digest, and
bundle revision), the observed `LOCATED_IN` path from each GPU and NIC
function through PCIe switches to its root port and node, link-width
observations, NVIDIA UUID bindings, and diagnostics. Observation failures that
could be mistaken for a confirmed absence mark the frame `PARTIAL`; width and
NVIDIA failures only add diagnostics.

Exit codes are 0 (artifact written), 1 (internal error), 2 (usage error),
3 (invalid fixture), 4 (bound exceeded), and 5 (live mode requested, which is
not implemented yet). `internal/agent` also provides `RunNVIDIAQuery`, a
bounded `nvidia-smi` runner (absolute path, no shell, fixed arguments, 5 s
timeout, 64 KiB stdout, 4 KiB stderr) for the future live mode.

This is offline fixture collection only. It does not observe a live host, does
not yet evaluate or explain a snapshot, and has not been run against real GPU
hardware. The collector is pure Go and does not use the native PCIe observer.
Its tests have been run on macOS arm64 and in a linux/arm64 container.

## Product direction

The pure lifecycle evaluation layer is implemented, but its executable adapters
are still future work. Planned host and Kubernetes adapters will collect and
bind live identity and path evidence and expose `GPUFleet`, `GPUDevice`, and
`NodePathState` resources. The native PCIe observer is a building block for
that host adapter, not the adapter itself. `path-agent` runs only in offline
fixture mode. No live agent, controller, `pathctl`, custom resource
deployment, live GPU collection, or live Kubernetes validation is available
yet.

Active GPU work, reset, drain, and driver management remain the responsibility
of operators such as GPU Operator.

## Documentation

- [Documentation index](docs/README.md)
- [Architecture](docs/architecture.md)
- [Getting started](docs/getting-started.md)

## Acknowledgments

AI assistance: [Codex](https://github.com/codex), OpenAI's coding agent, helped
with planning, implementation, review, and documentation. Project decisions and
maintenance remain with the repository maintainers.

## License

[Apache License 2.0](LICENSE).
