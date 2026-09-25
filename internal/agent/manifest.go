package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Manifest bounds and fixed values (GFO-031, GFO-032, GFO-095).
const (
	manifestMaxBytes      = 65536
	manifestSchemaVersion = "dpa.offline-fixture/v1"
	offlineProfilePrefix  = "offline:"
	maxFrames             = 16
	maxTrustSources       = 16
	maxOperatorBaselines  = 256
	maxEvidenceTTLSeconds = 86400
)

// Trust capability names a manifest may grant.
const (
	capabilitySysfsPhysicalParent = "SysfsPhysicalParent"
	capabilitySysfsPCIeWidth      = "SysfsPCIeWidth"
	capabilityNVIDIAUUIDBinding   = "NVIDIAUUIDBinding"
	capabilityOperatorBaseline    = "OperatorBaseline"
)

var clusterIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// observedAtPattern is the accepted timestamp shape: a four-digit year,
// uppercase T and Z, an optional fraction of one to nine digits, and either Z
// or a numeric offset.
var observedAtPattern = regexp.MustCompile(
	`^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(?:(Z)|([+-])([0-9]{2}):([0-9]{2}))$`)

// integerPattern is an unsigned decimal literal without sign, fraction,
// exponent or superfluous leading zero.
var integerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// Bounds of the accepted timestamps (GFO-033 v1.1): the GFL-013 range minus
// the zero instant 0001-01-01T00:00:00Z, which no Validate accepts.
var (
	minTimestamp = time.Date(1, time.January, 1, 0, 0, 0, 1, time.UTC)
	maxTimestamp = time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
)

// validWidths is the set of PCIe link widths a baseline or an attribute may
// state.
var validWidths = map[uint64]struct{}{1: {}, 2: {}, 4: {}, 8: {}, 12: {}, 16: {}, 32: {}}

// trustSource is one manifest trust grant.
type trustSource struct {
	capability, sourceType, sourceName string
}

// operatorBaseline is one operator-verified wiring claim.
type operatorBaseline struct {
	function, peer string
	width          int64
}

// frameTimes are the instants of one frame, both in UTC.
type frameTimes struct {
	observedAt, expiresAt time.Time
}

// manifest is a validated fixture manifest.
type manifest struct {
	clusterID string
	nodeName  string
	nodeUID   string
	bootID    string
	profileID string
	session   int64
	sources   []trustSource
	frames    []frameTimes
	baselines []operatorBaseline
}

// parseManifest validates data against GFO-031..033 and returns the manifest.
// Every error wraps ErrFixtureInvalid and names a field, never a value.
func parseManifest(data []byte) (manifest, error) {
	if len(data) > manifestMaxBytes {
		return manifest{}, fixtureInvalidf("manifest exceeds %d bytes", manifestMaxBytes)
	}
	doc, err := decodeStrictJSON(data)
	if err != nil {
		return manifest{}, fixtureInvalidf("manifest: %v", err)
	}
	var m manifest
	top, err := objectFields(doc, "manifest",
		[]string{"schemaVersion", "clusterID", "node", "bootID", "trust", "evidenceTTLSeconds", "frames"},
		[]string{"operatorBaselines"})
	if err != nil {
		return manifest{}, err
	}

	version, err := stringField(top["schemaVersion"], "schemaVersion")
	if err != nil {
		return manifest{}, err
	}
	if version != manifestSchemaVersion {
		return manifest{}, fixtureInvalidf("manifest schemaVersion is not %s", manifestSchemaVersion)
	}
	if m.clusterID, err = stringField(top["clusterID"], "clusterID"); err != nil {
		return manifest{}, err
	}
	if !clusterIDPattern.MatchString(m.clusterID) {
		return manifest{}, fixtureInvalidf("manifest clusterID is invalid")
	}

	node, err := objectFields(top["node"], "node", []string{"name", "uid"}, nil)
	if err != nil {
		return manifest{}, err
	}
	if m.nodeName, err = asciiField(node["name"], "node.name", 1, 253); err != nil {
		return manifest{}, err
	}
	if m.nodeUID, err = asciiField(node["uid"], "node.uid", 1, 128); err != nil {
		return manifest{}, err
	}
	if m.bootID, err = asciiField(top["bootID"], "bootID", 1, 128); err != nil {
		return manifest{}, err
	}

	if err := m.parseTrust(top["trust"]); err != nil {
		return manifest{}, err
	}

	ttl, err := integerField(top["evidenceTTLSeconds"], "evidenceTTLSeconds", 1, maxEvidenceTTLSeconds)
	if err != nil {
		return manifest{}, err
	}
	if err := m.parseFrames(top["frames"], ttl); err != nil {
		return manifest{}, err
	}
	if v, ok := top["operatorBaselines"]; ok {
		if err := m.parseBaselines(v); err != nil {
			return manifest{}, err
		}
	}
	return m, nil
}

