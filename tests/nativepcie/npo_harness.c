/*
 * npo_harness.c — independent C11 acceptance harness for libdpa_pcie
 * (native-pcie-observer spec v1.0, NPO-005..NPO-034, NPO-061, NPO-072).
 *
 * Written from the ratified spec only. The same source is linked three ways
 * by tests/nativepcie/Makefile: against the static archive, against the
 * shared library (rpath NATIVE_DIR), and against the sanitizer archive.
 *
 * Usage: npo_harness [--expect-static | --expect-shared]
 */
#include "dpa_pcie.h"
#include "npo_common.h"

#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

/* ---- compile-time contract: NPO-010, NPO-012, NPO-020, NPO-024 ---------- */

/* NPO-012 exact integer values. */
_Static_assert(DPA_PCIE_OK == 0, "NPO-012: DPA_PCIE_OK must be 0");
_Static_assert(DPA_PCIE_INVALID == 1, "NPO-012: DPA_PCIE_INVALID must be 1");
_Static_assert(DPA_PCIE_RANGE == 2, "NPO-012: DPA_PCIE_RANGE must be 2");
_Static_assert(DPA_PCIE_NODATA == 3, "NPO-012: DPA_PCIE_NODATA must be 3");
_Static_assert(DPA_PCIE_TOO_LARGE == 4, "NPO-012: DPA_PCIE_TOO_LARGE must be 4");
_Static_assert(DPA_PCIE_IO == 5, "NPO-012: DPA_PCIE_IO must be 5");

/* NPO-010 / NPO-020 / NPO-024: typedef names usable without `struct`, and the
 * fixed-width member types. _Generic yields 1 only for the exact type. */
#define NPO_IS_TYPE(expr, T) _Generic((expr), T : 1, default : 0)

_Static_assert(NPO_IS_TYPE(((dpa_pcie_bdf *)0)->domain, uint16_t),
	       "NPO-020: dpa_pcie_bdf.domain must be uint16_t");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_bdf *)0)->bus, uint8_t),
	       "NPO-020: dpa_pcie_bdf.bus must be uint8_t");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_bdf *)0)->device, uint8_t),
	       "NPO-020: dpa_pcie_bdf.device must be uint8_t");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_bdf *)0)->function, uint8_t),
	       "NPO-020: dpa_pcie_bdf.function must be uint8_t");
_Static_assert(sizeof(((dpa_pcie_aer_entry *)0)->name) == 64,
	       "NPO-024: dpa_pcie_aer_entry.name must be char[64]");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_aer_entry *)0)->name[0], char),
	       "NPO-024: dpa_pcie_aer_entry.name must be char[64]");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_aer_entry *)0)->count, uint64_t),
	       "NPO-024: dpa_pcie_aer_entry.count must be uint64_t");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_aer *)0)->count, uint32_t),
	       "NPO-024: dpa_pcie_aer.count must be uint32_t");
_Static_assert(sizeof(((dpa_pcie_aer *)0)->entries) /
			       sizeof(((dpa_pcie_aer *)0)->entries[0]) ==
		       64,
	       "NPO-024: dpa_pcie_aer.entries must have 64 elements");
_Static_assert(NPO_IS_TYPE(((dpa_pcie_aer *)0)->entries[0], dpa_pcie_aer_entry),
	       "NPO-024: dpa_pcie_aer.entries element must be dpa_pcie_aer_entry");
_Static_assert(NPO_IS_TYPE(dpa_pcie_abi_version(), uint32_t),
	       "NPO-011: dpa_pcie_abi_version must return uint32_t");

#define NPO_TEXT_LIMIT 16384u
#define NPO_READ_CAPACITY_MAX 16385u

/* ---- parser expectation helpers ------------------------------------------ */

static void expect_width(const char *clause, const char *text, size_t len,
			 int want, uint32_t want_value)
{
	npo_guard g;
	uint32_t out = 0xFFFFFFFFu;
	int st;

	if (npo_guard_alloc(&g, text, len) != 0) {
		npo_fail(clause, "guard allocation failed");
		return;
	}
	npo_current_clause = clause;
	st = (int)dpa_pcie_parse_width(g.data, len, &out);
	NPO_CHECK(clause, st == want,
		  "parse_width(%.*s len=%zu): got %s want %s",
		  (int)(len > 40 ? 40 : len), text ? text : "", len,
		  npo_status_name(st), npo_status_name(want));
	if (st == DPA_PCIE_OK)
		NPO_CHECK(clause, out == want_value,
			  "parse_width(%.*s): value %" PRIu32 " want %" PRIu32,
			  (int)(len > 40 ? 40 : len), text ? text : "", out,
			  want_value);
	else
		NPO_CHECK(clause, out == 0,
			  "parse_width(%.*s): output not zeroed on %s (0x%" PRIx32 ")",
			  (int)(len > 40 ? 40 : len), text ? text : "",
			  npo_status_name(st), out);
	npo_guard_free(&g);
}

static void expect_speed(const char *clause, const char *text, size_t len,
			 int want, uint32_t want_value)
{
	npo_guard g;
	uint32_t out = 0xFFFFFFFFu;
	int st;

	if (npo_guard_alloc(&g, text, len) != 0) {
		npo_fail(clause, "guard allocation failed");
		return;
	}
	npo_current_clause = clause;
	st = (int)dpa_pcie_parse_speed(g.data, len, &out);
	NPO_CHECK(clause, st == want,
		  "parse_speed(%.*s len=%zu): got %s want %s",
		  (int)(len > 40 ? 40 : len), text ? text : "", len,
		  npo_status_name(st), npo_status_name(want));
	if (st == DPA_PCIE_OK)
		NPO_CHECK(clause, out == want_value,
			  "parse_speed(%.*s): value %" PRIu32 " want %" PRIu32,
			  (int)(len > 40 ? 40 : len), text ? text : "", out,
			  want_value);
	else
		NPO_CHECK(clause, out == 0,
			  "parse_speed(%.*s): output not zeroed on %s (0x%" PRIx32 ")",
			  (int)(len > 40 ? 40 : len), text ? text : "",
			  npo_status_name(st), out);
	npo_guard_free(&g);
}

static void expect_numa(const char *clause, const char *text, size_t len,
			int want, int32_t want_value)
{
	npo_guard g;
	int32_t out = -0x7FFFFFFF;
	int st;

	if (npo_guard_alloc(&g, text, len) != 0) {
		npo_fail(clause, "guard allocation failed");
		return;
	}
	npo_current_clause = clause;
	st = (int)dpa_pcie_parse_numa(g.data, len, &out);
	NPO_CHECK(clause, st == want,
		  "parse_numa(%.*s len=%zu): got %s want %s",
		  (int)(len > 40 ? 40 : len), text ? text : "", len,
		  npo_status_name(st), npo_status_name(want));
	if (st == DPA_PCIE_OK)
		NPO_CHECK(clause, out == want_value,
			  "parse_numa(%.*s): value %" PRId32 " want %" PRId32,
			  (int)(len > 40 ? 40 : len), text ? text : "", out,
			  want_value);
	else
		NPO_CHECK(clause, out == 0,
			  "parse_numa(%.*s): output not zeroed on %s (%" PRId32 ")",
			  (int)(len > 40 ? 40 : len), text ? text : "",
			  npo_status_name(st), out);
	npo_guard_free(&g);
}

static void expect_bdf(const char *clause, const char *text, size_t len,
		       int want, uint16_t domain, uint8_t bus, uint8_t device,
		       uint8_t function)
{
	npo_guard g;
	dpa_pcie_bdf out;
	int st;

	memset(&out, 0xFF, sizeof(out));
	if (npo_guard_alloc(&g, text, len) != 0) {
		npo_fail(clause, "guard allocation failed");
		return;
	}
	npo_current_clause = clause;
	st = (int)dpa_pcie_parse_bdf(g.data, len, &out);
	NPO_CHECK(clause, st == want, "parse_bdf(%.*s len=%zu): got %s want %s",
		  (int)(len > 40 ? 40 : len), text ? text : "", len,
		  npo_status_name(st), npo_status_name(want));
	if (st == DPA_PCIE_OK) {
		NPO_CHECK(clause,
			  out.domain == domain && out.bus == bus &&
				  out.device == device && out.function == function,
			  "parse_bdf(%.*s): got %04x:%02x:%02x.%x want %04x:%02x:%02x.%x",
			  (int)len, text, out.domain, out.bus, out.device,
			  out.function, domain, bus, device, function);
	} else {
		NPO_CHECK(clause, npo_is_zero(&out, sizeof(out)),
			  "parse_bdf(%.*s): output object not fully zeroed on %s",
			  (int)(len > 40 ? 40 : len), text ? text : "",
			  npo_status_name(st));
	}
	npo_guard_free(&g);
}

static void expect_aer(const char *clause, const char *text, size_t len,
		       int want, const char *const *names,
		       const uint64_t *counts, uint32_t n)
{
	npo_guard g;
	dpa_pcie_aer *out = malloc(sizeof(*out));
	int st;
	uint32_t i;

	if (out == NULL) {
		npo_fail(clause, "malloc failed");
		return;
	}
	memset(out, 0xFF, sizeof(*out));
	if (npo_guard_alloc(&g, text, len) != 0) {
		npo_fail(clause, "guard allocation failed");
		free(out);
		return;
	}
	npo_current_clause = clause;
	st = (int)dpa_pcie_parse_aer(g.data, len, out);
	NPO_CHECK(clause, st == want, "parse_aer(%.*s len=%zu): got %s want %s",
		  (int)(len > 48 ? 48 : len), text ? text : "", len,
		  npo_status_name(st), npo_status_name(want));
	if (st == DPA_PCIE_OK) {
		NPO_CHECK(clause, out->count == n,
			  "parse_aer(%.*s): count %" PRIu32 " want %" PRIu32,
			  (int)(len > 48 ? 48 : len), text, out->count, n);
		for (i = 0; i < n && i < out->count && i < 64; i++) {
			const char *name = out->entries[i].name;
			int terminated = memchr(name, 0, 64) != NULL;

			NPO_CHECK(clause, terminated,
				  "parse_aer entry %" PRIu32 ": name not NUL-terminated within 64 bytes",
				  i);
			NPO_CHECK(clause,
				  terminated && strcmp(name, names[i]) == 0,
				  "parse_aer entry %" PRIu32 ": name %.64s want %s",
				  i, name, names[i]);
			NPO_CHECK(clause, out->entries[i].count == counts[i],
				  "parse_aer entry %" PRIu32 " (%s): count %" PRIu64
				  " want %" PRIu64,
				  i, names[i], out->entries[i].count, counts[i]);
		}
	} else {
		NPO_CHECK(clause, npo_is_zero(out, sizeof(*out)),
			  "parse_aer(%.*s): entire result not zeroed on %s",
			  (int)(len > 48 ? 48 : len), text ? text : "",
			  npo_status_name(st));
	}
	npo_guard_free(&g);
	free(out);
}

