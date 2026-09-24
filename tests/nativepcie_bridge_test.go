//go:build cgo

package tests_test

// Real cgo bridge: availability, sentinel mapping, canonical BDF, parsers.
// Requires `make native` first (INTEGRATION-CONTRACT). On darwin/arm64 an
// unavailable backend is a failure, not a skip (NPO-043, NPO-060).

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aetertinas9/data-path-assurance/internal/nativepcie"
)

const npoTextLimit = 16384

// npoRequireBackend fails on the supported local target when the backend is
// unavailable and skips elsewhere.
func npoRequireBackend(t *testing.T) {
	t.Helper()
	if nativepcie.Available() {
		if v := nativepcie.ABIVersion(); v != 1 {
			t.Fatalf("NPO-043: Available() is true but ABIVersion() = %d, want 1", v)
		}
		return
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Fatalf("NPO-043/NPO-060: Available() must be true on darwin/arm64 with cgo and native prerequisites built")
	}
	t.Skipf("NPO-060: native backend unavailable on %s/%s (unvalidated target)", runtime.GOOS, runtime.GOARCH)
}

func npoBytesOf(n int, b byte) []byte {
	return bytes.Repeat([]byte{b}, n)
}

func TestNPO043_AvailableOnSupportedTarget(t *testing.T) {
	avail := nativepcie.Available()
	ver := nativepcie.ABIVersion()
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && !avail {
		t.Fatalf("NPO-043/NPO-060: Available() = false on darwin/arm64 with cgo; want true")
	}
	if avail && ver != 1 {
		t.Errorf("NPO-043: ABIVersion() = %d with available backend, want 1", ver)
	}
	if !avail && ver != 0 {
		t.Errorf("NPO-043: ABIVersion() = %d with unavailable backend, want 0", ver)
	}
}

func TestNPO042_SentinelsAreDistinctAndMatchable(t *testing.T) {
	for i, a := range npoSentinels {
		if a == nil {
			t.Fatalf("NPO-042: sentinel %d is nil", i)
		}
		if !errors.Is(a, a) {
			t.Errorf("NPO-042: sentinel %v does not match itself", a)
		}
		for j, b := range npoSentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("NPO-042: sentinel %v matches distinct sentinel %v", a, b)
			}
		}
	}
}

func TestNPO042_StatusMappingAndZeroValues(t *testing.T) {
	npoRequireBackend(t)
	big := npoBytesOf(npoTextLimit+1, '1')

	// width
	if v, err := nativepcie.ParseWidth([]byte("16\n")); err != nil || v != 16 {
		t.Errorf("NPO-042: ParseWidth(16) = %d, %v; want 16, nil", v, err)
	}
	if v, err := nativepcie.ParseWidth([]byte("abc")); !errors.Is(err, nativepcie.ErrInvalid) || v != 0 {
		t.Errorf("NPO-042: ParseWidth(abc) = %d, %v; want 0, ErrInvalid", v, err)
	}
	if v, err := nativepcie.ParseWidth([]byte("3")); !errors.Is(err, nativepcie.ErrRange) || v != 0 {
		t.Errorf("NPO-042: ParseWidth(3) = %d, %v; want 0, ErrRange", v, err)
	}
	if v, err := nativepcie.ParseWidth([]byte("Unknown")); !errors.Is(err, nativepcie.ErrNoData) || v != 0 {
		t.Errorf("NPO-042: ParseWidth(Unknown) = %d, %v; want 0, ErrNoData", v, err)
	}
	if v, err := nativepcie.ParseWidth(big); !errors.Is(err, nativepcie.ErrTooLarge) || v != 0 {
		t.Errorf("NPO-042: ParseWidth(16385 bytes) = %d, %v; want 0, ErrTooLarge", v, err)
	}
	// Errors from an available backend never claim unavailability.
	if _, err := nativepcie.ParseWidth([]byte("abc")); errors.Is(err, nativepcie.ErrUnavailable) {
		t.Errorf("NPO-042: ParseWidth(abc) matched ErrUnavailable on an available backend")
	}
	// Each failing sentinel matches only itself.
	_, err := nativepcie.ParseWidth([]byte("3"))
	for _, s := range []error{nativepcie.ErrInvalid, nativepcie.ErrNoData, nativepcie.ErrTooLarge, nativepcie.ErrIO, nativepcie.ErrUnavailable} {
		if errors.Is(err, s) {
			t.Errorf("NPO-042: ErrRange result also matches %v", s)
		}
	}
}

