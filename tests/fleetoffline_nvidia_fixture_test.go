package tests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GFO-073 [OD-07 A]: any parse failure leaves the whole frame inventory
// Unknown (no binding at all), without affecting completeness [OD-06].
func TestGFO073_InventoryIsAtomic(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	good := gfoUUIDA + ", 00000000:03:00.0\n"
	cases := []struct {
		name, csv, code string
	}{
		{"malformed_row_among_valid", good + "not-a-row\n", "nvidia_malformed"},
		{"malformed_uuid_case", strings.ToUpper(gfoUUIDA) + ", 0000:03:00.0\n", "nvidia_malformed"},
		{"duplicate_uuid", good + gfoUUIDA + ", 0000:04:00.0\n", "nvidia_duplicate"},
		{"duplicate_bdf_notation", good + gfoUUIDB + ", 0000:03:00.0\n", "nvidia_duplicate"},
		{"rows_257", good + string(gfoRows(256)), "nvidia_output_limit"},
		{"empty", "", "nvidia_empty"},
		{"crlf_only", "\r\n", "nvidia_empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			f.Write(gfoCSV(0), c.csv)
			a := f.OK(bin, "GFO-073 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050 [OD-06] "+c.name)
			if len(fr.Payload.GPUBindings) != 0 {
				t.Errorf("GFO-073: %s produced bindings %+v, want none", c.name, fr.Payload.GPUBindings)
			}
			if !fr.HasDiag(c.code, gfoCSV(0)) {
				t.Errorf("GFO-074: want %s(%s), got %s", c.code, gfoCSV(0), fr.DiagString())
			}
		})
	}
}

// GFO-074: canned CSV is read as a regular file inside the root, never
// executed, and each failure maps to its diagnostic with subject
// frames/<i>/nvidia-smi.csv.
func TestGFO074_CannedOutputMapping(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	outsideCSV := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "nvidia-smi.csv")
		if err := os.WriteFile(p, []byte(gfoUUIDB+", 0000:03:00.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *gfoFx, p string)
		code  string // "" = binding expected
	}{
		{"absent", func(t *testing.T, f *gfoFx, p string) { f.Remove(p) }, "nvidia_output_absent"},
		{"fifo", func(t *testing.T, f *gfoFx, p string) { f.Remove(p); f.Fifo(p) }, "nvidia_output_unreadable"},
		{"directory", func(t *testing.T, f *gfoFx, p string) { f.Remove(p); f.Mkdir(p) }, "nvidia_output_unreadable"},
		{"symlink_outside_root", func(t *testing.T, f *gfoFx, p string) { f.Remove(p); f.Link(p, outsideCSV(t)) }, "nvidia_output_unreadable"},
		{"relative_symlink_outside_root", func(t *testing.T, f *gfoFx, p string) {
			o := outsideCSV(t)
			rel, err := filepath.Rel(filepath.Dir(f.P(p)), o)
			if err != nil {
				t.Fatal(err)
			}
			f.Remove(p)
			f.Link(p, rel)
		}, "nvidia_output_unreadable"},
		{"exactly_65536_bytes", func(t *testing.T, f *gfoFx, p string) {
			f.Write(p, string(gfoRow(t, gfoUUIDA, "00000000:03:00.0", 65536)))
		}, ""},
		{"65537_bytes", func(t *testing.T, f *gfoFx, p string) {
			f.Write(p, string(gfoRow(t, gfoUUIDA, "00000000:03:00.0", 65537)))
		}, "nvidia_output_limit"},
		{"malformed", func(t *testing.T, f *gfoFx, p string) { f.Write(p, "uuid, pci.bus_id\n") }, "nvidia_malformed"},
		{"executable_script_is_data", func(t *testing.T, f *gfoFx, p string) {
			marker := filepath.Join(t.TempDir(), "executed")
			f.Write(p, "#!/bin/sh\ntouch "+marker+"\n")
			if err := os.Chmod(f.P(p), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := os.Stat(marker); err == nil {
					t.Errorf("GFO-074/GFO-100: canned CSV was executed")
				}
			})
		}, "nvidia_malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := gfoNewBasic(t, 1)
			c.setup(t, f, gfoCSV(0))
			res := gfoExec(t, bin, gfoRunOpt{Timeout: 10 * time.Second}, "--fixture-root", f.save2())
			a := gfoOK(t, res, &f.M, "GFO-074 "+c.name)
			fr := a.Frames[0]
			gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-050 [OD-06] "+c.name)
			if c.code == "" {
				if b, ok := fr.Payload.Binding(gfoGPU); !ok || b.UUID != gfoUUIDA {
					t.Errorf("GFO-074: want the GPU binding, got %+v (%s)", fr.Payload.GPUBindings, fr.DiagString())
				}
				return
			}
			if len(fr.Payload.GPUBindings) != 0 || strings.Contains(string(res.Stdout), gfoUUIDB) {
				t.Errorf("GFO-074/GFO-101(a): %s must yield no binding and no outside content", c.name)
			}
			if !fr.HasDiag(c.code, gfoCSV(0)) {
				t.Errorf("GFO-074: want %s(%s), got %s", c.code, gfoCSV(0), fr.DiagString())
			}
		})
	}

	t.Run("per_frame_subject", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 2)
		f.Remove(gfoCSV(1))
		a := f.OK(bin, "GFO-074 per frame")
		if len(a.Frames[0].Payload.GPUBindings) != 1 || a.Frames[0].CountCode("nvidia_output_absent") != 0 {
			t.Errorf("GFO-074: frame 0 must keep its own CSV result")
		}
		if !a.Frames[1].HasDiag("nvidia_output_absent", "frames/1/nvidia-smi.csv") || len(a.Frames[1].Payload.GPUBindings) != 0 {
			t.Errorf("GFO-074: frame 1 want nvidia_output_absent(frames/1/nvidia-smi.csv), got %s", a.Frames[1].DiagString())
		}
	})
}

