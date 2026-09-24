# data-path-assurance — repository targets.

# Domain core packages, as import-path suffixes. Not all of them exist yet;
# arch-check inspects whichever ones are present in the tree and ignores the
# rest, so this list can name packages ahead of their arrival.
DOMAIN_CORE := pkg/model|internal/identity|internal/graph|internal/evidence|internal/fleet|internal/correlation|internal/domains|internal/impact|internal/policy|internal/app

# Import paths the domain core must never reach, directly or transitively.
# The domain core knows nothing about Kubernetes, HTTP, Prometheus, gNMI,
# gRPC or the native PCIe adapter; those live behind adapters at the edges.
# runtime/cgo is listed so a transitive cgo dependency is rejected as well.
FORBIDDEN_IMPORTS := k8s\.io|net/http|prometheus/|gnmi|grpc|internal/nativepcie|^runtime/cgo$$

# ---------------------------------------------------------------------------
# Native PCIe observer (libdpa_pcie). Requires only make, a C compiler and ar.
# ---------------------------------------------------------------------------

CC ?= cc
CXX ?= c++
AR ?= ar

# Output layout is build/native/<GOOS>-<GOARCH>. The names come from `go env`
# when Go is on PATH (so GOOS/GOARCH cross settings are honored). The native
# build itself needs only make, a C compiler and ar, so without Go the names
# fall back to GOOS/GOARCH from the environment or to the host's uname mapped
# to Go spelling; an unmappable host fails loudly instead of using a bogus
# directory.
NATIVE_GOOS := $(shell go env GOOS 2>/dev/null)
NATIVE_GOARCH := $(shell go env GOARCH 2>/dev/null)
ifeq ($(strip $(NATIVE_GOOS)$(NATIVE_GOARCH)),)
NATIVE_HOST_OS := $(shell uname -s 2>/dev/null)
NATIVE_HOST_ARCH := $(shell uname -m 2>/dev/null)
NATIVE_GOOS := $(or $(GOOS),$(if $(filter Darwin,$(NATIVE_HOST_OS)),darwin,$(if $(filter Linux,$(NATIVE_HOST_OS)),linux)))
NATIVE_GOARCH := $(or $(GOARCH),$(if $(filter arm64 aarch64,$(NATIVE_HOST_ARCH)),arm64,$(if $(filter x86_64 amd64,$(NATIVE_HOST_ARCH)),amd64)))
ifeq ($(strip $(NATIVE_GOOS)),)
$(error native: go not on PATH and host OS '$(NATIVE_HOST_OS)' has no known GOOS mapping (darwin, linux); install Go or set GOOS)
endif
ifeq ($(strip $(NATIVE_GOARCH)),)
$(error native: go not on PATH and host arch '$(NATIVE_HOST_ARCH)' has no known GOARCH mapping (arm64/aarch64, x86_64/amd64); install Go or set GOARCH)
endif
endif
NATIVE_TARGET := $(NATIVE_GOOS)-$(NATIVE_GOARCH)

NATIVE_PKG := internal/nativepcie
NATIVE_INCLUDE := $(NATIVE_PKG)/csrc
NATIVE_HEADER := $(NATIVE_INCLUDE)/dpa_pcie.h
NATIVE_SOURCES := $(NATIVE_INCLUDE)/dpa_pcie.c
NATIVE_EXAMPLE_SRC := $(NATIVE_PKG)/examples/inspect.c
NATIVE_STAMP := $(NATIVE_PKG)/dpa_pcie_stamp.h

NATIVE_DIR := build/native/$(NATIVE_TARGET)
NATIVE_ASAN_DIR := build/native/$(NATIVE_TARGET)-asan
NATIVE_TEST_BUILD := build/native-tests/$(NATIVE_TARGET)
NATIVE_OBJ := $(NATIVE_DIR)/dpa_pcie.o
NATIVE_ARCHIVE := $(NATIVE_DIR)/libdpa_pcie.a
NATIVE_ASAN_OBJ := $(NATIVE_ASAN_DIR)/dpa_pcie.o
NATIVE_ASAN_ARCHIVE := $(NATIVE_ASAN_DIR)/libdpa_pcie.a
NATIVE_EXAMPLE := $(NATIVE_DIR)/inspect

ifeq ($(NATIVE_GOOS),darwin)
NATIVE_SHARED := $(NATIVE_DIR)/libdpa_pcie.dylib
NATIVE_SHARED_FLAGS := -dynamiclib -install_name @rpath/libdpa_pcie.dylib
else
# Linux .so rule: present but deferred and unvalidated by this delivery.
NATIVE_SHARED := $(NATIVE_DIR)/libdpa_pcie.so
NATIVE_SHARED_FLAGS := -shared -Wl,-soname,libdpa_pcie.so
endif

NATIVE_CFLAGS := -std=c11 -fPIC -O2 -Wall -Wextra -Werror -Wshadow -Wconversion -Wstrict-prototypes -fvisibility=hidden
NATIVE_ASAN_FLAGS := -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer

