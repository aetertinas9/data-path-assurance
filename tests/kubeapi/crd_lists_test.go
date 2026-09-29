package kubeapi_test

// API-server behavior of list bounds and list types (GKA-043): maxItems at the
// limit and one past it, list-map key uniqueness and required keys, atomic
// lists that tolerate duplicates.

import (
	"fmt"
	"strings"
	"testing"
)

func gkaCListOf(l gkaCList, n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, l.Make(i))
	}
	return out
}

// GKA-043: every bounded list accepts exactly its maximum and rejects one more.
func TestGKA043_ListMaxItems(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, l := range gkaCRows(gkaCLists, func(l gkaCList) string { return l.Plural }, k.Res.Plural) {
			what := fmt.Sprintf("GKA-043 %s %s", l.Plural, l.Path)
			gkaCOK(t, fmt.Sprintf("%s with %d items", what, l.Max), gkaCTry(e, k, base, dry, l.Path, gkaCListOf(l, l.Max), false))
			gkaCInvalid(t, fmt.Sprintf("%s with %d items", what, l.Max+1),
				gkaCTry(e, k, base, dry, l.Path, gkaCListOf(l, l.Max+1), false),
				fmt.Sprintf("at most %d items", l.Max))
		}
	}
}

// GKA-043: list-map keys are unique and required.
func TestGKA043_ListMapKeys(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, l := range gkaCRows(gkaCLists, func(l gkaCList) string { return l.Plural }, k.Res.Plural) {
			if l.Atomic {
				continue
			}
			what := fmt.Sprintf("GKA-043 %s %s", l.Plural, l.Path)
			dup := []any{l.Make(0), l.Make(0)}
			gkaCInvalid(t, what+" with a duplicate "+l.Key, gkaCTry(e, k, base, dry, l.Path, dup, false), "Duplicate value")
			distinct := []any{l.Make(0), l.Make(1)}
			gkaCOK(t, what+" with two distinct keys", gkaCTry(e, k, base, dry, l.Path, distinct, false))
			noKey, _ := l.Make(0).(map[string]any)
			delete(noKey, l.Key)
			gkaCInvalid(t, what+" element without its key "+l.Key, gkaCTry(e, k, base, dry, l.Path, []any{noKey}, false), "Required value")
		}
		// conditions: key is type.
		what := "GKA-043 " + k.Res.Plural + " status.conditions"
		types := gkaCCondTypes[k.Res.Plural]
		dupConds := []any{gkaCCond(types[0], "Unknown", "Validating", "m"), gkaCCond(types[0], "False", "Degraded", "n")}
		gkaCInvalid(t, what+" with a duplicate type", gkaCTry(e, k, base, dry, "status.conditions", dupConds, false), "Duplicate value")
		noType := gkaCCond(types[0], "Unknown", "Validating", "m")
		delete(noType, "type")
		gkaCInvalid(t, what+" element without type", gkaCTry(e, k, base, dry, "status.conditions", []any{noType}, false), "Required value")
	}
}

// GKA-043: atomic lists have no key, so duplicates are legal; empty status
// lists are legal too.
func TestGKA043_AtomicListsAndEmptyLists(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		for _, l := range gkaCRows(gkaCLists, func(l gkaCList) string { return l.Plural }, k.Res.Plural) {
			what := fmt.Sprintf("GKA-043 %s %s", l.Plural, l.Path)
			if l.Atomic {
				gkaCOK(t, what+" with duplicate strings", gkaCTry(e, k, base, dry, l.Path, []any{"same", "same"}, false))
				continue
			}
			if !strings.HasPrefix(l.Path, "status.") {
				continue // spec lists have no stated minimum
			}
			gkaCOK(t, what+" empty", gkaCTry(e, k, base, dry, l.Path, []any{}, false))
		}
		gkaCOK(t, "GKA-043 "+k.Res.Plural+" status.conditions empty", gkaCTry(e, k, base, dry, "status.conditions", []any{}, false))
	}
}

// GKA-043: conditions hold at most 32 entries. A 33rd is rejected by maxItems.
// Thirty-two distinct types cannot all be allowed types (GKA-046), so the 32
// case is rejected by the type rule, not by maxItems: the boundary is shown by
// the error text.
func TestGKA043_ConditionsMaxItems(t *testing.T) {
	e := gkaCStart(t)
	for _, k := range gkaCKits {
		base := gkaCBaseObject(t, e, k)
		dry := gkaName(t, "dry-"+k.Res.Singular)
		build := func(n int) []any {
			out := make([]any, 0, n)
			for i := 0; i < n; i++ {
				out = append(out, gkaCCond(fmt.Sprintf("Type%02d", i), "Unknown", "Validating", "m"))
			}
			return out
		}
		what := "GKA-043 " + k.Res.Plural + " conditions"
		gkaCInvalid(t, what+" with 33 items", gkaCTry(e, k, base, dry, "status.conditions", build(33), false), "at most 32 items")
		err := gkaCTry(e, k, base, dry, "status.conditions", build(32), false)
		gkaCInvalid(t, what+" with 32 items (rejected by the type rule only)", err, gkaCCondMessage)
		if err != nil && strings.Contains(err.Error(), "at most 32 items") {
			t.Errorf("%s with 32 items: the maxItems limit fired at exactly the limit: %v", what, err)
		}
	}
}
