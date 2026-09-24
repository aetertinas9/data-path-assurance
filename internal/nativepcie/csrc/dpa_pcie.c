/*
 * dpa_pcie.c — canonical implementation of libdpa_pcie ABI version 1.
 *
 * This translation unit is the single source for the static archive, the
 * shared library and the sanitizer archive. It has no third-party
 * dependency, no dynamic allocation, no global mutable state, no threads,
 * no signal handling and no logging. All work happens on caller-supplied
 * bytes and caller-owned output memory; nothing is retained after return.
 *
 * Validation order for every parser (NPO-013): null output pointer, then
 * null data with positive length, then the length bound, and only then are
 * input bytes inspected. Any failure leaves the complete output zeroed.
 */
#define _POSIX_C_SOURCE 200809L

#include "dpa_pcie.h"

#include <errno.h>
#include <limits.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

/* Public struct layout is part of the ABI; pin it. */
_Static_assert(sizeof(dpa_pcie_bdf) == 6, "dpa_pcie_bdf size");
_Static_assert(offsetof(dpa_pcie_bdf, domain) == 0, "dpa_pcie_bdf.domain");
_Static_assert(offsetof(dpa_pcie_bdf, bus) == 2, "dpa_pcie_bdf.bus");
_Static_assert(offsetof(dpa_pcie_bdf, device) == 3, "dpa_pcie_bdf.device");
_Static_assert(offsetof(dpa_pcie_bdf, function) == 4, "dpa_pcie_bdf.function");
_Static_assert(sizeof(dpa_pcie_aer_entry) == 72, "dpa_pcie_aer_entry size");
_Static_assert(offsetof(dpa_pcie_aer_entry, name) == 0, "dpa_pcie_aer_entry.name");
_Static_assert(offsetof(dpa_pcie_aer_entry, count) == 64, "dpa_pcie_aer_entry.count");
_Static_assert(sizeof(dpa_pcie_aer) == 8 + 64 * 72, "dpa_pcie_aer size");
_Static_assert(offsetof(dpa_pcie_aer, count) == 0, "dpa_pcie_aer.count");
_Static_assert(offsetof(dpa_pcie_aer, entries) == 8, "dpa_pcie_aer.entries");
_Static_assert(DPA_PCIE_READ_MAX_CAPACITY == DPA_PCIE_MAX_TEXT + 1, "read capacity");
_Static_assert(DPA_PCIE_AER_NAME_SIZE == 64 && DPA_PCIE_AER_MAX_ENTRIES == 64, "aer limits");

/* ------------------------------------------------------------------------- */
/* Small pure helpers                                                        */
/* ------------------------------------------------------------------------- */

static inline int is_outer_ws(char c)
{
	return c == ' ' || c == '\t' || c == '\r' || c == '\n';
}

static inline int is_split_ws(char c)
{
	return c == ' ' || c == '\t';
}

static inline int is_digit(char c)
{
	return c >= '0' && c <= '9';
}

static inline int is_printable(char c)
{
	return c >= 0x20 && c <= 0x7E;
}

/* Removes leading and trailing ASCII space, tab, CR and LF. */
static void trim_outer(const char **data, size_t *len)
{
	const char *p = *data;
	size_t n = *len;

	while (n > 0 && is_outer_ws(p[0])) {
		p++;
		n--;
	}
	while (n > 0 && is_outer_ws(p[n - 1]))
		n--;
	*data = p;
	*len = n;
}

/* True when the span is one or more ASCII decimal digits. */
static int all_digits(const char *p, size_t n)
{
	size_t i;

	if (n == 0)
		return 0;
	for (i = 0; i < n; i++) {
		if (!is_digit(p[i]))
			return 0;
	}
	return 1;
}

/* Accumulates an all-digit span into *value. Returns 0, or 1 on overflow. */
static int parse_u64(const char *p, size_t n, uint64_t *value)
{
	uint64_t v = 0;
	size_t i;

	for (i = 0; i < n; i++) {
		uint64_t d = (uint64_t)((unsigned char)p[i] - '0');

		if (v > (UINT64_MAX - d) / 10u)
			return 1;
		v = v * 10u + d;
	}
	*value = v;
	return 0;
}

