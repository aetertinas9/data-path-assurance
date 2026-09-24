/*
 * npo_cxx_linkage.cpp — NPO-010 C++ linkage harness.
 *
 * A C++17 translation unit includes the public header and links every public
 * function against the static archive. Linking succeeds only when the header
 * provides C linkage (extern "C") to C++ callers; the runtime checks confirm
 * the ABI numeric contract is the same from C++ (NPO-011, NPO-012).
 */
#include "dpa_pcie.h"

#include <cstdint>
#include <cstdio>
#include <cstring>
#include <type_traits>

extern "C" {
#include "npo_common.h"
}

/* NPO-012 from C++. */
static_assert(DPA_PCIE_OK == 0, "NPO-012: DPA_PCIE_OK must be 0");
static_assert(DPA_PCIE_INVALID == 1, "NPO-012: DPA_PCIE_INVALID must be 1");
static_assert(DPA_PCIE_RANGE == 2, "NPO-012: DPA_PCIE_RANGE must be 2");
static_assert(DPA_PCIE_NODATA == 3, "NPO-012: DPA_PCIE_NODATA must be 3");
static_assert(DPA_PCIE_TOO_LARGE == 4, "NPO-012: DPA_PCIE_TOO_LARGE must be 4");
static_assert(DPA_PCIE_IO == 5, "NPO-012: DPA_PCIE_IO must be 5");

/* NPO-010: fixed-width public types with plain layout usable across the ABI. */
static_assert(std::is_standard_layout<dpa_pcie_bdf>::value,
	      "NPO-010: dpa_pcie_bdf must be standard layout");
static_assert(std::is_standard_layout<dpa_pcie_aer_entry>::value,
	      "NPO-010: dpa_pcie_aer_entry must be standard layout");
static_assert(std::is_standard_layout<dpa_pcie_aer>::value,
	      "NPO-010: dpa_pcie_aer must be standard layout");
static_assert(std::is_trivially_copyable<dpa_pcie_aer>::value,
	      "NPO-010: dpa_pcie_aer must be trivially copyable");
static_assert(std::is_same<decltype(dpa_pcie_abi_version()), uint32_t>::value,
	      "NPO-011: dpa_pcie_abi_version must return uint32_t");
static_assert(std::is_same<decltype(dpa_pcie_bdf::domain), uint16_t>::value,
	      "NPO-020: domain must be uint16_t");
static_assert(std::is_same<decltype(dpa_pcie_bdf::bus), uint8_t>::value,
	      "NPO-020: bus must be uint8_t");
static_assert(std::is_same<decltype(dpa_pcie_bdf::device), uint8_t>::value,
	      "NPO-020: device must be uint8_t");
static_assert(std::is_same<decltype(dpa_pcie_bdf::function), uint8_t>::value,
	      "NPO-020: function must be uint8_t");
static_assert(sizeof(dpa_pcie_aer_entry::name) == 64, "NPO-024: name must be char[64]");
static_assert(std::is_same<decltype(dpa_pcie_aer_entry::count), uint64_t>::value,
	      "NPO-024: entry count must be uint64_t");
static_assert(std::is_same<decltype(dpa_pcie_aer::count), uint32_t>::value,
	      "NPO-024: aer count must be uint32_t");
static_assert(sizeof(dpa_pcie_aer::entries) / sizeof(dpa_pcie_aer_entry) == 64,
	      "NPO-024: entries must have 64 elements");

namespace {

template <typename T> void expect_status(const char *clause, const char *what, T got, int want)
{
	NPO_CHECK(clause, static_cast<int>(got) == want, "%s: got %s want %s", what,
		  npo_status_name(static_cast<int>(got)), npo_status_name(want));
}

} // namespace

int main()
{
	uint32_t width = 0xFFFFFFFFu;
	uint32_t speed = 0xFFFFFFFFu;
	int32_t numa = -7;
	dpa_pcie_bdf bdf;
	dpa_pcie_aer aer;
	char buffer[8];
	size_t out_len = 99;
	int out_errno = 99;

	npo_install_fault_handler();
	std::memset(&bdf, 0xFF, sizeof(bdf));
	std::memset(&aer, 0xFF, sizeof(aer));

	NPO_CHECK("NPO-011", dpa_pcie_abi_version() == 1u, "abi version %u from C++",
		  static_cast<unsigned>(dpa_pcie_abi_version()));

	expect_status("NPO-010", "parse_width(\"16\") from C++",
		      dpa_pcie_parse_width("16", 2, &width), DPA_PCIE_OK);
	NPO_CHECK("NPO-021", width == 16u, "width %u want 16", static_cast<unsigned>(width));

	expect_status("NPO-010", "parse_speed(\"2.5 GT/s PCIe\") from C++",
		      dpa_pcie_parse_speed("2.5 GT/s PCIe", 13, &speed), DPA_PCIE_OK);
	NPO_CHECK("NPO-022", speed == 2500u, "speed %u want 2500", static_cast<unsigned>(speed));

	expect_status("NPO-010", "parse_numa(\"-1\") from C++", dpa_pcie_parse_numa("-1", 2, &numa),
		      DPA_PCIE_NODATA);
	NPO_CHECK("NPO-013", numa == 0, "numa output not zeroed on NODATA (%d)",
		  static_cast<int>(numa));

	expect_status("NPO-010", "parse_bdf(\"0000:00:1f.7\") from C++",
		      dpa_pcie_parse_bdf("0000:00:1f.7", 12, &bdf), DPA_PCIE_OK);
	NPO_CHECK("NPO-020", bdf.domain == 0 && bdf.bus == 0 && bdf.device == 31 && bdf.function == 7,
		  "bdf %04x:%02x:%02x.%x", bdf.domain, bdf.bus, bdf.device, bdf.function);

	expect_status("NPO-010", "parse_aer from C++",
		      dpa_pcie_parse_aer("RxErr 4\nTOTAL_ERR_COR 4\n", 24, &aer), DPA_PCIE_OK);
	NPO_CHECK("NPO-025", aer.count == 2u && std::strcmp(aer.entries[0].name, "RxErr") == 0 &&
			  aer.entries[0].count == 4u &&
			  std::strcmp(aer.entries[1].name, "TOTAL_ERR_COR") == 0,
		  "aer entries from C++ mismatch (count %u)", static_cast<unsigned>(aer.count));

	expect_status("NPO-010", "read_fd(-1) from C++",
		      dpa_pcie_read_fd(-1, buffer, sizeof(buffer), &out_len, &out_errno),
		      DPA_PCIE_INVALID);
	NPO_CHECK("NPO-030", out_len == 0 && out_errno == 0 && buffer[0] == '\0',
		  "read_fd(-1) outputs from C++: len=%zu errno=%d", out_len, out_errno);

	/* The status type converts to int from C++ without a cast. */
	{
		int st = dpa_pcie_parse_width("abc", 3, &width);

		NPO_CHECK("NPO-010", st == DPA_PCIE_INVALID, "status not usable as int from C++");
		NPO_CHECK("NPO-013", width == 0, "width not zeroed on INVALID from C++");
	}

	return npo_finish("npo_cxx_linkage");
}
