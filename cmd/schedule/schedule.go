package schedule

import (
	"axeq/internal/args"
	"axeq/internal/claude/audit"
	"axeq/internal/extension"
	browser "axeq/internal/playwright"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	aclio "github.com/agenticcliorchestra/aclio-go"
)

// framesFPS is the capture and playback frame rate for the recordings.
const framesFPS = 15

// backend is the browser-automation surface the audit drives, filled with
// either the playwright package's functions (its own Chromium) or the
// extension package's (the user's normal Chrome via the bridge extension).
// Both operate on the same action and state types, so the agents' turns are
// identical either way.
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
//     screenshot + DOM each turn — roams the site from the given URL(s) and
//     returns a JSON list of scenarios: user journeys written for someone who
//     cannot see the page.
//  2. For each scenario a fresh auditor agent — a screen-reader persona that
//     is keyboard-only and perceives nothing but the focused element — is
//     placed on the scenario's start page and told to perform its steps. It
//     reports every hindrance it meets; the orchestrator snapshots the page
//     as evidence each time and records the attempt on video.
//
// Everything lands under outputs/{run}: files/scenarios.json, files/audit.json
// and files/audit.md (the report), screenshots/ (evidence) and recordings/.
func Run(argsReceived []string, flagsReceived map[string][]string) {
	// run's deferred cleanup (browser/extension teardown) must fire before the
	// process exits, and os.Exit skips defers — so the exit happens out here,
	// after run has returned.
	if code := run(argsReceived, flagsReceived); code != 0 {
		os.Exit(code)
	}
}

