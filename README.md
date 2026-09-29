# data-path-assurance

data-path-assurance is a Go project for explaining physical data-path health in
GPU fleets. Its product direction is to connect GPU identity, Kubernetes node
identity, device location, and path evidence so that readiness and lifecycle
decisions can be traced back to observations.

## Current capabilities

The repository currently provides domain libraries, two offline tools (a
fixture collector and an explain command), and the first Kubernetes
control-plane pieces (custom resource types and a status controller), rather
than an installable service. The implemented code includes:

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
  `internal/nativepcie` (see below);
- an offline host collector in `internal/agent`, run by `path-agent` in
  fixture mode (see below); and
- an offline evaluation and explain flow: artifact and fleet file readers in
  `internal/offline`, the evaluation in `internal/app`, and the command-line
  adapter in `internal/cli/explain`, run by `pathctl explain` (see below);
- Kubernetes custom resource types `GPUFleet`, `GPUDevice`, and
  `NodePathState` in `internal/kubernetes/api/v1alpha1`, with generated CRD
  manifests in `deploy/crds/`; and
- a status controller in `internal/kubernetes/controller`, run by
  `path-controller`, that publishes status for those resources (see below).

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

This is offline fixture collection only. It does not observe a live host and
has not been run against real GPU hardware. `pathctl explain` (below) evaluates
and explains the artifact. The collector is pure Go and does not use the native PCIe observer.
Its tests have been run on macOS arm64 and in a linux/arm64 container.

## Offline explain

`pathctl explain` evaluates a `path-agent` snapshot artifact offline and
explains the result for one GPU or one node:

```sh
CGO_ENABLED=0 go build -o bin/pathctl ./cmd/pathctl
bin/pathctl explain gpu gpu-node-1-gpu0 --artifact snapshot.json --fleet fleet.json
bin/pathctl explain node gpu-node-1 --artifact snapshot.json --fleet fleet.json --output json
```

The second input is an offline fleet file (`dpa.offline-fleet/v1`). It holds
the fleet policy and the GPU device intents for the single node that the
artifact describes:

```json
{"schemaVersion":"dpa.offline-fleet/v1","clusterID":"lab-a",
 "fleet":{"name":"lab-a-gpus","uid":"5d7c1b7e-2f0a-4c11-9a51-0b6e3c2d4f10"},
 "policy":{"revision":"gpu-path-v1","freshnessSeconds":60,"readyForSeconds":30,
  "requiredCoverage":[
   {"name":"pcie-parent","pathKind":"gpu-pcie-parent","required":true},
   {"name":"pcie-root","pathKind":"gpu-pcie-root","required":true},
   {"name":"pcie-width","pathKind":"gpu-pcie-link-width-normal","required":true},
   {"name":"nic-lldp","pathKind":"nic-lldp-remote","required":false}]},
 "devices":[{"name":"gpu-node-1-gpu0","uid":"0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41",
  "nodeRef":{"name":"gpu-node-1","uid":"7c9e6679-7425-40de-944b-e07fc1f90ae7"},
  "desiredState":"InService","requestID":"enroll-1","metadataGeneration":1,
  "intentObservedAt":"2026-09-24T00:00:00Z",
  "inventoryClaim":{"vendor":"NVIDIA","uuid":"GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
   "source":"operator/asset-db","evidenceID":"asset-db:rack7-u12-gpu0"}}]}
```

The reader rejects an artifact whose payload digest, evidence IDs, or bundle
revisions do not match an independent recomputation, and a fleet file whose
cluster or node UID differs from the artifact. The evaluation admits the
frames in order (the first frame must be complete), evaluates each frame at
its own observed time with no wall clock, keeps link-width evidence across
frames so that a sustained width degradation can be recognized, and runs the
existing PCIe width rule, `EvaluateDevice`, and `AggregateNode`. The same
inputs always produce the same bytes.

The output shows the observed GPU PCIe path with the provenance of each hop,
active findings, every coverage requirement with its state and reason, the
identity binding, allocation, and a list of limitations. `--output json` (field
names follow the planned controller explain API) and the default text form
carry the same content; text sections are `PATH`, `FINDINGS`, `COVERAGE`,
`ALLOCATION`, and `LIMITATIONS`. Unknown results always carry their reason, and
collector diagnostics, a GPU directly below a host bridge, contradictory paths,
and findings from untrusted sources are shown as limitations without changing
the evaluated result.

