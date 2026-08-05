package model

import "strconv"

// enumInRange reports whether n names one of count enumerated constants whose
// first constant has the numeric value offset.
func enumInRange(n, offset, count int) bool {
	i := n - offset
	return i >= 0 && i < count
}

// enumName renders n as names[n-offset] when n is enumerated, and as the
// diagnostic form "TypeName(n)" otherwise.
//
// Only the enumerated renderings are part of the contract. The diagnostic form
// exists so that String never panics on a value that escaped validation, and so
// that such a value can never be mistaken for an enumerated one.
func enumName(typeName string, n, offset int, names []string) string {
	if i := n - offset; i >= 0 && i < len(names) {
		return names[i]
	}
	return typeName + "(" + strconv.Itoa(n) + ")"
}
