package pull_request

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"axeq/internal/args"
	"axeq/internal/claude/audit"
	"axeq/internal/config"
	"axeq/internal/github"
	"axeq/internal/harness"
	browser "axeq/internal/playwright"
)

// The pull_request command is the accessibility audit scoped to one pull
// request. It shares the auditor and the report with the schedule command but
// replaces the explorer with two agents that read code instead of driving a
// browser:
//
//  1. A planner agent reads the pull request's diff and the checked-out
//     repository and writes scenarios that exercise exactly what the pull
//     request changes in the interface.
//  2. Each scenario is performed by the auditor — the screen-reader persona —
//     exactly as in the schedule command, with evidence screenshots and a
//     recording.
//  3. A filter agent rules on every scenario and finding: caused or touched
//     by the pull request, or pre-existing. The orchestrator applies the
//     verdicts; the agent never rewrites the report.
//
// Usage: pull_request <pr-number> --repository owner/name --output-dir DIR,
// with GITHUB_TOKEN in the environment (an installation token with read
// access to the repository). The CWD is the repository root, checked out at
// the pull request's head, carrying the .axeqrc.json that says how to bring
// the application up and which URLs to audit.
//
// Output, under the output dir: files/audit.json, files/audit.md and
// files/scenarios.json hold the filtered result — what the pull request is
// responsible for. files/unfiltered/ holds the same three before filtering,
// files/filter.json the verdicts, files/pull-request.json and
// files/pull-request.diff what was reviewed. screenshots/ and recordings/ are
// shared by both views.
func Run(argsReceived []string, flagsReceived map[string][]string) {
	// run's deferred cleanup (browser and app teardown) must fire before the
	// process exits, and os.Exit skips defers — so the exit happens out here,
	// after run has returned.
	if code := run(argsReceived, flagsReceived); code != 0 {
		os.Exit(code)
	}
}

