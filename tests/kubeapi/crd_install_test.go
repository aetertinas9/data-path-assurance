package kubeapi_test

// Installed-CRD behavior: the checked-in manifests are accepted unmodified
// (GKA-047), the Table (kubectl get) view follows the printer columns
// (GKA-048), and the controller never creates or changes CRDs (GKA-049).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/app"
)

func gkaCGetCRD(t *testing.T, e *gkaEnvT, r gkaCRes) *unstructured.Unstructured {
	t.Helper()
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	ctx, cancel := gkaCCtx()
	defer cancel()
	if err := e.Client.Get(ctx, client.ObjectKey{Name: r.Plural + "." + gkaCGroup}, crd); err != nil {
		t.Fatalf("GKA-047: CRD %s is not installed: %v", r.Plural, err)
	}
	return crd
}

// GKA-047: the API server accepted the three checked-in files exactly as they
// are (the schema it serves equals the file's schema, CEL cost limits
// included), and each is Established.
func TestGKA047_InstalledSchemaEqualsCheckedInFile(t *testing.T) {
	e := gkaCStart(t)
	for _, r := range gkaCAllRes {
		crd := gkaCGetCRD(t, e, r)
		vers, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
		if len(vers) != 1 {
			t.Fatalf("GKA-047 %s: %d versions installed, want 1", r.Plural, len(vers))
		}
		ver, _ := vers[0].(map[string]any)
		served, _ := gkaCGet(map[string]any{"v": ver}, "v.schema.openAPIV3Schema")
		got := gkaCMustJSON(t, served)
		want := gkaCMustJSON(t, gkaCSchemaOf(t, gkaCLoadCRD(t, r)))
		if got != want {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			lo := max(0, i-60)
			t.Errorf("GKA-047 %s: the installed schema differs from deploy/crds/%s near byte %d:\n installed: ...%s\n file:      ...%s",
				r.Plural, r.File, i, got[lo:min(len(got), i+80)], want[lo:min(len(want), i+80)])
		}
		established := false
		conds, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]any)
			established = established || (m["type"] == "Established" && m["status"] == "True")
		}
		if !established {
			t.Errorf("GKA-047/GKA-172 %s: not Established", r.Plural)
		}
	}
}

