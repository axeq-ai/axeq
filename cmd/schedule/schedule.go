package schedule

import (
	"axeq/internal/app"
	"axeq/internal/args"
	"axeq/internal/claude/audit"
	"axeq/internal/config"
	browser "axeq/internal/playwright"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	aclio "github.com/agenticcliorchestra/aclio-go"
)

// framesFPS is the capture and playback frame rate for the recordings.
const framesFPS = 15

// The browser the audit drives: headless Chromium at a fixed desktop
// viewport, with a fresh profile per run.
const (
	viewportWidth  = 1440
	viewportHeight = 900
)

// Budget defaults, used when the config's schedule section leaves them unset,
// and the bounds a configured value must fall within. Every turn is one
// claude call, so these bound the run's cost and duration.
const (
	defaultMaxScenarios = 10
	maxMaxScenarios     = 50
)

// Turn budgets (the explorer's, and each scenario's) are handled more
// leniently than the scenario count: a configured value is clamped rather
// than rejected, and a low one only draws a warning. See turns.
const (
	defaultTurns    = 15
	minAdvisedTurns = 5
	maxTurnsCap     = 25
)

// backend is the browser-automation surface the audit drives, filled with
// the playwright package's functions. It stays a struct of functions so the
// audit loop never calls the driver package directly.
type backend struct {
	openInitialTabs func(urls []string) error
	waitForLoad     func() error
	performActions  func(actions []browser.Action) []browser.PerformedAction
	snapshot        func(dir string) (screenshotPath, domPath string, err error)
	focused         func() (*browser.FocusedElement, error)
	tabs            func() []browser.Tab
}

// The schedule command is a two-agent accessibility audit of a site:
//
//  1. An explorer agent — sighted, with the full action set and the
//     screenshot + DOM each turn — roams the site from the configured URL(s)
//     and returns a JSON list of scenarios: user journeys written for someone
//     who cannot see the page.
//  2. For each scenario a fresh auditor agent — a screen-reader persona that
//     is keyboard-only and perceives nothing but the focused element — is
//     placed on the scenario's start page and told to perform its steps. It
//     reports every hindrance it meets; the orchestrator snapshots the page
//     as evidence each time and records the attempt on video.
//
// Its one flag is --output-dir, where the results go. Everything else comes
// from the repository's .axeqrc.json in the CWD (the repository root): how to
// bring the application up, the URLs to audit, and the budgets. The prompts
// are this repository's.
//
// Everything lands under the output dir: files/scenarios.json,
// files/audit.json and files/audit.md (the report), screenshots/ (evidence)
// and recordings/.
func Run(argsReceived []string, flagsReceived map[string][]string) {
	// run's deferred cleanup (browser and app teardown) must fire before the
	// process exits, and os.Exit skips defers — so the exit happens out here,
	// after run has returned.
	if code := run(argsReceived, flagsReceived); code != 0 {
		os.Exit(code)
	}
}

