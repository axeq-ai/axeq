package extension

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"axeq/internal/playwright"
)

// screenshotOutDir is where agent-requested named screenshots are written, as
// {name}.png — same contract as the playwright backend. The extension returns
// PNGs as data URLs in its reply and this side owns the disk.
var screenshotOutDir string

// SetScreenshotOutputDir sets the output dir for the screenshot action.
func SetScreenshotOutputDir(dir string) {
	screenshotOutDir = dir
}

// performResult is the extension's reply to a "perform" command. Performed
// echoes the actions with status fields; only the statuses are read here (the
// original actions are already in hand, matched by index).
type performResult struct {
	Performed []struct {
		Attempted bool    `json:"attempted"`
		Succeeded bool    `json:"succeeded"`
		Reason    *string `json:"reason"`
	} `json:"performed"`
	Images map[string]string `json:"images"` // screenshot name -> PNG data URL
	Notes  []string          `json:"notes"`
	Error  string            `json:"error"`
}

// PerformActions applies the actions and reports each outcome, mirroring the
// playwright backend's contract: the first failure stops the run, so actions
// after it stay Attempted=false.
//
// Browser actions travel to the extension in order-preserving batches;
// write-file and read-file touch only the local disk and execute here.
// Screenshot actions
// are validated against the local disk before sending (name shape,
// exists-without-override) because the extension can't see the filesystem; an
// invalid one fails in sequence exactly where it stands.
func PerformActions(actions []playwright.Action) []playwright.PerformedAction {
	performed := make([]playwright.PerformedAction, len(actions))
	for i := range performed {
		performed[i].Action = &actions[i]
	}

	i := 0
	for i < len(actions) {
		if isLocalAction(actions[i].Type) {
			performed[i].Attempted = true
			if err := performLocally(&performed[i], actions[i]); err != nil {
				msg := err.Error()
				performed[i].Reason = &msg
				return performed
			}
			performed[i].Succeeded = true
			i++
			continue
		}

		// Batch: consecutive browser actions up to the next local (disk-only)
		// action, cut short at the first screenshot that fails local validation.
		start := i
		var validationErr string
		for i < len(actions) && !isLocalAction(actions[i].Type) {
			if actions[i].Type == playwright.ActionScreenshot {
				if d, ok := actions[i].Data.(playwright.ScreenshotAction); ok {
					if err := validateScreenshot(d); err != nil {
						validationErr = err.Error()
						break
					}
				}
			}
			i++
		}

		if i > start && !sendBatch(actions[start:i], performed[start:i]) {
			return performed
		}
		if validationErr != "" {
			performed[i].Attempted = true
			performed[i].Reason = &validationErr
			return performed
		}
	}
	return performed
}

// isLocalAction reports whether an action touches only the local disk — never
// the browser — and so executes here instead of bridging to the extension.
func isLocalAction(t playwright.ActionType) bool {
	return t == playwright.ActionWriteFile || t == playwright.ActionReadFile
}

// performLocally executes a disk-only action, storing read-file's content in
// the performed record's Result.
func performLocally(p *playwright.PerformedAction, a playwright.Action) error {
	switch d := a.Data.(type) {
	case playwright.WriteFileAction:
		return playwright.DoWriteFile(d)
	case playwright.ReadFileAction:
		content, err := playwright.DoReadFile(d)
		if err != nil {
			return err
		}
		p.Result = &content
		return nil
	default:
		return fmt.Errorf("%s action carried %T data", a.Type, a.Data)
	}
}

