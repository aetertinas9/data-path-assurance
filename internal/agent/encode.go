package agent

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/pkg/model"
)

// Artifact constants (GFO-080).
const (
	artifactSchemaVersion = "dpa.offline-snapshot/v1"
	artifactMode          = "offline"
	limitationOffline     = "offline"
	completenessComplete  = "COMPLETE"
	completenessPartial   = "PARTIAL"
)

// jsonWriter renders the artifact as compact JSON with a fixed key order.
// Every string must consist of bytes 0x20..0x7e; only '"' and '\' are escaped.
type jsonWriter struct {
	b   []byte
	err error
}

func (w *jsonWriter) raw(s string) {
	w.b = append(w.b, s...)
}

func (w *jsonWriter) str(s string) {
	w.b = append(w.b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			if w.err == nil {
				w.err = fmt.Errorf("agent: artifact string holds byte 0x%02x", c)
			}
			continue
		}
		if c == '"' || c == '\\' {
			w.b = append(w.b, '\\')
		}
		w.b = append(w.b, c)
	}
	w.b = append(w.b, '"')
}

// key writes a member name, preceded by a comma unless first.
func (w *jsonWriter) key(name string, first bool) {
	if !first {
		w.b = append(w.b, ',')
	}
	w.str(name)
	w.b = append(w.b, ':')
}

// field writes a string member.
func (w *jsonWriter) field(name, value string, first bool) {
	w.key(name, first)
	w.str(value)
}