func run(argsReceived []string, flagsReceived map[string][]string) int {
	// No positional args; the only flag is the required --output-dir. Anything
	// else given is a usage error.
	flagsRequested := []args.FlagRequested{
		{Name: "output-dir", MinimumCount: 1, MaximumCount: 1, Required: true},
	}
	_, cleanFlags := args.ValidateArgs("schedule", argsReceived, flagsReceived, nil, flagsRequested)

	// Where the results go. Created if needed; an unusable location is the
	// user's to fix.
	outputDir, err := filepath.Abs(cleanFlags["output-dir"][0])
	if err != nil {
		panic(fmt.Errorf("resolving absolute path for output dir: %w", err))
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fatalf("cannot create output dir %q: %v", outputDir, err)
	}

	// The repository's config is the command's whole input. A missing or
	// broken file is the user's to fix, so it is reported and exits 1 —
	// Load already validates URLs, the app section and the version.
	cfg, err := config.Load(".")
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			cwd, _ := os.Getwd()
			fatalf("no %s found in %s (the schedule command runs from the root of the repository being audited, which must carry that file)", config.FileName, cwd)
		}
		fatalf("%v", err)
	}

	// The site's entry points. Each opens in its own tab for the explorer, in
	// order, and together they define the hosts it may roam.
	urls := cfg.Urls

	// Budgets and the explorer's notes from the config's schedule section,
	// defaulted where unset. An out-of-range budget is a config error.
	var sched config.Schedule
	if cfg.Schedule != nil {
		sched = *cfg.Schedule
	}
	maxScenarios := budget("schedule.maxScenarios", sched.MaxScenarios, defaultMaxScenarios, maxMaxScenarios)
	maxExploreTurns := turns("schedule.maxExploreTurns", sched.MaxExploreTurns)
	maxScenarioTurns := turns("schedule.maxScenarioTurns", sched.MaxScenarioTurns)
	userFocus := sched.Prompt

	// Per-run temp dir holds the frames, the agents' working dir, and debug dumps.
	tmpDir, err := os.MkdirTemp("", "axeq-*")
	if err != nil {
		panic(fmt.Errorf("creating temp dir: %w", err))
	}
	fmt.Printf("Temp dir: %s\n", tmpDir)

	// Bring the application under test up, when the config says how. It runs
	// for the whole audit and is torn down with the browser. Its output goes
	// to its own log so it doesn't tangle with the audit's. A setup command
	// failing or the server never answering is the repository's problem, not
	// ours, so it is reported rather than panicked.
	if cfg.App != nil {
		cwd, err := os.Getwd()
		if err != nil {
			panic(fmt.Errorf("resolving working directory: %w", err))
		}
		appLog := filepath.Join(tmpDir, "app.log")
		fmt.Printf("App log: %s\n", appLog)
		proc, err := app.Start(*cfg.App, cwd, config.ReadyUrl(cfg), appLog)
		if err != nil {
			fatalf("%v", err)
		}
		defer app.Stop(proc)
	}

	// Everything the audit produces lands under the output dir: files/ for the
	// scenarios and the report, screenshots/ for evidence, recordings/ for the
	// videos. Siblings, so the markdown report links them as ../screenshots/…
	// and ../recordings/…. Each is created lazily on first use.
	runOutDir := outputDir
	screenshotsOutDir := filepath.Join(runOutDir, "screenshots")
	browser.SetScreenshotOutputDir(screenshotsOutDir)
	fmt.Printf("Screenshots dir: %s\n", screenshotsOutDir)

	filesOutDir := filepath.Join(runOutDir, "files")
	fmt.Printf("Files dir: %s\n", filesOutDir)

	recordingsOutDir := filepath.Join(runOutDir, "recordings")
	fmt.Printf("Recordings dir: %s\n", recordingsOutDir)

	// Every claude call dumps its settings, prompt, and output here for
	// debugging, timestamp-prefixed so ls lists them chronologically.
	debugDir := filepath.Join(tmpDir, "debug")
	audit.SetDebugDir(debugDir)
	fmt.Printf("Debug dir: %s\n", debugDir)

	// Agent working dir: claude runs from here and the explorer's per-turn
	// screenshot + DOM captures are written here.
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		panic(fmt.Errorf("creating agent dir: %w", err))
	}

	// Headless Chromium with a fresh profile in the temp dir.
	userDataDir := filepath.Join(tmpDir, "user-data")
	if err := browser.Prepare(filepath.Join(tmpDir, "playwright"), userDataDir, viewportWidth, viewportHeight, true); err != nil {
		panic(fmt.Errorf("preparing browser: %w", err))
	}
	defer browser.Teardown()

	be := backend{
		openInitialTabs: browser.OpenInitialTabs,
		waitForLoad:     browser.WaitForLoad,
		performActions:  browser.PerformActions,
		snapshot:        browser.Snapshot,
		focused:         browser.Focused,
		tabs:            browser.Tabs,
	}

	// The URLs were validated by the config loader and the app reported ready,
	// so a tab that still won't open is an environment fault.
	if err := be.openInitialTabs(urls); err != nil {
		panic(fmt.Errorf("opening initial tabs: %w", err))
	}
	if err := be.waitForLoad(); err != nil {
		panic(err)
	}

	report := audit.Report{Urls: urls, StartedAt: time.Now()}

	// Phase 1 — exploration. The explorer's whole conversation is one recording.
	var scenarios []audit.Scenario
	fmt.Printf("\n=== Exploring %s (up to %d turns) ===\n", strings.Join(urls, ", "), maxExploreTurns)
	code := recorded(filepath.Join(tmpDir, "frames", "exploration"), filepath.Join(recordingsOutDir, "exploration.mp4"), func(rec *browser.Recorder) int {
		var c int
		scenarios, c = explore(be, agentDir, urls, userFocus, maxExploreTurns, rec)
		return c
	})
	if code != 0 {
		return code
	}
	scenarios = normalizeScenarios(scenarios, urls, maxScenarios)
	if len(scenarios) == 0 {
		fmt.Fprintln(os.Stderr, "the explorer returned no usable scenario (each needs a title and at least one step)")
		return 1
	}
	report.Scenarios = scenarios

	// The scenario list is a deliverable in its own right: it documents what
	// was audited.
	scenariosPath := filepath.Join(filesOutDir, "scenarios.json")
	writeJSON(scenariosPath, scenarios)
	fmt.Printf("\nScenarios (%d): %s\n", len(scenarios), scenariosPath)
	for i, sc := range scenarios {
		fmt.Printf("  %2d. %s — %s\n", i+1, sc.Title, sc.StartUrl)
	}

	// Phase 2 — one fresh auditor conversation per scenario, each its own
	// recording. An interrupt mid-way still yields a report of what was
	// observed, flagged as partial.
	for i, sc := range scenarios {
		name := fmt.Sprintf("scenario-%02d", i+1)
		fmt.Printf("\n=== Scenario %d/%d: %s (up to %d turns) ===\n", i+1, len(scenarios), sc.Title, maxScenarioTurns)

		var result audit.ScenarioResult
		recordingPath := filepath.Join(recordingsOutDir, name+".mp4")
		code := recorded(filepath.Join(tmpDir, "frames", name), recordingPath, func(rec *browser.Recorder) int {
			var c int
			result, c = runScenario(be, agentDir, name, sc, maxScenarioTurns, rec)
			return c
		})
		if _, err := os.Stat(recordingPath); err == nil {
			result.Recording = "recordings/" + name + ".mp4"
		}
		report.Results = append(report.Results, result)

		if code != 0 {
			report.Partial = true
			finish(filesOutDir, report)
			return code
		}
	}

	finish(filesOutDir, report)
	return 0
}

