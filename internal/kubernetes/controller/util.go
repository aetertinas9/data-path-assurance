package controller

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func sortStrings(s []string) { sort.Strings(s) }

// validTime reports whether t is a usable, non-zero timestamp. The year is
// taken in UTC, the form metaTime and optTime write, so the answer does not
// depend on t's Location.
func validTime(t time.Time) bool {
	y := t.UTC().Year()
	return !t.IsZero() && y >= 1 && y <= 9999
}

func metaTime(t time.Time) metav1.Time { return metav1.NewTime(t.UTC()) }

// optTime returns nil for the zero time.
func optTime(t time.Time) *metav1.Time {
	if t.IsZero() {
		return nil
	}
	m := metav1.NewTime(t.UTC())
	return &m
}

func strPtr(s string) *string { return &s }

// sortedUnique returns the sorted distinct values of in.
func sortedUnique(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	w := 0
	for i, s := range out {
		if i == 0 || s != out[w-1] {
			out[w] = s
			w++
		}
	}
	return out[:w]
}
