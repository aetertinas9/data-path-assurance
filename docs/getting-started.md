# Getting started

data-path-assurance currently supports development and validation of its Go
domain libraries, an offline fixture mode of `path-agent`, offline
`pathctl explain`, Kubernetes custom resources with a status controller, and
an authenticated live snapshot ingest from `path-agent` to the controller,
tested against a local API server and in-process agents. Allocation
evidence, an explain API, and an installable deployment are not available
yet.

## Prerequisites

- Go 1.26
- `make`
- a C11 compiler (`cc`) and `ar`, for the native PCIe observer library
- network access once, to download the envtest API server binaries
  (`make envtest-assets`) and the pinned controller-gen used by
  `make generate` and `make verify-generated`

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
(`go test -p 1 ./...`) after the same native preparation; when
`build/envtest/k8s/1.35.0-<os>-<arch>` exists it also runs the Kubernetes API
tests in `tests/kubeapi` against a real API server, and otherwise skips them
with a log line. The race check is separate, and
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

## Offline fixture collection

`path-agent` currently runs only against a fixture directory and writes a
deterministic JSON snapshot to stdout (see the README for the fixture layout
and exit codes). Sample fixtures are not shipped with the repository, so point
`--fixture-root` at a fixture directory you prepared:

```sh
CGO_ENABLED=0 go build -o bin/path-agent ./cmd/path-agent
bin/path-agent --fixture-root path/to/fixture > snapshot.json
```

## Offline explain

`pathctl explain` evaluates that snapshot together with an offline fleet file
(`dpa.offline-fleet/v1`: the fleet policy and the GPU device intents for the
node in the snapshot; see the README for an example) and explains one GPU or
the node:

```sh
CGO_ENABLED=0 go build -o bin/pathctl ./cmd/pathctl
bin/pathctl explain gpu <gpu-device-name> --artifact snapshot.json --fleet fleet.json
bin/pathctl explain node <node-name> --artifact snapshot.json --fleet fleet.json --output json
```

The result is always marked `offline`. The live transport flags (`--server`,
`--cluster-id`, `--ca-file`, `--cert-file`, `--key-file`) are rejected with exit
code 5 until the controller API exists.

There is no cluster installation procedure yet: the source tree has no
deployment manifests. The controller, the live ingest, and the custom
resources described next are for local API server tests and disposable test
clusters.

## Kubernetes API tests and the status controller

The custom resource tests start a local kube-apiserver and etcd (envtest):

```sh
make envtest-assets   # once: kube-apiserver and etcd 1.35.0 into build/envtest
make test-envtest     # tests/kubeapi with -race
make verify-generated # checked-in deepcopy code and CRDs match controller-gen v0.20.1
```

To try the controller against a disposable test cluster, apply the checked-in
definitions and start it with a kubeconfig for that cluster:

```sh
kubectl apply -f deploy/crds/
CGO_ENABLED=0 go build -o bin/path-controller ./cmd/path-controller
bin/path-controller --kubeconfig <file> --cluster-id <id> --leader-election-namespace <namespace>
```

Without `--ingest-listen` the controller publishes cold-start status only:
devices stay `Unknown` with the `no_observation` message token. It writes no
taints or finalizers.

## Live ingest on a test cluster

The live path needs your own CA, a server certificate whose DNS name the agents
use, and one client certificate per node with the URI SAN
`spiffe://data-path-assurance.local/cluster/<cluster-id>/node/<node-uid>` and
the client-authentication key usage. Keep the keys out of the repository.

```sh
bin/path-controller --kubeconfig <file> --cluster-id lab-a --leader-election-namespace <namespace> \
  --ingest-listen :8443 --ingest-service-dns ingest.example.internal \
  --ingest-cert-file server.pem --ingest-key-file server-key.pem --ingest-ca-file ca.pem
CGO_ENABLED=0 go build -o bin/path-agent ./cmd/path-agent
bin/path-agent --controller ingest.example.internal:8443 --cluster-id lab-a \
  --node-name <node> --node-uid <node UID> --sysfs-root /sys \
  --ca-file ca.pem --cert-file node.pem --key-file node-key.pem
```

The node must be selected by a `GPUFleet` so that a `NodePathState` exists before
the agent's hello is accepted, and a live fleet should use a `freshnessSeconds`
of at least 60. Keep `--leader-election-renew-deadline` at 4 seconds or more when
the ingest is enabled: the ingest re-reads the leader Lease every 2 seconds and
treats a reading older than the renew deadline as lost leadership, which drops
every node's in-memory observations. The ingest tests run with `make test` (envtest assets present):

```sh
go test -count=1 ./tests/liveingest/...              # full run
go test -race -count=1 -short ./tests/liveingest/... # race run, heavy cases skipped
make verify-proto                                     # generated wire code is current (network)
```

## What comes next

Offline sysfs and NVIDIA inventory fixture collection, the offline evaluation
and explain flow, the Kubernetes custom resources with a status controller, and
authenticated live snapshot ingest are in place. The next milestones add
allocation evidence and maintenance fencing, the controller explain API, and
then an Audit deployment.

Those executable capabilities are future targets. They should not be treated
as available for deployment or as validated against real GPU hardware. See
[Architecture](architecture.md) for the planned adapter flow and ownership
boundaries.
