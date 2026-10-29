# data-path-assurance: build, vet, test and the standard-library-only check.

.PHONY: all build vet fmt test update-golden arch-check

all: build vet arch-check test

build:
	CGO_ENABLED=0 go build -o bin/pathctl ./cmd/pathctl

vet:
	go vet ./...

fmt:
	gofmt -l .

test:
	go test ./...

# Rewrites testdata/golden and testdata/fixtures from the synthetic hosts in
# internal/datapath/fixture_test.go. Review the diff before committing.
update-golden:
	cd internal/datapath && go test -update ./

# The whole program depends on the standard library only: no cgo, no module
# dependencies. A new import outside the module fails here.
arch-check:
	@set -e; \
	module="$$(go list -m)"; \
	bad="$$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./... | grep -v "^$$module" | grep -v '^$$' || true)"; \
	if [ -n "$$bad" ]; then echo "arch-check: FAIL imports outside the standard library:"; echo "$$bad" | sed 's/^/      /'; exit 1; fi; \
	cgo="$$(go list -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./... | grep -v '^$$' || true)"; \
	if [ -n "$$cgo" ]; then echo "arch-check: FAIL cgo in $$cgo"; exit 1; fi; \
	echo "arch-check: OK"
