package offline

import (
	"math"
	"strconv"
)

// Artifact structure bounds.
const (
	maxFrames          = 16
	maxTrustSources    = 16
	maxDiagnostics     = 256
	maxAssets          = 4096
	maxEdges           = 8192
	maxEdgeEvidence    = 8192
	maxObservations    = 32768
	maxBindings        = 256
	maxAliases         = 64
	maxDimensions      = 64
	maxCanonicalBytes  = 16 << 20 // frame CP length
	maxIDBytes         = 128
	maxSourceBytes     = 256
	maxIdentBytes      = 512
	maxValueString     = 4096
	maxOptionalBytes   = 256
	maxDiagnosticBytes = 16384
)

// The artifact is decoded in two passes: this file reads the JSON into raw
// structs, enforcing types, per-field grammar and counts; artifact_check.go
// then checks cross-field rules, recomputes IDs and digests and converts to
// ratified values. Key order in the document is free, so cross references are
// only checked once everything is read.

type rawArtifact struct {
	schemaVersion, mode string
	limitations         []string
	clusterID           string
	nodeName, nodeUID   string
	bootID              string
	trust               rawTrust
	frames              []rawFrame
}

type rawTrust struct {
	profileID, mode string
	session         uint64
	sources         []rawTrustSource
}

type rawTrustSource struct {
	capability, sourceType, sourceName string
}

type rawFrame struct {
	nodeUID, bootID      string
	session, sequence    uint64
	completeness         string
	observedAt           string
	payload              rawPayload
	payloadDigest        string
	bundleRevision       string
	diagnostics          []rawDiagnostic
	diagnosticsTruncated bool
	diagnosticsTotal     uint64
}

type rawDiagnostic struct {
	code, subject string
}

type rawPayload struct {
	assets       []rawAsset
	edges        []rawEdge
	edgeEvidence []rawEdgeEvidence
	observations []rawObservation
	gpuBindings  []rawBinding
}

type rawAlias struct {
	namespace, value string
}

type rawAsset struct {
	kind, canonical string
	aliases         []rawAlias
}

func (a rawAsset) key() string { return a.kind + "/" + a.canonical }

type rawEdge struct {
	fromKey, relation, toKey, origin string
}

type rawEdgeEvidence struct {
	edgeIndex              uint64
	kind                   string
	sourceType, sourceName string
	observedAt, expiresAt  string
	evidenceID             string
}

// rawValue holds exactly one of the four value kinds.
type rawValue struct {
	kind    string // "int", "float", "bool" or "string"
	integer int64
	float   float64
	boolean bool
	str     string
}

type rawDimension struct {
	key, value string
}

type rawObservation struct {
	id                     string
	sourceType, sourceName string
	subject                rawAsset
	signal                 string
	value                  rawValue
	unit                   string
	dimensions             []rawDimension // sorted by key after decoding
	observedAt             string
	receivedAt             string
	expiresAt              string
	sequence               uint64
	quality                string
	rawDigest              string
}

type rawBinding struct {
	uuid, serial, bdf      string
	sourceType, sourceName string
	evidenceID             string
	observedAt, expiresAt  string
}

// fieldSet records which keys of one object were present.
type fieldSet map[string]bool

func (r *jsonReader) requireFields(seen fieldSet, names ...string) error {
	for _, n := range names {
		if !seen[n] {
			return r.fail("required key " + n + " is missing")
		}
	}
	return nil
}

// strField reads a string that must satisfy ok.
func (r *jsonReader) strField(ok func(string) bool) (string, error) {
	s, err := r.str()
	if err != nil {
		return "", err
	}
	if !ok(s) {
		return "", r.fail("string does not satisfy its grammar")
	}
	return s, nil
}

func asciiRule(min, max int) func(string) bool {
	return func(s string) bool { return isASCII(s, min, max) }
}

func exactRule(want string) func(string) bool {
	return func(s string) bool { return s == want }
}

