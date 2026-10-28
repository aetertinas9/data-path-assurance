package nvidia

import "testing"

func TestParseQuery(t *testing.T) {
	data := []byte("0, NVIDIA B300, GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa, 00000000:03:00.0, 6, 6, 16, 16\n" +
		"bad line\n" +
		"1, NVIDIA B300, GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb, 00000000:04:00.0, [N/A], 6, 16, 16\n")
	gpus, errs := ParseQuery(data)
	if len(gpus) != 2 || len(errs) != 1 {
		t.Fatalf("gpus %d errs %v", len(gpus), errs)
	}
	if gpus[0].BDF != "0000:03:00.0" || gpus[0].LinkGen != 6 || gpus[0].Name != "NVIDIA B300" {
		t.Errorf("%+v", gpus[0])
	}
	if gpus[1].LinkGen != 0 || gpus[1].Index != 1 {
		t.Errorf("%+v", gpus[1])
	}
}

func TestCanonicalBDF(t *testing.T) {
	for in, want := range map[string]string{"00000000:1B:00.0": "0000:1b:00.0", "0000:1b:00.0": "0000:1b:00.0", "00010000:01:00.0": "10000:01:00.0"} {
		if got, ok := CanonicalBDF(in); !ok || got != want {
			t.Errorf("%s -> %s %v, want %s", in, got, ok, want)
		}
	}
	if _, ok := CanonicalBDF("0000:1b:20.0"); ok {
		t.Error("device 0x20 accepted")
	}
}

func TestParseNVLink(t *testing.T) {
	data := []byte("GPU 0: NVIDIA H100 (UUID: GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)\n" +
		"\t Link 0: 26.562 GB/s\n\t Link 1: <inactive>\n\t Link 2: 26.562 GB/s\n" +
		"GPU 1: NVIDIA RTX PRO 6000 (UUID: GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb)\n" +
		"\t NVML: Unable to retrieve NVLink information\n")
	links, errs := ParseNVLink(data)
	if len(errs) != 0 || len(links) != 2 {
		t.Fatalf("links %d errs %v", len(links), errs)
	}
	if links[0].Active != 2 || len(links[0].Links) != 3 || links[0].Links[1].Active {
		t.Errorf("%+v", links[0])
	}
	if len(links[1].Links) != 0 {
		t.Errorf("%+v", links[1])
	}
}

func TestParseTopo(t *testing.T) {
	data := []byte("\tGPU0\tGPU1\tNIC0\tCPU Affinity\tNUMA Affinity\tGPU NUMA ID\n" +
		"GPU0\t X \tNV18\tPIX\t0-63\t0\t\tN/A\n" +
		"GPU1\tNV18\t X \tPXB\t0-63\t0\t\tN/A\n" +
		"NIC0\tPIX\tPXB\t X \n\nLegend:\n\n  X    = Self\n\nNIC Legend:\n\n  NIC0: mlx5_0\n")
	topo, err := ParseTopo(data)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := topo.Rel("GPU0", "GPU1"); r != "NV18" {
		t.Errorf("GPU0-GPU1 %q", r)
	}
	if r, _ := topo.Rel("NIC0", "GPU1"); r != "PXB" {
		t.Errorf("NIC0-GPU1 %q", r)
	}
	if topo.NICNames["NIC0"] != "mlx5_0" {
		t.Errorf("legend %v", topo.NICNames)
	}
	if topo.Affinity["GPU0"]["CPU Affinity"] != "0-63" || topo.Affinity["GPU0"]["NUMA Affinity"] != "0" {
		t.Errorf("affinity %v", topo.Affinity["GPU0"])
	}
	if _, err := ParseTopo([]byte("nothing here\n")); err == nil {
		t.Error("no header accepted")
	}
}
