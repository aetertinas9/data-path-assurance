package offline

import (
	"math"
	"strconv"
	"time"

	"github.com/aetertinas9/data-path-assurance/internal/fleet"
)

// Fleet file bounds.
const (
	maxFleetDevices  = 256
	maxFleetCoverage = 32
)

type rawFleetFile struct {
	clusterID        string
	fleetName        string
	fleetUID         string
	policyRevision   string
	freshnessSeconds uint64
	readyForSeconds  uint64
	requiredCoverage []fleet.CoverageRequirement
	devices          []rawFleetDevice
}

type rawFleetDevice struct {
	name, uid          string
	nodeName, nodeUID  string
	desired            fleet.DesiredState
	requestID          string
	metadataGeneration uint64
	intentObservedAt   time.Time
	vendor             string
	uuid, serial       string
	hasUUID            bool
	source, evidenceID string
}

// fleetContent is a validated fleet file converted to ratified values.
type fleetContent struct {
	clusterID string
	fleetUID  string
	policy    fleet.Policy
	devices   []fleet.Intent
}

var pathKinds = map[string]bool{
	"gpu-pcie-parent":            true,
	"gpu-pcie-root":              true,
	"gpu-pcie-link-width-normal": true,
	"gpu-nic-shared-ancestor":    true,
	"nic-lldp-remote":            true,
}

var desiredByName = map[string]fleet.DesiredState{
	"InService":   fleet.DesiredInService,
	"Maintenance": fleet.DesiredMaintenance,
	"Retired":     fleet.DesiredRetired,
}

func fleetFail(where, problem string) error {
	return inputError(ErrFleetInvalid, where+": "+problem)
}

// decodeFleet reads and validates the fleet file and converts it to the
// policy and device intents.
func decodeFleet(data []byte) (fleetContent, error) {
	r, err := newJSONReader(data, ErrFleetInvalid)
	if err != nil {
		return fleetContent{}, err
	}
	var f rawFleetFile
	seen := fieldSet{}
	err = r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "schemaVersion":
			_, err = r.strField(exactRule("dpa.offline-fleet/v1"))
		case "clusterID":
			f.clusterID, err = r.strField(isClusterID)
		case "fleet":
			err = r.decodeFleetRef(&f)
		case "policy":
			err = r.decodePolicy(&f)
		case "devices":
			f.devices = []rawFleetDevice{}
			err = r.array(maxFleetDevices, func(int) error {
				d, err := r.decodeFleetDevice()
				f.devices = append(f.devices, d)
				return err
			})
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return fleetContent{}, err
	}
	if err := r.requireFields(seen, "schemaVersion", "clusterID", "fleet", "policy", "devices"); err != nil {
		return fleetContent{}, err
	}
	if err := r.end(); err != nil {
		return fleetContent{}, err
	}
	return convertFleet(&f)
}

func (r *jsonReader) decodeFleetRef(f *rawFleetFile) error {
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "name":
			f.fleetName, err = r.strField(isDNSName)
		case "uid":
			f.fleetUID, err = r.strField(asciiRule(1, 128))
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

func (r *jsonReader) decodePolicy(f *rawFleetFile) error {
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "revision":
			f.policyRevision, err = r.strField(asciiRule(1, 128))
		case "freshnessSeconds":
			f.freshnessSeconds, err = r.uintNumber(1, 86400)
		case "readyForSeconds":
			f.readyForSeconds, err = r.uintNumber(1, 86400)
		case "requiredCoverage":
			f.requiredCoverage = []fleet.CoverageRequirement{}
			err = r.array(maxFleetCoverage, func(int) error {
				c, err := r.decodeCoverageRequirement()
				f.requiredCoverage = append(f.requiredCoverage, c)
				return err
			})
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return err
	}
	return r.requireFields(seen, "revision", "freshnessSeconds", "readyForSeconds", "requiredCoverage")
}

func (r *jsonReader) decodeCoverageRequirement() (fleet.CoverageRequirement, error) {
	var c fleet.CoverageRequirement
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "name":
			c.Name, err = r.strField(isCoverageName)
		case "pathKind":
			c.PathKind, err = r.strField(func(s string) bool { return pathKinds[s] })
		case "required":
			c.Required, err = r.boolean()
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return fleet.CoverageRequirement{}, err
	}
	return c, r.requireFields(seen, "name", "pathKind", "required")
}