// printableRule accepts any string of 0x20-0x7E; the field-specific rules are
// checked later where they need context.
func printableRule(min, max int) func(string) bool {
	return func(s string) bool { return isPrintable(s, min, max) }
}

// decimalStringField reads a 64-bit unsigned integer written as a decimal
// string 0|[1-9][0-9]* in [min, max].
func (r *jsonReader) decimalStringField(min, max uint64) (uint64, error) {
	s, err := r.str()
	if err != nil {
		return 0, err
	}
	v, ok := parseDecimalUint(s)
	if !ok || v < min || v > max {
		return 0, r.fail("decimal string is malformed or out of range")
	}
	return v, nil
}

func decodeArtifact(data []byte) (rawArtifact, error) {
	r, err := newJSONReader(data, ErrArtifactInvalid)
	if err != nil {
		return rawArtifact{}, err
	}
	var a rawArtifact
	seen := fieldSet{}
	err = r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "schemaVersion":
			a.schemaVersion, err = r.strField(exactRule("dpa.offline-snapshot/v1"))
		case "mode":
			a.mode, err = r.strField(exactRule("offline"))
		case "limitations":
			err = r.array(1, func(int) error {
				s, err := r.strField(exactRule("offline"))
				a.limitations = append(a.limitations, s)
				return err
			})
			if err == nil && len(a.limitations) != 1 {
				err = r.fail("limitations must be exactly [\"offline\"]")
			}
		case "clusterID":
			a.clusterID, err = r.strField(isClusterID)
		case "node":
			err = r.decodeNode(&a)
		case "bootID":
			a.bootID, err = r.strField(asciiRule(1, 128))
		case "trust":
			a.trust, err = r.decodeTrust()
		case "frames":
			err = r.array(maxFrames, func(int) error {
				f, err := r.decodeFrame()
				a.frames = append(a.frames, f)
				return err
			})
			if err == nil && len(a.frames) == 0 {
				err = r.fail("frames is empty")
			}
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawArtifact{}, err
	}
	if err := r.requireFields(seen, "schemaVersion", "mode", "limitations", "clusterID", "node", "bootID", "trust", "frames"); err != nil {
		return rawArtifact{}, err
	}
	if err := r.end(); err != nil {
		return rawArtifact{}, err
	}
	return a, nil
}

func (r *jsonReader) decodeNode(a *rawArtifact) error {
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "name":
			a.nodeName, err = r.strField(asciiRule(1, 253))
		case "uid":
			a.nodeUID, err = r.strField(asciiRule(1, 128))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return err
	}
	return r.requireFields(seen, "name", "uid")
}

var trustCapabilityNames = map[string]bool{
	"SysfsPhysicalParent": true,
	"SysfsPCIeWidth":      true,
	"NVIDIAUUIDBinding":   true,
	"OperatorBaseline":    true,
}

func (r *jsonReader) decodeTrust() (rawTrust, error) {
	var t rawTrust
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "profileID":
			t.profileID, err = r.strField(func(s string) bool {
				return isASCII(s, 9, 128) && s[:8] == "offline:"
			})
		case "mode":
			t.mode, err = r.strField(exactRule("Offline"))
		case "session":
			t.session, err = r.decimalStringField(1, math.MaxInt64)
		case "sources":
			err = r.array(maxTrustSources, func(int) error {
				s, err := r.decodeTrustSource()
				t.sources = append(t.sources, s)
				return err
			})
			if err == nil && len(t.sources) == 0 {
				err = r.fail("sources is empty")
			}
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawTrust{}, err
	}
	return t, r.requireFields(seen, "profileID", "mode", "session", "sources")
}

func (r *jsonReader) decodeTrustSource() (rawTrustSource, error) {
	var s rawTrustSource
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "capability":
			s.capability, err = r.strField(func(v string) bool { return trustCapabilityNames[v] })
		case "source":
			s.sourceType, s.sourceName, err = r.decodeSourceRef()
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawTrustSource{}, err
	}
	return s, r.requireFields(seen, "capability", "source")
}

