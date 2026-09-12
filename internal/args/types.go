package args

// ArgRequested describes a positional argument a command expects. Args are
// matched to the received argument vector by position.
type ArgRequested struct {
	Name     string
	Required bool
}

// FlagRequested describes a flag a command expects.
//
// MinimumCount/MaximumCount bound how many values the flag's value slice may
// hold (inclusive). They are literal: a presence-only / boolean flag is
// MinimumCount 0, MaximumCount 0. When LastWins is set, repeated values
// collapse to just the final one in the cleaned output.
type FlagRequested struct {
	Name         string
	MinimumCount int
	MaximumCount int
	LastWins     bool
	Required     bool
	// MutuallyExclusive lists other flags that must not be set together with this
	// one. nil means no constraint; an empty (non-nil) slice is a configuration
	// error and panics.
	MutuallyExclusive []string
	// MutuallyExclusiveRequired is like MutuallyExclusive, but additionally at
	// least one of the group (this flag or one of the listed flags) must be set.
	// nil means no constraint; an empty (non-nil) slice panics.
	MutuallyExclusiveRequired []string
}
