package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"axeq/internal/args"
	"axeq/internal/claude/audit"
	"axeq/internal/config"
	"axeq/internal/harness"
	browser "axeq/internal/playwright"
)

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

	// The repository's config is the command's whole input. A missing or
	// broken file is the user's to fix, so it is reported and exits 1 —
	// Load already validates URLs, the app section and the version.
	cfg, err := config.Load(".")
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			cwd, _ := os.Getwd()
			harness.Fatalf("no %s found in %s (the schedule command runs from the root of the repository being audited, which must carry that file)", config.FileName, cwd)
		}
		harness.Fatalf("%v", err)
	}

	// Budgets and the explorer's notes from the config's schedule section,
	// defaulted where unset.
	var sched config.Schedule
	if cfg.Schedule != nil {
		sched = *cfg.Schedule
	}
	maxScenarios := harness.Budget("schedule.maxScenarios", sched.MaxScenarios, harness.DefaultMaxScenarios, harness.MaxMaxScenarios)
	maxExploreTurns := harness.Turns("schedule.maxExploreTurns", sched.MaxExploreTurns)
	maxScenarioTurns := harness.Turns("schedule.maxScenarioTurns", sched.MaxScenarioTurns)
	userFocus := sched.Prompt

	env := harness.Prepare(cfg, cleanFlags["output-dir"][0])
	defer harness.Teardown(env)
	urls := env.Urls

	report := audit.Report{Urls: urls, StartedAt: time.Now()}

	// Phase 1 — exploration. The explorer's whole conversation is one recording.
	var scenarios []audit.Scenario
	fmt.Printf("\n=== Exploring %s (up to %d turns) ===\n", strings.Join(urls, ", "), maxExploreTurns)
	code := harness.Recorded(filepath.Join(env.TmpDir, "frames", "exploration"), filepath.Join(env.RecordingsDir, "exploration.mp4"), func(rec *browser.Recorder) int {
		var c int
		scenarios, c = explore(env, urls, userFocus, maxExploreTurns, rec)
		return c
	})
	if code != 0 {
		return code
	}
	scenarios = harness.NormalizeScenarios(scenarios, urls, maxScenarios)
	if len(scenarios) == 0 {
		fmt.Fprintln(os.Stderr, "the explorer returned no usable scenario (each needs a title and at least one step)")
		return 1
	}
	report.Scenarios = scenarios

	// The scenario list is a deliverable in its own right: it documents what
	// was audited.
	scenariosPath := filepath.Join(env.FilesDir, "scenarios.json")
	harness.WriteJSON(scenariosPath, scenarios)
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
		recordingPath := filepath.Join(env.RecordingsDir, name+".mp4")
		code := harness.Recorded(filepath.Join(env.TmpDir, "frames", name), recordingPath, func(rec *browser.Recorder) int {
			var c int
			result, c = harness.RunScenario(env, name, sc, maxScenarioTurns, rec)
			return c
		})
		if _, err := os.Stat(recordingPath); err == nil {
			result.Recording = "recordings/" + name + ".mp4"
		}
		report.Results = append(report.Results, result)

		if code != 0 {
			report.Partial = true
			finish(env.FilesDir, report)
			return code
		}
	}

	finish(env.FilesDir, report)
	return 0
}

// explore runs the explorer's conversation to completion and returns the
// scenarios it produced. It returns a non-zero exit code instead when the
// explorer was interrupted (130) or failed to deliver scenarios within its
// turn budget (1).
func explore(env harness.Env, urls []string, userFocus string, maxTurns int, rec *browser.Recorder) ([]audit.Scenario, int) {
	be := env.Backend

	// First turn: capture the landing-page state (screenshot + DOM — the
	// explorer is sighted), then kick off the conversation.
	shotPath, domPath, err := be.Snapshot(env.AgentDir)
	if err != nil {
		panic(err)
	}

	turn := 1
	rec.Pause()
	convoId, resp, err := audit.ExploreInitial(env.AgentDir, "", urls, userFocus, shotPath, domPath, be.Tabs(), maxTurns)
	rec.Resume()
	if code := harness.AgentFailure(err); code != 0 {
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
			fmt.Fprintf(os.Stderr, "the explorer did not return scenarios within %d turns (raise \"schedule.maxExploreTurns\" in %s, up to %d)\n", maxTurns, config.FileName, harness.MaxTurnsCap)
			return nil, 1
		}

		performed := be.PerformActions(resp.PerformActions)

		shotPath, domPath, err = be.Snapshot(env.AgentDir)
		if err != nil {
			panic(err)
		}

		turn++
		rec.Pause()
		resp, err = audit.ExploreSubsequent(convoId, env.AgentDir, performed, "", shotPath, domPath, be.Tabs(), maxTurns-turn+1)
		rec.Resume()
		if code := harness.AgentFailure(err); code != 0 {
			return nil, code
		}
	}

	fmt.Printf("[explore] complete after %d turn(s): %d scenario(s) proposed\n", turn, len(resp.Scenarios))
	return resp.Scenarios, 0
}

// finish stamps the report, writes audit.json and audit.md, and prints where
// they are plus a one-line-per-scenario summary — the last thing the user
// sees, whether the run completed or was interrupted.
func finish(filesOutDir string, report audit.Report) {
	report.FinishedAt = time.Now()
	jsonPath, mdPath := harness.WriteReport(filesOutDir, report)

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