func (r *jsonReader) decodeFleetDevice() (rawFleetDevice, error) {
	var d rawFleetDevice
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "name":
			d.name, err = r.strField(isDNSName)
		case "uid":
			d.uid, err = r.strField(asciiRule(1, 128))
		case "nodeRef":
			err = r.decodeNodeRef(&d)
		case "desiredState":
			var s string
			s, err = r.strField(func(v string) bool { _, ok := desiredByName[v]; return ok })
			d.desired = desiredByName[s]
		case "requestID":
			d.requestID, err = r.strField(asciiRule(1, 128))
		case "metadataGeneration":
			d.metadataGeneration, err = r.uintNumber(1, math.MaxInt64)
		case "intentObservedAt":
			var s string
			s, err = r.str()
			if err == nil {
				t, ok := parseTimestamp(s)
				if !ok {
					return r.fail("timestamp is malformed or out of range")
				}
				if !hasEvaluationMargin(t) {
					return r.fail("timestamp leaves no evaluation margin inside the timestamp range")
				}
				d.intentObservedAt = t
			}
		case "inventoryClaim":
			err = r.decodeClaim(&d)
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return rawFleetDevice{}, err
	}
	return d, r.requireFields(seen, "name", "uid", "nodeRef", "desiredState", "requestID",
		"metadataGeneration", "intentObservedAt", "inventoryClaim")
}

func (r *jsonReader) decodeNodeRef(d *rawFleetDevice) error {
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "name":
			d.nodeName, err = r.strField(asciiRule(1, 253))
		case "uid":
			d.nodeUID, err = r.strField(asciiRule(1, 128))
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

func (r *jsonReader) decodeClaim(d *rawFleetDevice) error {
	seen := fieldSet{}
	err := r.object(func(key string) error {
		seen[key] = true
		var err error
		switch key {
		case "vendor":
			d.vendor, err = r.strField(exactRule(nvidiaVendor))
		case "uuid":
			d.uuid, err = r.strField(isGPUUUID)
			d.hasUUID = true
		case "serial":
			d.serial, err = r.strField(asciiRule(1, 128))
		case "source":
			d.source, err = r.strField(asciiRule(1, 256))
		case "evidenceID":
			d.evidenceID, err = r.strField(asciiRule(1, 128))
		default:
			return errUnknownKey
		}
		return err
	})
	if err != nil {
		return err
	}
	if err := r.requireFields(seen, "vendor", "source", "evidenceID"); err != nil {
		return err
	}
	if !seen["uuid"] && !seen["serial"] {
		return r.fail("claim needs a uuid or a serial")
	}
	return nil
}

// convertFleet applies the uniqueness rules and the domain validation of the
// policy and every intent.
func convertFleet(f *rawFleetFile) (fleetContent, error) {
	names := map[string]bool{}
	for i, c := range f.requiredCoverage {
		if names[c.Name] {
			return fleetContent{}, fleetFail("policy.requiredCoverage["+strconv.Itoa(i)+"].name", "duplicate name")
		}
		names[c.Name] = true
	}
	policy, err := fleet.NewPolicy(fleet.Policy{
		Revision:         f.policyRevision,
		RequiredCoverage: f.requiredCoverage,
		Freshness:        time.Duration(f.freshnessSeconds) * time.Second,
		ReadyFor:         time.Duration(f.readyForSeconds) * time.Second,
	})
	if err != nil {
		return fleetContent{}, fleetFail("policy", "policy is rejected by domain validation")
	}
	out := fleetContent{clusterID: f.clusterID, fleetUID: f.fleetUID, policy: policy}
	deviceNames := map[string]bool{}
	deviceUIDs := map[string]bool{}
	uuids := map[string]bool{}
	for i, d := range f.devices {
		where := "devices[" + strconv.Itoa(i) + "]"
		if deviceNames[d.name] {
			return fleetContent{}, fleetFail(where+".name", "duplicate device name")
		}
		deviceNames[d.name] = true
		if deviceUIDs[d.uid] {
			return fleetContent{}, fleetFail(where+".uid", "duplicate device uid")
		}
		deviceUIDs[d.uid] = true
		if d.hasUUID {
			if uuids[d.uuid] {
				return fleetContent{}, fleetFail(where+".inventoryClaim.uuid", "duplicate claimed uuid")
			}
			uuids[d.uuid] = true
		}
		intent := fleet.Intent{
			Device:             fleet.DeviceRef{Name: d.name, UID: d.uid},
			Node:               fleet.NodeRef{ClusterID: f.clusterID, Name: d.nodeName, UID: d.nodeUID},
			Desired:            d.desired,
			RequestID:          d.requestID,
			MetadataGeneration: int64(d.metadataGeneration),
			ObservedAt:         d.intentObservedAt,
			Claim: fleet.InventoryClaim{
				Vendor:     d.vendor,
				UUID:       d.uuid,
				Serial:     d.serial,
				Source:     d.source,
				EvidenceID: d.evidenceID,
			},
		}
		if err := intent.Validate(); err != nil {
			return fleetContent{}, fleetFail(where, "intent is rejected by domain validation")
		}
		out.devices = append(out.devices, intent)
	}
	return out, nil
}