// turns resolves one of the config's turn budgets (name is the config key):
// unset or below 1 falls back to the default, anything above the cap is
// clamped to it, and a value below the advised minimum is accepted with a
// warning — an agent needs a few turns just to orient itself on a page.
func turns(name string, v int) int {
	switch {
	case v < 1:
		return defaultTurns
	case v > maxTurnsCap:
		fmt.Printf("note: %s: %q is %d, above the maximum of %d; using %d\n", config.FileName, name, v, maxTurnsCap, maxTurnsCap)
		return maxTurnsCap
	case v < minAdvisedTurns:
		fmt.Printf("warning: %s: %q is %d. Using a turns value lower than %d may lead to inconsistent results, we recommend a value between 10 and 20.\n", config.FileName, name, v, minAdvisedTurns)
	}
	return v
}

// budget resolves one of the config's turn/scenario budgets: def when the
// value is unset (zero), else the value, which must lie within [1, max] — a
// config error otherwise. name is the config key, for the message.
func budget(name string, v, def, max int) int {
	if v == 0 {
		return def
	}
	if v < 1 || v > max {
		fatalf("%s: %q is %d (expected a number between 1 and %d)", config.FileName, name, v, max)
	}
	return v
}

// explore runs the explorer's conversation to completion and returns the
// scenarios it produced. It returns a non-zero exit code instead when the
// explorer was interrupted (130) or failed to deliver scenarios within its
// turn budget (1).
func explore(be backend, agentDir string, urls []string, userFocus string, maxTurns int, rec *browser.Recorder) ([]audit.Scenario, int) {
	// First turn: capture the landing-page state (screenshot + DOM — the
	// explorer is sighted), then kick off the conversation.
	shotPath, domPath, err := be.snapshot(agentDir)
	if err != nil {
		panic(err)
	}

	turn := 1
	rec.Pause()
	convoId, resp, err := audit.ExploreInitial(agentDir, "", urls, userFocus, shotPath, domPath, be.tabs(), maxTurns)
	rec.Resume()
	if code := agentFailure(err); code != 0 {
		return nil, code
	}

	for !resp.ExplorationComplete {
		if resp.Progress != nil && *resp.Progress != "" {
			fmt.Printf("[explore] turn %d/%d: %s\n", turn, maxTurns, *resp.Progress)
		}
		// The final turn's prompt demanded the scenarios; an explorer that still
		// hasn't finished won't on its own, and there is nothing to audit without
		// scenarios.
		if turn >= maxTurns {
			fmt.Fprintf(os.Stderr, "the explorer did not return scenarios within %d turns (raise \"schedule.maxExploreTurns\" in %s, up to %d)\n", maxTurns, config.FileName, maxTurnsCap)
			return nil, 1
		}

		performed := be.performActions(resp.PerformActions)

		shotPath, domPath, err = be.snapshot(agentDir)
		if err != nil {
			panic(err)
		}

		turn++
		rec.Pause()
		resp, err = audit.ExploreSubsequent(convoId, agentDir, performed, "", shotPath, domPath, be.tabs(), maxTurns-turn+1)
		rec.Resume()
		if code := agentFailure(err); code != 0 {
			return nil, code
		}
	}

	fmt.Printf("[explore] complete after %d turn(s): %d scenario(s) proposed\n", turn, len(resp.Scenarios))
	return resp.Scenarios, 0
}

