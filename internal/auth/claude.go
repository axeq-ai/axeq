package auth

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// claudeAuthStatus mirrors the JSON emitted by `claude auth status`.
type claudeAuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	Email            string `json:"email"`
	OrgID            string `json:"orgId"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

func checkAuthClaude() error {
	out, err := exec.Command("claude", "auth", "status").Output()
	if err != nil {
		return fmt.Errorf("claude auth status failed (run 'claude auth login'): %w", err)
	}

	var status claudeAuthStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return fmt.Errorf("parsing claude auth status: %w", err)
	}

	if !status.LoggedIn {
		return fmt.Errorf("not logged in to claude, run 'claude auth login'")
	}

	return nil
}
