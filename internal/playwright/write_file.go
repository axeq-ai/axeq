package playwright

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// outputsDir is where agent-requested files (the write-file action) are written,
// as {name}. SetOutputsDir sets it once per run; the dir itself is created lazily
// on the first write so an unused run leaves nothing behind.
var outputsDir string

// SetOutputsDir sets the output dir for the write-file action.
func SetOutputsDir(dir string) {
	outputsDir = dir
}

// DoWriteFile writes the agent's deliverable file into the outputs dir. It is
// exported because it touches only the local disk — never the browser — so the
// extension backend executes it directly instead of bridging it to Chrome.
func DoWriteFile(a WriteFileAction) error {
	if a.Name == "" {
		return fmt.Errorf("write-file needs a name")
	}
	// The name becomes a file name in the run's outputs dir — refuse anything
	// that could escape it.
	if strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
		return fmt.Errorf("write-file name %q must be a plain file name without path separators", a.Name)
	}

	if outputsDir == "" {
		return fmt.Errorf("outputs dir is not configured")
	}
	path := filepath.Join(outputsDir, a.Name)
	if _, err := os.Stat(path); err == nil && !a.Override {
		return fmt.Errorf("file %q already exists at %s; set override to true to replace it", a.Name, path)
	}
	if err := os.MkdirAll(outputsDir, 0o755); err != nil {
		return fmt.Errorf("creating outputs dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return fmt.Errorf("writing file %q: %w", a.Name, err)
	}
	return nil
}

// DoReadFile reads back a file previously saved with write-file and returns its
// content. Reading is restricted to the outputs dir — the name is validated the
// same way as on write, so nothing outside it is reachable. Exported for the
// same reason as DoWriteFile: it touches only the local disk.
func DoReadFile(a ReadFileAction) (string, error) {
	if a.Name == "" {
		return "", fmt.Errorf("read-file needs a name")
	}
	if strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
		return "", fmt.Errorf("read-file name %q must be a plain file name without path separators", a.Name)
	}

	if outputsDir == "" {
		return "", fmt.Errorf("outputs dir is not configured")
	}
	b, err := os.ReadFile(filepath.Join(outputsDir, a.Name))
	if err != nil {
		return "", fmt.Errorf("reading file %q (only files saved with write-file this run are readable): %w", a.Name, err)
	}
	return string(b), nil
}