// runScenario performs one scenario in a fresh auditor conversation: it
// resets the browser to the scenario's start page, then alternates between the
// auditor's turns and the key presses it asks for, collecting hindrances as
// findings until the auditor declares the scenario complete or its turn budget
// runs out (then the outcome is abandoned). The result is returned even on
// interruption — with exit code 130 — so the report can include what was
// observed so far. name is the run-local file-name stem for this scenario's
// evidence screenshots and recording.
func runScenario(be backend, agentDir, name string, sc audit.Scenario, maxTurns int, rec *browser.Recorder) (audit.ScenarioResult, int) {
	result := audit.ScenarioResult{Scenario: sc, Outcome: audit.OutcomeAbandoned, Findings: []audit.Finding{}}

	// A scenario whose start page won't open is the explorer's mistake, not a
	// hindrance: record it and move on to the next scenario.
	if reason := resetBrowser(be, sc.StartUrl); reason != "" {
		result.Summary = fmt.Sprintf("Not attempted: the start page %s could not be opened (%s).", sc.StartUrl, reason)
		fmt.Println(result.Summary)
		return result, 0
	}

	// First turn: the auditor is blind — no screenshot, no DOM — so its view of
	// the page is the focused element alone (nothing, right after a load).
	focused, err := be.focused()
	if err != nil {
		panic(err)
	}

	turn := 1
	rec.Pause()
	convoId, resp, err := audit.AuditInitial(agentDir, "", sc, focused, be.tabs(), maxTurns)
	rec.Resume()
	if code := agentFailure(err); code != 0 {
		result.Summary = "Interrupted before the auditor's first turn completed."
		return result, code
	}

	for {
		result.Turns = turn
		observe(&result, be, name, turn, resp)

		if resp.ScenarioComplete {
			if resp.Outcome != nil && *resp.Outcome != "" {
				result.Outcome = *resp.Outcome
			}
			result.Summary = "The auditor finished without a summary."
			if resp.Summary != nil && *resp.Summary != "" {
				result.Summary = *resp.Summary
			}
			break
		}
		// The final turn's prompt demanded a verdict; an auditor that still
		// hasn't finished is cut off, and the scenario counts as abandoned.
		if turn >= maxTurns {
			result.Summary = fmt.Sprintf("Turn budget of %d exhausted before the scenario completed.", maxTurns)
			break
		}

		performed := be.performActions(resp.PerformActions)

		focused, err = be.focused()
		if err != nil {
			panic(err)
		}

		turn++
		rec.Pause()
		resp, err = audit.AuditSubsequent(convoId, agentDir, performed, "", focused, be.tabs(), maxTurns-turn+1)
		rec.Resume()
		if code := agentFailure(err); code != 0 {
			result.Turns = turn
			result.Summary = fmt.Sprintf("Interrupted during turn %d.", turn)
			return result, code
		}
	}

	fmt.Printf("[%s] %s after %d turn(s), %d hindrance(s): %s\n", name, result.Outcome, result.Turns, len(result.Findings), result.Summary)
	return result, 0
}

