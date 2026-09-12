package deps

import (
	"fmt"
	"os/exec"
)

func checkDepsFfmpeg() error {
	if err := exec.Command("ffmpeg", "-version").Run(); err != nil {
		return fmt.Errorf("ffmpeg not found (install it and ensure it's on PATH): %w", err)
	}

	return nil
}
