package kubeapi_test

// GKA-163 (in-process half): an API error body injected through the transport
// seam (GKA-027) must not appear in the controller's log. The rule of GKA-084
// allows the error class, HTTP status code, reason and object kind/name only.

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
)

func TestGKA163_InjectedErrorBodyNeverLogged(t *testing.T) {
	e := gkaCStart(t)
	w := gkaCMakeWorld(t, e)
	const marker = "GKA163-BODY-MARKER-a41c9d"
	cfg, rec := gkaRecordingConfig(e.Config)
	var hits atomic.Int32
	statusPath := "/gpudevices/" + w.DeviceName + "/status"
	rec.Inject(func(req *http.Request) *http.Response {
		if req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, statusPath) {
			return nil
		}
		hits.Add(1)
		body := fmt.Sprintf(`{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"%s rejected","reason":"Invalid",`+
			`"details":{"name":"%s","group":"%s","kind":"GPUDevice","causes":[{"reason":"FieldValueInvalid","message":"%s","field":"status.x"}]},"code":422}`,
			marker, w.DeviceName, gkaCGroup, marker)
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity, Status: "422 Unprocessable Entity",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header:        http.Header{"Content-Type": {"application/json"}},
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}
	})

	logs := &gkaCBuf{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	assessor := newGkaFakeAssessor()
	assessor.SetScript(func(req app.NodeAssessmentRequest) (app.NodeAssessment, error) {
		return app.NodeAssessment{}, nil
	})
	gkaStartController(t, cfg, assessor, newGkaFakeClock(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)),
		func(o *controller.Options) { o.Logger = logger })

	gkaEventually(t, 20*time.Second, func() (bool, string) {
		return hits.Load() >= 1, "the injected 422 has not been hit: the controller never wrote the device status"
	})
	time.Sleep(1500 * time.Millisecond) // the next tick retries the object (GKA-125: 422 waits for a tick)

	text := logs.String()
	if strings.Contains(text, marker) {
		t.Errorf("GKA-084/163: the API response body reached the log:\n%s", text)
	}
	if !strings.Contains(text, "level=ERROR") {
		t.Errorf("GKA-125: a permanent write error must be logged at error level (fallback also rejected); log:\n%s", text)
	}
}
