package deps

import (
	"fmt"
	"os/exec"
)

func checkDepsClaude() error {
	if err := exec.Command("claude", "--version").Run(); err != nil {
		return fmt.Errorf("claude CLI not found (install it and ensure it's on PATH): %w", err)
	}

	return nil
}