func run(argsReceived []string, flagsReceived map[string][]string) int {
	argsRequested := []args.ArgRequested{
		{Name: "pr-number", Required: true},
	}
	flagsRequested := []args.FlagRequested{
		{Name: "output-dir", MinimumCount: 1, MaximumCount: 1, Required: true},
		{Name: "repository", MinimumCount: 1, MaximumCount: 1, Required: true},
	}
	cleanArgs, cleanFlags := args.ValidateArgs("pull_request", argsReceived, flagsReceived, argsRequested, flagsRequested)

	number, err := strconv.Atoi(strings.TrimPrefix(cleanArgs[0], "#"))
	if err != nil || number < 1 {
		harness.Fatalf("invalid pull request number %q (expected a positive integer)", cleanArgs[0])
	}
	owner, repoName, ok := strings.Cut(cleanFlags["repository"][0], "/")
	if !ok || owner == "" || repoName == "" || strings.Contains(repoName, "/") {
		harness.Fatalf("invalid --repository %q (expected owner/name)", cleanFlags["repository"][0])
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		harness.Fatalf("GITHUB_TOKEN is not set (a token with read access to %s/%s is needed to fetch the pull request)", owner, repoName)
	}

	// The repository's config is the audit's input. A missing or broken file
	// is the user's to fix, so it is reported and exits 1.
	cfg, err := config.Load(".")
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			cwd, _ := os.Getwd()
			harness.Fatalf("no %s found in %s (the pull_request command runs from the root of the repository being audited, which must carry that file)", config.FileName, cwd)
		}
		harness.Fatalf("%v", err)
	}

	// Budgets come from the config's schedule section, shared with the
	// schedule command; the planner is one turn, so only the scenario count
	// and the per-scenario turns apply.
	var sched config.Schedule
	if cfg.Schedule != nil {
		sched = *cfg.Schedule
	}
	maxScenarios := harness.Budget("schedule.maxScenarios", sched.MaxScenarios, harness.DefaultMaxScenarios, harness.MaxMaxScenarios)
	maxScenarioTurns := harness.Turns("schedule.maxScenarioTurns", sched.MaxScenarioTurns)
	userFocus := sched.Prompt

	// The pull request, fetched before anything is started: a wrong number or
	// a token without access should fail fast and cheaply.
	fmt.Printf("Fetching pull request #%d of %s/%s\n", number, owner, repoName)
	ghpr, err := github.FetchPullRequest(owner, repoName, number, token)
	if err != nil {
		harness.Fatalf("%v", err)
	}
	diff, notes := github.TrimDiff(ghpr.Diff)
	pr := audit.PullRequest{
		Number: ghpr.Number, Title: ghpr.Title, Body: ghpr.Body, Url: ghpr.HtmlUrl,
		HeadRef: ghpr.Head.Ref, BaseRef: ghpr.Base.Ref,
		ChangedFiles: github.ChangedFiles(ghpr.Diff), Diff: diff, Notes: notes,
	}
	fmt.Printf("#%d %s (%s into %s, %d changed file(s))\n", pr.Number, pr.Title, pr.HeadRef, pr.BaseRef, len(pr.ChangedFiles))
	for _, n := range notes {
		fmt.Println("note: " + n)
	}

	env := harness.Prepare(cfg, cleanFlags["output-dir"][0])
	defer harness.Teardown(env)
	urls := env.Urls
	unfilteredDir := filepath.Join(env.FilesDir, "unfiltered")

	// What was reviewed, for the record.
	harness.WriteJSON(filepath.Join(env.FilesDir, "pull-request.json"), ghpr)
	writeText(filepath.Join(env.FilesDir, "pull-request.diff"), ghpr.Diff)

	report := audit.Report{
		Title:     fmt.Sprintf("Accessibility audit of pull request #%d: %s", pr.Number, pr.Title),
		Urls:      urls,
		StartedAt: time.Now(),
	}
	report.Notes = append(report.Notes, fmt.Sprintf("**Pull request:** [#%d](%s), %s into %s", pr.Number, pr.Url, pr.HeadRef, pr.BaseRef))

	// Phase 1 — planning, from the diff. No browser, so no recording.
	fmt.Printf("\n=== Planning scenarios for #%d ===\n", pr.Number)
	plan, err := audit.Plan(env.Repo, pr, urls, userFocus, maxScenarios)
	if code := harness.AgentFailure(err); code != 0 {
		return code
	}
	if plan.Summary != "" {
		report.Notes = append(report.Notes, "**Change under review:** "+plan.Summary)
	}
	for _, nc := range plan.NotCovered {
		report.Notes = append(report.Notes, "**Not covered:** "+nc)
	}
	scenarios := harness.NormalizeScenarios(plan.Scenarios, urls, maxScenarios)
	report.Scenarios = scenarios

	// A pull request with no user-facing change is a legitimate outcome, not
	// an error: the report says so and the run ends.
	if len(scenarios) == 0 {
		fmt.Println("The planner found nothing in this pull request for a screen-reader user to exercise.")
		report.Notes = append(report.Notes, "**Result:** no scenario was planned — the pull request has no change a screen-reader user would meet.")
		harness.WriteJSON(filepath.Join(env.FilesDir, "scenarios.json"), scenarios)
		finish(env.FilesDir, report, "")
		return 0
	}

	harness.WriteJSON(filepath.Join(unfilteredDir, "scenarios.json"), scenarios)
	fmt.Printf("\nScenarios (%d):\n", len(scenarios))
	for i, sc := range scenarios {
		fmt.Printf("  %2d. %s — %s\n", i+1, sc.Title, sc.StartUrl)
	}

	// Phase 2 — one fresh auditor conversation per scenario, each its own
	// recording. An interrupt mid-way skips the filter: the unfiltered report
	// of what was observed is written as the result, flagged as partial.
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
			report.Notes = append(report.Notes, "**Unfiltered:** the run was interrupted before the findings could be filtered against the pull request.")
			harness.WriteJSON(filepath.Join(env.FilesDir, "scenarios.json"), scenarios)
			finish(env.FilesDir, report, "")
			return code
		}
	}

	// The complete, unfiltered record.
	report.FinishedAt = time.Now()
	harness.WriteReport(unfilteredDir, report)
	fmt.Printf("\nUnfiltered report: %s\n", filepath.Join(unfilteredDir, "audit.md"))

	// Phase 3 — the filter rules on everything; the verdicts are applied here.
	fmt.Printf("\n=== Filtering %d scenario(s) against the diff ===\n", len(report.Results))
	verdicts, err := audit.Filter(env.Repo, pr, report.Results)
	if code := harness.AgentFailure(err); code != 0 {
		report.Partial = true
		report.Notes = append(report.Notes, "**Unfiltered:** the run was interrupted before the findings could be filtered against the pull request.")
		harness.WriteJSON(filepath.Join(env.FilesDir, "scenarios.json"), scenarios)
		finish(env.FilesDir, report, "")
		return code
	}

	filtered, excluded := applyVerdicts(report, verdicts)
	harness.WriteJSON(filepath.Join(env.FilesDir, "filter.json"), filterRecord{Verdicts: verdicts, Excluded: excluded})
	harness.WriteJSON(filepath.Join(env.FilesDir, "scenarios.json"), filtered.Scenarios)
	finish(env.FilesDir, filtered, renderExcluded(excluded, verdicts.Summary))
	return 0
}

