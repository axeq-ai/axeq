package auth

// CheckAuth verifies every external authentication the connector relies on.
// It's a thin wrapper so new auth checks can be added in one place.
func CheckAuth() error {
	return checkAuthClaude()
}