static int hex_value(char c)
{
	if (c >= '0' && c <= '9')
		return c - '0';
	if (c >= 'a' && c <= 'f')
		return c - 'a' + 10;
	if (c >= 'A' && c <= 'F')
		return c - 'A' + 10;
	return -1;
}

static int is_unknown(const char *p, size_t n)
{
	static const char unknown[] = "Unknown";

	return n == sizeof(unknown) - 1 && memcmp(p, unknown, n) == 0;
}

/*
 * Shared prologue for the width/speed/NUMA/AER text parsers, applied after
 * the caller has rejected a null output pointer and zeroed its output.
 * Resolves the pointer/length rules and the embedded-NUL rule; on
 * DPA_PCIE_OK the input may be interpreted.
 */
static dpa_pcie_status check_text(const char *data, size_t len)
{
	if (data == NULL && len > 0)
		return DPA_PCIE_INVALID;
	if (len > DPA_PCIE_MAX_TEXT)
		return DPA_PCIE_TOO_LARGE;
	if (len > 0 && memchr(data, '\0', len) != NULL)
		return DPA_PCIE_INVALID;
	return DPA_PCIE_OK;
}

/* ------------------------------------------------------------------------- */
/* ABI                                                                       */
/* ------------------------------------------------------------------------- */

DPA_PCIE_API uint32_t dpa_pcie_abi_version(void)
{
	return DPA_PCIE_ABI_VERSION;
}

/* ------------------------------------------------------------------------- */
/* BDF                                                                       */
/* ------------------------------------------------------------------------- */

DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_bdf(const char *data, size_t len,
						dpa_pcie_bdf *out)
{
	unsigned int domain = 0, bus = 0, device = 0, function = 0;
	size_t i;

	if (out == NULL)
		return DPA_PCIE_INVALID;
	memset(out, 0, sizeof(*out));
	if (data == NULL && len > 0)
		return DPA_PCIE_INVALID;
	if (len != DPA_PCIE_BDF_LEN)
		return DPA_PCIE_INVALID;

	/* Layout: dddd ':' bb ':' dd '.' f */
	if (data[4] != ':' || data[7] != ':' || data[10] != '.')
		return DPA_PCIE_INVALID;
	for (i = 0; i < DPA_PCIE_BDF_LEN; i++) {
		int v;

		if (i == 4 || i == 7 || i == 10)
			continue;
		v = hex_value(data[i]);
		if (v < 0)
			return DPA_PCIE_INVALID;
		if (i < 4)
			domain = (domain << 4) | (unsigned int)v;
		else if (i < 7)
			bus = (bus << 4) | (unsigned int)v;
		else if (i < 10)
			device = (device << 4) | (unsigned int)v;
		else
			function = (unsigned int)v;
	}
	if (device > 31u || function > 7u)
		return DPA_PCIE_RANGE;

	out->domain = (uint16_t)domain;
	out->bus = (uint8_t)bus;
	out->device = (uint8_t)device;
	out->function = (uint8_t)function;
	return DPA_PCIE_OK;
}

/* ------------------------------------------------------------------------- */
/* Width                                                                     */
/* ------------------------------------------------------------------------- */

DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_width(const char *data, size_t len,
						  uint32_t *out)
{
	dpa_pcie_status st;
	uint64_t value;

	if (out == NULL)
		return DPA_PCIE_INVALID;
	*out = 0;
	st = check_text(data, len);
	if (st != DPA_PCIE_OK)
		return st;

	trim_outer(&data, &len);
	if (len == 0 || is_unknown(data, len))
		return DPA_PCIE_NODATA;
	if (!all_digits(data, len))
		return DPA_PCIE_INVALID;
	if (parse_u64(data, len, &value) != 0)
		return DPA_PCIE_RANGE;
	if (value == 0)
		return DPA_PCIE_NODATA;
	switch (value) {
	case 1:
	case 2:
	case 4:
	case 8:
	case 12:
	case 16:
	case 32:
		*out = (uint32_t)value;
		return DPA_PCIE_OK;
	default:
		return DPA_PCIE_RANGE;
	}
}

/* ------------------------------------------------------------------------- */
/* Speed                                                                     */
/* ------------------------------------------------------------------------- */

DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_speed(const char *data, size_t len,
						  uint32_t *out)
{
	static const char unit[] = " GT/s";
	static const char pcie[] = " PCIe";
	const size_t unit_len = sizeof(unit) - 1;
	const size_t pcie_len = sizeof(pcie) - 1;
	dpa_pcie_status st;
	size_t i = 0, int_len, frac_start = 0, frac_len = 0;
	uint64_t int_val, frac_val = 0, milli;

	if (out == NULL)
		return DPA_PCIE_INVALID;
	*out = 0;
	st = check_text(data, len);
	if (st != DPA_PCIE_OK)
		return st;

	trim_outer(&data, &len);
	if (len == 0 || is_unknown(data, len))
		return DPA_PCIE_NODATA;

	/* Complete scalar syntax validation precedes any range classification. */
	while (i < len && is_digit(data[i]))
		i++;
	int_len = i;
	if (int_len == 0)
		return DPA_PCIE_INVALID;
	if (i < len && data[i] == '.') {
		i++;
		frac_start = i;
		while (i < len && is_digit(data[i]))
			i++;
		frac_len = i - frac_start;
		if (frac_len < 1 || frac_len > 3)
			return DPA_PCIE_INVALID;
	}
	if (len - i < unit_len || memcmp(data + i, unit, unit_len) != 0)
		return DPA_PCIE_INVALID;
	i += unit_len;
	if (i < len) {
		if (len - i != pcie_len || memcmp(data + i, pcie, pcie_len) != 0)
			return DPA_PCIE_INVALID;
		i += pcie_len;
	}

	/* Numeric value in milli-GT/s. */
	if (parse_u64(data, int_len, &int_val) != 0)
		return DPA_PCIE_RANGE;
	if (frac_len > 0) {
		size_t k;

		(void)parse_u64(data + frac_start, frac_len, &frac_val);
		for (k = frac_len; k < 3; k++)
			frac_val *= 10u;
	}
	if (int_val > (UINT64_MAX - frac_val) / 1000u)
		return DPA_PCIE_RANGE;
	milli = int_val * 1000u + frac_val;
	if (milli == 0)
		return DPA_PCIE_NODATA;
	if (milli > UINT32_MAX)
		return DPA_PCIE_RANGE;
	*out = (uint32_t)milli;
	return DPA_PCIE_OK;
}

/* ------------------------------------------------------------------------- */
/* NUMA                                                                      */
/* ------------------------------------------------------------------------- */

DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_numa(const char *data, size_t len,
						 int32_t *out)
{
	dpa_pcie_status st;
	uint64_t value;

	if (out == NULL)
		return DPA_PCIE_INVALID;
	*out = 0;
	st = check_text(data, len);
	if (st != DPA_PCIE_OK)
		return st;

	trim_outer(&data, &len);
	if (len == 0 || is_unknown(data, len))
		return DPA_PCIE_NODATA;
	if (len == 2 && data[0] == '-' && data[1] == '1')
		return DPA_PCIE_NODATA;
	if (data[0] == '-') {
		/* Any other minus-digits form (including -0, -01) is out of range. */
		if (!all_digits(data + 1, len - 1))
			return DPA_PCIE_INVALID;
		return DPA_PCIE_RANGE;
	}
	if (!all_digits(data, len))
		return DPA_PCIE_INVALID;
	if (parse_u64(data, len, &value) != 0)
		return DPA_PCIE_RANGE;
	if (value > (uint64_t)INT32_MAX)
		return DPA_PCIE_RANGE;
	*out = (int32_t)value;
	return DPA_PCIE_OK;
}

/* ------------------------------------------------------------------------- */
/* AER                                                                       */
/* ------------------------------------------------------------------------- */

/*
 * Parses one nonblank, outer-trimmed AER line into *out at index out->count.
 * Returns DPA_PCIE_OK and increments out->count on success.
 */
