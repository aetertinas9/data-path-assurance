package model

// The helpers below implement the defensive copying every constructor performs.
// A nil slice or map is copied as nil so that a caller cannot distinguish a
// constructed value from the input by nil-ness alone.

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func cloneTypedIDs(ids []TypedID) []TypedID {
	if ids == nil {
		return nil
	}
	c := make([]TypedID, len(ids))
	for i, id := range ids {
		c[i] = id.clone()
	}
	return c
}

func cloneAssetRefs(refs []AssetRef) []AssetRef {
	if refs == nil {
		return nil
	}
	c := make([]AssetRef, len(refs))
	for i, r := range refs {
		c[i] = r.clone()
	}
	return c
}

func cloneEvidenceRefs(refs []EvidenceRef) []EvidenceRef {
	if refs == nil {
		return nil
	}
	c := make([]EvidenceRef, len(refs))
	copy(c, refs)
	return c
}

func cloneSignalRefs(sigs []SignalRef) []SignalRef {
	if sigs == nil {
		return nil
	}
	c := make([]SignalRef, len(sigs))
	copy(c, sigs)
	return c
}

func cloneImpactRefs(refs []ImpactRef) []ImpactRef {
	if refs == nil {
		return nil
	}
	c := make([]ImpactRef, len(refs))
	for i, r := range refs {
		c[i] = r.clone()
	}
	return c
}
