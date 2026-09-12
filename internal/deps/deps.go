package deps

// CheckDeps verifies every external dependency the connector needs is present.
// It's a thin wrapper so new dependency checks can be added in one place.
func CheckDeps() error {
	if err := checkDepsClaude(); err != nil {
		return err
	}
	return checkDepsFfmpeg()
}
