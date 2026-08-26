package usage

import "sort"

// rankModels reduces a per-model turn count to a primary model and, when
// more than one served the session, the full list most-used first.
//
// Ties break on the model name so the result is deterministic: a tally
// lands in a snapshot that gets compared across runs, and a field that
// reorders itself between reads of the same transcript would read as a
// change that did not happen.
func rankModels(perModel map[string]int) (primary string, all []string) {
	if len(perModel) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(perModel))
	for m := range perModel {
		names = append(names, m)
	}
	sort.Slice(names, func(i, j int) bool {
		if perModel[names[i]] != perModel[names[j]] {
			return perModel[names[i]] > perModel[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) == 1 {
		return names[0], nil
	}
	return names[0], names
}

// Tier maps a served model id to the tier a task would have asked for, so
// a requested tier and a served model can be compared at all. Returns ""
// for a model that names no tier we dispatch on.
//
// The match is on substring rather than an exact table because the ids
// carry dated suffixes — claude-haiku-4-5-20251001 — and a table keyed on
// the full id goes stale every model release, silently reporting "no
// tier" for the model that is actually serving everything.
func Tier(model string) string {
	switch {
	case model == "":
		return ""
	case contains(model, "opus"):
		return "opus"
	case contains(model, "sonnet"):
		return "sonnet"
	case contains(model, "haiku"):
		return "haiku"
	}
	return ""
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