func TestNPO041_CanonicalBDFString(t *testing.T) {
	npoRequireBackend(t)
	cases := []struct {
		in   string
		want string
	}{
		{"0000:00:00.0", "0000:00:00.0"},
		{"FFFF:FF:1F.7", "ffff:ff:1f.7"},
		{"ffff:ff:1f.7", "ffff:ff:1f.7"},
		{"aBcD:eF:1a.5", "abcd:ef:1a.5"},
		{"0001:02:03.4", "0001:02:03.4"},
		{"00A0:0a:00.0", "00a0:0a:00.0"},
	}
	for _, c := range cases {
		b, err := nativepcie.ParseBDF(c.in)
		if err != nil {
			t.Errorf("NPO-020/NPO-041: ParseBDF(%q) error %v", c.in, err)
			continue
		}
		if got := b.String(); got != c.want {
			t.Errorf("NPO-041: ParseBDF(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
	// Zero padding from constructed values.
	pad := []struct {
		b    nativepcie.BDF
		want string
	}{
		{nativepcie.BDF{Domain: 1, Bus: 2, Device: 3, Function: 4}, "0001:02:03.4"},
		{nativepcie.BDF{}, "0000:00:00.0"},
		{nativepcie.BDF{Domain: 0xffff, Bus: 0xff, Device: 31, Function: 7}, "ffff:ff:1f.7"},
		{nativepcie.BDF{Domain: 0xa, Bus: 0xb, Device: 0xc, Function: 1}, "000a:0b:0c.1"},
	}
	for _, c := range pad {
		if got := c.b.String(); got != c.want {
			t.Errorf("NPO-041: BDF%+v.String() = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestNPO020_ParseBDF(t *testing.T) {
	npoRequireBackend(t)
	ok := []struct {
		in   string
		want nativepcie.BDF
	}{
		{"0000:00:00.0", nativepcie.BDF{}},
		{"ffff:ff:1f.7", nativepcie.BDF{Domain: 0xffff, Bus: 0xff, Device: 31, Function: 7}},
		{"FFFF:FF:1F.7", nativepcie.BDF{Domain: 0xffff, Bus: 0xff, Device: 31, Function: 7}},
		{"0000:0a:10.3", nativepcie.BDF{Bus: 0x0a, Device: 16, Function: 3}},
	}
	for _, c := range ok {
		got, err := nativepcie.ParseBDF(c.in)
		if err != nil || got != c.want {
			t.Errorf("NPO-020: ParseBDF(%q) = %+v, %v; want %+v, nil", c.in, got, err, c.want)
		}
	}
	bad := []struct {
		in   string
		want error
	}{
		{"", nativepcie.ErrInvalid},
		{"0000:00:00.", nativepcie.ErrInvalid},
		{"0000:00:00.0 ", nativepcie.ErrInvalid},
		{"0000:00:00.0\n", nativepcie.ErrInvalid},
		{" 000:00:00.0", nativepcie.ErrInvalid},
		{"0000-00:00.0", nativepcie.ErrInvalid},
		{"0000:00:00:0", nativepcie.ErrInvalid},
		{"0000:00:00.g", nativepcie.ErrInvalid},
		{"0000:00:00.\x00", nativepcie.ErrInvalid},
		{"0000:00:2g.9", nativepcie.ErrInvalid},
		{"00:00:00.0é", nativepcie.ErrInvalid}, // exactly 12 bytes (é is 2 bytes), non-ASCII
		{"0000:00:20.0", nativepcie.ErrRange},
		{"0000:00:1f.8", nativepcie.ErrRange},
		{"0000:00:ff.f", nativepcie.ErrRange},
		{strings.Repeat("0", npoTextLimit+1), nativepcie.ErrInvalid},
	}
	if n := len("00:00:00.0é"); n != 12 {
		t.Fatalf("fixture: non-ASCII BDF literal is %d bytes, want exactly 12", n)
	}
	for _, c := range bad {
		got, err := nativepcie.ParseBDF(c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("NPO-020/NPO-014: ParseBDF(%q) error %v, want %v", c.in, err, c.want)
		}
		if got != (nativepcie.BDF{}) {
			t.Errorf("NPO-042: ParseBDF(%q) returned non-zero BDF %+v on error", c.in, got)
		}
	}
}

func TestNPO021_ParseWidth(t *testing.T) {
	npoRequireBackend(t)
	exact := append(npoBytesOf(npoTextLimit-1, ' '), '8')
	over := append(npoBytesOf(npoTextLimit, ' '), '8')
	if len(exact) != npoTextLimit || len(over) != npoTextLimit+1 {
		t.Fatalf("fixture: width literals are %d/%d bytes, want %d/%d", len(exact), len(over), npoTextLimit, npoTextLimit+1)
	}
	cases := []struct {
		name string
		in   []byte
		want uint32
		err  error
	}{
		{"1", []byte("1"), 1, nil},
		{"32 lf", []byte("32\n"), 32, nil},
		{"12 crlf", []byte("\t12\r\n"), 12, nil},
		{"leading zeros 016", []byte("016"), 16, nil},
		{"exactly 16384 bytes", exact, 8, nil},
		{"nil", nil, 0, nativepcie.ErrNoData},
		{"empty", []byte{}, 0, nativepcie.ErrNoData},
		{"whitespace", []byte(" \r\n"), 0, nativepcie.ErrNoData},
		{"Unknown", []byte("Unknown\n"), 0, nativepcie.ErrNoData},
		{"zero", []byte("0"), 0, nativepcie.ErrNoData},
		{"00", []byte("00"), 0, nativepcie.ErrNoData},
		{"3", []byte("3"), 0, nativepcie.ErrRange},
		{"03", []byte("03"), 0, nativepcie.ErrRange},
		{"64", []byte("64"), 0, nativepcie.ErrRange},
		{"uint64 overflow", []byte("18446744073709551616"), 0, nativepcie.ErrRange},
		{"sign", []byte("+16"), 0, nativepcie.ErrInvalid},
		{"neg", []byte("-16"), 0, nativepcie.ErrInvalid},
		{"hex", []byte("0x10"), 0, nativepcie.ErrInvalid},
		{"internal ws", []byte("1 6"), 0, nativepcie.ErrInvalid},
		{"unknown lower", []byte("unknown"), 0, nativepcie.ErrInvalid},
		{"embedded NUL", []byte("16\x00"), 0, nativepcie.ErrInvalid},
		{"vertical tab", []byte("\v16"), 0, nativepcie.ErrInvalid},
		{"16385 bytes", over, 0, nativepcie.ErrTooLarge},
		{"16385 garbage", npoBytesOf(npoTextLimit+1, 'x'), 0, nativepcie.ErrTooLarge},
	}
	for _, c := range cases {
		got, err := nativepcie.ParseWidth(c.in)
		if c.err == nil {
			if err != nil || got != c.want {
				t.Errorf("NPO-021 %s: = %d, %v; want %d, nil", c.name, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, c.err) || got != 0 {
			t.Errorf("NPO-021 %s: = %d, %v; want 0, %v", c.name, got, err, c.err)
		}
	}
}

func TestNPO022_ParseSpeed(t *testing.T) {
	npoRequireBackend(t)
	cases := []struct {
		name string
		in   []byte
		want uint32
		err  error
	}{
		{"2.5 PCIe", []byte("2.5 GT/s PCIe"), 2500, nil},
		{"16.0", []byte("16.0 GT/s"), 16000, nil},
		{"8 no fraction", []byte("8 GT/s\n"), 8000, nil},
		{"32.0 PCIe lf", []byte("32.0 GT/s PCIe\n"), 32000, nil},
		{"64.0", []byte("64.0 GT/s PCIe"), 64000, nil},
		{"three fraction digits", []byte("0.001 GT/s"), 1, nil},
		{"leading zeros", []byte("02.50 GT/s"), 2500, nil},
		{"uint32 max", []byte("4294967.295 GT/s"), 4294967295, nil},
		{"unknown rate", []byte("7.7 GT/s"), 7700, nil},
		{"nil", nil, 0, nativepcie.ErrNoData},
		{"empty", []byte(""), 0, nativepcie.ErrNoData},
		{"Unknown", []byte("Unknown\n"), 0, nativepcie.ErrNoData},
		{"zero", []byte("0 GT/s"), 0, nativepcie.ErrNoData},
		{"zero fraction", []byte("0.000 GT/s PCIe"), 0, nativepcie.ErrNoData},
		{"uint32 max + 1", []byte("4294967.296 GT/s"), 0, nativepcie.ErrRange},
		{"huge", []byte("99999999999999999999 GT/s"), 0, nativepcie.ErrRange},
		{"huge malformed", []byte("99999999999999999999 GT/sX"), 0, nativepcie.ErrInvalid},
		{"four fraction digits", []byte("2.5000 GT/s"), 0, nativepcie.ErrInvalid},
		{"no integer digit", []byte(".5 GT/s"), 0, nativepcie.ErrInvalid},
		{"bare point", []byte("2. GT/s"), 0, nativepcie.ErrInvalid},
		{"sign", []byte("+2.5 GT/s"), 0, nativepcie.ErrInvalid},
		{"exponent", []byte("2.5e0 GT/s"), 0, nativepcie.ErrInvalid},
		{"no space", []byte("2.5GT/s"), 0, nativepcie.ErrInvalid},
		{"double space", []byte("2.5  GT/s"), 0, nativepcie.ErrInvalid},
		{"lowercase suffix", []byte("2.5 gt/s"), 0, nativepcie.ErrInvalid},
		{"pcie lowercase", []byte("2.5 GT/s pcie"), 0, nativepcie.ErrInvalid},
		{"no suffix", []byte("2.5"), 0, nativepcie.ErrInvalid},
		{"embedded NUL", []byte("2.5 GT/s\x00"), 0, nativepcie.ErrInvalid},
		{"16385 bytes", npoBytesOf(npoTextLimit+1, ' '), 0, nativepcie.ErrTooLarge},
	}
	for _, c := range cases {
		got, err := nativepcie.ParseSpeed(c.in)
		if c.err == nil {
			if err != nil || got != c.want {
				t.Errorf("NPO-022 %s: = %d, %v; want %d, nil", c.name, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, c.err) || got != 0 {
			t.Errorf("NPO-022 %s: = %d, %v; want 0, %v", c.name, got, err, c.err)
		}
	}
}

func TestNPO023_ParseNUMA(t *testing.T) {
	npoRequireBackend(t)
	cases := []struct {
		name string
		in   []byte
		want int32
		err  error
	}{
		{"zero", []byte("0\n"), 0, nil},
		{"one", []byte("1"), 1, nil},
		{"int32 max", []byte("2147483647"), 2147483647, nil},
		{"trim", []byte(" \t7\r\n"), 7, nil},
		{"leading zero", []byte("01"), 1, nil},
		{"nil", nil, 0, nativepcie.ErrNoData},
		{"empty", []byte(""), 0, nativepcie.ErrNoData},
		{"Unknown", []byte("Unknown"), 0, nativepcie.ErrNoData},
		{"-1", []byte("-1\n"), 0, nativepcie.ErrNoData},
		{"-0", []byte("-0"), 0, nativepcie.ErrRange},
		{"-01", []byte("-01"), 0, nativepcie.ErrRange},
		{"-2", []byte("-2"), 0, nativepcie.ErrRange},
		{"int32 max + 1", []byte("2147483648"), 0, nativepcie.ErrRange},
		{"int32 min", []byte("-2147483648"), 0, nativepcie.ErrRange},
		{"huge", []byte("99999999999999999999"), 0, nativepcie.ErrRange},
		{"plus", []byte("+1"), 0, nativepcie.ErrInvalid},
		{"hex", []byte("0x1"), 0, nativepcie.ErrInvalid},
		{"float", []byte("1.0"), 0, nativepcie.ErrInvalid},
		{"internal ws", []byte("1 0"), 0, nativepcie.ErrInvalid},
		{"dash only", []byte("-"), 0, nativepcie.ErrInvalid},
		{"embedded NUL", []byte("0\x00"), 0, nativepcie.ErrInvalid},
		{"16385 bytes", npoBytesOf(npoTextLimit+1, '0'), 0, nativepcie.ErrTooLarge},
	}
	for _, c := range cases {
		got, err := nativepcie.ParseNUMA(c.in)
		if c.err == nil {
			if err != nil || got != c.want {
				t.Errorf("NPO-023 %s: = %d, %v; want %d, nil", c.name, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, c.err) || got != 0 {
			t.Errorf("NPO-023 %s: = %d, %v; want 0, %v", c.name, got, err, c.err)
		}
	}
}

func TestNPO025_ParseAER(t *testing.T) {
	npoRequireBackend(t)
	label63 := strings.Repeat("A", 63)
	label64 := strings.Repeat("B", 64)
	var sb64, sb65 strings.Builder
	want64 := make([]nativepcie.AERCounter, 0, 64)
	for i := 0; i < 64; i++ {
		name := "L" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
		sb64.WriteString(name + " " + strings.Repeat("9", 1+i%4) + "\n")
		var count uint64
		for j := 0; j < 1+i%4; j++ {
			count = count*10 + 9
		}
		want64 = append(want64, nativepcie.AERCounter{Name: name, Count: count})
	}
	sb65.WriteString(sb64.String())
	sb65.WriteString("Extra 1\n")
	exact := append([]byte("RxErr 7\n"), npoBytesOf(npoTextLimit-8, ' ')...)
	over := append([]byte("RxErr 7\n"), npoBytesOf(npoTextLimit-7, ' ')...)
	if len(exact) != npoTextLimit || len(over) != npoTextLimit+1 || len(label63) != 63 || len(label64) != 64 {
		t.Fatalf("fixture: AER literals are %d/%d bytes, labels %d/%d; want %d/%d, 63/64", len(exact), len(over), len(label63), len(label64), npoTextLimit, npoTextLimit+1)
	}

	ok := []struct {
		name string
		in   []byte
		want []nativepcie.AERCounter
	}{
		{"lf", []byte("RxErr 0\nBadTLP 3\nTOTAL_ERR_COR 3\n"), []nativepcie.AERCounter{{Name: "RxErr"}, {Name: "BadTLP", Count: 3}, {Name: "TOTAL_ERR_COR", Count: 3}}},
		{"crlf", []byte("RxErr 0\r\nBadTLP 3\r\n"), []nativepcie.AERCounter{{Name: "RxErr"}, {Name: "BadTLP", Count: 3}}},
		{"no trailing newline", []byte("RxErr 1"), []nativepcie.AERCounter{{Name: "RxErr", Count: 1}}},
		{"blank lines", []byte("\n\nRxErr 1\n   \n\t\nBadTLP 2\n\n"), []nativepcie.AERCounter{{Name: "RxErr", Count: 1}, {Name: "BadTLP", Count: 2}}},
		{"trim and tab delimiter", []byte("  RxErr\t7  \n"), []nativepcie.AERCounter{{Name: "RxErr", Count: 7}}},
		{"internal spaces", []byte("Bad TLP 3\n"), []nativepcie.AERCounter{{Name: "Bad TLP", Count: 3}}},
		{"digits in label", []byte("Err 2 5\n"), []nativepcie.AERCounter{{Name: "Err 2", Count: 5}}},
		{"total first, not reconciled", []byte("TOTAL_ERR_COR 5\nRxErr 1\n"), []nativepcie.AERCounter{{Name: "TOTAL_ERR_COR", Count: 5}, {Name: "RxErr", Count: 1}}},
		{"case-distinct labels", []byte("RxErr 1\nrxerr 2\n"), []nativepcie.AERCounter{{Name: "RxErr", Count: 1}, {Name: "rxerr", Count: 2}}},
		{"uint64 max", []byte("RxErr 18446744073709551615\n"), []nativepcie.AERCounter{{Name: "RxErr", Count: 18446744073709551615}}},
		{"63-byte label", []byte(label63 + " 1\n"), []nativepcie.AERCounter{{Name: label63, Count: 1}}},
		{"64 entries", []byte(sb64.String()), want64},
		{"exactly 16384 bytes", exact, []nativepcie.AERCounter{{Name: "RxErr", Count: 7}}},
	}
	for _, c := range ok {
		got, err := nativepcie.ParseAER(c.in)
		if err != nil {
			t.Errorf("NPO-025 %s: error %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("NPO-025 %s: %d counters, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("NPO-025 %s: counter %d = %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}

	bad := []struct {
		name string
		in   []byte
		err  error
	}{
		{"nil", nil, nativepcie.ErrNoData},
		{"empty", []byte(""), nativepcie.ErrNoData},
		{"only blank", []byte("\n \n\t\r\n"), nativepcie.ErrNoData},
		{"duplicate", []byte("RxErr 1\nRxErr 2\n"), nativepcie.ErrInvalid},
		{"no count", []byte("RxErr\n"), nativepcie.ErrInvalid},
		{"no label", []byte("5\n"), nativepcie.ErrInvalid},
		{"bad count", []byte("RxErr 1a\n"), nativepcie.ErrInvalid},
		{"negative count", []byte("RxErr -1\n"), nativepcie.ErrInvalid},
		{"tab in label", []byte("Rx\tErr 3\n"), nativepcie.ErrInvalid},
		{"control byte", []byte("Rx\x01Err 1\n"), nativepcie.ErrInvalid},
		{"non-ascii", []byte("Rxé 1\n"), nativepcie.ErrInvalid},
		{"embedded NUL", []byte("RxErr 1\x00\n"), nativepcie.ErrInvalid},
		{"lone CR", []byte("RxErr 1\rBadTLP 2"), nativepcie.ErrInvalid},
		{"count overflow", []byte("RxErr 18446744073709551616\n"), nativepcie.ErrRange},
		{"64-byte label", []byte(label64 + " 1\n"), nativepcie.ErrTooLarge},
		{"65 entries", []byte(sb65.String()), nativepcie.ErrTooLarge},
		{"16385 bytes", over, nativepcie.ErrTooLarge},
	}
	for _, c := range bad {
		got, err := nativepcie.ParseAER(c.in)
		if !errors.Is(err, c.err) {
			t.Errorf("NPO-025 %s: error %v, want %v", c.name, err, c.err)
		}
		if len(got) != 0 {
			t.Errorf("NPO-042: ParseAER %s returned %d counters on error, want empty", c.name, len(got))
		}
	}
}

func TestNPO006_ResultsAreGoOwnedAndConcurrencySafe(t *testing.T) {
	npoRequireBackend(t)
	// The returned counters must not alias the caller's input buffer.
	in := []byte("RxErr 1\nBadTLP 2\n")
	got, err := nativepcie.ParseAER(in)
	if err != nil || len(got) != 2 {
		t.Fatalf("NPO-025: ParseAER = %v, %v", got, err)
	}
	for i := range in {
		in[i] = 'Z'
	}
	if got[0].Name != "RxErr" || got[1].Name != "BadTLP" {
		t.Errorf("NPO-006: result aliases caller input after mutation: %+v", got)
	}

	// Concurrent callers with distinct outputs (run under -race).
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			speedText := []byte([]string{"2.5 GT/s PCIe", "16.0 GT/s", "32.0 GT/s PCIe", "8 GT/s"}[w%4])
			wantSpeed := []uint32{2500, 16000, 32000, 8000}[w%4]
			aerText := []byte("Worker 1\nTOTAL_ERR_COR " + string(rune('0'+w)) + "\n")
			for i := 0; i < 500; i++ {
				if v, err := nativepcie.ParseSpeed(speedText); err != nil || v != wantSpeed {
					errs <- "speed"
					return
				}
				if v, err := nativepcie.ParseWidth([]byte("junk")); !errors.Is(err, nativepcie.ErrInvalid) || v != 0 {
					errs <- "width"
					return
				}
				c, err := nativepcie.ParseAER(aerText)
				if err != nil || len(c) != 2 || c[1].Count != uint64(w) {
					errs <- "aer"
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("NPO-006: concurrent %s call produced a wrong result", e)
	}
}