// exclusion is one thing the filter set aside, for filter.json and the
// report's closing section.
type exclusion struct {
	Scenario string `json:"scenario"`
	// Finding is the 1-based position within the scenario, 0 when the whole
	// scenario was set aside.
	Finding     int    `json:"finding,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
}

type filterRecord struct {
	Verdicts audit.FilterResponse `json:"verdicts"`
	Excluded []exclusion          `json:"excluded"`
}

// applyVerdicts builds the filtered report: scenarios the filter ruled
// unrelated go entirely, and within the rest only related findings stay. A
// scenario or finding the filter returned no verdict for is kept — better one
// pre-existing finding too many than one caused by the change too few — with
// that noted in the reason. Kept scenarios keep their outcome and summary
// even when every finding of theirs was set aside.
func applyVerdicts(report audit.Report, v audit.FilterResponse) (audit.Report, []exclusion) {
	scenarioVerdict := map[string]audit.ScenarioVerdict{}
	for _, sv := range v.Scenarios {
		scenarioVerdict[sv.ID] = sv
	}
	type key struct {
		id      string
		finding int
	}
	findingVerdict := map[key]audit.FindingVerdict{}
	for _, fv := range v.Findings {
		findingVerdict[key{fv.ScenarioID, fv.Finding}] = fv
	}

	filtered := report
	filtered.Scenarios = nil
	filtered.Results = nil
	var excluded []exclusion

	for _, res := range report.Results {
		sc := res.Scenario
		if sv, ok := scenarioVerdict[sc.ID]; ok && !sv.Related {
			excluded = append(excluded, exclusion{Scenario: sc.Title, Description: "whole scenario: " + sc.Goal, Reason: sv.Reason})
			continue
		}

		kept := res
		kept.Findings = []audit.Finding{}
		for i, f := range res.Findings {
			fv, ok := findingVerdict[key{sc.ID, i + 1}]
			switch {
			case !ok:
				fmt.Printf("note: no verdict for finding %d of scenario %q; keeping it\n", i+1, sc.Title)
				kept.Findings = append(kept.Findings, f)
			case fv.Related:
				kept.Findings = append(kept.Findings, f)
			default:
				excluded = append(excluded, exclusion{Scenario: sc.Title, Finding: i + 1, Severity: f.Severity, Description: f.Description, Reason: fv.Reason})
			}
		}
		filtered.Scenarios = append(filtered.Scenarios, sc)
		filtered.Results = append(filtered.Results, kept)
	}

	// Planned-but-not-performed scenarios (only on a partial run) stay listed.
	for i := len(report.Results); i < len(report.Scenarios); i++ {
		filtered.Scenarios = append(filtered.Scenarios, report.Scenarios[i])
	}
	return filtered, excluded
}

// renderExcluded is the report's closing section: what the filter set aside
// and why, so its judgement can be checked against files/unfiltered/.
func renderExcluded(excluded []exclusion, summary string) string {
	var b strings.Builder
	b.WriteString("## Set aside as unrelated to this pull request\n\n")
	if summary != "" {
		b.WriteString(summary + "\n\n")
	}
	if len(excluded) == 0 {
		b.WriteString("Nothing was set aside: every finding above is attributed to the pull request.\n\n")
		return b.String()
	}
	b.WriteString("The complete, unfiltered results are in `unfiltered/audit.md`.\n\n")
	for _, e := range excluded {
		if e.Finding == 0 {
			fmt.Fprintf(&b, "- **%s** — %s\n  - _Why:_ %s\n", e.Scenario, e.Description, e.Reason)
			continue
		}
		fmt.Fprintf(&b, "- **%s**, hindrance %d (%s) — %s\n  - _Why:_ %s\n", e.Scenario, e.Finding, e.Severity, e.Description, e.Reason)
	}
	b.WriteString("\n")
	return b.String()
}

// finish stamps the report, writes audit.json and audit.md (with the extra
// section appended, when given), and prints where they are plus a
// one-line-per-scenario summary.
func finish(filesOutDir string, report audit.Report, extra string) {
	report.FinishedAt = time.Now()
	jsonPath, mdPath := harness.WriteReport(filesOutDir, report)
	if extra != "" {
		md, err := os.ReadFile(mdPath)
		if err != nil {
			panic(fmt.Errorf("re-reading report %s: %w", mdPath, err))
		}
		writeText(mdPath, string(md)+extra)
	}

	fmt.Println()
	switch {
	case report.Partial:
		fmt.Printf("Audit interrupted: %d of %d scenario(s) performed\n", len(report.Results), len(report.Scenarios))
	case len(report.Results) == 0:
		fmt.Println("Audit complete: nothing to exercise")
	default:
		fmt.Printf("Audit complete: %d scenario(s) attributed to the pull request\n", len(report.Results))
	}
	for i, r := range report.Results {
		fmt.Printf("  %2d. %-26s %2d hindrance(s)  %s\n", i+1, r.Outcome, len(r.Findings), r.Scenario.Title)
	}
	fmt.Printf("Report: %s\n", mdPath)
	fmt.Printf("Data:   %s\n", jsonPath)
}

// writeText writes s to path, creating the parent dir as needed.
func writeText(path, s string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(fmt.Errorf("creating dir for %s: %w", path, err))
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		panic(fmt.Errorf("writing %s: %w", path, err))
	}
}