// decodeSourceRef reads {type, name}, both ASCII 1-256.
func (r *jsonReader) decodeSourceRef() (string, string, error) {
	var typ, name string
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "type":
			typ, err = r.strField(asciiRule(1, maxSourceBytes))
		case "name":
			name, err = r.strField(asciiRule(1, maxSourceBytes))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return "", "", err
	}
	return typ, name, r.requireFields(seen, "type", "name")
}

func (r *jsonReader) decodeFrame() (rawFrame, error) {
	var f rawFrame
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "nodeUID":
			f.nodeUID, err = r.strField(asciiRule(1, 128))
		case "bootID":
			f.bootID, err = r.strField(asciiRule(1, 128))
		case "session":
			f.session, err = r.decimalStringField(0, math.MaxUint64)
		case "sequence":
			f.sequence, err = r.decimalStringField(0, math.MaxUint64)
		case "completeness":
			f.completeness, err = r.strField(func(s string) bool { return s == "COMPLETE" || s == "PARTIAL" })
		case "observedAt":
			f.observedAt, err = r.strField(printableRule(1, 64))
		case "payload":
			f.payload, err = r.decodePayload()
		case "payloadDigest":
			f.payloadDigest, err = r.strField(isDigestHex)
		case "bundleRevision":
			f.bundleRevision, err = r.strField(printableRule(1, 256))
		case "diagnostics":
			err = r.array(maxDiagnostics, func(int) error {
				d, err := r.decodeDiagnostic()
				f.diagnostics = append(f.diagnostics, d)
				return err
			})
		case "diagnosticsTruncated":
			f.diagnosticsTruncated, err = r.boolean()
		case "diagnosticsTotal":
			f.diagnosticsTotal, err = r.uintNumber(0, math.MaxInt64)
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawFrame{}, err
	}
	return f, r.requireFields(seen, "nodeUID", "bootID", "session", "sequence", "completeness", "observedAt",
		"payload", "payloadDigest", "bundleRevision", "diagnostics", "diagnosticsTruncated", "diagnosticsTotal")
}

var diagnosticCodes = map[string]bool{
	"sysfs_list_failed":         true,
	"sysfs_entry_name_invalid":  true,
	"sysfs_link_invalid":        true,
	"sysfs_link_escape":         true,
	"sysfs_ancestor_missing":    true,
	"sysfs_ancestor_not_bridge": true,
	"sysfs_layout_unsupported":  true,
	"sysfs_attr_missing":        true,
	"sysfs_attr_permission":     true,
	"sysfs_attr_unreadable":     true,
	"sysfs_attr_malformed":      true,
	"sysfs_attr_nodata":         true,
	"topology_contradiction":    true,
	"width_omitted":             true,
	"baseline_peer_mismatch":    true,
	"baseline_unmatched":        true,
	"nvidia_output_absent":      true,
	"nvidia_output_unreadable":  true,
	"nvidia_output_limit":       true,
	"nvidia_empty":              true,
	"nvidia_malformed":          true,
	"nvidia_duplicate":          true,
	"nvidia_bdf_not_gpu":        true,
	"nvidia_gpu_unlisted":       true,
}

func (r *jsonReader) decodeDiagnostic() (rawDiagnostic, error) {
	var d rawDiagnostic
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "code":
			d.code, err = r.strField(func(s string) bool { return diagnosticCodes[s] })
		case "subject":
			d.subject, err = r.strField(asciiRule(1, maxDiagnosticBytes))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawDiagnostic{}, err
	}
	return d, r.requireFields(seen, "code", "subject")
}

