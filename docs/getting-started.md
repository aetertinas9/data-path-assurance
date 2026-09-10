# Getting started

data-path-assurance currently supports development and validation of its Go
domain libraries. It does not yet provide an agent, controller, `pathctl`
binary, Kubernetes custom resources, or an installable deployment.

## Prerequisites

- Go 1.26
- `make`

The module path is `github.com/aetertinas9/data-path-assurance`.

## Check the source

From the repository root, confirm the Go toolchain and run the project checks:

```sh
go version
make all test
go test -race ./...
gofmt -l .
```

`make all` builds all current packages, runs `go vet`, and checks domain-core
dependency boundaries. The `test` target runs the Go test suite. The race check
is separate, and `gofmt -l .` lists files whose formatting differs from
`gofmt` output; no output means all checked Go files are formatted.

There is no runtime command or cluster installation procedure yet. In
particular, do not expect `pathctl`, an agent, a controller, or Kubernetes
resources to be present in the current source tree.

## What comes next

The intended next product surface adds source adapters and a separately
integrated Kubernetes layer around the domain core. That layer is expected to
represent real GPU UUID, Node UID and incarnation, and PCI BDF relationships;
publish `GPUFleet`, `GPUDevice`, and `NodePathState`; and support explainable
readiness and lifecycle qualification.

Those capabilities are future targets. They should not be treated as available
for deployment or as validated against real GPU hardware. See
[Architecture](architecture.md) for the planned data flow and ownership
boundaries.
