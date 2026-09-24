/*
 * dpa_pcie.h — public C ABI of libdpa_pcie (ABI version 1).
 *
 * libdpa_pcie is a bounded, read-only PCIe sysfs text parser and descriptor
 * reader. It never opens a path, consults the environment, allocates memory,
 * starts a thread, installs a signal handler, terminates the process, or
 * logs. Every function works only on caller-supplied bytes and caller-owned
 * output memory, retains nothing after return, and keeps no shared mutable
 * state, so distinct callers may use the ABI concurrently.
 *
 * Memory contract for every function: each supplied extent must be valid for
 * the declared length or object type, input and output extents must not
 * overlap, and ownership of all supplied memory stays with the caller.
 */
#ifndef DPA_PCIE_H
#define DPA_PCIE_H

#include <stddef.h>
#include <stdint.h>

#if defined(__GNUC__) || defined(__clang__)
#define DPA_PCIE_API __attribute__((visibility("default")))
#else
#define DPA_PCIE_API
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* ABI version reported by dpa_pcie_abi_version(). */
#define DPA_PCIE_ABI_VERSION 1u

/* Maximum accepted text length, in bytes, for width/speed/NUMA/AER input. */
#define DPA_PCIE_MAX_TEXT 16384u

/* Exact accepted length of a BDF string ("dddd:bb:dd.f"). */
#define DPA_PCIE_BDF_LEN 12u

/* Largest buffer capacity accepted by dpa_pcie_read_fd (DPA_PCIE_MAX_TEXT + 1). */
#define DPA_PCIE_READ_MAX_CAPACITY 16385u

/* Size of dpa_pcie_aer_entry.name, including the terminating NUL. */
#define DPA_PCIE_AER_NAME_SIZE 64u

/* Maximum number of AER counters in one dpa_pcie_aer result. */
#define DPA_PCIE_AER_MAX_ENTRIES 64u

/* Status values. The integer values are part of the ABI. */
typedef enum dpa_pcie_status {
	DPA_PCIE_OK = 0,
	DPA_PCIE_INVALID = 1,
	DPA_PCIE_RANGE = 2,
	DPA_PCIE_NODATA = 3,
	DPA_PCIE_TOO_LARGE = 4,
	DPA_PCIE_IO = 5
} dpa_pcie_status;

/* PCI domain:bus:device.function address. */
typedef struct dpa_pcie_bdf {
	uint16_t domain;
	uint8_t bus;
	uint8_t device;
	uint8_t function;
} dpa_pcie_bdf;

/* One AER counter: NUL-terminated label and its count. */
typedef struct dpa_pcie_aer_entry {
	char name[DPA_PCIE_AER_NAME_SIZE];
	uint64_t count;
} dpa_pcie_aer_entry;

/* Parsed AER counter file: `count` valid entries in input order. */
typedef struct dpa_pcie_aer {
	uint32_t count;
	dpa_pcie_aer_entry entries[DPA_PCIE_AER_MAX_ENTRIES];
} dpa_pcie_aer;

/* Returns DPA_PCIE_ABI_VERSION of the linked library. */
DPA_PCIE_API uint32_t dpa_pcie_abi_version(void);

/*
 * Parses exactly 12 bytes in "dddd:bb:dd.f" hexadecimal form. Returns
 * DPA_PCIE_INVALID for any other length or syntax, and DPA_PCIE_RANGE when
 * device > 31 or function > 7. On any failure *out is zeroed.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_bdf(const char *data, size_t len,
						dpa_pcie_bdf *out);

/*
 * Parses a sysfs link-width value (1, 2, 4, 8, 12, 16 or 32) after trimming
 * leading/trailing space, tab, CR and LF. Empty, "Unknown" and zero yield
 * DPA_PCIE_NODATA; other valid decimals yield DPA_PCIE_RANGE.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_width(const char *data, size_t len,
						  uint32_t *out);

/*
 * Parses a sysfs link-speed value such as "16.0 GT/s" or "2.5 GT/s PCIe" and
 * stores the rate in milli-GT/s (16000, 2500). No generation is inferred.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_speed(const char *data, size_t len,
						  uint32_t *out);

/*
 * Parses a sysfs numa_node value. Empty, "Unknown" and "-1" yield
 * DPA_PCIE_NODATA; other negative values or values above INT32_MAX yield
 * DPA_PCIE_RANGE.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_numa(const char *data, size_t len,
						 int32_t *out);

/*
 * Parses an AER counter file ("<label> <count>" per LF or CRLF line) into
 * *out, preserving label spelling and input order. Blank lines are ignored.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_aer(const char *data, size_t len,
						dpa_pcie_aer *out);

/*
 * Reads the content of `fd` from offset zero with pread(2), without changing
 * the caller's descriptor offset, into `buffer` (capacity 1..16385 bytes).
 * On DPA_PCIE_OK the content (at most capacity-1 bytes) is NUL-terminated,
 * *out_len is its length and *out_errno is 0. When at least `capacity` bytes
 * are available the call returns DPA_PCIE_TOO_LARGE; a failed read returns
 * DPA_PCIE_IO with the OS errno in *out_errno. EINTR is retried. On any
 * failure *out_len and *out_errno are set (errno only for DPA_PCIE_IO is
 * nonzero) and buffer[0] is NUL. The descriptor is neither closed nor kept.
 */
DPA_PCIE_API dpa_pcie_status dpa_pcie_read_fd(int fd, char *buffer,
					      size_t capacity, size_t *out_len,
					      int *out_errno);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* DPA_PCIE_H */