// observe records one auditor turn into the result: its narration line and
// each newly reported hindrance as a finding. The page is, at this point,
// exactly as the auditor perceived it when it wrote the response (its next
// actions have not run yet), so an evidence screenshot taken now shows what it
// was describing. Screenshots go through the backend's own action path so
// either driver produces them the same way.
func observe(result *audit.ScenarioResult, be backend, name string, turn int, resp audit.AuditorResponse) {
	if resp.Narration != nil && *resp.Narration != "" {
		result.Narration = append(result.Narration, fmt.Sprintf("Turn %d: %s", turn, *resp.Narration))
		fmt.Printf("[%s] turn %d: %s\n", name, turn, *resp.Narration)
	}

	if len(resp.Hindrances) == 0 {
		return
	}
	pageUrl := activeUrl(be.tabs())
	for _, h := range resp.Hindrances {
		finding := audit.Finding{Hindrance: h, Turn: turn, Url: pageUrl}

		shot := fmt.Sprintf("%s-hindrance-%02d", name, len(result.Findings)+1)
		performed := be.performActions([]browser.Action{
			{Type: browser.ActionScreenshot, Data: browser.ScreenshotAction{Name: shot}},
		})
		if len(performed) == 1 && performed[0].Succeeded {
			finding.Screenshot = "screenshots/" + shot + ".png"
		} else {
			fmt.Printf("note: evidence screenshot %s failed: %s\n", shot, failureReason(performed))
		}

		result.Findings = append(result.Findings, finding)
		fmt.Printf("  hindrance [%s/%s] step %d: %s\n", h.Severity, h.Category, h.Step, h.Description)
	}
}

// resetBrowser puts the browser in the state every scenario starts from: the
// extra tabs a previous phase left open are closed (highest index first — the
// first tab is the one the backends refuse to close) and the remaining tab is
// navigated to startUrl. Both go through the backend's own action path so
// either driver behaves the same. It returns the failure reason, or "" on
// success.
func resetBrowser(be backend, startUrl string) string {
	var actions []browser.Action
	for i := len(be.tabs()); i > 1; i-- {
		actions = append(actions, browser.Action{Type: browser.ActionTabClose, Data: browser.TabCloseAction{Index: i}})
	}
	actions = append(actions, browser.Action{Type: browser.ActionGoTo, Data: browser.GoToAction{Url: startUrl}})

	performed := be.performActions(actions)
	for _, p := range performed {
		if !p.Succeeded {
			return failureReason(performed)
		}
	}
	return ""
}

// recorded runs one phase of the audit — the exploration, or one scenario —
// under its own constant-rate frame recording into framesDir, encoded to
// outPath once the phase returns, so each phase gets its own video. The phase
// pauses the recorder while waiting on the agent so those gaps don't appear
// in the output.
func recorded(framesDir, outPath string, phase func(rec *browser.Recorder) int) int {
	rec, err := browser.StartRecording(framesDir, framesFPS)
	if err != nil {
		panic(fmt.Errorf("starting recorder: %w", err))
	}
	code := phase(rec)
	framesDir = rec.Stop()

	// An interrupted phase may end before a single frame landed, and ffmpeg
	// fails on an empty frame pattern — there is simply nothing to encode.
	if entries, err := os.ReadDir(framesDir); err != nil || len(entries) == 0 {
		return code
	}
	if err := browser.EncodeVideo(framesDir, outPath, framesFPS); err != nil {
		panic(fmt.Errorf("encoding recording: %w", err))
	}
	fmt.Printf("Recording: %s\n", outPath)
	return code
}