func (r *jsonReader) decodePayload() (rawPayload, error) {
	var p rawPayload
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		switch key {
		case "assets":
			return r.array(maxAssets, func(int) error {
				a, err := r.decodeAsset()
				p.assets = append(p.assets, a)
				return err
			})
		case "edges":
			return r.array(maxEdges, func(int) error {
				e, err := r.decodeEdge()
				p.edges = append(p.edges, e)
				return err
			})
		case "edgeEvidence":
			return r.array(maxEdgeEvidence, func(int) error {
				e, err := r.decodeEdgeEvidence()
				p.edgeEvidence = append(p.edgeEvidence, e)
				return err
			})
		case "observations":
			return r.array(maxObservations, func(int) error {
				o, err := r.decodeObservation()
				p.observations = append(p.observations, o)
				return err
			})
		case "gpuBindings":
			return r.array(maxBindings, func(int) error {
				b, err := r.decodeBinding()
				p.gpuBindings = append(p.gpuBindings, b)
				return err
			})
		default:
			return errUnknownKey
		}
	})
	if err != nil {
		return rawPayload{}, err
	}
	return p, r.requireFields(seen, "assets", "edges", "edgeEvidence", "observations", "gpuBindings")
}

func (r *jsonReader) decodeAsset() (rawAsset, error) {
	var a rawAsset
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "kind":
			a.kind, err = r.strField(asciiRule(1, maxIdentBytes))
		case "canonical":
			a.canonical, err = r.strField(asciiRule(1, maxIdentBytes))
		case "aliases":
			a.aliases = []rawAlias{}
			err = r.array(maxAliases, func(int) error {
				al, err := r.decodeAlias()
				a.aliases = append(a.aliases, al)
				return err
			})
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawAsset{}, err
	}
	return a, r.requireFields(seen, "kind", "canonical", "aliases")
}

func (r *jsonReader) decodeAlias() (rawAlias, error) {
	var a rawAlias
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "namespace":
			a.namespace, err = r.strField(asciiRule(1, maxIdentBytes))
		case "value":
			a.value, err = r.strField(asciiRule(1, maxIdentBytes))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawAlias{}, err
	}
	return a, r.requireFields(seen, "namespace", "value")
}

func (r *jsonReader) decodeEdge() (rawEdge, error) {
	var e rawEdge
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "fromKey":
			e.fromKey, err = r.strField(asciiRule(1, maxIdentBytes))
		case "relation":
			e.relation, err = r.strField(exactRule("LOCATED_IN"))
		case "toKey":
			e.toKey, err = r.strField(asciiRule(1, maxIdentBytes))
		case "origin":
			e.origin, err = r.strField(exactRule("Observed"))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawEdge{}, err
	}
	return e, r.requireFields(seen, "fromKey", "relation", "toKey", "origin")
}

func (r *jsonReader) decodeEdgeEvidence() (rawEdgeEvidence, error) {
	var e rawEdgeEvidence
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "edgeIndex":
			e.edgeIndex, err = r.uintNumber(0, math.MaxUint32)
		case "kind":
			e.kind, err = r.strField(exactRule("Observed"))
		case "sourceType":
			e.sourceType, err = r.strField(asciiRule(1, maxSourceBytes))
		case "sourceName":
			e.sourceName, err = r.strField(asciiRule(1, maxSourceBytes))
		case "observedAt":
			e.observedAt, err = r.strField(printableRule(1, 64))
		case "expiresAt":
			e.expiresAt, err = r.strField(printableRule(1, 64))
		case "evidenceID":
			e.evidenceID, err = r.strField(asciiRule(1, maxIDBytes))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawEdgeEvidence{}, err
	}
	return e, r.requireFields(seen, "edgeIndex", "kind", "sourceType", "sourceName", "observedAt", "expiresAt", "evidenceID")
}

var qualityNames = map[string]bool{"Unknown": true, "Good": true, "Degraded": true}