// parseTrust reads the trust object.
func (m *manifest) parseTrust(v jsonValue) error {
	trust, err := objectFields(v, "trust", []string{"profileID", "session", "sources"}, nil)
	if err != nil {
		return err
	}
	if m.profileID, err = asciiField(trust["profileID"], "trust.profileID", 9, 128); err != nil {
		return err
	}
	if !strings.HasPrefix(m.profileID, offlineProfilePrefix) {
		return fixtureInvalidf("manifest trust.profileID lacks the %s prefix", offlineProfilePrefix)
	}
	if m.session, err = integerField(trust["session"], "trust.session", 1, 9223372036854775807); err != nil {
		return err
	}
	items, err := arrayField(trust["sources"], "trust.sources", 1, maxTrustSources)
	if err != nil {
		return err
	}
	seen := map[trustSource]struct{}{}
	for i, item := range items {
		name := fmt.Sprintf("trust.sources[%d]", i)
		f, err := objectFields(item, name, []string{"capability", "sourceType", "sourceName"}, nil)
		if err != nil {
			return err
		}
		var s trustSource
		if s.capability, err = stringField(f["capability"], name+".capability"); err != nil {
			return err
		}
		switch s.capability {
		case capabilitySysfsPhysicalParent, capabilitySysfsPCIeWidth, capabilityNVIDIAUUIDBinding, capabilityOperatorBaseline:
		default:
			return fixtureInvalidf("manifest %s.capability is not an accepted capability", name)
		}
		if s.sourceType, err = asciiField(f["sourceType"], name+".sourceType", 1, 256); err != nil {
			return err
		}
		if s.sourceName, err = asciiField(f["sourceName"], name+".sourceName", 1, 256); err != nil {
			return err
		}
		if _, dup := seen[s]; dup {
			return fixtureInvalidf("manifest %s repeats a capability and source", name)
		}
		seen[s] = struct{}{}
		m.sources = append(m.sources, s)
	}
	return nil
}

// parseFrames reads the frames array and derives each frame's instants.
func (m *manifest) parseFrames(v jsonValue, ttl int64) error {
	items, err := arrayField(v, "frames", 1, maxFrames)
	if err != nil {
		return err
	}
	for i, item := range items {
		name := fmt.Sprintf("frames[%d]", i)
		f, err := objectFields(item, name, []string{"observedAt"}, nil)
		if err != nil {
			return err
		}
		text, err := stringField(f["observedAt"], name+".observedAt")
		if err != nil {
			return err
		}
		observed, ok := parseObservedAt(text)
		if !ok {
			return fixtureInvalidf("manifest %s.observedAt is not a valid timestamp", name)
		}
		expires := observed.Add(time.Duration(ttl) * time.Second)
		if expires.After(maxTimestamp) {
			return fixtureInvalidf("manifest %s expiry is outside the timestamp range", name)
		}
		if i > 0 && !observed.After(m.frames[i-1].observedAt) {
			return fixtureInvalidf("manifest %s.observedAt does not strictly follow the previous frame", name)
		}
		m.frames = append(m.frames, frameTimes{observedAt: observed, expiresAt: expires})
	}
	return nil
}

// parseBaselines reads the optional operatorBaselines array.
func (m *manifest) parseBaselines(v jsonValue) error {
	items, err := arrayField(v, "operatorBaselines", 0, maxOperatorBaselines)
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for i, item := range items {
		name := fmt.Sprintf("operatorBaselines[%d]", i)
		f, err := objectFields(item, name, []string{"function", "peer", "expectedWidth"}, nil)
		if err != nil {
			return err
		}
		var b operatorBaseline
		if b.function, err = stringField(f["function"], name+".function"); err != nil {
			return err
		}
		if b.peer, err = stringField(f["peer"], name+".peer"); err != nil {
			return err
		}
		if !isCanonicalBDF(b.function) || !isCanonicalBDF(b.peer) {
			return fixtureInvalidf("manifest %s names a noncanonical address", name)
		}
		if b.function == b.peer {
			return fixtureInvalidf("manifest %s names the same function and peer", name)
		}
		if b.width, err = integerField(f["expectedWidth"], name+".expectedWidth", 1, 32); err != nil {
			return err
		}
		if _, ok := validWidths[uint64(b.width)]; !ok {
			return fixtureInvalidf("manifest %s.expectedWidth is not a PCIe link width", name)
		}
		if _, dup := seen[b.function]; dup {
			return fixtureInvalidf("manifest %s repeats a function", name)
		}
		seen[b.function] = struct{}{}
		m.baselines = append(m.baselines, b)
	}
	return nil
}

