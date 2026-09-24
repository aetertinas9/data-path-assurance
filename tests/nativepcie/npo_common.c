/* npo_common.c — shared helpers for the native-pcie-observer harnesses. */
#include "npo_common.h"

#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

int npo_failures;
int npo_checks;
const char *npo_current_clause = "NPO-000";

void npo_fail(const char *clause, const char *fmt, ...)
{
	va_list ap;

	npo_failures++;
	fprintf(stderr, "FAIL %s: ", clause);
	va_start(ap, fmt);
	vfprintf(stderr, fmt, ap);
	va_end(ap);
	fputc('\n', stderr);
}

void npo_note(const char *clause, const char *fmt, ...)
{
	va_list ap;

	fprintf(stderr, "NOTE %s: ", clause);
	va_start(ap, fmt);
	vfprintf(stderr, fmt, ap);
	va_end(ap);
	fputc('\n', stderr);
}

/* Names follow the NPO-012 numeric contract, independent of the header. */
const char *npo_status_name(int status)
{
	switch (status) {
	case 0:
		return "DPA_PCIE_OK";
	case 1:
		return "DPA_PCIE_INVALID";
	case 2:
		return "DPA_PCIE_RANGE";
	case 3:
		return "DPA_PCIE_NODATA";
	case 4:
		return "DPA_PCIE_TOO_LARGE";
	case 5:
		return "DPA_PCIE_IO";
	default:
		return "<undefined status>";
	}
}

int npo_is_zero(const void *p, size_t n)
{
	const unsigned char *b = (const unsigned char *)p;
	size_t i;

	for (i = 0; i < n; i++) {
		if (b[i] != 0)
			return 0;
	}
	return 1;
}

int npo_finish(const char *harness)
{
	fprintf(stderr, "%s: %d checks, %d failures\n", harness, npo_checks,
		npo_failures);
	fflush(stderr);
	return npo_failures == 0 ? 0 : 1;
}

#if defined(__has_feature)
#if __has_feature(address_sanitizer)
#define NPO_UNDER_ASAN 1
#endif
#endif
#if defined(__SANITIZE_ADDRESS__)
#define NPO_UNDER_ASAN 1
#endif

#ifndef NPO_UNDER_ASAN
static void npo_fault(int sig)
{
	static const char prefix[] = "FAIL ";
	static const char suffix[] =
		": memory fault inside the library call (guard page hit, "
		"read past explicit length, or invalid access)\n";
	const char *clause = npo_current_clause;
	ssize_t ignored;

	(void)sig;
	ignored = write(2, prefix, sizeof(prefix) - 1);
	ignored = write(2, clause, strlen(clause));
	ignored = write(2, suffix, sizeof(suffix) - 1);
	(void)ignored;
	_exit(1);
}
#endif

void npo_install_fault_handler(void)
{
#ifdef NPO_UNDER_ASAN
	/* ASan reports faults itself with a stack trace; keep its handler. */
	return;
#else
	struct sigaction sa;

	memset(&sa, 0, sizeof(sa));
	sa.sa_handler = npo_fault;
	sigemptyset(&sa.sa_mask);
	sa.sa_flags = 0;
	(void)sigaction(SIGSEGV, &sa, NULL);
	(void)sigaction(SIGBUS, &sa, NULL);
#endif
}

int npo_guard_alloc(npo_guard *g, const void *src, size_t len)
{
	long page_long = sysconf(_SC_PAGESIZE);
	size_t page;
	size_t data_pages;
	void *base;

	memset(g, 0, sizeof(*g));
	if (page_long <= 0)
		return -1;
	page = (size_t)page_long;
	data_pages = (len + page - 1) / page;
	if (data_pages > (SIZE_MAX / page) - 1)
		return -1;
	g->map_len = (data_pages + 1) * page;
	base = mmap(NULL, g->map_len, PROT_READ | PROT_WRITE,
		    MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (base == MAP_FAILED)
		return -1;
	if (mprotect((char *)base + data_pages * page, page, PROT_NONE) != 0) {
		(void)munmap(base, g->map_len);
		return -1;
	}
	g->base = base;
	g->len = len;
	g->data = (char *)base + data_pages * page - len;
	if (len > 0 && src != NULL)
		memcpy(g->data, src, len);
	return 0;
}

void npo_guard_free(npo_guard *g)
{
	if (g->base != NULL)
		(void)munmap(g->base, g->map_len);
	memset(g, 0, sizeof(*g));
}
