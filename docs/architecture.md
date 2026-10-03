# Architecture

data-path-assurance is organized as a modular Go monolith. Domain packages stay
independent of Kubernetes and other infrastructure clients; adapters belong at
the edge of the application.

## Implemented core

The current code is a library-level domain core, a host agent, an offline
explain tool, and a Kubernetes status controller with an authenticated
snapshot ingest. `internal/agent`, run by `path-agent --fixture-root`, turns a
fixture sysfs tree and canned NVIDIA inventory into a JSON snapshot artifact;
in live mode `path-agent` streams the same snapshots to the controller (see
[Live snapshot ingest](#live-snapshot-ingest)). `pathctl explain` evaluates an
artifact offline (see [Offline explain](#offline-explain)). `path-controller`
publishes status for the `GPUFleet`, `GPUDevice`, and `NodePathState` custom
resources (see [Kubernetes API and status controller](#kubernetes-api-and-status-controller)).

Arrows point from a shared building block to the package that consumes it.

```mermaid
flowchart LR
    M["pkg/model<br/>shared domain types"]
    I["internal/identity<br/>typed identity handling"]
    E["internal/evidence<br/>immutable evidence processing"]
    G["internal/graph<br/>single-writer reducer<br/>immutable graph snapshots"]
    P["internal/domains/pcie<br/>deterministic persistent<br/>link-width evaluation"]

    M --> I
    M --> E
    M --> G
    M --> P
    E --> G
    E --> P
```

The graph reducer serializes state changes through one writer and exposes
immutable state. Evidence is also treated as immutable. These constraints make
repeated processing easier to reason about and keep PCIe evaluation
deterministic for the same supplied inputs.

The PCIe domain evaluates persistent negotiated-width degradation against an
expected width. It is passive and does not reset devices, change drivers, or
alter host state.

`internal/fleet` adds pure GPU and node evaluation to this core. Its public
functions are `NewPolicy`, `AdmitSnapshot`, `EvaluateDevice`, `AggregateNode`,
and `EvaluateGate`. They consume explicitly supplied policy, evidence, and time
and return domain values or actions. A scheduling-gate action is a
decision value; the package does not mutate Kubernetes.

Snapshot admission distinguishes accepted, duplicate, older or out-of-order,
gap, wrong-session, and conflicting input. A duplicate preserves the admitted
original. Fresh evidence and distinct accepted normal points drive readiness,
and identity, request, or policy changes reset that progress. Unknown is not
success evidence.

GPU identity is bound to node UID, boot identity, and PCI BDF through trusted
observed evidence. Maintenance or retirement completion requires a trusted
fence matching the current intent and a complete, fresh allocation-empty
assessment. These are evaluations of supplied inputs, not host collection or
cluster operations.

Current qualification coverage is limited to the GPU PCIe parent path, root
path, and link-width evidence. GPU/NIC shared-ancestor proof is not implemented
and remains Unknown; live LLDP coverage also remains Unknown until collection
is added.

The domain packages, including `internal/fleet`, are protected from direct and
transitive imports of external adapters.

### Native PCIe observer adapter

`internal/nativepcie` is an adapter, not part of the domain core. It bridges
through cgo to `libdpa_pcie` (`internal/nativepcie/csrc`), a bounded C11 library
that only parses caller-supplied sysfs text and reads caller-opened
descriptors from offset zero. The C code never opens a path, reads the
environment, allocates, spawns threads, handles signals, or logs, and it keeps
no state between calls. `ReadDevice` resolves every device path through a
caller-injected `*os.Root` (invariant 7: no `/sys` constant) and returns raw
per-field facts with independent statuses. It performs no discovery, identity
resolution, topology inference, or readiness/health evaluation; feeding its
observations into evidence remains future host-adapter work. `make arch-check`
rejects any domain-core dependency on `internal/nativepcie` or on cgo. On
unsupported targets or with `CGO_ENABLED=0` the package is an unavailable
stub.

### Offline explain

`pathctl explain` is wired in `cmd/pathctl` from three packages. Each arrow
points from a package to the package it hands values to.

```mermaid
flowchart LR
    A["path-agent artifact<br/>dpa.offline-snapshot/v1"]
    F["offline fleet file<br/>dpa.offline-fleet/v1"]
    O["internal/offline<br/>strict readers,<br/>independent digest and ID check"]
    P["internal/app<br/>replay evaluation and<br/>explanation model"]
    D["internal/fleet · internal/graph ·<br/>internal/evidence · internal/domains/pcie"]
    C["internal/cli/explain<br/>arguments, exit codes,<br/>JSON and text output"]

    A --> O
    F --> O
    O --> P
    D --> P
    P --> C
```

`internal/offline` is a driven adapter. It reads the two files, recomputes the
artifact digests and IDs independently of the collector, and converts the
frames into domain values. `internal/app` belongs to the domain core checked by
`make arch-check`: it runs the frame admission chain, evaluates each frame at
its own observed time, and assembles the explanation without depending on file
formats or I/O, so the future controller explain API can reuse it.
`internal/cli/explain` is the driving adapter that parses arguments and formats
the explanation. The command depends on neither `internal/agent` nor the native
PCIe observer and builds with `CGO_ENABLED=0`.

### Kubernetes API and status controller

`cmd/path-controller` wires three parts. Kubernetes types and clients stay in
`internal/kubernetes`; the evaluation port lives in `internal/app`, which remains
part of the domain core checked by `make arch-check`.

```mermaid
flowchart LR
    K["GPUFleet · GPUDevice · Node<br/>(watched objects)"]
    C["internal/kubernetes/controller<br/>selection, scopes, projection,<br/>leader election, status writes"]
    P["internal/app NodeAssessor<br/>LiveAssessor"]
    S["LiveBundleSource<br/>(live ingest, or no observation)"]
    F["internal/fleet<br/>EvaluateDevice · AggregateNode"]
    O["CRD status · NodePathState ·<br/>Audit Node condition"]

    K --> C
    C --> P
    S --> P
    F --> P
    P --> C
    C --> O
```

The controller reads its inputs from informer caches and runs one serial pass at
a time: it classifies every fleet, node, and device scope, calls the assessor
once per evaluable node, and writes only statuses that changed. A missing
observation is represented by the absence of a decision, never by a synthesized
one, so a cold start renders `Unknown` from the intent alone. Previous decisions
are kept in memory only and are not restored from status after a restart or a
leader change. Custom resource definitions are applied from the checked-in
manifests; the controller never creates or changes them.

### Live snapshot ingest

With `--ingest-listen`, `cmd/path-controller` also wires the ingest server and
hands its bundle source to the evaluation port; without it the port reports no
observation.

```mermaid
flowchart LR
    A["path-agent live<br/>internal/agent/liveclient"]
    T["internal/transport/mtls<br/>TLS 1.3, URI SAN identity"]
    I["internal/ingest<br/>gRPC server, size and rate limits,<br/>acks and status codes"]
    L["internal/app/liveingest<br/>validation, canonical digest,<br/>admission, per-node reducer"]
    R["internal/app/framecore<br/>topology and digests shared<br/>with the offline path"]
    D["internal/kubernetes/ingestadapter<br/>Node lookup, collectorSession,<br/>Lease leader gate"]
    P["internal/app LiveAssessor"]

    A --> T --> I --> L
    R --> L
    D --> L
    L --> P
```

The wire contract is `api/proto/dpa/ingest/v1alpha1/ingest.proto`, compiled into
the checked-in `internal/ingest/ingestpb`. A stream starts with a hello whose
cluster, node name, and node UID must match the client certificate and the real
Node; the adapter then raises `NodePathState.status.collectorSession` with an
optimistic-concurrency update that rewrites only that field, and the previous
stream of the node is ended. Each frame is limited (4 MiB on the wire, 16 MiB
inflated, 4096 observations, one frame per 10 s with a burst of 3), validated,
digested with the same canonical encoding as the offline artifact, and admitted
by the fleet snapshot rules; a rejected frame changes nothing, and an accepted
one is published as an immutable per-node view. The reducer keeps the latest
16 accepted frames, at most 8192 observations, and builds each bundle at the
controller's evaluation time from the newest frame and that window, with the
same topology and digest code as the offline explain path. Sources outside the
trust profile are stamped so that the fleet library treats them as untrusted.
The ingest accepts hellos only while the process holds the leader Lease, keeps
no evidence across restarts, and persists only the session counter.
`internal/app/liveingest` and `internal/app/framecore` import no gRPC,
protobuf, or Kubernetes package, and `path-agent` links no Kubernetes or native
PCIe code.

## Target executable integration

The following flow describes the intended integration around the implemented
domain libraries. The Kubernetes resources, the status controller, and the
authenticated snapshot ingest exist; PodResources allocation, maintenance
fencing, the controller explain API, and the scheduling gate are not
implemented yet.

```mermaid
flowchart LR
    S["Read-only host evidence<br/>including sysfs"]
    K["Kubernetes identity<br/>Node UID and incarnation"]
    X["Expected fleet intent"]
    A["Separate source and<br/>Kubernetes adapters"]
    EG["Physical-path evidence graph<br/>GPU UUID ↔ Node UID/incarnation ↔ BDF"]
    C["Observed / expected / inferred<br/>kept distinct"]
    R["GPUFleet<br/>GPUDevice<br/>NodePathState"]
    Q["Readiness, maintenance,<br/>return, and retirement qualification"]

    S --> A
    K --> A
    X --> A
    A --> EG
    EG --> C
    C --> R
    R --> Q
```

The target keeps infrastructure integration outside the domain core. Host
collection is read-only, and the project does not introduce a custom time
series database. Evidence needed for a decision belongs in the lifecycle data
model, while active operations remain with the systems that already own them.

## Boundaries

data-path-assurance is intended to qualify and explain physical-path state. It
does not own active GPU workloads, device reset, node drain, or driver
management. A GPU operator or another cluster operator continues to perform
those actions.

The fleet library evaluates supplied identity, evidence, lifecycle intent, and
allocation state. It does not collect those inputs from a real host or cluster;
the offline explain path evaluates recorded fixture snapshots, and the live
ingest evaluates what an authenticated agent reports. PodResources allocation
evidence, fencing, the controller explain API, the scheduling gate, and
deployment manifests remain future work. The Kubernetes resources, the
controller, and the ingest are tested against a local API server and in-process
agents only; the system has not been validated on a real cluster or real GPUs or
made available for installation.
