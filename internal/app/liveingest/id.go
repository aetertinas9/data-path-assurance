package liveingest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// The tags of the deterministic evidence IDs.
const (
	idPrefixEdgeEvidence = "pe"
	idPrefixObservation  = "ob"
	idPrefixGPUBinding   = "nb"
	idTagEdgeEvidence    = "dpa.edge-evidence.v1"
	idTagObservation     = "dpa.observation.v1"
	idTagGPUBinding      = "dpa.gpu-binding.v1"
)

// deterministicID renders <prefix>:<session>:<sequence>:<H>, where H is the
// first 16 bytes, in lowercase hex, of the SHA-256 of the parts, each written
// as its 32-bit big-endian length followed by its bytes.
func deterministicID(prefix string, session int64, sequence uint64, parts ...string) string {
	h := sha256.New()
	var n [4]byte
	for _, p := range parts {
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(p))
	}
	sum := h.Sum(nil)
	return prefix + ":" + strconv.FormatInt(session, 10) + ":" + strconv.FormatUint(sequence, 10) + ":" + hex.EncodeToString(sum[:16])
}

// EdgeEvidenceID is the evidence ID of the edge fromKey -relation-> toKey of
// the given origin in the frame with this session and sequence.
func EdgeEvidenceID(session int64, sequence uint64, fromKey, relation, toKey, origin string) string {
	return deterministicID(idPrefixEdgeEvidence, session, sequence, idTagEdgeEvidence, fromKey, relation, toKey, origin)
}

// ObservationID is the ID of the observation of signal about the asset with
// key subjectKey in the frame with this session and sequence.
func ObservationID(session int64, sequence uint64, subjectKey, signal string) string {
	return deterministicID(idPrefixObservation, session, sequence, idTagObservation, subjectKey, signal)
}

// BindingEvidenceID is the evidence ID of the binding of the GPU uuid to the
// function at bdf in the frame with this session and sequence.
func BindingEvidenceID(session int64, sequence uint64, uuid, bdf string) string {
	return deterministicID(idPrefixGPUBinding, session, sequence, idTagGPUBinding, uuid, bdf)
}

// Revision renders the bundle revision of a frame:
// <session>:<sequence>:<digest in lowercase hex>, all in decimal without
// leading zeros.
func Revision(session int64, sequence uint64, digest [32]byte) string {
	return strconv.FormatInt(session, 10) + ":" + strconv.FormatUint(sequence, 10) + ":" + hex.EncodeToString(digest[:])
}
