package playwright

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

// screenshotOutDir is where agent-requested named screenshots are written, as
// {name}.png. SetScreenshotOutputDir sets it once per run; the dir itself is
// created lazily on the first screenshot so an unused run leaves nothing behind.
var screenshotOutDir string

// SetScreenshotOutputDir sets the output dir for the screenshot action.
func SetScreenshotOutputDir(dir string) {
	screenshotOutDir = dir
}

func doScreenshot(page playwright.Page, a ScreenshotAction) error {
	if a.Name == "" {
		return fmt.Errorf("screenshot needs a name")
	}
	// The name becomes a file name in the run's screenshots dir — refuse
	// anything that could escape it.
	if strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
		return fmt.Errorf("screenshot name %q must be a plain file name without path separators", a.Name)
	}

	hasCoords := a.X != nil || a.Y != nil
	if a.FullPage != nil && (a.Selector != nil || hasCoords) {
		return fmt.Errorf("full-page must be omitted when a selector or coordinates are given")
	}
	if a.Selector != nil && hasCoords {
		return fmt.Errorf("screenshot takes a selector or x/y coordinates, not both")
	}
	if hasCoords && (a.X == nil || a.Y == nil) {
		return fmt.Errorf("coordinate screenshot needs both x and y")
	}

	if screenshotOutDir == "" {
		return fmt.Errorf("screenshot output dir is not configured")
	}
	path := filepath.Join(screenshotOutDir, a.Name+".png")
	if _, err := os.Stat(path); err == nil && !a.Override {
		return fmt.Errorf("screenshot %q already exists at %s; set override to true to replace it", a.Name, path)
	}
	if err := os.MkdirAll(screenshotOutDir, 0o755); err != nil {
		return fmt.Errorf("creating screenshots dir: %w", err)
	}

	switch {
	case a.Selector != nil:
		if _, err := page.Locator(*a.Selector).Screenshot(playwright.LocatorScreenshotOptions{
			Path: playwright.String(path),
		}); err != nil {
			return fmt.Errorf("capturing screenshot of %q: %w", *a.Selector, err)
		}
	case hasCoords:
		if err := screenshotElementAt(page, *a.X, *a.Y, path); err != nil {
			return err
		}
	default:
		fullPage := a.FullPage != nil && *a.FullPage
		if _, err := page.Screenshot(playwright.PageScreenshotOptions{
			Path:     playwright.String(path),
			FullPage: playwright.Bool(fullPage),
		}); err != nil {
			return fmt.Errorf("capturing screenshot: %w", err)
		}
	}
	return nil
}

// screenshotElementAt captures the element at viewport point (x, y) — the same
// element a click there would hit — into path.
func screenshotElementAt(page playwright.Page, x, y float64, path string) error {
	handle, err := page.EvaluateHandle("([x, y]) => document.elementFromPoint(x, y)", []interface{}{x, y})
	if err != nil {
		return fmt.Errorf("locating element at (%v, %v): %w", x, y, err)
	}
	el := handle.AsElement()
	if el == nil {
		return fmt.Errorf("no element at (%v, %v) — the point may be outside the viewport", x, y)
	}
	if _, err := el.Screenshot(playwright.ElementHandleScreenshotOptions{
		Path: playwright.String(path),
	}); err != nil {
		return fmt.Errorf("capturing screenshot of element at (%v, %v): %w", x, y, err)
	}
	return nil
}