/* ---- NPO-011: ABI version ------------------------------------------------ */

static void test_npo_011(void)
{
	uint32_t v = dpa_pcie_abi_version();

	NPO_CHECK("NPO-011", v == 1u, "dpa_pcie_abi_version() = %" PRIu32 " want 1",
		  v);
}

/* ---- NPO-012: status values at runtime (in addition to static asserts) --- */

static void test_npo_012(void)
{
	NPO_CHECK("NPO-012", (int)DPA_PCIE_OK == 0, "DPA_PCIE_OK != 0");
	NPO_CHECK("NPO-012", (int)DPA_PCIE_INVALID == 1, "DPA_PCIE_INVALID != 1");
	NPO_CHECK("NPO-012", (int)DPA_PCIE_RANGE == 2, "DPA_PCIE_RANGE != 2");
	NPO_CHECK("NPO-012", (int)DPA_PCIE_NODATA == 3, "DPA_PCIE_NODATA != 3");
	NPO_CHECK("NPO-012", (int)DPA_PCIE_TOO_LARGE == 4, "DPA_PCIE_TOO_LARGE != 4");
	NPO_CHECK("NPO-012", (int)DPA_PCIE_IO == 5, "DPA_PCIE_IO != 5");
}

/* ---- NPO-013: pointer / output rules and validation precedence ----------- */