// timeField writes a UTC RFC 3339 timestamp member with trailing fraction
// zeros removed.
func (w *jsonWriter) timeField(name string, t time.Time, first bool) {
	w.field(name, formatTime(t), first)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// encodeArtifact renders the whole artifact and its final newline.
func encodeArtifact(m *manifest, frames []frameResult) ([]byte, error) {
	w := &jsonWriter{}
	w.raw("{")
	w.field("schemaVersion", artifactSchemaVersion, true)
	w.field("mode", artifactMode, false)
	w.key("limitations", false)
	w.raw("[")
	w.str(limitationOffline)
	w.raw("]")
	w.field("clusterID", m.clusterID, false)
	w.key("node", false)
	w.raw("{")
	w.field("name", m.nodeName, true)
	w.field("uid", m.nodeUID, false)
	w.raw("}")
	w.field("bootID", m.bootID, false)
	writeTrust(w, m)
	w.key("frames", false)
	w.raw("[")
	for i := range frames {
		if i > 0 {
			w.raw(",")
		}
		writeFrame(w, m, &frames[i])
	}
	w.raw("]}\n")
	if w.err != nil {
		return nil, w.err
	}
	return w.b, nil
}

// writeTrust writes the offline trust profile with its sources ordered by
// (capability, type, name) bytes.
func writeTrust(w *jsonWriter, m *manifest) {
	sources := slices.Clone(m.sources)
	slices.SortFunc(sources, func(x, y trustSource) int {
		if c := strings.Compare(x.capability, y.capability); c != 0 {
			return c
		}
		if c := strings.Compare(x.sourceType, y.sourceType); c != 0 {
			return c
		}
		return strings.Compare(x.sourceName, y.sourceName)
	})
	w.key("trust", false)
	w.raw("{")
	w.field("profileID", m.profileID, true)
	w.field("mode", fleet.TrustModeOffline.String(), false)
	w.field("session", strconv.FormatInt(m.session, 10), false)
	w.key("sources", false)
	w.raw("[")
	for i, s := range sources {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.field("capability", s.capability, true)
		w.key("source", false)
		w.raw("{")
		w.field("type", s.sourceType, true)
		w.field("name", s.sourceName, false)
		w.raw("}}")
	}
	w.raw("]}")
}

// writeFrame writes one frame object.
func writeFrame(w *jsonWriter, m *manifest, f *frameResult) {
	fc := f.context
	w.raw("{")
	w.field("nodeUID", m.nodeUID, true)
	w.field("bootID", m.bootID, false)
	w.field("session", strconv.FormatInt(fc.session, 10), false)
	w.field("sequence", strconv.FormatUint(fc.sequence, 10), false)
	completeness := completenessPartial
	if f.complete {
		completeness = completenessComplete
	}
	w.field("completeness", completeness, false)
	w.timeField("observedAt", fc.times.observedAt, false)
	w.key("payload", false)
	writePayload(w, fc, &f.payload)
	w.field("payloadDigest", f.digest, false)
	w.field("bundleRevision", f.revision, false)
	w.key("diagnostics", false)
	w.raw("[")
	for i, d := range f.diagnostics {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.field("code", d.code, true)
		w.field("subject", d.subject, false)
		w.raw("}")
	}
	w.raw("]")
	w.key("diagnosticsTruncated", false)
	w.raw(strconv.FormatBool(f.diagTotal > maxDiagnostics))
	w.key("diagnosticsTotal", false)
	w.raw(strconv.Itoa(f.diagTotal))
	w.raw("}")
}

// writePayload writes the HostSnapshotV1 payload object.
func writePayload(w *jsonWriter, fc frameContext, p *payload) {
	relation, origin := model.RelLocatedIn.String(), model.OriginObserved.String()
	w.raw("{")

	w.key("assets", true)
	w.raw("[")
	for i, a := range p.assets {
		if i > 0 {
			w.raw(",")
		}
		writeAsset(w, a)
	}
	w.raw("]")

	w.key("edges", false)
	w.raw("[")
	for i, e := range p.edges {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.field("fromKey", e.from.Key(), true)
		w.field("relation", relation, false)
		w.field("toKey", e.to.Key(), false)
		w.field("origin", origin, false)
		w.raw("}")
	}
	w.raw("]")

	w.key("edgeEvidence", false)
	w.raw("[")
	for i := range p.edges {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.key("edgeIndex", true)
		w.raw(strconv.Itoa(i))
		w.field("kind", fleet.EdgeEvidenceObserved.String(), false)
		w.field("sourceType", parentSource.Type, false)
		w.field("sourceName", parentSource.Name, false)
		w.timeField("observedAt", fc.times.observedAt, false)
		w.timeField("expiresAt", fc.times.expiresAt, false)
		w.field("evidenceID", p.evidenceIDs[i], false)
		w.raw("}")
	}
	w.raw("]")

	w.key("observations", false)
	w.raw("[")
	for i, o := range p.observations {
		if i > 0 {
			w.raw(",")
		}
		writeObservation(w, o)
	}
	w.raw("]")

	w.key("gpuBindings", false)
	w.raw("[")
	for i, b := range p.bindings {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.field("uuid", b.uuid, true)
		w.field("serial", "", false)
		w.field("bdf", b.bdf, false)
		w.field("sourceType", nvidiaSource.Type, false)
		w.field("sourceName", nvidiaSource.Name, false)
		w.field("evidenceID", b.evidenceID, false)
		w.timeField("observedAt", fc.times.observedAt, false)
		w.timeField("expiresAt", fc.times.expiresAt, false)
		w.raw("}")
	}
	w.raw("]")

	w.raw("}")
}

// writeAsset writes {kind, canonical, aliases}.
func writeAsset(w *jsonWriter, a model.AssetRef) {
	w.raw("{")
	w.field("kind", a.Kind.String(), true)
	w.field("canonical", a.Canonical, false)
	w.key("aliases", false)
	w.raw("[")
	for i, alias := range a.Aliases {
		if i > 0 {
			w.raw(",")
		}
		w.raw("{")
		w.field("namespace", alias.Namespace, true)
		w.field("value", alias.Value, false)
		w.raw("}")
	}
	w.raw("]}")
}

// writeObservation writes one observation with its fields in model order.
func writeObservation(w *jsonWriter, o model.Observation) {
	w.raw("{")
	w.field("id", o.ID, true)
	w.key("source", false)
	w.raw("{")
	w.field("type", o.Source.Type, true)
	w.field("name", o.Source.Name, false)
	w.raw("}")
	w.key("subject", false)
	writeAsset(w, o.Subject)
	w.field("signal", string(o.Signal), false)
	w.key("value", false)
	v, ok := o.Value.Int()
	if !ok && w.err == nil {
		// The collector emits integer observations only.
		w.err = fmt.Errorf("agent: observation %s holds a %s value", o.ID, o.Value.Kind())
	}
	w.raw("{")
	w.field("int", strconv.FormatInt(v, 10), true)
	w.raw("}")
	w.field("unit", o.Unit, false)
	w.key("dimensions", false)
	w.raw("{")
	for i, k := range sortedKeys(o.Dimensions) {
		w.field(k, o.Dimensions[k], i == 0)
	}
	w.raw("}")
	w.timeField("observedAt", o.ObservedAt, false)
	w.timeField("receivedAt", o.ReceivedAt, false)
	w.timeField("expiresAt", o.ExpiresAt, false)
	w.field("sequence", strconv.FormatUint(o.Sequence, 10), false)
	w.field("quality", o.Quality.String(), false)
	w.field("rawDigest", o.RawDigest, false)
	w.raw("}")
}