Exit codes are 0 (explanation written, whatever the result), 1 (internal
error), 2 (usage error), 4 (device or node not found), 5 (a live transport flag
such as `--server` was given; the live path is not implemented yet),
6 (invalid artifact), 7 (invalid fleet file), and 8 (an input or the output
exceeds its size bound).

Every result is marked `offline`. Offline evidence is never treated as live
trust, and nothing is published to Kubernetes. The offline input has no
allocation or fence data, so maintenance and retirement can be shown as
pending but never as complete. Inputs near the artifact size bounds (tens of
thousands of observations in one frame) can take a minute or more to evaluate.

## Kubernetes API and status controller

`deploy/crds/` holds three cluster-scoped `v1alpha1` custom resource definitions
in the `infrastructure.data-path-assurance.io` group, generated with a pinned
controller-gen from the Go types in `internal/kubernetes/api/v1alpha1`:

- `GPUFleet` selects nodes with `nodeSelector` and carries the coverage policy
  (`requiredCoverage`, `freshnessSeconds`, `readyForSeconds`) and a `mode`
  (`Audit` by default, or `Enforce` with a required `canarySelector`);
- `GPUDevice` records the operator's intent for one GPU: an immutable node
  reference and inventory claim, the fleet it belongs to, `desiredState`
  (`InService`, `Maintenance`, or `Retired`), and a request ID; and
- `NodePathState` is created and owned by the controller, one per selected
  node, with a Node owner reference.

Structural schema and CEL rules reject invalid objects at admission without a
webhook: immutable references and claims, `Retired` as a terminal state, a new
request ID for every desired-state change, non-empty selectors, and bounded
lists and strings. Status fields are bounded, and values that are not known
yet are omitted instead of guessed.

`path-controller` runs one active controller under a `coordination.k8s.io`
Lease. It watches the three resources and Nodes, re-evaluates at least every
30 seconds, and writes each resource's status through the status subresource
with a dedicated field manager, skipping writes when nothing changed. For every
selected node it passes the fleet policy and the device intents to an
evaluation port in `internal/app`. Overlapping fleets make a node `Conflict`, a
fleet selecting more than 1024 nodes is not evaluated, duplicate inventory
claims make the devices `IdentityConflict`, and a fleet with an invalid
selector holds its nodes' state instead of deleting it. For nodes whose fleets
are all in `Audit` mode it also publishes a `DataPathGPUFleetReady` Node
condition with server-side apply, leaving every other condition, taint, and
label untouched. It never writes taints, finalizers, or Pods; an `Enforce`
fleet is reported with `gate_not_implemented` in its condition messages.

There is no live evidence source yet. The controller wires an evaluation port
that reports no observation, so every device stays `Unknown` with reason
`Validating` and the message token `no_observation` until authenticated live
ingest is implemented. This is a truthful cold start, not a qualification
result.

```sh
make envtest-assets     # download kube-apiserver and etcd 1.35.0 into build/envtest (network)
make test-envtest       # run tests/kubeapi against a real API server, with -race
make generate           # regenerate deepcopy code and deploy/crds with controller-gen v0.20.1
make verify-generated   # fail if the checked-in generated files are stale
CGO_ENABLED=0 go build -o bin/path-controller ./cmd/path-controller
bin/path-controller --kubeconfig <file> --cluster-id <id> --leader-election-namespace <namespace>
```

`make test` includes the `tests/kubeapi` suite whenever the envtest binaries are
present and skips it with a log line otherwise. The controller opens no network
listener and never logs kubeconfig contents or API response bodies.

## Product direction

The pure lifecycle evaluation layer, the Kubernetes custom resources, and the
status controller are implemented; live evidence is still future work. Planned
host and transport adapters will collect and bind live identity and path
evidence and feed the controller's evaluation port. The native PCIe observer is
a building block for that host adapter, not the adapter itself. `path-agent`
runs only in offline fixture mode, `pathctl` has only the offline explain path,
and `path-controller` publishes only cold-start status. No live agent, live
evidence ingest, controller explain API, scheduling gate, deployment manifests,
live GPU collection, or validation on a real cluster is available yet.

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
