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
  `internal/graph`; and
- deterministic evaluation of persistent PCIe link-width degradation in
  `internal/domains/pcie`.

PCIe evaluation is passive: it compares negotiated and expected link width
from supplied evidence and produces deterministic results. Source adapters,
agents, controllers, `pathctl`, Kubernetes custom resources, and deployment
manifests are not implemented yet.

## Product direction

The planned lifecycle layer will model the physical path between a real GPU
UUID and its Kubernetes Node UID, node incarnation, and PCI BDF. It will keep
observed, expected, and inferred information distinct and use three Kubernetes
resources—`GPUFleet`, `GPUDevice`, and `NodePathState`—to support readiness,
maintenance, return-to-service, and retirement qualification.

This lifecycle layer is a design target, not a currently installable or
hardware-validated product. Active GPU work, reset, drain, and driver
management remain the responsibility of operators such as GPU Operator.

## Documentation

- [Documentation index](docs/README.md)
- [Architecture](docs/architecture.md)
- [Getting started](docs/getting-started.md)

## Acknowledgments

AI assistance: [OpenAI](https://openai.com/) helped with planning, implementation,
review, and documentation through its AI tools. Project decisions and maintenance
remain with the repository maintainers.

## License

[Apache License 2.0](LICENSE).
