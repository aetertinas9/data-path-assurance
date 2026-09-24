# tests/nativepcie/Makefile — independent native harness for libdpa_pcie
# (independent black-box harnesses). Invoked by the top-level `native-test` and
# `native-test-sanitize` targets with absolute REPO_ROOT, NATIVE_INCLUDE,
# NATIVE_DIR, NATIVE_ASAN_DIR, NATIVE_TEST_BUILD, CC and CXX. Every value has a
# standalone default so `make -C tests/nativepcie test` also works after
# `make native` at the repository root.
#
# Outputs are written only below NATIVE_TEST_BUILD.

REPO_ROOT ?= $(abspath ../..)
GOOS ?= $(shell go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')
GOARCH ?= $(shell go env GOARCH 2>/dev/null || uname -m)
NATIVE_INCLUDE ?= $(REPO_ROOT)/internal/nativepcie/csrc
NATIVE_DIR ?= $(REPO_ROOT)/build/native/$(GOOS)-$(GOARCH)
NATIVE_ASAN_DIR ?= $(REPO_ROOT)/build/native/$(GOOS)-$(GOARCH)-asan
NATIVE_TEST_BUILD ?= $(REPO_ROOT)/build/native-tests/$(GOOS)-$(GOARCH)

# GNU make predefines CC=cc and CXX=g++; the contract default is cc / c++.
ifeq ($(origin CC),default)
CC := cc
endif
ifeq ($(origin CXX),default)
CXX := c++
endif

STATIC_LIB := $(NATIVE_DIR)/libdpa_pcie.a
ASAN_LIB := $(NATIVE_ASAN_DIR)/libdpa_pcie.a
ifeq ($(GOOS),darwin)
SHARED_LIB := $(NATIVE_DIR)/libdpa_pcie.dylib
else
SHARED_LIB := $(NATIVE_DIR)/libdpa_pcie.so
endif
EXAMPLE_BIN := $(NATIVE_DIR)/inspect

WARN := -Wall -Wextra -Werror
DEFS := -D_GNU_SOURCE -D_DARWIN_C_SOURCE
CFLAGS := -std=c11 $(WARN) -Wshadow -Wstrict-prototypes $(DEFS) -I$(NATIVE_INCLUDE) -pthread
CXXFLAGS := -std=c++17 $(WARN) -Wshadow $(DEFS) -I$(NATIVE_INCLUDE) -pthread
LDLIBS := -pthread
ifneq ($(GOOS),darwin)
LDLIBS += -ldl
endif
SANFLAGS := -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer

B := $(NATIVE_TEST_BUILD)

HARNESS_SRC := npo_harness.c npo_common.c
EXAMPLE_SRC := npo_example_check.c npo_common.c
CXX_SRC := npo_cxx_linkage.cpp

.PHONY: test test-sanitize build build-sanitize clean

build: $(B)/npo_harness_static $(B)/npo_harness_shared $(B)/npo_cxx_linkage $(B)/npo_example_check

build-sanitize: $(B)/npo_harness_asan $(B)/npo_cxx_linkage_asan

$(B):
	mkdir -p $(B)

$(STATIC_LIB):
	@echo "native harness: missing $(STATIC_LIB); run 'make native' at the repository root" >&2; exit 1

$(SHARED_LIB):
	@echo "native harness: missing $(SHARED_LIB); run 'make native' at the repository root" >&2; exit 1

$(ASAN_LIB):
	@echo "native harness: missing $(ASAN_LIB); run 'make native-asan' at the repository root" >&2; exit 1

# The example binary is built by the root Makefile. Always ask it to
# bring it up to date (make decides freshness from inspect.c and the archive)
# so NPO-070 never runs against a stale binary.
.PHONY: native-example-fresh
native-example-fresh:
	$(MAKE) -C $(REPO_ROOT) native-example

$(B)/npo_harness_static: $(HARNESS_SRC) npo_common.h $(STATIC_LIB) | $(B)
	$(CC) $(CFLAGS) -o $@ $(HARNESS_SRC) $(STATIC_LIB) $(LDLIBS)

# Link the shared library by path so the static archive in the same directory
# cannot be picked instead; the install name @rpath/libdpa_pcie.dylib is
# resolved through -rpath NATIVE_DIR at run time.
$(B)/npo_harness_shared: $(HARNESS_SRC) npo_common.h $(SHARED_LIB) | $(B)
	$(CC) $(CFLAGS) -o $@ $(HARNESS_SRC) $(SHARED_LIB) -Wl,-rpath,$(NATIVE_DIR) $(LDLIBS)

$(B)/npo_common_cxx.o: npo_common.c npo_common.h | $(B)
	$(CC) $(CFLAGS) -c -o $@ npo_common.c

$(B)/npo_cxx_linkage: $(CXX_SRC) npo_common.h $(B)/npo_common_cxx.o $(STATIC_LIB) | $(B)
	$(CXX) $(CXXFLAGS) -o $@ $(CXX_SRC) $(B)/npo_common_cxx.o $(STATIC_LIB) $(LDLIBS)

$(B)/npo_example_check: $(EXAMPLE_SRC) npo_common.h | $(B)
	$(CC) $(CFLAGS) -o $@ $(EXAMPLE_SRC) $(LDLIBS)

$(B)/npo_harness_asan: $(HARNESS_SRC) npo_common.h $(ASAN_LIB) | $(B)
	$(CC) $(CFLAGS) $(SANFLAGS) -o $@ $(HARNESS_SRC) $(ASAN_LIB) $(SANFLAGS) $(LDLIBS)

$(B)/npo_common_asan.o: npo_common.c npo_common.h | $(B)
	$(CC) $(CFLAGS) $(SANFLAGS) -c -o $@ npo_common.c

$(B)/npo_cxx_linkage_asan: $(CXX_SRC) npo_common.h $(B)/npo_common_asan.o $(ASAN_LIB) | $(B)
	$(CXX) $(CXXFLAGS) $(SANFLAGS) -o $@ $(CXX_SRC) $(B)/npo_common_asan.o $(ASAN_LIB) $(SANFLAGS) $(LDLIBS)

# Run every harness even after one fails; exit non-zero if any failed.
test: build native-example-fresh
	@status=0; \
	echo "== npo_harness_static ($(STATIC_LIB))"; \
	"$(B)/npo_harness_static" --expect-static || status=1; \
	echo "== npo_harness_shared ($(SHARED_LIB))"; \
	"$(B)/npo_harness_shared" --expect-shared || status=1; \
	echo "== npo_cxx_linkage"; \
	"$(B)/npo_cxx_linkage" || status=1; \
	echo "== npo_example_check ($(EXAMPLE_BIN))"; \
	"$(B)/npo_example_check" "$(EXAMPLE_BIN)" || status=1; \
	if [ $$status -ne 0 ]; then echo "native harness: FAILED" >&2; fi; \
	exit $$status

test-sanitize: build-sanitize
	@status=0; \
	echo "== npo_harness_asan ($(ASAN_LIB))"; \
	ASAN_OPTIONS="$${ASAN_OPTIONS:-abort_on_error=0:detect_leaks=0}" \
	UBSAN_OPTIONS="$${UBSAN_OPTIONS:-print_stacktrace=1}" \
	"$(B)/npo_harness_asan" --expect-static || status=1; \
	echo "== npo_cxx_linkage_asan"; \
	ASAN_OPTIONS="$${ASAN_OPTIONS:-abort_on_error=0:detect_leaks=0}" \
	UBSAN_OPTIONS="$${UBSAN_OPTIONS:-print_stacktrace=1}" \
	"$(B)/npo_cxx_linkage_asan" || status=1; \
	if [ $$status -ne 0 ]; then echo "native harness (sanitize): FAILED" >&2; fi; \
	exit $$status

clean:
	rm -rf "$(B)"
