# Architecture

data-path-assurance is organized as a modular Go monolith. Domain packages stay
independent of Kubernetes and other infrastructure clients; adapters belong at
the edge of the application.

## Implemented core

The current code is a library-level domain core. It has no running agent,
controller, CLI, Kubernetes adapter, or custom resources.

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

## Target executable integration

The following flow describes the intended integration around the implemented
domain libraries. Its adapters, control plane, and Kubernetes resources are not
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
allocation state. It does not collect those inputs from a real host or cluster.
Collectors, transport, Kubernetes APIs, and deployment remain future work, and
the system has not been validated on real GPUs or made available for
installation.