# Digest of every native input that must invalidate the Go build cache:
# source, header, compiler identity, compile flags and target. sha256 is used
# when available; cksum is the POSIX fallback.
NATIVE_DIGEST_CMD := { if command -v sha256sum >/dev/null 2>&1; then sha256sum; elif command -v shasum >/dev/null 2>&1; then shasum -a 256; else cksum; fi; } | cut -d' ' -f1
NATIVE_DIGEST := $(shell { cat $(NATIVE_HEADER) $(NATIVE_SOURCES); printf '%s\n' '$(CC)'; $(CC) --version 2>/dev/null | head -n 1; printf '%s\n' '$(NATIVE_CFLAGS)' '$(NATIVE_TARGET)'; } 2>/dev/null | $(NATIVE_DIGEST_CMD))
# The sanitizer archive additionally depends on the sanitizer flags.
NATIVE_ASAN_DIGEST := $(shell printf '%s\n' '$(NATIVE_DIGEST)' '$(NATIVE_ASAN_FLAGS)' | $(NATIVE_DIGEST_CMD))

# Staleness is decided by content, not by mtime: every native output records
# the digest it was built from in <output>.digest, and at parse time an
# output whose record differs from the current digest gets the phony
# `native-force` prerequisite, which rebuilds it regardless of timestamps.
# This keeps consecutive builds with different inputs correct even within
# one second (mtime resolution), while unchanged inputs rebuild nothing.
#   $(call native-stale,<output>,<digest>) -> "native-force" or empty
#   $(call native-record,<output>,<digest>) -> shell command writing the record
native-stale = $(if $(filter $(2),$(shell cat '$(1).digest' 2>/dev/null)),,native-force)
native-record = printf '%s\n' '$(2)' > '$(1).digest'

.PHONY: all build vet fmt test arch-check
.PHONY: native native-asan native-example native-test native-test-sanitize clean-native native-force

all: build vet arch-check

# Existing workflows prepare the native prerequisites first so the cgo bridge
# can link on supported targets. `go build` without `make native` does not
# bootstrap them (see README).
build: native
	go build ./...

vet: native
	go vet ./...

test: native
	go test ./...

fmt:
	gofmt -l .

# --- native ----------------------------------------------------------------

native: $(NATIVE_ARCHIVE) $(NATIVE_SHARED) $(NATIVE_STAMP)

native-asan: $(NATIVE_ASAN_ARCHIVE)

native-example: $(NATIVE_EXAMPLE)

native-force:
	@:

# build/ ignores itself: every native rule that creates build/ first drops a
# one-line `*` .gitignore there, so generated artifacts never show up in git
# status regardless of the (untracked, local) root .gitignore. clean-native
# keeps this file so the rule survives cleanup.
NATIVE_BUILD_IGNORE := build/.gitignore

$(NATIVE_BUILD_IGNORE):
	@mkdir -p build
	@printf '*\n' > $@

$(NATIVE_OBJ): $(NATIVE_SOURCES) $(NATIVE_HEADER) $(call native-stale,$(NATIVE_OBJ),$(NATIVE_DIGEST)) | $(NATIVE_BUILD_IGNORE)
	@mkdir -p $(NATIVE_DIR)
	$(CC) $(NATIVE_CFLAGS) -I$(NATIVE_INCLUDE) -c -o $@ $(NATIVE_SOURCES)
	@$(call native-record,$@,$(NATIVE_DIGEST))

$(NATIVE_ARCHIVE): $(NATIVE_OBJ) $(call native-stale,$(NATIVE_ARCHIVE),$(NATIVE_DIGEST))
	rm -f $@
	$(AR) rcs $@ $(NATIVE_OBJ)
	@$(call native-record,$@,$(NATIVE_DIGEST))

$(NATIVE_SHARED): $(NATIVE_OBJ) $(call native-stale,$(NATIVE_SHARED),$(NATIVE_DIGEST))
	$(CC) $(NATIVE_CFLAGS) $(NATIVE_SHARED_FLAGS) -o $@ $(NATIVE_OBJ)
	@$(call native-record,$@,$(NATIVE_DIGEST))

# The stamp header carries its digest in its content, so it is its own
# record; it is rewritten only when that content changes, which keeps the Go
# build cache valid across repeated `make native` runs with the same inputs.
$(NATIVE_STAMP): $(if $(shell grep -l '"$(NATIVE_DIGEST)"' '$(NATIVE_STAMP)' 2>/dev/null),,native-force)
	@printf '%s\n' \
		'/* Generated by `make native`; ignored by git. Do not edit. */' \
		'#ifndef DPA_PCIE_STAMP_H' \
		'#define DPA_PCIE_STAMP_H' \
		'#define DPA_PCIE_BUILD_STAMP "$(NATIVE_DIGEST)"' \
		'#define DPA_PCIE_BUILD_TARGET "$(NATIVE_TARGET)"' \
		'#endif' > $@.tmp
	@if cmp -s $@.tmp $@; then rm -f $@.tmp; else mv $@.tmp $@; fi

