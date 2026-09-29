package controller

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func sortStrings(s []string) { sort.Strings(s) }

// validTime reports whether t is a usable, non-zero timestamp.
func validTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999
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
