package playwright

import (
	"fmt"

	"github.com/mxschmitt/playwright-go"
)

// Module-level handles for the single browser session this connector drives.
// Prepare populates them and Teardown closes them; the active tab lives in
// tabs.go.
var (
	pw             *playwright.Playwright
	browser        playwright.Browser
	browserContext playwright.BrowserContext
)

// Prepare installs the Chromium driver if needed, starts Playwright, opens a
// browser context (screen-recording into videoDir at the given viewport), and
// sets the initial tab as active.
//
// When userDataDir is non-empty it launches a persistent context against that
// profile dir, so state (cookies, logins, localStorage) survives across runs.
// A persistent context has no separate Browser object — `browser` stays nil and
// Teardown skips it. When userDataDir is empty it falls back to an ephemeral
// browser + context.
func Prepare(videoDir, userDataDir string, width, height int, headless bool) error {
	if err := playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
	}); err != nil {
		return fmt.Errorf("installing playwright driver: %w", err)
	}

	var err error
	pw, err = playwright.Run()
	if err != nil {
		return fmt.Errorf("starting playwright: %w", err)
	}

	// One size drives both the page viewport and the recorded video. The pointer
	// is read-only options, so it's safe to share.
	size := &playwright.Size{Width: width, Height: height}

	if userDataDir != "" {
		browserContext, err = pw.Chromium.LaunchPersistentContext(userDataDir, playwright.BrowserTypeLaunchPersistentContextOptions{
			Headless:    playwright.Bool(headless),
			Viewport:    size,
			RecordVideo: &playwright.RecordVideo{Dir: &videoDir, Size: size},
		})
		if err != nil {
			return fmt.Errorf("launching persistent context: %w", err)
		}
	} else {
		browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(headless),
		})
		if err != nil {
			return fmt.Errorf("launching browser: %w", err)
		}

		browserContext, err = browser.NewContext(playwright.BrowserNewContextOptions{
			Viewport:    size,
			RecordVideo: &playwright.RecordVideo{Dir: &videoDir, Size: size},
		})
		if err != nil {
			return fmt.Errorf("creating browser context: %w", err)
		}
	}

	// A persistent context opens with a default page; reuse it if present,
	// otherwise create one. Either way it becomes the active tab.
	if pages := browserContext.Pages(); len(pages) > 0 {
		setActive(pages[0])
		return nil
	}

	page, err := browserContext.NewPage()
	if err != nil {
		return fmt.Errorf("creating page: %w", err)
	}
	setActive(page)
	return nil
}

// Goto navigates the active tab to target (used for the initial navigation).
func Goto(target string) error {
	if _, err := active().Goto(target); err != nil {
		return fmt.Errorf("navigating to %q: %w", target, err)
	}
	return nil
}
