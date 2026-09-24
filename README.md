# data-path-assurance

data-path-assurance is a Go project for explaining physical data-path health in
GPU fleets. Its product direction is to connect GPU identity, Kubernetes node
identity, device location, and path evidence so that readiness and lifecycle
decisions can be traced back to observations.

## Current capabilities

The repository currently provides domain libraries rather than an installable
service. The implemented core includes:

- shared domain types in `pkg/model`;
- typed identity handling in `internal/identity`;
- immutable evidence processing in `internal/evidence`;
- an immutable physical graph maintained by a single-writer reducer in
  `internal/graph`;
- deterministic evaluation of persistent PCIe link-width degradation in
  `internal/domains/pcie`;
- pure GPU device lifecycle, qualification, node aggregation, and scheduling-gate
  evaluation in `internal/fleet`; and
- a bounded native PCIe observer, `libdpa_pcie`, with its Go bridge in
  `internal/nativepcie` (see below).

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

## Product direction

The pure lifecycle evaluation layer is implemented, but its executable adapters
are still future work. Planned host and Kubernetes adapters will collect and
bind live identity and path evidence and expose `GPUFleet`, `GPUDevice`, and
`NodePathState` resources. The native PCIe observer is a building block for
that host adapter, not the adapter itself. No runnable agent, controller,
`pathctl`, custom resource deployment, live GPU collection, or live Kubernetes
validation is available yet.

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
