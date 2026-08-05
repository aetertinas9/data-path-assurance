# data-path-assurance — repository targets.

# Domain core packages, as import-path suffixes. Not all of them exist yet;
# arch-check inspects whichever ones are present in the tree and ignores the
# rest, so this list can name packages ahead of their arrival.
DOMAIN_CORE := pkg/model|internal/identity|internal/graph|internal/evidence|internal/correlation|internal/domains|internal/impact|internal/policy|internal/app

# Import paths the domain core must never reach, directly or transitively.
# The domain core knows nothing about Kubernetes, HTTP, Prometheus, gNMI or
# gRPC; those live behind adapters at the edges.
FORBIDDEN_IMPORTS := k8s\.io|net/http|prometheus/|gnmi|grpc

.PHONY: all build vet fmt test arch-check

all: build vet arch-check

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -l .

# arch-check enforces the dependency direction of the domain core.
#
# It walks the transitive dependencies of every domain core package that
# currently exists and fails if any of them reaches a forbidden import, then
# separately checks that pkg/model depends on nothing but the standard library.
# Packages that do not exist yet are simply absent from `go list ./...` and are
# never a reason to fail.
arch-check:
	@set -u; \
	module="$$(go list -m)" || { echo "arch-check: cannot determine module path" >&2; exit 1; }; \
	all_packages="$$(go list ./...)" || { echo "arch-check: go list ./... failed" >&2; exit 1; }; \
	packages="$$(printf '%s\n' "$$all_packages" | grep -E "^$$module/($(DOMAIN_CORE))(/.*)?$$" || true)"; \
	status=0; \
	if [ -z "$$packages" ]; then \
		echo "arch-check: no domain core package present yet; nothing to check"; \
	fi; \
	for pkg in $$packages; do \
		deps="$$(go list -deps -f '{{.ImportPath}}' "$$pkg")" || { echo "arch-check: go list -deps $$pkg failed" >&2; exit 1; }; \
		forbidden="$$(printf '%s\n' "$$deps" | grep -E '$(FORBIDDEN_IMPORTS)' || true)"; \
		if [ -n "$$forbidden" ]; then \
			status=1; \
			echo "arch-check: FAIL $$pkg reaches forbidden imports:"; \
			printf '%s\n' "$$forbidden" | sed 's/^/      /'; \
		fi; \
	done; \
	if printf '%s\n' "$$packages" | grep -qx "$$module/pkg/model"; then \
		deps="$$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./pkg/model)" || { echo "arch-check: go list -deps ./pkg/model failed" >&2; exit 1; }; \
		nonstd="$$(printf '%s\n' "$$deps" | grep -v '^$$' | grep -vx "$$module/pkg/model" || true)"; \
		if [ -n "$$nonstd" ]; then \
			status=1; \
			echo "arch-check: FAIL pkg/model imports outside the standard library:"; \
			printf '%s\n' "$$nonstd" | sed 's/^/      /'; \
		fi; \
	fi; \
	if [ $$status -eq 0 ]; then \
		echo "arch-check: OK"; \
	fi; \
	exit $$status