// GKA-048: the Table view lists Name first, then the printer columns in the
// declared order, with the cell values of the object.
func TestGKA048_TableResponseFollowsPrinterColumns(t *testing.T) {
	e := gkaCStart(t)
	names := map[string]string{}
	for _, k := range gkaCKits {
		name := gkaName(t, k.Res.Singular)
		obj := gkaCCreate(t, e, k.Res, name, k.Spec())
		gkaCStatusReal(t, e, obj, k.Status())
		names[k.Res.Plural] = name
	}
	bare := gkaName(t, "device-bare")
	gkaCCreate(t, e, gkaCDevRes, bare, gkaCDeviceSpec())

	want := map[string][]any{ // cells after Name and before AGE
		"gpufleets":      {"Audit", float64(1), float64(0), float64(0)},
		"gpudevices":     {"gpu-node-1", "InService", "Ready", "Qualified"},
		"nodepathstates": {"gpu-node-1", gkaCGraphRev, "Complete"},
	}
	for _, k := range gkaCKits {
		ctx, cancel := gkaCCtx()
		raw, err := e.Clientset.Discovery().RESTClient().Get().
			AbsPath("/apis/"+gkaCGroup+"/"+gkaCVersion+"/"+k.Res.Plural).
			SetHeader("Accept", "application/json;as=Table;g=meta.k8s.io;v=v1").DoRaw(ctx)
		cancel()
		if err != nil {
			t.Fatalf("GKA-048 %s: Table request failed: %v", k.Res.Plural, err)
		}
		var tbl metav1.Table
		if err := json.Unmarshal(raw, &tbl); err != nil {
			t.Fatalf("GKA-048 %s: cannot decode the Table response: %v\n%s", k.Res.Plural, err, raw)
		}
		wantCols := gkaCColumns[k.Res.Plural]
		if len(tbl.ColumnDefinitions) != len(wantCols)+1 {
			t.Errorf("GKA-048 %s: %d columns, want Name plus %d printer columns", k.Res.Plural, len(tbl.ColumnDefinitions), len(wantCols))
			continue
		}
		if c := tbl.ColumnDefinitions[0]; c.Name != "Name" || c.Type != "string" {
			t.Errorf("GKA-048 %s: first column %q/%q, want Name/string", k.Res.Plural, c.Name, c.Type)
		}
		for i, c := range wantCols {
			got := tbl.ColumnDefinitions[i+1]
			if got.Name != c.Name || got.Type != c.Type {
				t.Errorf("GKA-048 %s: column %d = %q/%q, want %q/%q", k.Res.Plural, i+1, got.Name, got.Type, c.Name, c.Type)
			}
		}
		for _, row := range tbl.Rows {
			if len(row.Cells) != len(wantCols)+1 {
				t.Errorf("GKA-048 %s: a row has %d cells, want %d", k.Res.Plural, len(row.Cells), len(wantCols)+1)
				continue
			}
			switch row.Cells[0] {
			case names[k.Res.Plural]:
				for i, w := range want[k.Res.Plural] {
					if row.Cells[i+1] != w {
						t.Errorf("GKA-048 %s: cell %d (%s) = %#v, want %#v", k.Res.Plural, i+1, wantCols[i].Name, row.Cells[i+1], w)
					}
				}
				if age, _ := row.Cells[len(row.Cells)-1].(string); age == "" {
					t.Errorf("GKA-048 %s: AGE cell = %#v, want a duration string", k.Res.Plural, row.Cells[len(row.Cells)-1])
				}
			case bare:
				// no status yet: the status-backed columns are empty.
				if row.Cells[3] != nil || row.Cells[4] != nil {
					t.Errorf("GKA-048 %s: PHASE/QUALIFIED of a device without status = %#v/%#v, want null", k.Res.Plural, row.Cells[3], row.Cells[4])
				}
			}
		}
		found := false
		for _, row := range tbl.Rows {
			found = found || (len(row.Cells) > 0 && row.Cells[0] == names[k.Res.Plural])
		}
		if !found {
			t.Errorf("GKA-048 %s: the created object %s is not in the Table", k.Res.Plural, names[k.Res.Plural])
		}
	}
}

// GKA-049: while it runs and projects status, the controller sends no request
// at all to customresourcedefinitions and leaves the installed CRDs untouched.
func TestGKA049_ControllerNeverTouchesCRDs(t *testing.T) {
	e := gkaCStart(t)
	w := gkaCMakeWorld(t, e)
	before := map[string]string{}
	for _, r := range gkaCAllRes {
		before[r.Plural] = gkaCGetCRD(t, e, r).GetResourceVersion()
	}
	cfg, rec := gkaRecordingConfig(e.Config)
	assessor := newGkaFakeAssessor()
	assessor.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return app.NodeAssessment{}, nil
	})
	gkaStartController(t, cfg, assessor, newGkaFakeClock(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)), nil)
	gkaEventually(t, 20*time.Second, func() (bool, string) {
		_, written := gkaCDeviceStatusMessage(t, e, w.DeviceName)
		return written, "the controller has not written the device status yet"
	})
	time.Sleep(2 * time.Second) // ten 200 ms passes with the world in steady state
	for _, req := range rec.All() {
		if strings.Contains(req, "customresourcedefinitions") {
			t.Errorf("GKA-049/GKA-128: the controller sent a request to customresourcedefinitions: %s", req)
		}
	}
	for _, r := range gkaCAllRes {
		if got := gkaCGetCRD(t, e, r).GetResourceVersion(); got != before[r.Plural] {
			t.Errorf("GKA-049: CRD %s changed while the controller ran (resourceVersion %s -> %s)", r.Plural, before[r.Plural], got)
		}
	}
}
