package harness

import (
	"fmt"

	"axeq/internal/config"
)

// Scenario-count budget: default when the config leaves it unset, and the
// bound a configured value must fall within.
const (
	DefaultMaxScenarios = 10
	MaxMaxScenarios     = 50
)

// Turn budgets (the explorer's, and each scenario's) are handled more
// leniently than the scenario count: a configured value is clamped rather
// than rejected, and a low one only draws a warning. See Turns.
const (
	DefaultTurns    = 15
	MinAdvisedTurns = 5
	MaxTurnsCap     = 25
)

// Turns resolves one of the config's turn budgets (name is the config key):
// unset or below 1 falls back to the default, anything above the cap is
// clamped to it, and a value below the advised minimum is accepted with a
// warning — an agent needs a few turns just to orient itself on a page.
func Turns(name string, v int) int {
	switch {
	case v < 1:
		return DefaultTurns
	case v > MaxTurnsCap:
		fmt.Printf("note: %s: %q is %d, above the maximum of %d; using %d\n", config.FileName, name, v, MaxTurnsCap, MaxTurnsCap)
		return MaxTurnsCap
	case v < MinAdvisedTurns:
		fmt.Printf("warning: %s: %q is %d. Using a turns value lower than %d may lead to inconsistent results, we recommend a value between 10 and 20.\n", config.FileName, name, v, MinAdvisedTurns)
	}
	return v
}

// Budget resolves one of the config's strict budgets: def when the value is
// unset (zero), else the value, which must lie within [1, max] — a config
// error otherwise. name is the config key, for the message.
func Budget(name string, v, def, max int) int {
	if v == 0 {
		return def
	}
	if v < 1 || v > max {
		Fatalf("%s: %q is %d (expected a number between 1 and %d)", config.FileName, name, v, max)
	}
	return v
}