// GFO-075: bindings only for rows whose canonical BDF is a selected GPU of
// the frame; other rows are dropped with nvidia_bdf_not_gpu; GPUs without a
// row get nvidia_gpu_unlisted.
func TestGFO075_BindingEmission(t *testing.T) {
	t.Parallel()
	bin := gfoBuildAgent(t)
	const second = "0000:03:00.1"
	f := gfoNewBasic(t, 1)
	f.Dev(0, gfoGPUDev(gfoHB, gfoRPA, gfoSWU, gfoSWD, second))
	f.Dev(0, gfoGPUDev(gfoHB, "0000:0f:00.0").with("vendor", "0x1002\n")) // non-NVIDIA display
	f.Dev(0, gfoGPUDev(gfoHB, "0000:0e:00.0"))
	f.Link(gfoDevDir(0, []string{gfoHB, "0000:0e:00.0"})+"/physfn", "../"+gfoRPA) // VF
	f.Write(gfoCSV(0), strings.Join([]string{
		"GPU-3c4d5e6f-7081-92a3-b4c5-d6e7f8091a2b, 00000000:04:00.0", // the NIC
		"GPU-4d5e6f70-8192-a3b4-c5d6-e7f8091a2b3c, 0000:99:00.0",     // absent
		"GPU-5e6f7081-92a3-b4c5-d6e7-f8091a2b3c4d, 0000:0F:00.0",     // AMD display
		"GPU-6f708192-a3b4-c5d6-e7f8-091a2b3c4d5e, 0000:0e:00.0",     // VF
		gfoUUIDA + ", 00000000:03:00.0",
	}, "\n")+"\n")
	a := f.OK(bin, "GFO-075")
	fr := a.Frames[0]
	gfoAssertCompleteness(t, fr, "COMPLETE", "GFO-075")
	want := gfoBinding{
		UUID: gfoUUIDA, Serial: "", BDF: gfoGPU, SourceType: "agent", SourceName: gfoNVIDIASrc,
		EvidenceID: gfoBindingID("7", "0", gfoUUIDA, gfoGPU), ObservedAt: gfoT0, ExpiresAt: "2026-09-24T00:05:00Z",
	}
	if len(fr.Payload.GPUBindings) != 1 || fr.Payload.GPUBindings[0] != want {
		t.Errorf("GFO-075: bindings %+v, want exactly %+v", fr.Payload.GPUBindings, want)
	}
	for _, sub := range []string{gfoNIC, "0000:99:00.0", "0000:0f:00.0", "0000:0e:00.0"} {
		if !fr.HasDiag("nvidia_bdf_not_gpu", sub) {
			t.Errorf("GFO-075: want nvidia_bdf_not_gpu(%s), got %s", sub, fr.DiagString())
		}
	}
	if !fr.HasDiag("nvidia_gpu_unlisted", second) || fr.CountCode("nvidia_gpu_unlisted") != 1 {
		t.Errorf("GFO-075: want exactly nvidia_gpu_unlisted(%s), got %s", second, fr.DiagString())
	}

	t.Run("sorted_by_bdf_then_uuid", func(t *testing.T) {
		t.Parallel()
		f := gfoNewBasic(t, 1)
		f.Dev(0, gfoGPUDev(gfoHB, gfoRPA, gfoSWU, gfoSWD, second))
		f.Write(gfoCSV(0), gfoUUIDA+", 0000:03:00.1\n"+gfoUUIDB+", 0000:03:00.0\n")
		b := f.OK(bin, "GFO-075 order")
		got := b.Frames[0].Payload.GPUBindings
		if len(got) != 2 || got[0].BDF != gfoGPU || got[0].UUID != gfoUUIDB || got[1].BDF != second || got[1].UUID != gfoUUIDA {
			t.Errorf("GFO-075/GFO-085: bindings %+v, want (0000:03:00.0,%s),(0000:03:00.1,%s)", got, gfoUUIDB, gfoUUIDA)
		}
	})
}