static void test_npo_013(void)
{
	const char *c = "NPO-013";
	npo_guard g;
	uint32_t u32;
	int32_t i32;
	dpa_pcie_bdf bdf;
	dpa_pcie_aer *aer = malloc(sizeof(*aer));
	char *big;
	int st;

	if (aer == NULL) {
		npo_fail(c, "malloc failed");
		return;
	}
	npo_current_clause = c;

	/* Null output pointer -> INVALID, for every parser, with valid data. */
	st = (int)dpa_pcie_parse_width(NPO_LIT("16"), NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_width(out=NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_speed(NPO_LIT("2.5 GT/s"), NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_speed(out=NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_numa(NPO_LIT("0"), NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_numa(out=NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_bdf(NPO_LIT("0000:00:00.0"), NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_bdf(out=NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_aer(NPO_LIT("RxErr 1\n"), NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_aer(out=NULL): %s", npo_status_name(st));

	/* Null output has precedence over everything else (NULL data, len>0;
	 * oversized len; BDF wrong length). */
	st = (int)dpa_pcie_parse_width(NULL, 5, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_width(NULL,5,NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_aer(NULL, NPO_TEXT_LIMIT + 1, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_aer(NULL,16385,NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_bdf(NULL, 13, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_bdf(NULL,13,NULL): %s", npo_status_name(st));
	st = (int)dpa_pcie_parse_width(NULL, 0, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_width(NULL,0,NULL): %s", npo_status_name(st));

	/* data == NULL with len > 0 -> INVALID regardless of len (even when the
	 * len alone would be TOO_LARGE), and output zeroed. */
	u32 = 0xFFFFFFFFu;
	st = (int)dpa_pcie_parse_width(NULL, 1, &u32);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && u32 == 0, "parse_width(NULL,1): %s out=%" PRIu32,
		  npo_status_name(st), u32);
	u32 = 0xFFFFFFFFu;
	st = (int)dpa_pcie_parse_speed(NULL, NPO_TEXT_LIMIT + 1, &u32);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && u32 == 0,
		  "parse_speed(NULL,16385): %s (must be INVALID, not TOO_LARGE) out=%" PRIu32,
		  npo_status_name(st), u32);
	i32 = -1;
	st = (int)dpa_pcie_parse_numa(NULL, NPO_TEXT_LIMIT, &i32);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && i32 == 0, "parse_numa(NULL,16384): %s out=%" PRId32,
		  npo_status_name(st), i32);
	memset(aer, 0xFF, sizeof(*aer));
	st = (int)dpa_pcie_parse_aer(NULL, 100000, aer);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "parse_aer(NULL,100000): %s (must be INVALID)",
		  npo_status_name(st));
	NPO_CHECK(c, npo_is_zero(aer, sizeof(*aer)), "parse_aer(NULL,100000): result not zeroed");
	memset(&bdf, 0xFF, sizeof(bdf));
	st = (int)dpa_pcie_parse_bdf(NULL, 12, &bdf);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && npo_is_zero(&bdf, sizeof(bdf)),
		  "parse_bdf(NULL,12): %s", npo_status_name(st));

	/* data == NULL, len == 0: same as empty text for width/speed/numa/aer
	 * (NODATA); BDF parser -> INVALID. */
	u32 = 0xFFFFFFFFu;
	st = (int)dpa_pcie_parse_width(NULL, 0, &u32);
	NPO_CHECK(c, st == DPA_PCIE_NODATA && u32 == 0, "parse_width(NULL,0): %s out=%" PRIu32,
		  npo_status_name(st), u32);
	u32 = 0xFFFFFFFFu;
	st = (int)dpa_pcie_parse_speed(NULL, 0, &u32);
	NPO_CHECK(c, st == DPA_PCIE_NODATA && u32 == 0, "parse_speed(NULL,0): %s out=%" PRIu32,
		  npo_status_name(st), u32);
	i32 = -1;
	st = (int)dpa_pcie_parse_numa(NULL, 0, &i32);
	NPO_CHECK(c, st == DPA_PCIE_NODATA && i32 == 0, "parse_numa(NULL,0): %s out=%" PRId32,
		  npo_status_name(st), i32);
	memset(aer, 0xFF, sizeof(*aer));
	st = (int)dpa_pcie_parse_aer(NULL, 0, aer);
	NPO_CHECK(c, st == DPA_PCIE_NODATA && npo_is_zero(aer, sizeof(*aer)),
		  "parse_aer(NULL,0): %s", npo_status_name(st));
	memset(&bdf, 0xFF, sizeof(bdf));
	st = (int)dpa_pcie_parse_bdf(NULL, 0, &bdf);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && npo_is_zero(&bdf, sizeof(bdf)),
		  "parse_bdf(NULL,0): %s (BDF parser must return INVALID)", npo_status_name(st));

	/* len == 0 with a non-null pointer to an inaccessible page: the parser
	 * must not read through data (a read faults -> harness exits 1). */
	if (npo_guard_alloc(&g, NULL, 0) == 0) {
		u32 = 0xFFFFFFFFu;
		st = (int)dpa_pcie_parse_width(g.data, 0, &u32);
		NPO_CHECK(c, st == DPA_PCIE_NODATA && u32 == 0, "parse_width(guard,0): %s",
			  npo_status_name(st));
		u32 = 0xFFFFFFFFu;
		st = (int)dpa_pcie_parse_speed(g.data, 0, &u32);
		NPO_CHECK(c, st == DPA_PCIE_NODATA && u32 == 0, "parse_speed(guard,0): %s",
			  npo_status_name(st));
		i32 = -1;
		st = (int)dpa_pcie_parse_numa(g.data, 0, &i32);
		NPO_CHECK(c, st == DPA_PCIE_NODATA && i32 == 0, "parse_numa(guard,0): %s",
			  npo_status_name(st));
		memset(aer, 0xFF, sizeof(*aer));
		st = (int)dpa_pcie_parse_aer(g.data, 0, aer);
		NPO_CHECK(c, st == DPA_PCIE_NODATA && npo_is_zero(aer, sizeof(*aer)),
			  "parse_aer(guard,0): %s", npo_status_name(st));
		memset(&bdf, 0xFF, sizeof(bdf));
		st = (int)dpa_pcie_parse_bdf(g.data, 0, &bdf);
		NPO_CHECK(c, st == DPA_PCIE_INVALID && npo_is_zero(&bdf, sizeof(bdf)),
			  "parse_bdf(guard,0): %s", npo_status_name(st));
		npo_guard_free(&g);
	} else {
		npo_fail(c, "guard allocation failed");
	}

	/* Embedded NUL within the explicit length -> INVALID for every text parser. */
	expect_width(c, NPO_LIT("16\0"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("\0"), DPA_PCIE_INVALID, 0);
	/* String concatenation keeps the NUL from absorbing the following digit
	 * as an octal escape: {'1', 0x00, '6'}. */
	expect_width(c, NPO_LIT("1\0" "6"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe\0"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("\0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("0\0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-1\0"), DPA_PCIE_INVALID, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.\0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	/* Exactly 12 bytes with a NUL in place of the first digit, so only the
	 * embedded-NUL rule (not the length rule) can reject it. */
	expect_bdf(c, NPO_LIT("\0" "000:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:0\0" ".0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_aer(c, NPO_LIT("RxErr 1\n\0"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("Rx\0Err 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\0\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* The length bound is resolved before any input byte is inspected: a
	 * 16,385-byte buffer whose readable part is only the last page must not
	 * be touched at all. We give an inaccessible page for the whole extent. */
	if (npo_guard_alloc(&g, NULL, 0) == 0) {
		/* g.data is the guard page: any byte read faults. */
		u32 = 0xFFFFFFFFu;
		st = (int)dpa_pcie_parse_width(g.data, NPO_TEXT_LIMIT + 1, &u32);
		NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && u32 == 0,
			  "parse_width(guard,16385): %s (must be TOO_LARGE without reading)",
			  npo_status_name(st));
		memset(aer, 0xFF, sizeof(*aer));
		st = (int)dpa_pcie_parse_aer(g.data, 1000000, aer);
		NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && npo_is_zero(aer, sizeof(*aer)),
			  "parse_aer(guard,1000000): %s", npo_status_name(st));
		memset(&bdf, 0xFF, sizeof(bdf));
		st = (int)dpa_pcie_parse_bdf(g.data, 13, &bdf);
		NPO_CHECK(c, st == DPA_PCIE_INVALID && npo_is_zero(&bdf, sizeof(bdf)),
			  "parse_bdf(guard,13): %s (wrong length must be INVALID without reading)",
			  npo_status_name(st));
		memset(&bdf, 0xFF, sizeof(bdf));
		st = (int)dpa_pcie_parse_bdf(g.data, NPO_TEXT_LIMIT + 1, &bdf);
		NPO_CHECK(c, st == DPA_PCIE_INVALID && npo_is_zero(&bdf, sizeof(bdf)),
			  "parse_bdf(guard,16385): %s (must be INVALID, not TOO_LARGE)",
			  npo_status_name(st));
		npo_guard_free(&g);
	}

	/* Oversized but fully readable garbage: still TOO_LARGE. */
	big = malloc(NPO_TEXT_LIMIT + 1);
	if (big != NULL) {
		memset(big, '?', NPO_TEXT_LIMIT + 1);
		expect_width(c, big, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);
		expect_speed(c, big, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);
		expect_numa(c, big, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);
		expect_aer(c, big, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);
		expect_bdf(c, big, NPO_TEXT_LIMIT + 1, DPA_PCIE_INVALID, 0, 0, 0, 0);
		free(big);
	}
	free(aer);
}

/* ---- NPO-014: general text limits ---------------------------------------- */

static void test_npo_014(void)
{
	const char *c = "NPO-014";
	char *buf = malloc(NPO_TEXT_LIMIT + 2);
	static const char *const one_name[] = { "RxErr" };
	static const uint64_t one_count[] = { 1 };

	if (buf == NULL) {
		npo_fail(c, "malloc failed");
		return;
	}

	/* Exactly 16,384 bytes is accepted: 16,383 spaces + "8". */
	memset(buf, ' ', NPO_TEXT_LIMIT);
	buf[NPO_TEXT_LIMIT - 1] = '8';
	expect_width(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_OK, 8);
	/* 16,385 bytes with the same valid content -> TOO_LARGE. */
	memset(buf, ' ', NPO_TEXT_LIMIT + 1);
	buf[NPO_TEXT_LIMIT] = '8';
	expect_width(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);

	/* Speed: 16,384 bytes with valid content, then +1. */
	memset(buf, ' ', NPO_TEXT_LIMIT + 1);
	memcpy(buf + NPO_TEXT_LIMIT - 8, "2.5 GT/s", 8);
	expect_speed(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_OK, 2500);
	memset(buf, ' ', NPO_TEXT_LIMIT + 1);
	memcpy(buf + NPO_TEXT_LIMIT + 1 - 8, "2.5 GT/s", 8);
	expect_speed(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);

	/* NUMA: same shape. */
	memset(buf, ' ', NPO_TEXT_LIMIT + 1);
	buf[NPO_TEXT_LIMIT - 1] = '3';
	expect_numa(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_OK, 3);
	memset(buf, ' ', NPO_TEXT_LIMIT + 1);
	buf[NPO_TEXT_LIMIT] = '3';
	expect_numa(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, 0);

	/* AER: one record padded with whitespace-only lines to exactly 16,384. */
	memcpy(buf, "RxErr 1\n", 8);
	memset(buf + 8, ' ', NPO_TEXT_LIMIT + 1 - 8);
	expect_aer(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_OK, one_name, one_count, 1);
	expect_aer(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);

	/* BDF: any length other than 12 is INVALID, never TOO_LARGE. */
	expect_bdf(c, NPO_LIT("0000:00:00."), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.0 "), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.00"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT(""), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	memset(buf, '0', NPO_TEXT_LIMIT + 1);
	expect_bdf(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_INVALID, 0, 0, 0, 0);

	free(buf);
}

/* ---- NPO-020: BDF parsing ------------------------------------------------ */

static void test_npo_020(void)
{
	const char *c = "NPO-020";

	/* Success, both cases, boundaries of each component. */
	expect_bdf(c, NPO_LIT("0000:00:00.0"), DPA_PCIE_OK, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("ffff:ff:1f.7"), DPA_PCIE_OK, 0xFFFF, 0xFF, 31, 7);
	expect_bdf(c, NPO_LIT("FFFF:FF:1F.7"), DPA_PCIE_OK, 0xFFFF, 0xFF, 31, 7);
	expect_bdf(c, NPO_LIT("aBcD:eF:1a.5"), DPA_PCIE_OK, 0xABCD, 0xEF, 26, 5);
	expect_bdf(c, NPO_LIT("0001:02:03.4"), DPA_PCIE_OK, 1, 2, 3, 4);
	expect_bdf(c, NPO_LIT("0000:00:1F.7"), DPA_PCIE_OK, 0, 0, 31, 7);
	expect_bdf(c, NPO_LIT("0000:00:0f.7"), DPA_PCIE_OK, 0, 0, 15, 7);
	expect_bdf(c, NPO_LIT("0000:00:10.0"), DPA_PCIE_OK, 0, 0, 16, 0);
	expect_bdf(c, NPO_LIT("00A0:0a:00.0"), DPA_PCIE_OK, 0xA0, 0x0A, 0, 0);

	/* Range: device > 31 or function > 7 with valid hexadecimal syntax. */
	expect_bdf(c, NPO_LIT("0000:00:20.0"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:1f.8"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:ff.f"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:FF.F"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:20.8"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("ffff:ff:20.7"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.8"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.9"), DPA_PCIE_RANGE, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.a"), DPA_PCIE_RANGE, 0, 0, 0, 0);

	/* Syntax defects: separators, non-hex, whitespace, wrong positions. */
	expect_bdf(c, NPO_LIT("0000-00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00-00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00:0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000.00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00.00:0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.g"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("000g:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:0G:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT(" 000:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.0\n"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:0.0\n"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00. "), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00\t0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT(":000:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("00000:0:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:000:0.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.+"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.-"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0x00:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000:00:00.0"), DPA_PCIE_OK, 0, 0, 0, 0);
	/* Syntax defect and out-of-range component together: the RANGE rule
	 * requires syntactically hexadecimal text, so INVALID wins. */
	expect_bdf(c, NPO_LIT("0000:00:2g.9"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("0000-00:20.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	/* Non-ASCII byte in a 12-byte extent. */
	expect_bdf(c, NPO_LIT("0000:00:00.\xC3"), DPA_PCIE_INVALID, 0, 0, 0, 0);
	expect_bdf(c, NPO_LIT("000\xC3\xA9:00:00.0"), DPA_PCIE_INVALID, 0, 0, 0, 0);
}

/* ---- NPO-021: width parsing ---------------------------------------------- */

static void test_npo_021(void)
{
	const char *c = "NPO-021";
	static const uint32_t accepted[] = { 1, 2, 4, 8, 12, 16, 32 };
	static const uint32_t rejected[] = { 3, 5, 6, 7, 9, 10, 11, 13, 15, 17,
					     24, 31, 33, 64, 128, 256, 4096 };
	char text[64];
	size_t i;
	size_t n;

	for (i = 0; i < sizeof(accepted) / sizeof(accepted[0]); i++) {
		n = (size_t)snprintf(text, sizeof(text), "%" PRIu32, accepted[i]);
		expect_width(c, text, n, DPA_PCIE_OK, accepted[i]);
		n = (size_t)snprintf(text, sizeof(text), "%" PRIu32 "\n", accepted[i]);
		expect_width(c, text, n, DPA_PCIE_OK, accepted[i]);
	}
	for (i = 0; i < sizeof(rejected) / sizeof(rejected[0]); i++) {
		n = (size_t)snprintf(text, sizeof(text), "%" PRIu32, rejected[i]);
		expect_width(c, text, n, DPA_PCIE_RANGE, 0);
	}

	/* Trimming: only space, tab, CR, LF at both ends. */
	expect_width(c, NPO_LIT(" 16"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("16 "), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("\t16\t"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("\r\n16\r\n"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT(" \t\r\n16 \t\r\n"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("16\n\n"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("\v16"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16\f"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16\xC2\xA0"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("\xEF\xBC\x91\xEF\xBC\x96"), DPA_PCIE_INVALID, 0);

	/* NODATA: empty, whitespace-only, exactly "Unknown", numeric zero. */
	expect_width(c, NPO_LIT(""), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT(" "), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("\n"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT(" \t\r\n"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("Unknown"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("Unknown\n"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT(" Unknown "), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("0"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("00"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("000000000000000000000000"), DPA_PCIE_NODATA, 0);
	expect_width(c, NPO_LIT("0\n"), DPA_PCIE_NODATA, 0);

	/* Unknown must match exactly. */
	expect_width(c, NPO_LIT("unknown"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("UNKNOWN"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("UnknownX"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("Unknow"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("Unknown Unknown"), DPA_PCIE_INVALID, 0);

	/* Leading zeros classify by numeric value (UQ-04). */
	expect_width(c, NPO_LIT("016"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("0016\n"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("0000000000000000000000000000016"), DPA_PCIE_OK, 16);
	expect_width(c, NPO_LIT("01"), DPA_PCIE_OK, 1);
	expect_width(c, NPO_LIT("03"), DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("0000000000000000000000003"), DPA_PCIE_RANGE, 0);

	/* Range: valid decimal outside the set, including uint64 overflow. */
	expect_width(c, NPO_LIT("18446744073709551615"), DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("18446744073709551616"), DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("0018446744073709551616"), DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("4294967296"), DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("99999999999999999999999999999999999999999"),
		     DPA_PCIE_RANGE, 0);
	expect_width(c, NPO_LIT("160"), DPA_PCIE_RANGE, 0);

	/* Invalid syntax: sign, hex, internal whitespace, non-decimal. */
	expect_width(c, NPO_LIT("+16"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("-16"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("-0"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("+0"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("0x10"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("0X10"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("1 6"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("1\t6"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16 16"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16x"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("x16"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("1.6"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16."), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("1e1"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("1,6"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("sixteen"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("x"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("-"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("+"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("16GT/s"), DPA_PCIE_INVALID, 0);
	/* A sign in front of an oversized number is still INVALID, not RANGE. */
	expect_width(c, NPO_LIT("+18446744073709551616"), DPA_PCIE_INVALID, 0);
	expect_width(c, NPO_LIT("-18446744073709551616"), DPA_PCIE_INVALID, 0);
	/* Malformed trailing bytes after a huge number: INVALID, not RANGE. */
	expect_width(c, NPO_LIT("18446744073709551616x"), DPA_PCIE_INVALID, 0);
}

/* ---- NPO-022: speed parsing ---------------------------------------------- */

static void test_npo_022(void)
{
	const char *c = "NPO-022";

	/* Spec examples. */
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("16.0 GT/s"), DPA_PCIE_OK, 16000);

	/* Grammar: integer with optional 1..3 fractional digits, " GT/s",
	 * optional " PCIe". */
	expect_speed(c, NPO_LIT("8 GT/s"), DPA_PCIE_OK, 8000);
	expect_speed(c, NPO_LIT("8 GT/s PCIe"), DPA_PCIE_OK, 8000);
	expect_speed(c, NPO_LIT("5.0 GT/s PCIe"), DPA_PCIE_OK, 5000);
	expect_speed(c, NPO_LIT("32.0 GT/s PCIe"), DPA_PCIE_OK, 32000);
	expect_speed(c, NPO_LIT("64.0 GT/s PCIe"), DPA_PCIE_OK, 64000);
	expect_speed(c, NPO_LIT("128.0 GT/s PCIe"), DPA_PCIE_OK, 128000);
	expect_speed(c, NPO_LIT("1 GT/s"), DPA_PCIE_OK, 1000);
	expect_speed(c, NPO_LIT("0.1 GT/s"), DPA_PCIE_OK, 100);
	expect_speed(c, NPO_LIT("0.12 GT/s"), DPA_PCIE_OK, 120);
	expect_speed(c, NPO_LIT("0.123 GT/s"), DPA_PCIE_OK, 123);
	expect_speed(c, NPO_LIT("0.001 GT/s"), DPA_PCIE_OK, 1);
	expect_speed(c, NPO_LIT("0.010 GT/s"), DPA_PCIE_OK, 10);
	expect_speed(c, NPO_LIT("2.05 GT/s"), DPA_PCIE_OK, 2050);
	expect_speed(c, NPO_LIT("2.005 GT/s"), DPA_PCIE_OK, 2005);
	expect_speed(c, NPO_LIT("2.50 GT/s"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("2.500 GT/s"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("12.345 GT/s PCIe"), DPA_PCIE_OK, 12345);
	/* Previously unknown rates pass through as raw milli-GT/s. */
	expect_speed(c, NPO_LIT("7.7 GT/s"), DPA_PCIE_OK, 7700);
	expect_speed(c, NPO_LIT("256.0 GT/s PCIe"), DPA_PCIE_OK, 256000);

	/* Trimming: only space, tab, CR, LF at the ends. */
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe\n"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe\r\n"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("\t2.5 GT/s"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("  2.5 GT/s PCIe  "), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe\t"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("2.5 GT/s "), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("\v2.5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s\f"), DPA_PCIE_INVALID, 0);

	/* Leading zeros classify by numeric value. */
	expect_speed(c, NPO_LIT("02.5 GT/s"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("0002.5 GT/s PCIe"), DPA_PCIE_OK, 2500);
	expect_speed(c, NPO_LIT("016.0 GT/s"), DPA_PCIE_OK, 16000);
	expect_speed(c, NPO_LIT("004294967.295 GT/s"), DPA_PCIE_OK, 4294967295u);

	/* NODATA: empty, exactly Unknown, numeric zero in valid syntax. */
	expect_speed(c, NPO_LIT(""), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("   \n"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("Unknown"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT(" Unknown\n"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("0 GT/s"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("0 GT/s PCIe"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("0.0 GT/s"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("0.000 GT/s PCIe"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("00.00 GT/s"), DPA_PCIE_NODATA, 0);
	expect_speed(c, NPO_LIT("000 GT/s\n"), DPA_PCIE_NODATA, 0);

	/* Unknown must be exact. */
	expect_speed(c, NPO_LIT("unknown"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("Unknown GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("Unknown PCIe"), DPA_PCIE_INVALID, 0);

	/* Range: valid syntax, milli-GT/s exceeds UINT32_MAX (4294967295). */
	expect_speed(c, NPO_LIT("4294967.295 GT/s"), DPA_PCIE_OK, 4294967295u);
	expect_speed(c, NPO_LIT("4294967.295 GT/s PCIe"), DPA_PCIE_OK, 4294967295u);
	expect_speed(c, NPO_LIT("4294967.296 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("4294967.3 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("4294968 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("4294968.0 GT/s PCIe"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("4294967296 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("18446744073709551616 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999 GT/s"), DPA_PCIE_RANGE, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999.999 GT/s PCIe"),
		     DPA_PCIE_RANGE, 0);

	/* Syntax validation precedes range classification. */
	expect_speed(c, NPO_LIT("99999999999999999999999999999 GT/sX"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999 GT/s PCIe!"),
		     DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999.9999 GT/s"),
		     DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999. GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("99999999999999999999999999999"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("+99999999999999999999999999999 GT/s"), DPA_PCIE_INVALID, 0);

	/* Invalid: missing integer digit, sign, exponent, >3 fraction digits,
	 * bare point, wrong suffix/case, extra/internal whitespace. */
	expect_speed(c, NPO_LIT(".5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2. GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5000 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("0.0001 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5.0 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("+2.5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("-2.5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5e0 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5E0 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("1e3 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2,5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5  GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5\tGT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s  PCIe"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s\tPCIe"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/sPCIe"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe PCIe"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 gt/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/S"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 Gt/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s pcie"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIE"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCI"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe2"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GB/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 MT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("0"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("0.0"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT(" GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("PCIe"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("Gen4"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s PCIe\xC2\xA0"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("\xEF\xBC\x92.5 GT/s"), DPA_PCIE_INVALID, 0);
	expect_speed(c, NPO_LIT("2.5 GT/s\n2.5 GT/s"), DPA_PCIE_INVALID, 0);
}

/* ---- NPO-023: NUMA parsing ----------------------------------------------- */

static void test_npo_023(void)
{
	const char *c = "NPO-023";

	/* Success, including zero and INT32_MAX. */
	expect_numa(c, NPO_LIT("0"), DPA_PCIE_OK, 0);
	expect_numa(c, NPO_LIT("0\n"), DPA_PCIE_OK, 0);
	expect_numa(c, NPO_LIT("1"), DPA_PCIE_OK, 1);
	expect_numa(c, NPO_LIT("7"), DPA_PCIE_OK, 7);
	expect_numa(c, NPO_LIT("255"), DPA_PCIE_OK, 255);
	expect_numa(c, NPO_LIT("2147483647"), DPA_PCIE_OK, INT32_MAX);
	expect_numa(c, NPO_LIT("2147483647\n"), DPA_PCIE_OK, INT32_MAX);
	expect_numa(c, NPO_LIT(" 3 "), DPA_PCIE_OK, 3);
	expect_numa(c, NPO_LIT("\t3\r\n"), DPA_PCIE_OK, 3);
	/* Leading zeros: unsigned decimal, classified by value. */
	expect_numa(c, NPO_LIT("01"), DPA_PCIE_OK, 1);
	expect_numa(c, NPO_LIT("00"), DPA_PCIE_OK, 0);
	expect_numa(c, NPO_LIT("0000000000000000000002147483647"), DPA_PCIE_OK, INT32_MAX);

	/* NODATA: empty, exactly Unknown, exactly -1. */
	expect_numa(c, NPO_LIT(""), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT("\n"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT(" \t"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT("Unknown"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT("Unknown\n"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT("-1"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT("-1\n"), DPA_PCIE_NODATA, 0);
	expect_numa(c, NPO_LIT(" -1 "), DPA_PCIE_NODATA, 0);

	/* RANGE: negative other than exactly -1 (incl. -0, -01), and beyond int32. */
	expect_numa(c, NPO_LIT("-0"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-00"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-01"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-001"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-2"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-10"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-2147483648"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-2147483649"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("-99999999999999999999999999"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("2147483648"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("4294967295"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("4294967296"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("18446744073709551615"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("18446744073709551616"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("99999999999999999999999999"), DPA_PCIE_RANGE, 0);
	expect_numa(c, NPO_LIT("002147483648"), DPA_PCIE_RANGE, 0);

	/* INVALID: leading plus, non-decimal syntax, internal whitespace. */
	expect_numa(c, NPO_LIT("+1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("+0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("+-1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-+1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("--1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("- 1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-1-"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("1-"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-1 1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("1 0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("0x1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("1.0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("1e1"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("unknown"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("none"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-1x"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("-x"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("\v0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("0\xC2\xA0"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("+2147483648"), DPA_PCIE_INVALID, 0);
	expect_numa(c, NPO_LIT("2147483648x"), DPA_PCIE_INVALID, 0);
}

/* ---- NPO-025: AER records ------------------------------------------------ */

static void test_npo_025(void)
{
	const char *c = "NPO-025";
	static const char *const n3[] = { "RxErr", "BadTLP", "TOTAL_ERR_COR" };
	static const uint64_t c3[] = { 0, 3, 3 };
	static const char *const n1_rx[] = { "RxErr" };
	static const uint64_t c1_7[] = { 7 };
	static const uint64_t c1_1[] = { 1 };
	static const uint64_t c1_max[] = { UINT64_MAX };
	static const char *const n_badtlp[] = { "Bad TLP" };
	static const uint64_t c_3[] = { 3 };
	static const char *const n_err2[] = { "Err2" };
	static const char *const n_err_2[] = { "Err 2" };
	static const uint64_t c_5[] = { 5 };
	static const char *const n_total_first[] = { "TOTAL_ERR_COR", "RxErr" };
	static const uint64_t c_total_first[] = { 5, 1 };
	static const char *const n_case[] = { "RxErr", "rxerr" };
	static const uint64_t c_case[] = { 1, 2 };
	static const char *const n_sym[] = { "Rx-Err/#1", "a b  c" };
	static const uint64_t c_sym[] = { 2, 9 };
	static const char *const n_spaces[] = { "Multiple Word Label With Digits 42 Inside" };
	static const uint64_t c_spaces[] = { 17 };
	static const char *const n_total_only[] = { "TOTAL_ERR_FATAL" };
	static const uint64_t c_total_only[] = { 0 };
	char *buf = malloc(NPO_TEXT_LIMIT + 2);
	char label63[64];
	char label64[65];
	char line[200];
	size_t off;
	size_t i;
	const char *names64[64];
	uint64_t counts64[64];
	static char name_store[64][8];

	if (buf == NULL) {
		npo_fail(c, "malloc failed");
		return;
	}

	/* Basic LF, CRLF, no trailing newline. */
	expect_aer(c, NPO_LIT("RxErr 0\nBadTLP 3\nTOTAL_ERR_COR 3\n"), DPA_PCIE_OK, n3, c3, 3);
	expect_aer(c, NPO_LIT("RxErr 0\r\nBadTLP 3\r\nTOTAL_ERR_COR 3\r\n"), DPA_PCIE_OK, n3, c3, 3);
	expect_aer(c, NPO_LIT("RxErr 0\nBadTLP 3\nTOTAL_ERR_COR 3"), DPA_PCIE_OK, n3, c3, 3);
	expect_aer(c, NPO_LIT("RxErr 0\r\nBadTLP 3\nTOTAL_ERR_COR 3\r"), DPA_PCIE_OK, n3, c3, 3);
	expect_aer(c, NPO_LIT("RxErr 1"), DPA_PCIE_OK, n1_rx, c1_1, 1);
	expect_aer(c, NPO_LIT("RxErr 1\n"), DPA_PCIE_OK, n1_rx, c1_1, 1);

	/* Blank and whitespace-only lines are ignored anywhere. */
	expect_aer(c, NPO_LIT("\n\nRxErr 0\n   \n\t\nBadTLP 3\n\r\n\nTOTAL_ERR_COR 3\n\n"),
		   DPA_PCIE_OK, n3, c3, 3);

	/* Outer trimming and tab as the label/count delimiter. */
	expect_aer(c, NPO_LIT("  RxErr   7  \n"), DPA_PCIE_OK, n1_rx, c1_7, 1);
	expect_aer(c, NPO_LIT("RxErr\t7\n"), DPA_PCIE_OK, n1_rx, c1_7, 1);
	expect_aer(c, NPO_LIT("\tRxErr \t 7\t\n"), DPA_PCIE_OK, n1_rx, c1_7, 1);
	expect_aer(c, NPO_LIT("RxErr 1   "), DPA_PCIE_OK, n1_rx, c1_1, 1);

	/* Internal spaces in the label are preserved; digits in the label stay. */
	expect_aer(c, NPO_LIT("Bad TLP 3\n"), DPA_PCIE_OK, n_badtlp, c_3, 1);
	expect_aer(c, NPO_LIT("Err2 5\n"), DPA_PCIE_OK, n_err2, c_5, 1);
	expect_aer(c, NPO_LIT("Err 2 5\n"), DPA_PCIE_OK, n_err_2, c_5, 1);
	expect_aer(c, NPO_LIT("Multiple Word Label With Digits 42 Inside 17\n"),
		   DPA_PCIE_OK, n_spaces, c_spaces, 1);
	expect_aer(c, NPO_LIT("Rx-Err/#1 2\na b  c 9\n"), DPA_PCIE_OK, n_sym, c_sym, 2);

	/* A tab inside the label (after trimming) is a forbidden control byte. */
	expect_aer(c, NPO_LIT("Rx\tErr 3\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* TOTAL_ERR_* is an ordinary entry, in input order, never reconciled. */
	expect_aer(c, NPO_LIT("TOTAL_ERR_COR 5\nRxErr 1\n"), DPA_PCIE_OK, n_total_first,
		   c_total_first, 2);
	expect_aer(c, NPO_LIT("TOTAL_ERR_FATAL 0\n"), DPA_PCIE_OK, n_total_only, c_total_only, 1);

	/* Byte-exact labels differing only in case are distinct entries. */
	expect_aer(c, NPO_LIT("RxErr 1\nrxerr 2\n"), DPA_PCIE_OK, n_case, c_case, 2);

	/* Duplicate labels -> INVALID, whole result zero. */
	expect_aer(c, NPO_LIT("RxErr 1\nRxErr 2\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\nBadTLP 2\nRxErr 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("  RxErr 1\nRxErr\t2\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* Missing label or count. */
	expect_aer(c, NPO_LIT("RxErr\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr \n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("5\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("  5  \n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\nBadTLP\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* Malformed counts. */
	expect_aer(c, NPO_LIT("RxErr 1a\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr -1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr +1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 0x10\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1.0\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1e3\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1 x\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr one\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\nBadTLP 2z\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\xC2\xA0\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* Count range: exactly UINT64_MAX ok, one more -> RANGE (whole zero). */
	expect_aer(c, NPO_LIT("RxErr 18446744073709551615\n"), DPA_PCIE_OK, n1_rx, c1_max, 1);
	expect_aer(c, NPO_LIT("RxErr 18446744073709551616\n"), DPA_PCIE_RANGE, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 99999999999999999999999\n"), DPA_PCIE_RANGE, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr 1\nBadTLP 18446744073709551616\n"), DPA_PCIE_RANGE,
		   NULL, NULL, 0);
	/* Leading zeros in the count are unsigned decimal. */
	expect_aer(c, NPO_LIT("RxErr 007\n"), DPA_PCIE_OK, n1_rx, c1_7, 1);
	expect_aer(c, NPO_LIT("RxErr 0018446744073709551615\n"), DPA_PCIE_OK, n1_rx, c1_max, 1);

	/* Forbidden bytes in the label. */
	expect_aer(c, NPO_LIT("Rx\x01" "Err 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("Rx\x7f" "Err 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("Rx\xC3\xA9 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("Rx\x1b[0mErr 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("\x80 1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);
	/* A lone CR is not a line delimiter; it becomes a control byte inside
	 * the label. */
	expect_aer(c, NPO_LIT("RxErr 1\rBadTLP 2"), DPA_PCIE_INVALID, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("RxErr\r1\n"), DPA_PCIE_INVALID, NULL, NULL, 0);

	/* NODATA: nothing nonblank. */
	expect_aer(c, NPO_LIT(""), DPA_PCIE_NODATA, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("\n"), DPA_PCIE_NODATA, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("\n\n\r\n"), DPA_PCIE_NODATA, NULL, NULL, 0);
	expect_aer(c, NPO_LIT(" \t\n \n\t"), DPA_PCIE_NODATA, NULL, NULL, 0);
	expect_aer(c, NPO_LIT("   "), DPA_PCIE_NODATA, NULL, NULL, 0);

	/* Label length boundary: 63 ok, 64 TOO_LARGE. */
	memset(label63, 'A', 63);
	label63[63] = '\0';
	memset(label64, 'B', 64);
	label64[64] = '\0';
	{
		const char *const n63[] = { label63 };
		static const uint64_t c63[] = { 1 };
		size_t n;

		n = (size_t)snprintf(line, sizeof(line), "%s 1\n", label63);
		expect_aer(c, line, n, DPA_PCIE_OK, n63, c63, 1);
		n = (size_t)snprintf(line, sizeof(line), "%s 1\n", label64);
		expect_aer(c, line, n, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);
		/* 63 printable bytes with internal spaces is still a valid label. */
		label63[10] = ' ';
		label63[40] = ' ';
		n = (size_t)snprintf(line, sizeof(line), "  %s\t1\n", label63);
		expect_aer(c, line, n, DPA_PCIE_OK, n63, c63, 1);
		/* 64-byte label with a valid sibling: still TOO_LARGE, all zero. */
		n = (size_t)snprintf(line, sizeof(line), "RxErr 1\n%s 1\n", label64);
		expect_aer(c, line, n, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);
	}

	/* Entry count boundary: 64 ok (order preserved), 65 TOO_LARGE. */
	off = 0;
	for (i = 0; i < 64; i++) {
		(void)snprintf(name_store[i], sizeof(name_store[i]), "L%02zu", i);
		names64[i] = name_store[i];
		counts64[i] = (uint64_t)i * 1000u;
		off += (size_t)snprintf(buf + off, NPO_TEXT_LIMIT - off, "L%02zu %" PRIu64 "\n",
					i, counts64[i]);
	}
	expect_aer(c, buf, off, DPA_PCIE_OK, names64, counts64, 64);
	off += (size_t)snprintf(buf + off, NPO_TEXT_LIMIT - off, "L64 1\n");
	expect_aer(c, buf, off, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);

	/* Exactly 16,384 bytes accepted; 16,385 rejected (see also NPO-014). */
	memcpy(buf, "RxErr 7\n", 8);
	memset(buf + 8, ' ', NPO_TEXT_LIMIT + 1 - 8);
	expect_aer(c, buf, NPO_TEXT_LIMIT, DPA_PCIE_OK, n1_rx, c1_7, 1);
	expect_aer(c, buf, NPO_TEXT_LIMIT + 1, DPA_PCIE_TOO_LARGE, NULL, NULL, 0);

	free(buf);
}

/* ---- descriptor reader helpers ------------------------------------------- */

static int make_temp_file(const char *content, size_t len, char *path_out,
			  size_t path_cap)
{
	const char *tmp = getenv("TMPDIR");
	int fd;
	int n;

	if (tmp == NULL || *tmp == '\0')
		tmp = "/tmp";
	n = snprintf(path_out, path_cap, "%s/npo_read_XXXXXX", tmp);
	if (n < 0 || (size_t)n >= path_cap)
		return -1;
	fd = mkstemp(path_out);
	if (fd < 0)
		return -1;
	while (len > 0) {
		ssize_t w = write(fd, content, len);

		if (w <= 0) {
			close(fd);
			unlink(path_out);
			return -1;
		}
		content += w;
		len -= (size_t)w;
	}
	return fd;
}

/* Runs dpa_pcie_read_fd with the buffer placed directly before a guard page
 * so any write at buffer[capacity] faults. */
static int guarded_read(const char *clause, int fd, size_t capacity,
			char **buffer_out, npo_guard *g, size_t *out_len,
			int *out_errno)
{
	int st;

	if (npo_guard_alloc(g, NULL, capacity) != 0) {
		npo_fail(clause, "guard allocation failed");
		return -1;
	}
	memset(g->data, 'X', capacity);
	*out_len = (size_t)-1;
	*out_errno = -1;
	npo_current_clause = clause;
	st = (int)dpa_pcie_read_fd(fd, g->data, capacity, out_len, out_errno);
	*buffer_out = g->data;
	return st;
}

/* ---- NPO-030: reader validation ------------------------------------------ */

static void test_npo_030(void)
{
	const char *c = "NPO-030";
	char buffer[64];
	size_t out_len;
	int out_errno;
	int st;
	int fd;
	char path[512];

	npo_current_clause = c;

	/* fd < 0 */
	memset(buffer, 'X', sizeof(buffer));
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(-1, buffer, sizeof(buffer), &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "read_fd(fd=-1): %s", npo_status_name(st));
	NPO_CHECK(c, out_len == 0 && out_errno == 0 && buffer[0] == '\0',
		  "read_fd(fd=-1): len=%zu errno=%d buffer[0]=%d (want 0,0,NUL)",
		  out_len, out_errno, buffer[0]);
	memset(buffer, 'X', sizeof(buffer));
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(INT32_MIN, buffer, sizeof(buffer), &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(fd=INT32_MIN): %s", npo_status_name(st));

	fd = make_temp_file("abc", 3, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}

	/* capacity 0 with a valid fd: INVALID, buffer untouched (no positive
	 * capacity), outputs zero. */
	memset(buffer, 'X', sizeof(buffer));
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(fd, buffer, 0, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "read_fd(capacity=0): %s", npo_status_name(st));
	NPO_CHECK(c, out_len == 0 && out_errno == 0, "read_fd(capacity=0): len=%zu errno=%d",
		  out_len, out_errno);
	NPO_CHECK(c, buffer[0] == 'X', "read_fd(capacity=0): buffer written despite capacity 0");

	/* capacity 16,386: INVALID, buffer[0] NUL, outputs zero. */
	{
		char *big = malloc(NPO_READ_CAPACITY_MAX + 1);

		if (big != NULL) {
			memset(big, 'X', NPO_READ_CAPACITY_MAX + 1);
			out_len = 99;
			out_errno = 99;
			st = (int)dpa_pcie_read_fd(fd, big, NPO_READ_CAPACITY_MAX + 1, &out_len,
						   &out_errno);
			NPO_CHECK(c, st == DPA_PCIE_INVALID, "read_fd(capacity=16386): %s",
				  npo_status_name(st));
			NPO_CHECK(c, out_len == 0 && out_errno == 0 && big[0] == '\0',
				  "read_fd(capacity=16386): len=%zu errno=%d buffer[0]=%d",
				  out_len, out_errno, big[0]);
			out_len = 99;
			out_errno = 99;
			memset(big, 'X', NPO_READ_CAPACITY_MAX + 1);
			st = (int)dpa_pcie_read_fd(fd, big, SIZE_MAX, &out_len, &out_errno);
			NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && out_errno == 0 &&
					  big[0] == '\0',
				  "read_fd(capacity=SIZE_MAX): %s", npo_status_name(st));
			free(big);
		}
	}

	/* NULL buffer: INVALID, outputs zero. */
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(fd, NULL, 16, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && out_errno == 0,
		  "read_fd(buffer=NULL): %s len=%zu errno=%d", npo_status_name(st), out_len,
		  out_errno);
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(fd, NULL, 0, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && out_errno == 0,
		  "read_fd(buffer=NULL,capacity=0): %s", npo_status_name(st));

	/* NULL out_len: INVALID, out_errno zero, buffer[0] NUL. */
	memset(buffer, 'X', sizeof(buffer));
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(fd, buffer, sizeof(buffer), NULL, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_errno == 0 && buffer[0] == '\0',
		  "read_fd(out_len=NULL): %s errno=%d buffer[0]=%d", npo_status_name(st),
		  out_errno, buffer[0]);

	/* NULL out_errno: INVALID, out_len zero, buffer[0] NUL. */
	memset(buffer, 'X', sizeof(buffer));
	out_len = 99;
	st = (int)dpa_pcie_read_fd(fd, buffer, sizeof(buffer), &out_len, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && buffer[0] == '\0',
		  "read_fd(out_errno=NULL): %s len=%zu buffer[0]=%d", npo_status_name(st),
		  out_len, buffer[0]);

	/* Everything null. */
	st = (int)dpa_pcie_read_fd(-1, NULL, 0, NULL, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "read_fd(all invalid): %s", npo_status_name(st));
	st = (int)dpa_pcie_read_fd(fd, NULL, 16, NULL, NULL);
	NPO_CHECK(c, st == DPA_PCIE_INVALID, "read_fd(null outputs): %s", npo_status_name(st));

	/* Invalid fd together with capacity 0 and non-null buffer: buffer
	 * untouched. */
	memset(buffer, 'X', sizeof(buffer));
	out_len = 99;
	out_errno = 99;
	st = (int)dpa_pcie_read_fd(-1, buffer, 0, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_INVALID && out_len == 0 && out_errno == 0 && buffer[0] == 'X',
		  "read_fd(fd=-1,capacity=0): %s buffer[0]=%d", npo_status_name(st), buffer[0]);

	/* Validation does not touch the descriptor: offset unchanged. */
	NPO_CHECK(c, lseek(fd, 0, SEEK_CUR) == 3,
		  "read_fd validation failures moved the descriptor offset");

	/* Capacity boundaries that are valid: 1 and 16,385 (checked with content
	 * in NPO-031/032; here only that they are not INVALID). */
	memset(buffer, 'X', sizeof(buffer));
	st = (int)dpa_pcie_read_fd(fd, buffer, 1, &out_len, &out_errno);
	NPO_CHECK(c, st != DPA_PCIE_INVALID, "read_fd(capacity=1) rejected as INVALID");

	close(fd);
	unlink(path);
}

/* ---- NPO-031: success, offset preservation, empty source ----------------- */

static void test_npo_031(void)
{
	const char *c = "NPO-031";
	npo_guard g;
	char *buffer;
	size_t out_len;
	int out_errno;
	int st;
	int fd;
	char path[512];

	/* Regular file "hello", capacity 16: OK, len 5, NUL appended,
	 * errno 0, offset unchanged (offset 2 before the call). */
	fd = make_temp_file("hello", 5, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}
	if (lseek(fd, 2, SEEK_SET) != 2)
		npo_fail(c, "lseek failed");
	st = guarded_read(c, fd, 16, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK, "read_fd(hello, cap 16): %s", npo_status_name(st));
	NPO_CHECK(c, out_len == 5, "read_fd(hello): len %zu want 5", out_len);
	NPO_CHECK(c, memcmp(buffer, "hello", 5) == 0 && buffer[5] == '\0',
		  "read_fd(hello): content/NUL mismatch");
	NPO_CHECK(c, out_errno == 0, "read_fd(hello): errno %d want 0", out_errno);
	NPO_CHECK(c, lseek(fd, 0, SEEK_CUR) == 2,
		  "read_fd(hello): caller offset changed (want 2, got %lld)",
		  (long long)lseek(fd, 0, SEEK_CUR));
	npo_guard_free(&g);

	/* Offset at EOF: still reads from offset zero and keeps the offset. */
	if (lseek(fd, 0, SEEK_END) != 5)
		npo_fail(c, "lseek END failed");
	st = guarded_read(c, fd, 6, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 5 && memcmp(buffer, "hello", 5) == 0 &&
			  buffer[5] == '\0' && out_errno == 0,
		  "read_fd(hello, offset at EOF, cap 6 = exact fit): %s len=%zu",
		  npo_status_name(st), out_len);
	NPO_CHECK(c, lseek(fd, 0, SEEK_CUR) == 5, "read_fd(hello): offset not preserved at EOF");
	npo_guard_free(&g);

	/* Content with embedded NUL bytes is copied by length, not by strlen. */
	close(fd);
	unlink(path);
	fd = make_temp_file("a\0b\0", 4, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}
	st = guarded_read(c, fd, 8, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 4 && memcmp(buffer, "a\0b\0", 4) == 0 &&
			  buffer[4] == '\0' && out_errno == 0,
		  "read_fd(binary with NULs): %s len=%zu", npo_status_name(st), out_len);
	npo_guard_free(&g);
	close(fd);
	unlink(path);

	/* Empty source: OK, len 0, buffer[0] NUL, errno 0 — capacity 1 too. */
	fd = make_temp_file("", 0, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}
	st = guarded_read(c, fd, 16, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 0 && buffer[0] == '\0' && out_errno == 0,
		  "read_fd(empty, cap 16): %s len=%zu errno=%d buffer[0]=%d",
		  npo_status_name(st), out_len, out_errno, buffer[0]);
	npo_guard_free(&g);
	st = guarded_read(c, fd, 1, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 0 && buffer[0] == '\0' && out_errno == 0,
		  "read_fd(empty, cap 1): %s len=%zu errno=%d", npo_status_name(st), out_len,
		  out_errno);
	npo_guard_free(&g);
	st = guarded_read(c, fd, NPO_READ_CAPACITY_MAX, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 0 && buffer[0] == '\0' && out_errno == 0,
		  "read_fd(empty, cap 16385): %s", npo_status_name(st));
	npo_guard_free(&g);
	close(fd);
	unlink(path);

	/* Maximum: 16,384 bytes with capacity 16,385. */
	{
		char *content = malloc(NPO_TEXT_LIMIT);

		if (content == NULL) {
			npo_fail(c, "malloc failed");
			return;
		}
		memset(content, 'z', NPO_TEXT_LIMIT);
		content[0] = 'a';
		content[NPO_TEXT_LIMIT - 1] = 'b';
		fd = make_temp_file(content, NPO_TEXT_LIMIT, path, sizeof(path));
		if (fd < 0) {
			npo_fail(c, "cannot create temp file");
			free(content);
			return;
		}
		if (lseek(fd, 100, SEEK_SET) != 100)
			npo_fail(c, "lseek failed");
		st = guarded_read(c, fd, NPO_READ_CAPACITY_MAX, &buffer, &g, &out_len,
				  &out_errno);
		NPO_CHECK(c, st == DPA_PCIE_OK, "read_fd(16384 bytes, cap 16385): %s",
			  npo_status_name(st));
		NPO_CHECK(c, out_len == NPO_TEXT_LIMIT, "read_fd(16384): len %zu", out_len);
		NPO_CHECK(c, memcmp(buffer, content, NPO_TEXT_LIMIT) == 0 &&
				  buffer[NPO_TEXT_LIMIT] == '\0',
			  "read_fd(16384): content or terminator mismatch");
		NPO_CHECK(c, out_errno == 0, "read_fd(16384): errno %d", out_errno);
		NPO_CHECK(c, lseek(fd, 0, SEEK_CUR) == 100, "read_fd(16384): offset not preserved");
		npo_guard_free(&g);
		close(fd);
		unlink(path);
		free(content);
	}
}

/* ---- NPO-031: EINTR retry (where controllable) --------------------------- */

static volatile sig_atomic_t npo_alarm_fired;

static void npo_on_alarm(int sig)
{
	(void)sig;
	npo_alarm_fired = 1;
}

static void test_npo_031_eintr(void)
{
	const char *c = "NPO-031";
	int p[2];
	pid_t pid;
	struct sigaction sa;
	struct sigaction old;
	struct itimerval it;
	struct itimerval off;
	char buffer[16];
	size_t out_len = (size_t)-1;
	int out_errno = -1;
	int st;
	int wstatus;

	if (pipe(p) != 0) {
		npo_fail(c, "pipe failed");
		return;
	}
	pid = fork();
	if (pid < 0) {
		npo_fail(c, "fork failed");
		close(p[0]);
		close(p[1]);
		return;
	}
	if (pid == 0) {
		struct timespec ts;

		close(p[0]);
		ts.tv_sec = 0;
		ts.tv_nsec = 300 * 1000 * 1000;
		(void)nanosleep(&ts, NULL);
		(void)!write(p[1], "abc", 3);
		close(p[1]);
		_exit(0);
	}
	close(p[1]);

	memset(&sa, 0, sizeof(sa));
	sa.sa_handler = npo_on_alarm;
	sigemptyset(&sa.sa_mask);
	sa.sa_flags = 0; /* no SA_RESTART: a blocking read returns EINTR */
	npo_alarm_fired = 0;
	(void)sigaction(SIGALRM, &sa, &old);
	memset(&it, 0, sizeof(it));
	it.it_value.tv_usec = 100 * 1000;
	(void)setitimer(ITIMER_REAL, &it, NULL);

	memset(buffer, 'X', sizeof(buffer));
	npo_current_clause = c;
	st = (int)dpa_pcie_read_fd(p[0], buffer, sizeof(buffer), &out_len, &out_errno);

	memset(&off, 0, sizeof(off));
	(void)setitimer(ITIMER_REAL, &off, NULL);
	(void)sigaction(SIGALRM, &old, NULL);
	close(p[0]);
	(void)waitpid(pid, &wstatus, 0);

	if (st == DPA_PCIE_OK) {
		NPO_CHECK(c, out_len == 3 && memcmp(buffer, "abc", 3) == 0 && buffer[3] == '\0' &&
				  out_errno == 0,
			  "read_fd(pipe after EINTR): len=%zu errno=%d", out_len, out_errno);
		if (!npo_alarm_fired)
			npo_note(c, "EINTR was not delivered during the read; retry path not exercised");
	} else if (st == DPA_PCIE_IO && out_errno == EINTR) {
		npo_fail(c, "read_fd returned DPA_PCIE_IO with errno EINTR instead of retrying");
	} else if (st == DPA_PCIE_IO) {
		npo_note(c, "reader rejects a pipe (DPA_PCIE_IO errno=%d %s); EINTR retry not "
			    "controllable through a pipe on this reader — not run",
			 out_errno, strerror(out_errno));
		NPO_CHECK(c, out_len == 0 && buffer[0] == '\0', "read_fd(pipe): IO outputs not reset");
	} else {
		npo_fail(c, "read_fd(pipe) returned %s (errno=%d)", npo_status_name(st), out_errno);
	}
}

/* ---- NPO-032: overflow / exact fit ---------------------------------------- */

static void test_npo_032(void)
{
	const char *c = "NPO-032";
	npo_guard g;
	char *buffer;
	char *content = malloc(NPO_TEXT_LIMIT + 8);
	size_t out_len;
	int out_errno;
	int st;
	int fd;
	char path[512];

	if (content == NULL) {
		npo_fail(c, "malloc failed");
		return;
	}
	memset(content, 'q', NPO_TEXT_LIMIT + 8);

	/* 5 bytes: capacity 6 exact fit -> OK; capacity 5 -> TOO_LARGE. */
	fd = make_temp_file("hello", 5, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		free(content);
		return;
	}
	st = guarded_read(c, fd, 6, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 5 && buffer[5] == '\0' && out_errno == 0,
		  "read_fd(5 bytes, cap 6): %s len=%zu", npo_status_name(st), out_len);
	npo_guard_free(&g);
	if (lseek(fd, 1, SEEK_SET) != 1)
		npo_fail(c, "lseek failed");
	st = guarded_read(c, fd, 5, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE, "read_fd(5 bytes, cap 5): %s want TOO_LARGE",
		  npo_status_name(st));
	NPO_CHECK(c, out_len == 0 && out_errno == 0 && buffer[0] == '\0',
		  "read_fd(5 bytes, cap 5): len=%zu errno=%d buffer[0]=%d", out_len, out_errno,
		  buffer[0]);
	npo_guard_free(&g);
	st = guarded_read(c, fd, 1, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(5 bytes, cap 1): %s", npo_status_name(st));
	npo_guard_free(&g);
	st = guarded_read(c, fd, 2, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(5 bytes, cap 2): %s", npo_status_name(st));
	npo_guard_free(&g);
	close(fd);
	unlink(path);

	/* 1 byte with capacity 1 -> TOO_LARGE; capacity 2 -> OK. */
	fd = make_temp_file("k", 1, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		free(content);
		return;
	}
	st = guarded_read(c, fd, 1, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(1 byte, cap 1): %s", npo_status_name(st));
	npo_guard_free(&g);
	st = guarded_read(c, fd, 2, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 1 && buffer[0] == 'k' && buffer[1] == '\0',
		  "read_fd(1 byte, cap 2): %s", npo_status_name(st));
	npo_guard_free(&g);
	close(fd);
	unlink(path);

	/* 16,384 bytes: capacity 16,385 OK (NPO-031); 16,385 bytes: TOO_LARGE. */
	fd = make_temp_file(content, NPO_TEXT_LIMIT + 1, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		free(content);
		return;
	}
	st = guarded_read(c, fd, NPO_READ_CAPACITY_MAX, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(16385 bytes, cap 16385): %s len=%zu", npo_status_name(st), out_len);
	npo_guard_free(&g);
	close(fd);
	unlink(path);
	fd = make_temp_file(content, NPO_TEXT_LIMIT + 7, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		free(content);
		return;
	}
	st = guarded_read(c, fd, NPO_READ_CAPACITY_MAX, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(16391 bytes, cap 16385): %s", npo_status_name(st));
	npo_guard_free(&g);
	st = guarded_read(c, fd, 100, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_TOO_LARGE && out_len == 0 && out_errno == 0 &&
			  buffer[0] == '\0',
		  "read_fd(16391 bytes, cap 100): %s", npo_status_name(st));
	npo_guard_free(&g);
	close(fd);
	unlink(path);
	free(content);
}

/* ---- NPO-033: I/O failure and saved errno --------------------------------- */

static void test_npo_033(void)
{
	const char *c = "NPO-033";
	npo_guard g;
	char *buffer;
	size_t out_len;
	int out_errno;
	int st;
	int fd;
	int closed_fd;
	char path[512];

	/* Write-only descriptor: read fails with EBADF. */
	fd = make_temp_file("data", 4, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}
	close(fd);
	fd = open(path, O_WRONLY);
	if (fd < 0) {
		npo_fail(c, "open O_WRONLY failed");
		unlink(path);
		return;
	}
	errno = 0;
	st = guarded_read(c, fd, 16, &buffer, &g, &out_len, &out_errno);
	NPO_CHECK(c, st == DPA_PCIE_IO, "read_fd(write-only fd): %s want IO", npo_status_name(st));
	NPO_CHECK(c, out_errno == EBADF, "read_fd(write-only fd): saved errno %d (%s) want EBADF",
		  out_errno, strerror(out_errno));
	NPO_CHECK(c, out_len == 0 && buffer[0] == '\0',
		  "read_fd(write-only fd): len=%zu buffer[0]=%d want 0,NUL", out_len, buffer[0]);
	npo_guard_free(&g);
	close(fd);

	/* Closed descriptor number (no reuse between close and call). */
	closed_fd = open(path, O_RDONLY);
	if (closed_fd >= 0) {
		close(closed_fd);
		st = guarded_read(c, closed_fd, 16, &buffer, &g, &out_len, &out_errno);
		NPO_CHECK(c, st == DPA_PCIE_IO && out_errno == EBADF && out_len == 0 &&
				  buffer[0] == '\0',
			  "read_fd(closed fd): %s errno=%d", npo_status_name(st), out_errno);
		npo_guard_free(&g);
	}
	unlink(path);

	/* Directory descriptor: read fails with EISDIR (Linux and Darwin). */
	fd = open(".", O_RDONLY);
	if (fd >= 0) {
		st = guarded_read(c, fd, 64, &buffer, &g, &out_len, &out_errno);
		NPO_CHECK(c, st == DPA_PCIE_IO, "read_fd(directory fd): %s want IO",
			  npo_status_name(st));
		NPO_CHECK(c, out_errno == EISDIR,
			  "read_fd(directory fd): saved errno %d (%s) want EISDIR", out_errno,
			  strerror(out_errno));
		NPO_CHECK(c, out_len == 0 && buffer[0] == '\0',
			  "read_fd(directory fd): outputs not reset");
		npo_guard_free(&g);
		close(fd);
	}
}

/* ---- NPO-034: borrowed descriptor is neither closed nor consumed --------- */

static void test_npo_034(void)
{
	const char *c = "NPO-034";
	npo_guard g;
	char *buffer;
	size_t out_len;
	int out_errno;
	int st;
	int fd;
	int i;
	char path[512];

	fd = make_temp_file("borrowed", 8, path, sizeof(path));
	if (fd < 0) {
		npo_fail(c, "cannot create temp file");
		return;
	}
	for (i = 0; i < 3; i++) {
		st = guarded_read(c, fd, 32, &buffer, &g, &out_len, &out_errno);
		NPO_CHECK(c, st == DPA_PCIE_OK && out_len == 8, "read_fd call %d: %s", i,
			  npo_status_name(st));
		npo_guard_free(&g);
		NPO_CHECK(c, fcntl(fd, F_GETFD) != -1,
			  "borrowed descriptor was closed by the library (call %d)", i);
	}
	/* The descriptor still works for the caller afterwards. */
	{
		char tail[9];
		ssize_t r;

		if (lseek(fd, 0, SEEK_SET) != 0)
			npo_fail(c, "lseek failed");
		r = read(fd, tail, 8);
		NPO_CHECK(c, r == 8 && memcmp(tail, "borrowed", 8) == 0,
			  "caller cannot read its own descriptor after read_fd");
	}
	close(fd);
	unlink(path);
}

/* ---- NPO-005: no global logging on stdout/stderr -------------------------- */

static void test_npo_005(void)
{
	const char *c = "NPO-005";
	char path[512];
	int capture;
	int saved_out;
	int saved_err;
	uint32_t u32;
	int32_t i32;
	dpa_pcie_bdf bdf;
	dpa_pcie_aer *aer = malloc(sizeof(*aer));
	char buffer[8];
	size_t out_len;
	int out_errno;
	struct stat sb;
	int wrote = -1;

	if (aer == NULL) {
		npo_fail(c, "malloc failed");
		return;
	}
	capture = make_temp_file("", 0, path, sizeof(path));
	if (capture < 0) {
		npo_fail(c, "cannot create capture file");
		free(aer);
		return;
	}
	fflush(stdout);
	fflush(stderr);
	saved_out = dup(1);
	saved_err = dup(2);
	if (saved_out < 0 || saved_err < 0 || dup2(capture, 1) < 0 || dup2(capture, 2) < 0) {
		npo_fail(c, "cannot redirect stdout/stderr");
	} else {
		/* A batch of failing and succeeding calls. */
		(void)dpa_pcie_parse_width(NPO_LIT("garbage"), &u32);
		(void)dpa_pcie_parse_width(NULL, 5, &u32);
		(void)dpa_pcie_parse_width(NPO_LIT("16"), NULL);
		(void)dpa_pcie_parse_speed(NPO_LIT("2.5 GT/s"), &u32);
		(void)dpa_pcie_parse_speed(NPO_LIT("bad"), &u32);
		(void)dpa_pcie_parse_numa(NPO_LIT("+1"), &i32);
		(void)dpa_pcie_parse_bdf(NPO_LIT("0000:00:20.0"), &bdf);
		(void)dpa_pcie_parse_bdf(NPO_LIT("x"), &bdf);
		(void)dpa_pcie_parse_aer(NPO_LIT("RxErr 1\nRxErr 2\n"), aer);
		(void)dpa_pcie_parse_aer(NPO_LIT("\x01 1\n"), aer);
		(void)dpa_pcie_read_fd(-1, buffer, sizeof(buffer), &out_len, &out_errno);
		(void)dpa_pcie_read_fd(saved_out, buffer, sizeof(buffer), &out_len, &out_errno);
		(void)dpa_pcie_read_fd(12345, buffer, sizeof(buffer), &out_len, &out_errno);
		(void)dpa_pcie_abi_version();
		fflush(stdout);
		fflush(stderr);
		wrote = fstat(capture, &sb) == 0 ? (sb.st_size != 0) : -1;
	}
	if (saved_out >= 0) {
		(void)dup2(saved_out, 1);
		close(saved_out);
	}
	if (saved_err >= 0) {
		(void)dup2(saved_err, 2);
		close(saved_err);
	}
	close(capture);
	unlink(path);
	NPO_CHECK(c, wrote == 0, "library wrote to stdout/stderr during calls (capture size nonzero)");
	free(aer);
}

/* ---- NPO-006: concurrent callers with distinct outputs ------------------- */

struct npo_worker {
	int index;
	int failures;
};

static void *npo_worker_main(void *arg)
{
	struct npo_worker *w = arg;
	static const char *const texts[4] = { "2.5 GT/s PCIe", "16.0 GT/s", "32.0 GT/s PCIe",
					      "8 GT/s" };
	static const uint32_t values[4] = { 2500, 16000, 32000, 8000 };
	static const char *const bad[4] = { "junk", "-1", "+16", "0x10" };
	const char *text = texts[w->index % 4];
	uint32_t want = values[w->index % 4];
	dpa_pcie_aer *aer = malloc(sizeof(*aer));
	dpa_pcie_bdf bdf;
	char aer_text[64];
	int n;
	int i;

	if (aer == NULL) {
		w->failures++;
		return NULL;
	}
	n = snprintf(aer_text, sizeof(aer_text), "Worker%d 1\nTOTAL_ERR_COR %d\n", w->index,
		     w->index);
	for (i = 0; i < 4000; i++) {
		uint32_t speed = 0xFFFFFFFFu;
		uint32_t width = 0xFFFFFFFFu;
		int st;

		st = (int)dpa_pcie_parse_speed(text, strlen(text), &speed);
		if (st != DPA_PCIE_OK || speed != want)
			w->failures++;
		st = (int)dpa_pcie_parse_width(bad[w->index % 4], strlen(bad[w->index % 4]),
					       &width);
		if (st != DPA_PCIE_INVALID || width != 0)
			w->failures++;
		st = (int)dpa_pcie_parse_aer(aer_text, (size_t)n, aer);
		if (st != DPA_PCIE_OK || aer->count != 2 ||
		    aer->entries[1].count != (uint64_t)w->index)
			w->failures++;
		st = (int)dpa_pcie_parse_bdf("0000:00:1f.7", 12, &bdf);
		if (st != DPA_PCIE_OK || bdf.device != 31 || bdf.function != 7)
			w->failures++;
		st = (int)dpa_pcie_parse_bdf("0000:00:20.0", 12, &bdf);
		if (st != DPA_PCIE_RANGE || bdf.device != 0)
			w->failures++;
	}
	free(aer);
	return NULL;
}

static void test_npo_006(void)
{
	const char *c = "NPO-006";
	pthread_t threads[8];
	struct npo_worker workers[8];
	int i;
	int started = 0;

	npo_current_clause = c;
	for (i = 0; i < 8; i++) {
		workers[i].index = i;
		workers[i].failures = 0;
		if (pthread_create(&threads[i], NULL, npo_worker_main, &workers[i]) != 0)
			break;
		started++;
	}
	NPO_CHECK(c, started == 8, "could not start 8 worker threads (%d)", started);
	for (i = 0; i < started; i++) {
		(void)pthread_join(threads[i], NULL);
		NPO_CHECK(c, workers[i].failures == 0,
			  "worker %d observed %d cross-call corruptions/errors", i,
			  workers[i].failures);
	}
}

/* ---- NPO-061 / NPO-072: which artifact provided the symbols ---------------- */

static void test_linkage_mode(const char *mode)
{
	Dl_info info;
	const char *fname;
	int shared;

	memset(&info, 0, sizeof(info));
	if (dladdr((void *)&dpa_pcie_abi_version, &info) == 0 || info.dli_fname == NULL) {
		npo_fail("NPO-072", "dladdr cannot resolve dpa_pcie_abi_version; linkage mode %s unconfirmed",
			 mode);
		return;
	}
	fname = info.dli_fname;
	shared = strstr(fname, "libdpa_pcie") != NULL;
	fprintf(stderr, "npo_harness: dpa_pcie_abi_version resolved from %s\n", fname);
	if (strcmp(mode, "--expect-shared") == 0)
		NPO_CHECK("NPO-072", shared,
			  "harness expected the shared libdpa_pcie but symbols came from %s", fname);
	else if (strcmp(mode, "--expect-static") == 0)
		NPO_CHECK("NPO-072", !shared,
			  "harness expected the static archive but symbols came from %s", fname);
}

int main(int argc, char **argv)
{
	npo_install_fault_handler();
	if (argc > 1)
		test_linkage_mode(argv[1]);

	test_npo_011();
	test_npo_012();
	test_npo_013();
	test_npo_014();
	test_npo_020();
	test_npo_021();
	test_npo_022();
	test_npo_023();
	test_npo_025();
	test_npo_030();
	test_npo_031();
	test_npo_031_eintr();
	test_npo_032();
	test_npo_033();
	test_npo_034();
	test_npo_005();
	test_npo_006();

	return npo_finish("npo_harness");
}