$(NATIVE_ASAN_OBJ): $(NATIVE_SOURCES) $(NATIVE_HEADER) $(call native-stale,$(NATIVE_ASAN_OBJ),$(NATIVE_ASAN_DIGEST)) | $(NATIVE_BUILD_IGNORE)
	@mkdir -p $(NATIVE_ASAN_DIR)
	@printf 'int main(void) { return 0; }\n' | $(CC) -x c $(NATIVE_ASAN_FLAGS) -o $(NATIVE_ASAN_DIR)/.probe - >/dev/null 2>&1 \
		|| { echo "native-asan: $(CC) does not support '$(NATIVE_ASAN_FLAGS)'; sanitizer archive not built" >&2; exit 1; }
	@rm -f $(NATIVE_ASAN_DIR)/.probe
	$(CC) $(NATIVE_CFLAGS) $(NATIVE_ASAN_FLAGS) -I$(NATIVE_INCLUDE) -c -o $@ $(NATIVE_SOURCES)
	@$(call native-record,$@,$(NATIVE_ASAN_DIGEST))

$(NATIVE_ASAN_ARCHIVE): $(NATIVE_ASAN_OBJ) $(call native-stale,$(NATIVE_ASAN_ARCHIVE),$(NATIVE_ASAN_DIGEST))
	rm -f $@
	$(AR) rcs $@ $(NATIVE_ASAN_OBJ)
	@$(call native-record,$@,$(NATIVE_ASAN_DIGEST))

$(NATIVE_EXAMPLE): $(NATIVE_EXAMPLE_SRC) $(NATIVE_ARCHIVE) $(NATIVE_HEADER) $(call native-stale,$(NATIVE_EXAMPLE),$(NATIVE_DIGEST))
	$(CC) $(NATIVE_CFLAGS) -I$(NATIVE_INCLUDE) -o $@ $(NATIVE_EXAMPLE_SRC) $(NATIVE_ARCHIVE)
	@$(call native-record,$@,$(NATIVE_DIGEST))

NATIVE_TEST_VARS := REPO_ROOT="$(CURDIR)" \
	NATIVE_INCLUDE="$(CURDIR)/$(NATIVE_INCLUDE)" \
	NATIVE_DIR="$(CURDIR)/$(NATIVE_DIR)" \
	NATIVE_ASAN_DIR="$(CURDIR)/$(NATIVE_ASAN_DIR)" \
	NATIVE_TEST_BUILD="$(CURDIR)/$(NATIVE_TEST_BUILD)" \
	CC="$(CC)" CXX="$(CXX)"

native-test: native $(NATIVE_BUILD_IGNORE)
	$(MAKE) -C tests/nativepcie test $(NATIVE_TEST_VARS)

native-test-sanitize: native-asan $(NATIVE_BUILD_IGNORE)
	$(MAKE) -C tests/nativepcie test-sanitize $(NATIVE_TEST_VARS)

# Removes only the paths this feature owns for the current target: its
# artifact directories, the harness build directory and the stamp. Unrelated
# files are left untouched; build/native and build/native-tests go away only
# when that leaves them empty, and build/.gitignore stays so build/ keeps
# ignoring itself.
clean-native:
	rm -rf $(NATIVE_DIR) $(NATIVE_ASAN_DIR) $(NATIVE_TEST_BUILD)
	rm -f $(NATIVE_STAMP) $(NATIVE_STAMP).tmp
	@rmdir build/native build/native-tests 2>/dev/null || true

# arch-check enforces the dependency direction of the domain core.
#
# It walks the transitive dependencies of every domain core package that
# currently exists and fails if any of them reaches a forbidden import, then
# separately checks that pkg/model depends on nothing but the standard library,
# and that no domain core package uses cgo itself. Packages that do not exist
# yet are simply absent from `go list ./...` and are never a reason to fail.
#
# arch-check needs no native artifact: `go list` resolves imports and CgoFiles
# without linking, so it must not depend on the native build (or on the
# native sources being present at all).
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
		cgofiles="$$(go list -f '{{join .CgoFiles " "}}' "$$pkg")" || { echo "arch-check: go list $$pkg failed" >&2; exit 1; }; \
		if [ -n "$$cgofiles" ]; then \
			status=1; \
			echo "arch-check: FAIL $$pkg uses cgo: $$cgofiles"; \
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
	for pkg in $$packages; do \
		case "$$pkg" in "$$module/internal/domains"|"$$module/internal/domains/"*) ;; *) continue ;; esac; \
		deps="$$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$$pkg")" || { echo "arch-check: go list -deps $$pkg failed" >&2; exit 1; }; \
		for dep in $$deps; do \
			case "$$dep" in "$$pkg"|"$$module/pkg/model"|"$$module/internal/evidence") continue ;; esac; \
			status=1; \
			echo "arch-check: FAIL $$pkg reaches dependency outside model/evidence: $$dep"; \
		done; \
	done; \
	if [ $$status -eq 0 ]; then \
		echo "arch-check: OK"; \
	fi; \
	exit $$status
