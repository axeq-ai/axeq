package args

import (
	"fmt"
	"os"
	"sort"
)

// ValidateArgs checks the received positional args and flags against a
// command's declared requirements and returns cleaned-up copies containing
// only what was requested.
//
// Positional args are matched by position to argsRequested: a missing required
// arg is an error, and any extra args beyond those requested are an error.
//
// For each requested flag the received value count must fall within
// [MinimumCount, MaximumCount]; a missing required flag is an error, and any
// flag that was not requested is an error. When LastWins is set and more than
// one value was received, only the final value is kept (still returned as a
// slice).
//
// All validation errors are collected and printed to stderr (not just the
// first), so the user sees everything wrong in one pass. If there is any error,
// it then prints the usage message and exits 1. Otherwise it returns the cleaned
// args and cleaned flags. It never returns on error — it exits the process.
func ValidateArgs(
	command string,
	argsReceived []string,
	flagsReceived map[string][]string,
	argsRequested []ArgRequested,
	flagsRequested []FlagRequested,
) ([]string, map[string][]string) {
	// --help (or -h via the flag mapping): print usage for the command and exit.
	if _, ok := flagsReceived["help"]; ok {
		fmt.Print(Usage(command, argsRequested, flagsRequested))
		os.Exit(0)
	}

	hasError := false
	reportError := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		hasError = true
	}

	cleanedArgs := []string{}
	for i, req := range argsRequested {
		if i < len(argsReceived) {
			cleanedArgs = append(cleanedArgs, argsReceived[i])
			continue
		}
		if req.Required {
			reportError("missing required argument %q (position %d)", req.Name, i+1)
		}
	}
	for i := len(argsRequested); i < len(argsReceived); i++ {
		reportError("unexpected argument %q (command %q takes %d positional argument(s))", argsReceived[i], command, len(argsRequested))
	}

	// Any received flag that the command did not declare is an error, not
	// silently dropped — a typo'd flag name must not pass unnoticed. Sorted so
	// the report order is stable.
	knownFlags := map[string]bool{}
	for _, req := range flagsRequested {
		knownFlags[req.Name] = true
	}
	unknown := []string{}
	for name := range flagsReceived {
		if !knownFlags[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		reportError("unknown flag %q", name)
	}

	cleanedFlags := map[string][]string{}
	for _, req := range flagsRequested {
		vals, present := flagsReceived[req.Name]
		if !present {
			if req.Required {
				reportError("missing required flag %q", req.Name)
			}
			continue
		}

		count := len(vals)
		if count < req.MinimumCount {
			reportError("flag %q expects at least %d value(s), got %d", req.Name, req.MinimumCount, count)
			continue
		}
		if count > req.MaximumCount {
			reportError("flag %q expects at most %d value(s), got %d", req.Name, req.MaximumCount, count)
			continue
		}

		if req.LastWins && count > 1 {
			vals = vals[count-1:]
		}
		cleanedFlags[req.Name] = vals
	}

	// Mutual-exclusivity checks run now that cleanedFlags tells us which flags
	// are actually set. Declarations must be symmetric; each group is reported
	// once, combined.
	isSet := func(name string) bool {
		_, ok := cleanedFlags[name]
		return ok
	}
	validateExclusivity(flagsRequested, func(f FlagRequested) []string { return f.MutuallyExclusive }, "MutuallyExclusive", false, isSet, reportError)
	validateExclusivity(flagsRequested, func(f FlagRequested) []string { return f.MutuallyExclusiveRequired }, "MutuallyExclusiveRequired", true, isSet, reportError)

	if hasError {
		fmt.Fprint(os.Stderr, Usage(command, argsRequested, flagsRequested))
		os.Exit(1)
	}

	return cleanedArgs, cleanedFlags
}
