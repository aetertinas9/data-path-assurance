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
dependency boundaries, including the boundary around `internal/fleet`. The
`test` target runs the Go test suite. The race check is separate, and
`gofmt -l .` lists files whose formatting differs from `gofmt` output; no
output means all checked Go files are formatted.

The fleet library provides pure snapshot admission, GPU lifecycle and
qualification evaluation, node aggregation, and scheduling-gate decisions from
explicitly supplied policy, evidence, and time. It does not collect live host
or Kubernetes data, and a gate decision does not mutate Kubernetes.

There is no runtime command or cluster installation procedure yet. In
particular, do not expect `pathctl`, an agent, a controller, or Kubernetes
resources to be present in the current source tree.

## What comes next

The next milestone adds injected host/sysfs and NVIDIA inventory fixture
collection plus an application-level offline explain flow. Later milestones
add Kubernetes APIs and transport, followed by an Audit deployment.

Those executable capabilities are future targets. They should not be treated
as available for deployment or as validated against real GPU hardware. See
[Architecture](architecture.md) for the planned adapter flow and ownership
boundaries.
