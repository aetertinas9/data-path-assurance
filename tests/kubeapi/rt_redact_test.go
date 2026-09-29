package kubeapi_test

// GKA-084 and GKA-163 on the library paths: an API error text that comes back on a
// list, watch or Lease request (client-go reflectors and leader election log such
// errors themselves) must not appear anywhere in the controller's log, even with the
// logger opened to the lowest level. The status-write path is covered by the
// permanent-error test.

import (
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

func TestGKA084_163_LibraryPathErrorTextsNeverReachTheLog(t *testing.T) {
	e := gkaEnv(t)
	sc := gkaRNewScene(t, e, 1, 1, v1alpha1.FleetModeAudit)
	marker := "GKA-403-LIB-MARKER-" + gkaRHash(t.Name(), 16)
	forbidden := func(req *http.Request) *http.Response {
		return gkaRStatusResponse(req, http.StatusForbidden, metav1.StatusReasonForbidden, "forbidden: "+marker)
	}

	// The first two initial synchronisation requests (LIST or initial WATCH) of every collection
	// and the first three Lease requests answer 403 with the marker in the Status message; everything
	// after that reaches the API server, so the controller recovers and keeps running.
	resources := []string{"gpufleets", "gpudevices", "nodepathstates", "nodes"}
	hits := make([]atomic.Int64, len(resources))
	var leaseHits atomic.Int64
	rig := gkaRNewRig(e)
	rig.Rec.Inject(func(req *http.Request) *http.Response {
		if gkaRIsLease(req.URL.Path) {
			if leaseHits.Add(1) <= 3 {
				return forbidden(req)
			}
			return nil
		}
		if req.Method != http.MethodGet {
			return nil
		}
		q := req.URL.Query()
		watch := q.Get("watch") == "true" || q.Get("watch") == "1"
		initial := q.Get("watch") == "" || (watch && q.Get("sendInitialEvents") == "true")
		if !initial {
			return nil // an ordinary watch, not part of the initial synchronisation
		}
		for i, r := range resources {
			if strings.HasSuffix(req.URL.Path, "/"+r) && hits[i].Add(1) <= 2 {
				return forbidden(req)
			}
		}
		return nil
	})
	logs := &gkaSyncBuf{}
	rig.Start(t, nil, func(o *controller.Options) {
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.Level(-12)}))
	})
	gkaRWaitSettled(t, e, sc)
	time.Sleep(500 * time.Millisecond)

	// The injection must really have happened, or the absence of the marker proves nothing.
	if leaseHits.Load() < 1 {
		t.Errorf("test premise: no Lease request was answered with 403")
	}
	for i, r := range resources {
		if hits[i].Load() < 1 {
			t.Errorf("test premise: no initial %s request was answered with 403", r)
		}
	}
	got := logs.String()
	if got == "" {
		t.Errorf("test premise: the controller logged nothing at the lowest level")
	}
	if strings.Contains(got, marker) {
		t.Errorf("GKA-084/163: an API error text from a list, watch or Lease request reached the controller log")
	}
	// Nor does it reach a status or any write body.
	for _, w := range rig.Rec.WriteEntries("") {
		if strings.Contains(string(w.Body), marker) {
			t.Errorf("GKA-074(e)/084: the API error text was written into %s", w)
		}
	}
	// The 403 answers did not stop the controller.
	select {
	case err := <-rig.Ctl.Done:
		t.Errorf("GKA-146: Run returned (%v) because list, watch or Lease requests were forbidden for a while", err)
	default:
	}
}