// finish stamps the report, writes audit.json and audit.md, and prints where
// they are plus a one-line-per-scenario summary — the last thing the user
// sees, whether the run completed or was interrupted.
func finish(filesOutDir string, report audit.Report) {
	report.FinishedAt = time.Now()
	jsonPath, mdPath := writeReport(filesOutDir, report)

	fmt.Println()
	if report.Partial {
		fmt.Printf("Audit interrupted: %d of %d scenario(s) performed\n", len(report.Results), len(report.Scenarios))
	} else {
		fmt.Printf("Audit complete: %d scenario(s) performed\n", len(report.Results))
	}
	for i, r := range report.Results {
		fmt.Printf("  %2d. %-26s %2d hindrance(s)  %s\n", i+1, r.Outcome, len(r.Findings), r.Scenario.Title)
	}
	fmt.Printf("Report: %s\n", mdPath)
	fmt.Printf("Data:   %s\n", jsonPath)
}

// normalizeScenarios makes the explorer's (or a file's) scenarios safe to run:
// entries without a title or steps are dropped with a note, a missing or
// non-absolute start URL falls back to the first entry URL, blank or duplicate
// ids get positional ones, and the list is cut to max entries (they are
// ordered by importance, so the tail goes).
func normalizeScenarios(in []audit.Scenario, urls []string, max int) []audit.Scenario {
	out := make([]audit.Scenario, 0, len(in))
	seenIds := map[string]bool{}
	for i, sc := range in {
		if strings.TrimSpace(sc.Title) == "" || len(sc.Steps) == 0 {
			fmt.Printf("note: dropping scenario %d (%q): it needs a title and at least one step\n", i+1, sc.Title)
			continue
		}
		if u, err := url.Parse(sc.StartUrl); err != nil || u.Scheme == "" || u.Host == "" {
			fmt.Printf("note: scenario %q has no usable start URL (%q); starting it at %s\n", sc.Title, sc.StartUrl, urls[0])
			sc.StartUrl = urls[0]
		}
		if strings.TrimSpace(sc.ID) == "" || seenIds[sc.ID] {
			sc.ID = fmt.Sprintf("scenario-%d", len(out)+1)
		}
		seenIds[sc.ID] = true
		out = append(out, sc)
	}
	if len(out) > max {
		fmt.Printf("note: %d scenario(s) proposed, performing the first %d (raise --max-scenarios for more)\n", len(out), max)
		out = out[:max]
	}
	return out
}

// agentFailure maps an agent-turn error to an exit code: 0 for none, 130 for
// an interrupt (reported, not panicked — ctrl+c is the user's choice), and a
// panic for anything else, where the stack trace is the debugging aid.
func agentFailure(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, aclio.ErrInterrupted) {
		fmt.Fprintln(os.Stderr, "interrupted")
		return 130
	}
	panic(fmt.Errorf("agent interaction: %w", err))
}

// failureReason describes the first failed action of a performed set — its
// reason when one was given, else which action type stalled.
func failureReason(performed []browser.PerformedAction) string {
	for _, p := range performed {
		if p.Succeeded {
			continue
		}
		if p.Reason != nil {
			return *p.Reason
		}
		if p.Action != nil {
			return fmt.Sprintf("%s action did not complete", p.Type)
		}
		return "action did not complete"
	}
	return "unknown"
}

// activeUrl is the URL of the active tab, or "" when none is flagged active.
func activeUrl(tabs []browser.Tab) string {
	for _, t := range tabs {
		if t.Active {
			return t.Url
		}
	}
	return ""
}

// fatalf reports a user-caused error (bad input, unusable file, ...) to stderr
// and exits 1. Panics stay reserved for internal errors, where the stack trace
// is the debugging aid.
func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