static dpa_pcie_status parse_aer_line(const char *line, size_t n,
				      dpa_pcie_aer *out)
{
	size_t split = n, label_len, count_len, k;
	uint64_t count;
	dpa_pcie_aer_entry *entry;

	/* Final space/tab-delimited token is the count. */
	for (k = n; k > 0; k--) {
		if (is_split_ws(line[k - 1])) {
			split = k - 1;
			break;
		}
	}
	if (split == n)
		return DPA_PCIE_INVALID;
	count_len = n - split - 1;
	label_len = split;
	while (label_len > 0 && is_split_ws(line[label_len - 1]))
		label_len--;
	if (label_len == 0 || count_len == 0)
		return DPA_PCIE_INVALID;
	if (label_len >= DPA_PCIE_AER_NAME_SIZE)
		return DPA_PCIE_TOO_LARGE;
	for (k = 0; k < label_len; k++) {
		if (!is_printable(line[k]))
			return DPA_PCIE_INVALID;
	}
	if (!all_digits(line + split + 1, count_len))
		return DPA_PCIE_INVALID;
	if (parse_u64(line + split + 1, count_len, &count) != 0)
		return DPA_PCIE_RANGE;
	for (k = 0; k < out->count; k++) {
		const char *name = out->entries[k].name;

		if (strlen(name) == label_len && memcmp(name, line, label_len) == 0)
			return DPA_PCIE_INVALID;
	}
	if (out->count >= DPA_PCIE_AER_MAX_ENTRIES)
		return DPA_PCIE_TOO_LARGE;

	entry = &out->entries[out->count];
	memcpy(entry->name, line, label_len);
	entry->name[label_len] = '\0';
	entry->count = count;
	out->count++;
	return DPA_PCIE_OK;
}

DPA_PCIE_API dpa_pcie_status dpa_pcie_parse_aer(const char *data, size_t len,
						dpa_pcie_aer *out)
{
	dpa_pcie_status st;
	size_t start = 0;

	if (out == NULL)
		return DPA_PCIE_INVALID;
	memset(out, 0, sizeof(*out));
	st = check_text(data, len);
	if (st != DPA_PCIE_OK)
		return st;

	while (start < len) {
		const char *line = data + start;
		const char *lf = memchr(line, '\n', len - start);
		size_t n = lf != NULL ? (size_t)(lf - line) : len - start;

		start += n + 1;
		trim_outer(&line, &n);
		if (n == 0)
			continue;
		st = parse_aer_line(line, n, out);
		if (st != DPA_PCIE_OK)
			goto fail;
	}
	if (out->count == 0)
		return DPA_PCIE_NODATA;
	return DPA_PCIE_OK;

fail:
	memset(out, 0, sizeof(*out));
	return st;
}

/* ------------------------------------------------------------------------- */
/* Bounded descriptor reader                                                 */
/* ------------------------------------------------------------------------- */

/* pread(2) with EINTR retry. On failure returns -1 with *saved_errno set. */
static ssize_t pread_retry(int fd, char *dst, size_t count, size_t offset,
			   int *saved_errno)
{
	for (;;) {
		ssize_t n = pread(fd, dst, count, (off_t)offset);

		if (n >= 0)
			return n;
		if (errno == EINTR)
			continue;
		*saved_errno = errno;
		return -1;
	}
}

DPA_PCIE_API dpa_pcie_status dpa_pcie_read_fd(int fd, char *buffer,
					      size_t capacity, size_t *out_len,
					      int *out_errno)
{
	size_t limit, total = 0;
	int saved_errno = 0;

	if (out_len != NULL)
		*out_len = 0;
	if (out_errno != NULL)
		*out_errno = 0;
	if (fd < 0 || buffer == NULL || out_len == NULL || out_errno == NULL ||
	    capacity < 1 || capacity > DPA_PCIE_READ_MAX_CAPACITY) {
		if (buffer != NULL && capacity > 0)
			buffer[0] = '\0';
		return DPA_PCIE_INVALID;
	}

	limit = capacity - 1;
	while (total < limit) {
		ssize_t n = pread_retry(fd, buffer + total, limit - total, total,
					&saved_errno);

		if (n < 0) {
			buffer[0] = '\0';
			*out_errno = saved_errno;
			return DPA_PCIE_IO;
		}
		if (n == 0)
			break;
		total += (size_t)n;
	}
	if (total == limit) {
		/* Probe one more byte to distinguish exact fit from overflow. */
		char probe;
		ssize_t n = pread_retry(fd, &probe, 1, limit, &saved_errno);

		if (n < 0) {
			buffer[0] = '\0';
			*out_errno = saved_errno;
			return DPA_PCIE_IO;
		}
		if (n > 0) {
			buffer[0] = '\0';
			return DPA_PCIE_TOO_LARGE;
		}
	}
	buffer[total] = '\0';
	*out_len = total;
	return DPA_PCIE_OK;
}
