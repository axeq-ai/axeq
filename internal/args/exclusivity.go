package args

import (
	"fmt"
	"sort"
	"strings"
)

// validateExclusivity enforces a mutual-exclusivity constraint declared via one
// of the FlagRequested list fields (selected by listOf).
//
// A flag's group is itself plus the flags it lists. The declaration must be
// symmetric: every member of a group must declare exactly the same group, or it
// panics (a configuration error — also panics on an empty-but-non-nil list or a
// reference to an unknown flag). Each distinct group is then checked once: with
// requireOne, exactly one member must be set; otherwise at most one. Violations
// go through reportError as a single combined message.
func validateExclusivity(
	flagsRequested []FlagRequested,
	listOf func(FlagRequested) []string,
	fieldName string,
	requireOne bool,
	isSet func(string) bool,
	reportError func(string, ...any),
) {
	byName := make(map[string]FlagRequested, len(flagsRequested))
	for _, f := range flagsRequested {
		byName[f.Name] = f
	}

	// group returns the sorted, deduped members of a flag's group and a key.
	group := func(f FlagRequested) (members []string, key string) {
		members = uniqueSorted(append([]string{f.Name}, listOf(f)...))
		return members, strings.Join(members, "\x00")
	}

	processed := map[string]bool{}

	for _, f := range flagsRequested {
		list := listOf(f)
		if list == nil {
			continue
		}
		if len(list) == 0 {
			panic(fmt.Sprintf("flag %q: %s must not be an empty slice", f.Name, fieldName))
		}

		members, key := group(f)

		// Symmetry: every member must be a known flag declaring the same group.
		for _, m := range members {
			mf, ok := byName[m]
			if !ok {
				panic(fmt.Sprintf("flag %q: %s references unknown flag %q", f.Name, fieldName, m))
			}
			if _, mKey := group(mf); mKey != key {
				panic(fmt.Sprintf("%s is not symmetric: flags %q and %q declare different groups", fieldName, f.Name, m))
			}
		}

		if processed[key] {
			continue
		}
		processed[key] = true

		setCount := 0
		for _, m := range members {
			if isSet(m) {
				setCount++
			}
		}

		names := quoteJoin(members)
		if requireOne {
			if setCount != 1 {
				state := "none are set"
				if setCount > 1 {
					state = "multiple are set"
				}
				reportError("Exactly one of %s must be set; %s", names, state)
			}
		} else if setCount > 1 {
			reportError("At most one of %s may be set; multiple are set", names)
		}
	}
}

func uniqueSorted(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func quoteJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}
