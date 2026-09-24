/*
 * npo_example_check.c — NPO-070 checks for the `inspect` example binary.
 *
 * Usage: npo_example_check <path-to-inspect>
 *
 * Spawns the example with controlled arguments and checks exact stdout,
 * stderr, and exit status per NPO-070 / UQ-03.
 */
#include "npo_common.h"

#include <errno.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

extern char **environ;

struct npo_run {
	char out[256];
	char err[256];
	size_t out_len;
	size_t err_len;
	int out_truncated;
	int err_truncated;
	int exited;
	int status;
};

static size_t drain(int fd, char *buf, size_t cap, int *truncated)
{
	size_t len = 0;
	char sink[256];

	for (;;) {
		ssize_t r;

		if (len < cap)
			r = read(fd, buf + len, cap - len);
		else
			r = read(fd, sink, sizeof(sink));
		if (r < 0) {
			if (errno == EINTR)
				continue;
			break;
		}
		if (r == 0)
			break;
		if (len < cap)
			len += (size_t)r;
		else
			*truncated = 1;
	}
	return len;
}

static int run_example(const char *exe, char *const argv[], struct npo_run *r)
{
	posix_spawn_file_actions_t fa;
	int out_pipe[2];
	int err_pipe[2];
	pid_t pid;
	int rc;
	int wstatus;

	memset(r, 0, sizeof(*r));
	if (pipe(out_pipe) != 0)
		return -1;
	if (pipe(err_pipe) != 0) {
		close(out_pipe[0]);
		close(out_pipe[1]);
		return -1;
	}
	posix_spawn_file_actions_init(&fa);
	posix_spawn_file_actions_adddup2(&fa, out_pipe[1], 1);
	posix_spawn_file_actions_adddup2(&fa, err_pipe[1], 2);
	posix_spawn_file_actions_addclose(&fa, out_pipe[0]);
	posix_spawn_file_actions_addclose(&fa, err_pipe[0]);
	posix_spawn_file_actions_addclose(&fa, out_pipe[1]);
	posix_spawn_file_actions_addclose(&fa, err_pipe[1]);
	rc = posix_spawn(&pid, exe, &fa, NULL, argv, environ);
	posix_spawn_file_actions_destroy(&fa);
	close(out_pipe[1]);
	close(err_pipe[1]);
	if (rc != 0) {
		close(out_pipe[0]);
		close(err_pipe[0]);
		errno = rc;
		return -1;
	}
	/* Outputs are tiny; sequential draining cannot deadlock on a pipe buffer. */
	r->out_len = drain(out_pipe[0], r->out, sizeof(r->out), &r->out_truncated);
	r->err_len = drain(err_pipe[0], r->err, sizeof(r->err), &r->err_truncated);
	close(out_pipe[0]);
	close(err_pipe[0]);
	while (waitpid(pid, &wstatus, 0) < 0) {
		if (errno != EINTR)
			return -1;
	}
	r->exited = WIFEXITED(wstatus);
	r->status = r->exited ? WEXITSTATUS(wstatus) : -1;
	return 0;
}

static void expect_run(const char *exe, const char *label, char *const argv[],
		       const char *want_out, const char *want_err, int want_status)
{
	const char *c = "NPO-070";
	struct npo_run r;

	if (run_example(exe, argv, &r) != 0) {
		npo_fail(c, "%s: cannot spawn %s: %s", label, exe, strerror(errno));
		return;
	}
	NPO_CHECK(c, r.exited, "%s: example did not exit normally", label);
	NPO_CHECK(c, r.exited && r.status == want_status, "%s: exit status %d want %d", label,
		  r.status, want_status);
	NPO_CHECK(c,
		  !r.out_truncated && r.out_len == strlen(want_out) &&
			  memcmp(r.out, want_out, r.out_len) == 0,
		  "%s: stdout %s%.*s%s want %s%s%s", label, "\"", (int)r.out_len, r.out,
		  r.out_truncated ? "...\"" : "\"", "\"", want_out, "\"");
	NPO_CHECK(c,
		  !r.err_truncated && r.err_len == strlen(want_err) &&
			  memcmp(r.err, want_err, r.err_len) == 0,
		  "%s: stderr %s%.*s%s want %s%s%s", label, "\"", (int)r.err_len, r.err,
		  r.err_truncated ? "...\"" : "\"", "\"", want_err, "\"");
}

int main(int argc, char **argv)
{
	const char *exe;
	char *big;

	if (argc != 2) {
		fprintf(stderr, "usage: npo_example_check <path-to-inspect>\n");
		return 2;
	}
	exe = argv[1];
	if (access(exe, X_OK) != 0) {
		npo_fail("NPO-070", "example binary %s is not executable: %s", exe, strerror(errno));
		return npo_finish("npo_example_check");
	}

	/* Success path: exact stdout, empty stderr, exit 0. */
	{
		char *args[] = { (char *)"inspect", (char *)"16", NULL };
		expect_run(exe, "inspect 16", args, "width=16\n", "", 0);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"32", NULL };
		expect_run(exe, "inspect 32", args, "width=32\n", "", 0);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"1", NULL };
		expect_run(exe, "inspect 1", args, "width=1\n", "", 0);
	}
	{
		/* Leading zeros and outer whitespace follow the width parser. */
		char *args[] = { (char *)"inspect", (char *)"016", NULL };
		expect_run(exe, "inspect 016", args, "width=16\n", "", 0);
	}
	{
		char *args[] = { (char *)"inspect", (char *)" 12\n", NULL };
		expect_run(exe, "inspect ' 12\\n'", args, "width=12\n", "", 0);
	}

	/* Invalid width: status=<integer> on stderr, empty stdout, exit 1. */
	{
		char *args[] = { (char *)"inspect", (char *)"abc", NULL };
		expect_run(exe, "inspect abc", args, "", "status=1\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"+16", NULL };
		expect_run(exe, "inspect +16", args, "", "status=1\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"3", NULL };
		expect_run(exe, "inspect 3", args, "", "status=2\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"18446744073709551616", NULL };
		expect_run(exe, "inspect 2^64", args, "", "status=2\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"0", NULL };
		expect_run(exe, "inspect 0", args, "", "status=3\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"Unknown", NULL };
		expect_run(exe, "inspect Unknown", args, "", "status=3\n", 1);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"", NULL };
		expect_run(exe, "inspect ''", args, "", "status=3\n", 1);
	}
	big = malloc(16386);
	if (big != NULL) {
		char *args[] = { (char *)"inspect", big, NULL };

		memset(big, '1', 16385);
		big[16385] = '\0';
		expect_run(exe, "inspect <16385 bytes>", args, "", "status=4\n", 1);
		memset(big, ' ', 16384);
		big[16383] = '8';
		big[16384] = '\0';
		expect_run(exe, "inspect <16384 bytes>", args, "width=8\n", "", 0);
		free(big);
	}

	/* Wrong argument count: usage on stderr, empty stdout, exit 2. */
	{
		char *args[] = { (char *)"inspect", NULL };
		expect_run(exe, "inspect (no args)", args, "", "usage: inspect <width>\n", 2);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"16", (char *)"16", NULL };
		expect_run(exe, "inspect 16 16", args, "", "usage: inspect <width>\n", 2);
	}
	{
		char *args[] = { (char *)"inspect", (char *)"abc", (char *)"16", (char *)"x", NULL };
		expect_run(exe, "inspect abc 16 x", args, "", "usage: inspect <width>\n", 2);
	}

	return npo_finish("npo_example_check");
}
