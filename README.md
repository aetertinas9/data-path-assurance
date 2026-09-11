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
  `internal/domains/pcie`; and
- pure GPU device lifecycle, qualification, node aggregation, and scheduling-gate
  evaluation in `internal/fleet`.

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

## Product direction

The pure lifecycle evaluation layer is implemented, but its executable adapters
are still future work. Planned host and Kubernetes adapters will collect and
bind live identity and path evidence and expose `GPUFleet`, `GPUDevice`, and
`NodePathState` resources. No runnable agent, controller, `pathctl`, custom
resource deployment, live GPU collection, or live Kubernetes validation is
available yet.

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
