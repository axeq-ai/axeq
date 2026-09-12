// Package harness is the orchestration shared by the audit commands: it
// brings up the application under test and a headless browser, runs auditor
// scenarios against them, and writes the report. The commands differ only in
// where their scenarios come from — the explorer agent for schedule, the
// diff-driven planner for pull_request — and in what they do with the results.
package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"axeq/internal/app"
	"axeq/internal/claude/audit"
	"axeq/internal/config"
	browser "axeq/internal/playwright"
)

// The browser the audit drives: headless Chromium at a fixed desktop
// viewport, with a fresh profile per run.
const (
	viewportWidth  = 1440
	viewportHeight = 900
)

// Backend is the browser-automation surface the audit drives, filled with the
// playwright package's functions. It stays a struct of functions so the audit
// loop never calls the driver package directly.
type Backend struct {
	OpenInitialTabs func(urls []string) error
	WaitForLoad     func() error
	PerformActions  func(actions []browser.Action) []browser.PerformedAction
	Snapshot        func(dir string) (screenshotPath, domPath string, err error)
	Focused         func() (*browser.FocusedElement, error)
	Tabs            func() []browser.Tab
}

// Env is a prepared run: the application up (when configured), the browser
// open on the config's URLs, and every directory the audit writes to.
type Env struct {
	// Repo is the CWD the command was launched from — the root of the
	// repository being audited.
	Repo string
	// TmpDir holds the frames, the agents' working dir, and debug dumps.
	TmpDir string
	// OutputDir is where the deliverables land; the three below are its
	// children: files/ for JSON and markdown, screenshots/ for evidence,
	// recordings/ for the videos. Siblings, so the markdown report links them
	// as ../screenshots/… and ../recordings/…. Each is created lazily.
	OutputDir      string
	FilesDir       string
	ScreenshotsDir string
	RecordingsDir  string
	// AgentDir is where the claude CLI runs from for browser-driving agents,
	// and where per-turn screenshot + DOM captures are written.
	AgentDir string
	Urls     []string
	Backend  Backend

	app     app.Process
	browser bool
}

// Prepare sets a run up. Config and output-dir faults are the user's and are
// reported through Fatalf; anything past that point (temp dir, browser) is an
// environment or program fault and panics. The returned Env must be torn down
// with Teardown, whether or not the run completes.
func Prepare(cfg config.Config, outputDir string) Env {
	repo, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("resolving working directory: %w", err))
	}

	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		panic(fmt.Errorf("resolving absolute path for output dir: %w", err))
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		Fatalf("cannot create output dir %q: %v", outputDir, err)
	}

	tmpDir, err := os.MkdirTemp("", "axeq-*")
	if err != nil {
		panic(fmt.Errorf("creating temp dir: %w", err))
	}
	fmt.Printf("Temp dir: %s\n", tmpDir)

	env := Env{
		Repo:           repo,
		TmpDir:         tmpDir,
		OutputDir:      outputDir,
		FilesDir:       filepath.Join(outputDir, "files"),
		ScreenshotsDir: filepath.Join(outputDir, "screenshots"),
		RecordingsDir:  filepath.Join(outputDir, "recordings"),
		AgentDir:       filepath.Join(tmpDir, "agent"),
		Urls:           cfg.Urls,
	}

	// Bring the application under test up, when the config says how. It runs
	// for the whole audit and is torn down with the browser. Its output goes
	// to its own log so it doesn't tangle with the audit's. A setup command
	// failing or the server never answering is the repository's problem, not
	// ours, so it is reported rather than panicked.
	if cfg.App != nil {
		appLog := filepath.Join(tmpDir, "app.log")
		fmt.Printf("App log: %s\n", appLog)
		proc, err := app.Start(*cfg.App, repo, config.ReadyUrl(cfg), appLog)
		if err != nil {
			Fatalf("%v", err)
		}
		env.app = proc
	}

	browser.SetScreenshotOutputDir(env.ScreenshotsDir)
	fmt.Printf("Screenshots dir: %s\n", env.ScreenshotsDir)
	fmt.Printf("Files dir: %s\n", env.FilesDir)
	fmt.Printf("Recordings dir: %s\n", env.RecordingsDir)

	// Every claude call dumps its settings, prompt, and output here for
	// debugging, timestamp-prefixed so ls lists them chronologically.
	debugDir := filepath.Join(tmpDir, "debug")
	audit.SetDebugDir(debugDir)
	fmt.Printf("Debug dir: %s\n", debugDir)

	if err := os.MkdirAll(env.AgentDir, 0o755); err != nil {
		panic(fmt.Errorf("creating agent dir: %w", err))
	}

	// Headless Chromium with a fresh profile in the temp dir.
	userDataDir := filepath.Join(tmpDir, "user-data")
	if err := browser.Prepare(filepath.Join(tmpDir, "playwright"), userDataDir, viewportWidth, viewportHeight, true); err != nil {
		Teardown(env)
		panic(fmt.Errorf("preparing browser: %w", err))
	}
	env.browser = true

	env.Backend = Backend{
		OpenInitialTabs: browser.OpenInitialTabs,
		WaitForLoad:     browser.WaitForLoad,
		PerformActions:  browser.PerformActions,
		Snapshot:        browser.Snapshot,
		Focused:         browser.Focused,
		Tabs:            browser.Tabs,
	}

	// The URLs were validated by the config loader and the app reported ready,
	// so a tab that still won't open is an environment fault.
	if err := env.Backend.OpenInitialTabs(cfg.Urls); err != nil {
		Teardown(env)
		panic(fmt.Errorf("opening initial tabs: %w", err))
	}
	if err := env.Backend.WaitForLoad(); err != nil {
		Teardown(env)
		panic(err)
	}
	return env
}

// Teardown closes the browser and stops the application. Safe to call on a
// partially prepared Env.
func Teardown(env Env) {
	if env.browser {
		browser.Teardown()
	}
	app.Stop(env.app)
}

// Fatalf reports a user-caused error (bad input, unusable file, ...) to stderr
// and exits 1. Panics stay reserved for internal errors, where the stack trace
// is the debugging aid.
func Fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