// parseObservedAt converts a manifest timestamp to its UTC instant. It reports
// false for any other shape, an invalid calendar date or time of day, or an
// instant outside the accepted range, which excludes the zero instant.
func parseObservedAt(s string) (time.Time, bool) {
	m := observedAtPattern.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, minute, second := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	nanos := 0
	if frac := m[7]; frac != "" {
		nanos = atoi(frac + strings.Repeat("0", 9-len(frac)))
	}
	offset := 0
	if m[8] != "Z" {
		oh, om := atoi(m[10]), atoi(m[11])
		if oh > 23 || om > 59 {
			return time.Time{}, false
		}
		offset = oh*3600 + om*60
		if m[9] == "-" {
			offset = -offset
		}
	}
	local := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.FixedZone("", offset))
	if local.Year() != year || int(local.Month()) != month || local.Day() != day {
		return time.Time{}, false
	}
	utc := local.UTC()
	if utc.Before(minTimestamp) || utc.After(maxTimestamp) {
		return time.Time{}, false
	}
	return utc, true
}

// atoi converts a string of at most nine ASCII digits, as guaranteed by the
// callers' patterns.
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// objectFields checks that v is an object whose keys are all in required or
// optional and include every required key, and returns its members by key.
// The strict decoder has already rejected duplicate keys.
func objectFields(v jsonValue, name string, required, optional []string) (map[string]jsonValue, error) {
	if v.kind != jsonObject {
		return nil, fixtureInvalidf("manifest %s is not an object", name)
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, k := range required {
		allowed[k] = true
	}
	for _, k := range optional {
		allowed[k] = false
	}
	fields := make(map[string]jsonValue, len(v.members))
	for _, member := range v.members {
		if _, ok := allowed[member.key]; !ok {
			return nil, fixtureInvalidf("manifest %s has an unknown key", name)
		}
		fields[member.key] = member.value
	}
	for _, k := range required {
		if _, ok := fields[k]; !ok {
			return nil, fixtureInvalidf("manifest %s lacks %s", name, k)
		}
	}
	return fields, nil
}

// arrayField checks that v is an array of min to max items.
func arrayField(v jsonValue, name string, minItems, maxItems int) ([]jsonValue, error) {
	if v.kind != jsonArray {
		return nil, fixtureInvalidf("manifest %s is not an array", name)
	}
	if len(v.items) < minItems || len(v.items) > maxItems {
		return nil, fixtureInvalidf("manifest %s must hold %d to %d items", name, minItems, maxItems)
	}
	return v.items, nil
}

// stringField checks that v is a string.
func stringField(v jsonValue, name string) (string, error) {
	if v.kind != jsonString {
		return "", fixtureInvalidf("manifest %s is not a string", name)
	}
	return v.text, nil
}

// asciiField checks that v is a string of min to max bytes, each 0x21..0x7e.
func asciiField(v jsonValue, name string, minLen, maxLen int) (string, error) {
	s, err := stringField(v, name)
	if err != nil {
		return "", err
	}
	if len(s) < minLen || len(s) > maxLen {
		return "", fixtureInvalidf("manifest %s must be %d to %d bytes", name, minLen, maxLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return "", fixtureInvalidf("manifest %s holds a byte outside 0x21-0x7e", name)
		}
	}
	return s, nil
}

// integerField checks that v is an unsigned decimal integer literal whose value
// is within [minValue, maxValue].
func integerField(v jsonValue, name string, minValue, maxValue int64) (int64, error) {
	if v.kind != jsonNumber || !integerPattern.MatchString(v.text) {
		return 0, fixtureInvalidf("manifest %s is not a decimal integer", name)
	}
	n, err := strconv.ParseInt(v.text, 10, 64)
	if err != nil || n < minValue || n > maxValue {
		return 0, fixtureInvalidf("manifest %s is outside %d..%d", name, minValue, maxValue)
	}
	return n, nil
}

// fixtureInvalidf builds an ErrFixtureInvalid error.
func fixtureInvalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFixtureInvalid, fmt.Sprintf(format, args...))
}