// sendBatch bridges one batch to the extension, fills the matching performed
// slots (aliasing the caller's slice), persists returned screenshots, and
// reports whether every action in the batch succeeded.
func sendBatch(batch []playwright.Action, performed []playwright.PerformedAction) bool {
	res, err := send(map[string]any{"kind": "perform", "actions": batch}, performTimeout(batch))
	if err != nil {
		// The bridge itself failed — surface it on the batch's first action.
		msg := err.Error()
		performed[0].Attempted = true
		performed[0].Reason = &msg
		return false
	}

	var pr performResult
	if uerr := json.Unmarshal(res, &pr); uerr != nil || pr.Error != "" {
		msg := pr.Error
		if uerr != nil {
			msg = fmt.Sprintf("decoding extension reply: %v", uerr)
		}
		performed[0].Attempted = true
		performed[0].Reason = &msg
		return false
	}

	for i, w := range pr.Performed {
		if i >= len(performed) {
			break
		}
		performed[i].Attempted = w.Attempted
		performed[i].Succeeded = w.Succeeded
		performed[i].Reason = w.Reason
	}

	// Truncation warnings and CSP-fallback notes are for the operator, not the
	// agent — print them.
	for _, n := range pr.Notes {
		fmt.Printf("extension: %s\n", n)
	}

	writeImages(pr.Images, performed) // may downgrade a screenshot's success

	for i := range performed {
		if !performed[i].Succeeded {
			return false
		}
	}
	return true
}

// WaitForLoad is a no-op: the extension waits for the page to settle after
// every action it executes, and again before capturing a snapshot.
func WaitForLoad() error {
	return nil
}

// validateScreenshot mirrors the local-disk checks of the playwright backend's
// doScreenshot; content checks (full-page vs selector exclusivity, missing
// coordinates) stay with the extension, which reports them per action.
func validateScreenshot(a playwright.ScreenshotAction) error {
	if a.Name == "" {
		return fmt.Errorf("screenshot needs a name")
	}
	if strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
		return fmt.Errorf("screenshot name %q must be a plain file name without path separators", a.Name)
	}
	if screenshotOutDir == "" {
		return fmt.Errorf("screenshot output dir is not configured")
	}
	path := filepath.Join(screenshotOutDir, a.Name+".png")
	if _, err := os.Stat(path); err == nil && !a.Override {
		return fmt.Errorf("screenshot %q already exists at %s; set override to true to replace it", a.Name, path)
	}
	return nil
}

// writeImages persists the extension's named screenshots. A write failure is
// reported on the matching screenshot action, downgrading its success.
func writeImages(images map[string]string, performed []playwright.PerformedAction) {
	for name, dataUrl := range images {
		err := writeImage(name, dataUrl)
		if err == nil {
			continue
		}
		msg := err.Error()
		for i := range performed {
			d, ok := performed[i].Data.(playwright.ScreenshotAction)
			if ok && d.Name == name {
				performed[i].Succeeded = false
				performed[i].Reason = &msg
				break
			}
		}
	}
}

func writeImage(name, dataUrl string) error {
	comma := strings.Index(dataUrl, ",")
	if comma < 0 {
		return fmt.Errorf("screenshot %q: extension returned a malformed data URL", name)
	}
	png, err := base64.StdEncoding.DecodeString(dataUrl[comma+1:])
	if err != nil {
		return fmt.Errorf("screenshot %q: decoding image: %w", name, err)
	}
	if err := os.MkdirAll(screenshotOutDir, 0o755); err != nil {
		return fmt.Errorf("creating screenshots dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(screenshotOutDir, name+".png"), png, 0o644); err != nil {
		return fmt.Errorf("screenshot %q: writing file: %w", name, err)
	}
	return nil
}

// performTimeout scales the reply deadline with the batch: explicit waits pass
// through in full, screenshots get generous room (a stitched full-page capture
// runs about a second per viewport band), everything else a flat allowance.
func performTimeout(actions []playwright.Action) time.Duration {
	d := 60 * time.Second
	for _, a := range actions {
		switch a.Type {
		case playwright.ActionWait:
			if w, ok := a.Data.(playwright.WaitAction); ok {
				d += time.Duration(w.Ms) * time.Millisecond
			}
		case playwright.ActionScreenshot:
			d += 2 * time.Minute
		default:
			d += 30 * time.Second
		}
	}
	return min(d, 15*time.Minute)
}
