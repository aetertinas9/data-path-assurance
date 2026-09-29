package controller

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// installOnce makes the one process-global configuration this package
// performs happen a single time: the klog and controller-runtime loggers are
// pointed at the slog logger of the first Controller created, so that every line
// the process writes is a slog record (GKA-005, GKA-162). klog reads its global
// logger without synchronization, so replacing it while other goroutines use
// client-go would be a data race; later Controllers keep their own Logger for
// their own lines only.
var installOnce sync.Once

func installLoggers(l *slog.Logger) {
	installOnce.Do(func() {
		safe := slog.New(redactingHandler{next: l.Handler()})
		klog.SetSlogLogger(safe)
		crlog.SetLogger(logr.FromSlogHandler(safe.Handler()))
	})
}

// withLogger returns ctx carrying the redacting logger of l. client-go, the
// leader elector and the informers take their logger from the context, so a
// Controller's library lines go to its own Logger even when several Controllers
// share a process.
func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return klog.NewContext(ctx, logr.FromSlogHandler(redactingHandler{next: l.Handler()}))
}

// redactingHandler removes what an API server put into an error before the
// library logs it: the message of a Status is server-provided text (it can
// quote request or response content), so it is replaced and only the library's
// own context, plus the code and reason, remain (GKA-163).
type redactingHandler struct{ next slog.Handler }

// Enabled turns the library's trace records off at every handler level.
// client-go writes request and response bodies (and headers) at verbosity 6 and
// above, which the logr bridge maps to slog levels below Debug; none of them may
// reach a log line (GKA-084, GKA-163).
func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug && h.next.Enabled(ctx, level)
}

// Handle drops a record that carries a body whatever its level and redacts the
// rest.
func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "Request Body" || r.Message == "Response Body" {
		return nil
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	drop := false
	r.Attrs(func(a slog.Attr) bool {
		if strings.EqualFold(a.Key, "body") {
			drop = true
			return false
		}
		out.AddAttrs(redactAttr(a))
		return true
	})
	if drop {
		return nil
	}
	return h.next.Handle(ctx, out)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redactAttr(a)
	}
	return redactingHandler{next: h.next.WithAttrs(red)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindAny {
		return a
	}
	err, ok := a.Value.Any().(error)
	if !ok {
		return a
	}
	var st apierrors.APIStatus
	if !errors.As(err, &st) {
		return a
	}
	text := err.Error()
	if msg := st.Status().Message; msg != "" {
		text = strings.ReplaceAll(text, msg, "<redacted>")
	}
	return slog.String(a.Key, text)
}
