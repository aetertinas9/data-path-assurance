/*
 * inspect — minimal libdpa_pcie usage example.
 *
 * Usage: inspect <width>
 *   Parses one PCIe link-width string with the static libdpa_pcie ABI.
 *   Success prints "width=<n>" to stdout and exits 0; a rejected width
 *   prints "status=<integer>" to stderr and exits 1; any other argument
 *   count prints the usage line to stderr and exits 2.
 */
#include <stdio.h>
#include <string.h>

#include "dpa_pcie.h"

int main(int argc, char **argv)
{
	uint32_t width = 0;
	dpa_pcie_status st;

	if (argc != 2) {
		fputs("usage: inspect <width>\n", stderr);
		return 2;
	}
	st = dpa_pcie_parse_width(argv[1], strlen(argv[1]), &width);
	if (st != DPA_PCIE_OK) {
		fprintf(stderr, "status=%d\n", (int)st);
		return 1;
	}
	printf("width=%u\n", (unsigned int)width);
	return 0;
}
