/*
 * npo_common.h — shared helpers for the independent native-pcie-observer
 * harnesses (independent black-box harnesses written from the specification).
 *
 * Every failure is reported as "FAIL NPO-0xx: ..." on stderr so the clause
 * that was violated is visible without reading the harness source.
 */
#ifndef NPO_COMMON_H
#define NPO_COMMON_H

#include <stddef.h>
#include <stdint.h>

#ifdef __GNUC__
#define NPO_PRINTF(a, b) __attribute__((format(printf, a, b)))
#else
#define NPO_PRINTF(a, b)
#endif

/* Counters are owned by the harness process; single-threaded use only,
 * except that worker threads report through their own local counters. */
extern int npo_failures;
extern int npo_checks;

/* Clause currently being exercised; printed by the fault handler when a
 * guard page is hit or the library crashes. */
extern const char *npo_current_clause;

void npo_fail(const char *clause, const char *fmt, ...) NPO_PRINTF(2, 3);
void npo_note(const char *clause, const char *fmt, ...) NPO_PRINTF(2, 3);
const char *npo_status_name(int status);
int npo_is_zero(const void *p, size_t n);
int npo_finish(const char *harness);
void npo_install_fault_handler(void);

/*
 * npo_guard places a copy of `len` bytes so that data + len is the first
 * byte of an inaccessible page. Any read past the explicit length faults
 * (SIGSEGV/SIGBUS) and the harness reports the current clause and exits 1.
 * With len == 0, `data` itself points at the inaccessible page.
 */
typedef struct npo_guard {
	void *base;
	size_t map_len;
	char *data;
	size_t len;
} npo_guard;

int npo_guard_alloc(npo_guard *g, const void *src, size_t len);
void npo_guard_free(npo_guard *g);

#define NPO_CHECK(clause, cond, ...)                                         \
	do {                                                                 \
		npo_checks++;                                                \
		if (!(cond))                                                 \
			npo_fail((clause), __VA_ARGS__);                     \
	} while (0)

/* Pass a string literal as (pointer, explicit length) — embedded NUL bytes
 * inside the literal are counted, the implicit terminator is not. */
#define NPO_LIT(s) (s), (sizeof(s) - 1)

#endif /* NPO_COMMON_H */