func (r *jsonReader) decodeObservation() (rawObservation, error) {
	var o rawObservation
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "id":
			o.id, err = r.strField(asciiRule(1, maxIDBytes))
		case "source":
			o.sourceType, o.sourceName, err = r.decodeSourceRef()
		case "subject":
			o.subject, err = r.decodeAsset()
		case "signal":
			o.signal, err = r.strField(asciiRule(1, maxIdentBytes))
		case "value":
			o.value, err = r.decodeValue()
		case "unit":
			o.unit, err = r.strField(asciiRule(1, maxIdentBytes))
		case "dimensions":
			o.dimensions, err = r.decodeDimensions()
		case "observedAt":
			o.observedAt, err = r.strField(printableRule(1, 64))
		case "receivedAt":
			o.receivedAt, err = r.strField(printableRule(1, 64))
		case "expiresAt":
			o.expiresAt, err = r.strField(printableRule(1, 64))
		case "sequence":
			o.sequence, err = r.decimalStringField(0, math.MaxUint64)
		case "quality":
			o.quality, err = r.strField(func(s string) bool { return qualityNames[s] })
		case "rawDigest":
			o.rawDigest, err = r.strField(func(s string) bool { return s == "" || isASCII(s, 1, maxOptionalBytes) })
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawObservation{}, err
	}
	return o, r.requireFields(seen, "id", "source", "subject", "signal", "value", "unit", "dimensions",
		"observedAt", "receivedAt", "expiresAt", "sequence", "quality", "rawDigest")
}

func (r *jsonReader) decodeValue() (rawValue, error) {
	var v rawValue
	count := 0
	err := r.object(func(key string) error {
		switch key {
		case "int", "float", "bool", "string":
		default:
			return errUnknownKey
		}
		count++
		if count > 1 {
			return r.fail("value holds more than one kind")
		}
		v.kind = key
		switch key {
		case "int":
			s, err := r.str()
			if err != nil {
				return err
			}
			n, ok := parseDecimalInt(s)
			if !ok {
				return r.fail("int value is malformed or out of range")
			}
			v.integer = n
		case "float":
			s, err := r.str()
			if err != nil {
				return err
			}
			f, perr := strconv.ParseFloat(s, 64)
			if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) || strconv.FormatFloat(f, 'g', -1, 64) != s {
				return r.fail("float value is not a canonical finite number")
			}
			v.float = f
		case "bool":
			b, err := r.boolean()
			if err != nil {
				return err
			}
			v.boolean = b
		default:
			s, err := r.strField(printableRule(0, maxValueString))
			if err != nil {
				return err
			}
			v.str = s
		}
		return nil
	})
	if err != nil {
		return rawValue{}, err
	}
	if count != 1 {
		return rawValue{}, r.fail("value holds no kind")
	}
	return v, nil
}

func (r *jsonReader) decodeDimensions() ([]rawDimension, error) {
	dims := []rawDimension{}
	err := r.mapObject(func(key string) error {
		if len(dims) >= maxDimensions {
			return r.fail("too many dimensions")
		}
		if !isASCII(key, 1, maxIdentBytes) {
			return r.fail("dimension key does not satisfy its grammar")
		}
		value, err := r.strField(asciiRule(1, maxIdentBytes))
		if err != nil {
			return err
		}
		dims = append(dims, rawDimension{key: key, value: value})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortDimensions(dims)
	return dims, nil
}

func (r *jsonReader) decodeBinding() (rawBinding, error) {
	var b rawBinding
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "uuid":
			b.uuid, err = r.strField(isGPUUUID)
		case "serial":
			b.serial, err = r.strField(exactRule(""))
		case "bdf":
			b.bdf, err = r.strField(isCanonicalBDF)
		case "sourceType":
			b.sourceType, err = r.strField(asciiRule(1, maxSourceBytes))
		case "sourceName":
			b.sourceName, err = r.strField(asciiRule(1, maxSourceBytes))
		case "evidenceID":
			b.evidenceID, err = r.strField(asciiRule(1, maxIDBytes))
		case "observedAt":
			b.observedAt, err = r.strField(printableRule(1, 64))
		case "expiresAt":
			b.expiresAt, err = r.strField(printableRule(1, 64))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawBinding{}, err
	}
	return b, r.requireFields(seen, "uuid", "serial", "bdf", "sourceType", "sourceName", "evidenceID", "observedAt", "expiresAt")
}