func run(argsReceived []string, flagsReceived map[string][]string) int {
	argsRequested := []args.ArgRequested{}
	flagsRequested := []args.FlagRequested{
		{Name: "url", MinimumCount: 1, MaximumCount: 100, Required: true},
		{Name: "prompt", MinimumCount: 1, MaximumCount: 1, MutuallyExclusive: []string{"prompt-path"}},
		{Name: "prompt-path", MinimumCount: 1, MaximumCount: 1, MutuallyExclusive: []string{"prompt"}},
		{Name: "scenarios-path", MinimumCount: 1, MaximumCount: 1},
		{Name: "max-scenarios", MinimumCount: 1, MaximumCount: 1},
		{Name: "max-explore-turns", MinimumCount: 1, MaximumCount: 1},
		{Name: "max-scenario-turns", MinimumCount: 1, MaximumCount: 1},
		{Name: "driver", MinimumCount: 1, MaximumCount: 1},
		{Name: "extension-port", MinimumCount: 1, MaximumCount: 1},
		{Name: "system-prompt-file", MinimumCount: 1, MaximumCount: 1},
		{Name: "user-data-dir", MinimumCount: 1, MaximumCount: 1},
		{Name: "viewport", MinimumCount: 1, MaximumCount: 1},
		{Name: "headed", MinimumCount: 0, MaximumCount: 0},
		{Name: "no-video", MinimumCount: 0, MaximumCount: 0},
	}

	_, cleanFlags := args.ValidateArgs("schedule", argsReceived, flagsReceived, argsRequested, flagsRequested)

	// Required, repeatable: the site's entry points. Each opens in its own tab
	// for the explorer, in order, and together they define the hosts it may
	// roam. Each must be an absolute URL.
	urls := cleanFlags["url"]
	for _, raw := range urls {
		if u, err := url.Parse(raw); err != nil || u.Scheme == "" || u.Host == "" {
			fatalf("invalid --url %q (expected an absolute URL like https://example.com)", raw)
		}
	}

	// Browser driver: playwright launches and drives its own Chromium;
	// extension drives the user's normal Chrome through the bridge extension in
	// test-chrome-extension/ — for sensitive sites whose bot prevention blocks
	// automated browsers.
	driver := flagValue(cleanFlags, "driver", "playwright")
	switch driver {
	case "playwright", "extension":
	default:
		fatalf("invalid --driver %q (expected: playwright, extension)", driver)
	}
	extensionPort, err := strconv.Atoi(flagValue(cleanFlags, "extension-port", "8377"))
	if err != nil || extensionPort < 1 || extensionPort > 65535 {
		fatalf("invalid --extension-port %q (expected a port number)", flagValue(cleanFlags, "extension-port", ""))
	}

	// Optional system prompt file, passed through to the claude CLI's
	// --system-prompt-file for both agents. Resolved to an absolute path
	// because the CLI runs from the agent dir, not the CWD this program was
	// launched from.
	systemPromptFile := flagValue(cleanFlags, "system-prompt-file", "")
	if systemPromptFile != "" {
		abs, err := filepath.Abs(expandTilde(systemPromptFile))
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for system prompt file: %w", err))
		}
		if _, err := os.Stat(abs); err != nil {
			fatalf("system prompt file %q not accessible: %v", abs, err)
		}
		systemPromptFile = abs
	}

	// Viewport size (WIDTHxHEIGHT, e.g. 1440x900).
	viewport := flagValue(cleanFlags, "viewport", "1440x900")
	viewportWidth, viewportHeight := parseViewport(viewport)

	// Optional persistent browser profile dir.
	userDataDir := flagValue(cleanFlags, "user-data-dir", "")

	// Boolean flags: presence means true.
	_, headed := cleanFlags["headed"]
	_, noVideo := cleanFlags["no-video"]

	// Budgets. Every turn is one claude call, so these bound the run's cost and
	// duration: at most max-scenarios scenarios are performed (the explorer
	// orders them by importance, so the tail is what gets cut), the explorer
	// gets max-explore-turns to map the site, and each auditor conversation
	// gets max-scenario-turns before the scenario is recorded as abandoned.
	maxScenarios := intFlag(cleanFlags, "max-scenarios", 10, 1, 50)
	maxExploreTurns := intFlag(cleanFlags, "max-explore-turns", 30, 1, 200)
	maxScenarioTurns := intFlag(cleanFlags, "max-scenario-turns", 40, 1, 200)

	// Optional notes for the explorer — what to concentrate on, test data or
	// credentials to pass into the scenarios — as literal text (--prompt) or a
	// file (--prompt-path); ValidateArgs enforces at most one of the two.
	userFocus := flagValue(cleanFlags, "prompt", "")
	if promptPath := flagValue(cleanFlags, "prompt-path", ""); promptPath != "" {
		abs, err := filepath.Abs(expandTilde(promptPath))
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for prompt file: %w", err))
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			fatalf("prompt file %q not accessible: %v", abs, err)
		}
		userFocus = string(b)
	}

	// --scenarios-path skips the exploration and performs pre-written scenarios
	// instead — a previous run's files/scenarios.json, typically, to re-audit
	// the same journeys after fixes. The explorer-only flags are then
	// meaningless, so they are rejected rather than silently ignored.
	var scenarios []audit.Scenario
	if scenariosPath := flagValue(cleanFlags, "scenarios-path", ""); scenariosPath != "" {
		for _, f := range []string{"prompt", "prompt-path", "max-explore-turns"} {
			if _, ok := cleanFlags[f]; ok {
				fatalf("--%s is only valid without --scenarios-path (it steers the exploration, which --scenarios-path skips)", f)
			}
		}
		abs, err := filepath.Abs(expandTilde(scenariosPath))
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for scenarios file: %w", err))
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			fatalf("scenarios file %q not accessible: %v", abs, err)
		}
		if err := json.Unmarshal(b, &scenarios); err != nil {
			fatalf("scenarios file %q is not a JSON array of scenarios: %v", abs, err)
		}
		scenarios = normalizeScenarios(scenarios, urls, maxScenarios)
		if len(scenarios) == 0 {
			fatalf("scenarios file %q contains no usable scenario (each needs a title and at least one step)", abs)
		}
	}

	// Per-run temp dir holds the frames, the agents' working dir, and debug dumps.
	tmpDir, err := os.MkdirTemp("", "ai-browser-connector-*")
	if err != nil {
		panic(fmt.Errorf("creating temp dir: %w", err))
	}
	fmt.Printf("Temp dir: %s\n", tmpDir)

	// Everything the audit produces lands under one CWD-relative, per-run
	// folder: outputs/{run}/files for the scenarios and the report,
	// outputs/{run}/screenshots for evidence, outputs/{run}/recordings for the
	// videos. Siblings, so the markdown report links them as ../screenshots/…
	// and ../recordings/…. Each is created lazily on first use. Both backends
	// resolve screenshot names against their own copy of the setting.
	runOutDir := filepath.Join("outputs", filepath.Base(tmpDir))
	screenshotsOutDir := filepath.Join(runOutDir, "screenshots")
	browser.SetScreenshotOutputDir(screenshotsOutDir)
	extension.SetScreenshotOutputDir(screenshotsOutDir)
	fmt.Printf("Screenshots dir: %s\n", screenshotsOutDir)

	filesOutDir := filepath.Join(runOutDir, "files")
	fmt.Printf("Files dir: %s\n", filesOutDir)

	recordingsOutDir := filepath.Join(runOutDir, "recordings")
	if !noVideo {
		fmt.Printf("Recordings dir: %s\n", recordingsOutDir)
	}

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

	// The chosen driver supplies the audit's browser operations. Both expose
	// the same free-function surface over the same action types, so the agents
	// drive either one identically.
	var be backend
	if driver == "extension" {
		// The extension drives the user's real Chrome: viewport, headedness, and
		// profile belong to that browser, and video recording (15fps screenshot
		// sampling) exceeds captureVisibleTab's rate limit.
		for _, f := range []string{"viewport", "headed", "user-data-dir"} {
			if _, ok := cleanFlags[f]; ok {
				fmt.Printf("note: --%s is ignored with --driver extension (the user's own Chrome is driven)\n", f)
			}
		}
		if !noVideo {
			fmt.Println("note: --driver extension cannot record video; continuing without recording")
			noVideo = true
		}

		if err := extension.Prepare(extensionPort); err != nil {
			panic(fmt.Errorf("preparing extension bridge: %w", err))
		}
		defer extension.Teardown()

		fmt.Printf("Extension bridge listening on http://127.0.0.1:%d\n", extensionPort)
		fmt.Println("Waiting for the extension: load test-chrome-extension/ at chrome://extensions, enable it in its popup, and adopt the tab to drive.")
		if err := extension.WaitForExtension(5 * time.Minute); err != nil {
			fatalf("%v", err)
		}
		fmt.Println("Extension connected.")

		be = backend{
			openInitialTabs: extension.OpenInitialTabs,
			waitForLoad:     extension.WaitForLoad,
			performActions:  extension.PerformActions,
			snapshot:        extension.Snapshot,
			focused:         extension.Focused,
			tabs:            extension.Tabs,
		}
	} else {
		// Persistent browser profile: default into the temp dir when not specified.
		if userDataDir == "" {
			userDataDir = filepath.Join(tmpDir, "user-data")
		}
		fmt.Printf("Browser profile: %s\n", userDataDir)

		if err := browser.Prepare(filepath.Join(tmpDir, "playwright"), userDataDir, viewportWidth, viewportHeight, !headed); err != nil {
			panic(fmt.Errorf("preparing browser: %w", err))
		}
		defer browser.Teardown()

		be = backend{
			openInitialTabs: browser.OpenInitialTabs,
			waitForLoad:     browser.WaitForLoad,
			performActions:  browser.PerformActions,
			snapshot:        browser.Snapshot,
			focused:         browser.Focused,
			tabs:            browser.Tabs,
		}
	}

	if err := be.openInitialTabs(urls); err != nil {
		panic(fmt.Errorf("opening initial tabs: %w", err))
	}
	if err := be.waitForLoad(); err != nil {
		panic(err)
	}

	report := audit.Report{Urls: urls, StartedAt: time.Now()}

	// Phase 1 — exploration. Skipped when scenarios came from --scenarios-path.
	// The explorer's whole conversation is one recording.
	if scenarios == nil {
		fmt.Printf("\n=== Exploring %s (up to %d turns) ===\n", strings.Join(urls, ", "), maxExploreTurns)
		code := recorded(!noVideo, filepath.Join(tmpDir, "frames", "exploration"), filepath.Join(recordingsOutDir, "exploration.mp4"), func(rec *browser.Recorder) int {
			var c int
			scenarios, c = explore(be, agentDir, urls, userFocus, systemPromptFile, maxExploreTurns, rec)
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
	}
	report.Scenarios = scenarios

	// The scenario list is a deliverable in its own right: it documents what
	// was audited and feeds --scenarios-path on a re-run.
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
		code := recorded(!noVideo, filepath.Join(tmpDir, "frames", name), recordingPath, func(rec *browser.Recorder) int {
			var c int
			result, c = runScenario(be, agentDir, name, sc, systemPromptFile, maxScenarioTurns, rec)
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

// explore runs the explorer's conversation to completion and returns the
// scenarios it produced. It returns a non-zero exit code instead when the
// explorer was interrupted (130) or failed to deliver scenarios within its
// turn budget (1).
func explore(be backend, agentDir string, urls []string, userFocus, systemPromptFile string, maxTurns int, rec *browser.Recorder) ([]audit.Scenario, int) {
	// First turn: capture the landing-page state (screenshot + DOM — the
	// explorer is sighted), then kick off the conversation.
	shotPath, domPath, err := be.snapshot(agentDir)
	if err != nil {
		panic(err)
	}

	turn := 1
	rec.Pause()
	convoId, resp, err := audit.ExploreInitial(agentDir, systemPromptFile, urls, userFocus, shotPath, domPath, be.tabs(), maxTurns)
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
			fmt.Fprintf(os.Stderr, "the explorer did not return scenarios within %d turns (raise --max-explore-turns)\n", maxTurns)
			return nil, 1
		}

		performed := be.performActions(resp.PerformActions)

		shotPath, domPath, err = be.snapshot(agentDir)
		if err != nil {
			panic(err)
		}

		turn++
		rec.Pause()
		resp, err = audit.ExploreSubsequent(convoId, agentDir, performed, systemPromptFile, shotPath, domPath, be.tabs(), maxTurns-turn+1)
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
func runScenario(be backend, agentDir, name string, sc audit.Scenario, systemPromptFile string, maxTurns int, rec *browser.Recorder) (audit.ScenarioResult, int) {
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
	convoId, resp, err := audit.AuditInitial(agentDir, systemPromptFile, sc, focused, be.tabs(), maxTurns)
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
		resp, err = audit.AuditSubsequent(convoId, agentDir, performed, systemPromptFile, focused, be.tabs(), maxTurns-turn+1)
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
// in the output. With recording disabled the phase runs with a nil recorder,
// whose Pause/Resume no-op.
func recorded(enabled bool, framesDir, outPath string, phase func(rec *browser.Recorder) int) int {
	if !enabled {
		return phase(nil)
	}

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

// expandTilde resolves a leading "~" or "~/" to the user's home directory —
// the shell doesn't expand it when the path was quoted.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("resolving home directory: %w", err))
		}
		return filepath.Join(home, p[1:])
	}
	return p
}

// flagValue returns the single value of a validated flag, or def when the flag
// was not provided.
func flagValue(flags map[string][]string, name, def string) string {
	if v, ok := flags[name]; ok && len(v) > 0 {
		return v[0]
	}
	return def
}

// intFlag returns the single value of a validated flag as an integer within
// [min, max], or def when the flag was not provided.
func intFlag(flags map[string][]string, name string, def, min, max int) int {
	raw, ok := flags[name]
	if !ok || len(raw) == 0 {
		return def
	}
	n, err := strconv.Atoi(raw[0])
	if err != nil || n < min || n > max {
		fatalf("invalid --%s %q (expected a number between %d and %d)", name, raw[0], min, max)
	}
	return n
}

// parseViewport parses a WIDTHxHEIGHT string (case-insensitive x), e.g. 1440x900.
func parseViewport(s string) (int, int) {
	parts := strings.Split(strings.ToLower(s), "x")
	if len(parts) != 2 {
		fatalf("invalid --viewport %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}

	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || width <= 0 {
		fatalf("invalid --viewport width in %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || height <= 0 {
		fatalf("invalid --viewport height in %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}
	return width, height
}

// fatalf reports a user-caused error (bad input, unusable file, ...) to stderr
// and exits 1. Panics stay reserved for internal errors, where the stack trace
// is the debugging aid.
func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
