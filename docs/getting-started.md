# Getting started

data-path-assurance currently supports development and validation of its Go
domain libraries. It does not yet provide an agent, controller, `pathctl`
binary, Kubernetes custom resources, or an installable deployment.

## Prerequisites

- Go 1.26
- `make`
- a C11 compiler (`cc`) and `ar`, for the native PCIe observer library

The module path is `github.com/aetertinas9/data-path-assurance`.

## Check the source

From the repository root, confirm the Go toolchain and run the project checks:

```sh
go version
make all test
go test -race ./...
gofmt -l .
```

`make all` builds the native library first, then builds all current packages,
runs `go vet`, and checks domain-core dependency boundaries, including the
boundary around `internal/fleet` and the rule that no domain-core package
reaches `internal/nativepcie` or cgo. The `test` target runs the Go test suite
after the same native preparation. The race check is separate, and
`gofmt -l .` lists files whose formatting differs from `gofmt` output; no
output means all checked Go files are formatted.

## Native PCIe observer

The cgo bridge in `internal/nativepcie` links a generated static archive, so
plain `go build ./...` or `go test ./...` with cgo enabled needs `make native`
to have run first; the plain commands do not bootstrap the native build. With
`CGO_ENABLED=0` no native artifact is needed and the package builds as an
unavailable stub.

```sh
make native                # static archive, Darwin shared library, build stamp
make native-asan           # sanitizer archive (fails clearly if unsupported)
make native-example        # build/native/<GOOS>-<GOARCH>/inspect
make native-test           # native C/C++ harnesses (needs tests/nativepcie)
make native-test-sanitize  # the same harnesses against the sanitizer archive
make clean-native          # remove native artifacts and the stamp
```

Artifacts land under `build/native/<GOOS>-<GOARCH>/`, where the values come
from `go env GOOS` and `go env GOARCH` (`darwin-arm64` on an Apple silicon
host). The native targets themselves need only `make`, a C compiler, and
`ar`: when `go` is not on `PATH`, the names fall back to `GOOS`/`GOARCH` from
the environment or to the host `uname` mapped to Go spelling (darwin/linux,
arm64/aarch64, x86_64), and an unmappable host fails with an explicit message. Only darwin/arm64 has been built and exercised locally; the Linux
source configuration exists but is unvalidated.

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
