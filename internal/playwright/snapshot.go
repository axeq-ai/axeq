package playwright

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/yosssi/gohtml"
)

// Snapshot writes the current page state into dir as a screenshot (.png) and the
// full formatted DOM (.html), sharing one UTC-timestamp base name, and returns
// their absolute paths.
func Snapshot(dir string) (screenshotPath, domPath string, err error) {
	page := active()
	base := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000Z"))
	screenshotPath = base + ".png"
	domPath = base + ".html"

	if err = saveScreenshot(page, screenshotPath); err != nil {
		return "", "", err
	}
	if err = saveDom(page, domPath); err != nil {
		return "", "", err
	}
	return screenshotPath, domPath, nil
}

func saveScreenshot(page playwright.Page, path string) error {
	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(path),
	}); err != nil {
		return fmt.Errorf("capturing screenshot: %w", err)
	}
	return nil
}

func saveDom(page playwright.Page, path string) error {
	dom, err := page.Content()
	if err != nil {
		return fmt.Errorf("capturing DOM: %w", err)
	}
	if err := os.WriteFile(path, []byte(gohtml.Format(dom)), 0o644); err != nil {
		return fmt.Errorf("writing DOM: %w", err)
	}
	return nil
}
