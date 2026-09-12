package schedule

import (
	"axeq/internal/claude/audit"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// severityRank orders hindrance severities worst-first for sorting and for the
// summary table's columns.
var severityRank = map[string]int{"critical": 0, "serious": 1, "moderate": 2, "minor": 3}

var severities = []string{"critical", "serious", "moderate", "minor"}

// writeReport writes the run's results into filesOutDir as audit.json (the
// full record, for tooling) and audit.md (the human-readable report), and
// returns both paths. Links in the markdown are relative to files/, so the
// evidence screenshots and recordings — siblings of it under the run folder —
// are reached via ../.
func writeReport(filesOutDir string, report audit.Report) (jsonPath, mdPath string) {
	jsonPath = filepath.Join(filesOutDir, "audit.json")
	writeJSON(jsonPath, report)

	mdPath = filepath.Join(filesOutDir, "audit.md")
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(report)), 0o644); err != nil {
		panic(fmt.Errorf("writing report %s: %w", mdPath, err))
	}
	return jsonPath, mdPath
}

// writeJSON writes v as indented JSON to path, creating the parent dir as
// needed.
func writeJSON(path string, v any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(fmt.Errorf("creating dir for %s: %w", path, err))
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Errorf("encoding %s: %w", path, err))
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		panic(fmt.Errorf("writing %s: %w", path, err))
	}
}

// renderMarkdown renders the report: a header describing the run, a summary
// table (one row per scenario with its outcome and hindrance counts by
// severity), every finding worst-first, and then each scenario in full — its
// steps, outcome, hindrances with evidence, recording, and the auditor's
// turn-by-turn transcript.
func renderMarkdown(r audit.Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Accessibility audit: %s\n\n", strings.Join(hostsOf(r.Urls), ", "))

	b.WriteString("- **Audited:** " + strings.Join(r.Urls, ", ") + "\n")
	b.WriteString("- **Persona:** blind screen-reader user, keyboard only (perceives only the focused element and tab titles)\n")
	fmt.Fprintf(&b, "- **Run:** %s to %s\n", r.StartedAt.Format("2006-01-02 15:04"), r.FinishedAt.Format("2006-01-02 15:04"))
	if r.Partial {
		fmt.Fprintf(&b, "- **Partial:** the run was interrupted after %d of %d planned scenarios\n", len(r.Results), len(r.Scenarios))
	}
	b.WriteString("\n")

	// Summary table.
	b.WriteString("## Summary\n\n")
	b.WriteString("| # | Scenario | Outcome | Critical | Serious | Moderate | Minor |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	totals := map[string]int{}
	for i, res := range r.Results {
		counts := countBySeverity(res.Findings)
		for _, s := range severities {
			totals[s] += counts[s]
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %d | %d | %d | %d |\n", i+1, res.Scenario.Title, res.Outcome, counts["critical"], counts["serious"], counts["moderate"], counts["minor"])
	}
	fmt.Fprintf(&b, "| | **Total** | | **%d** | **%d** | **%d** | **%d** |\n\n", totals["critical"], totals["serious"], totals["moderate"], totals["minor"])

	// Every finding, worst first, as a skimmable list pointing into the
	// scenario sections below.
	type ranked struct {
		scenarioIndex int
		finding       audit.Finding
	}
	var all []ranked
	for i, res := range r.Results {
		for _, f := range res.Findings {
			all = append(all, ranked{i, f})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		return severityRank[all[i].finding.Severity] < severityRank[all[j].finding.Severity]
	})
	b.WriteString("## Findings by severity\n\n")
	if len(all) == 0 {
		b.WriteString("No hindrances were reported.\n\n")
	}
	for _, x := range all {
		f := x.finding
		fmt.Fprintf(&b, "- **%s** · %s · scenario %d step %d — %s", f.Severity, f.Category, x.scenarioIndex+1, f.Step, f.Description)
		if f.Wcag != nil && *f.Wcag != "" {
			fmt.Fprintf(&b, " _(WCAG %s)_", *f.Wcag)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Each scenario in full.
	b.WriteString("## Scenarios\n\n")
	for i, res := range r.Results {
		sc := res.Scenario
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, sc.Title)
		fmt.Fprintf(&b, "**Goal.** %s\n\n", sc.Goal)
		fmt.Fprintf(&b, "**Start:** %s\n\n", sc.StartUrl)
		if len(sc.Focus) > 0 {
			fmt.Fprintf(&b, "**Exercises:** %s\n\n", strings.Join(sc.Focus, ", "))
		}
		b.WriteString("**Steps:**\n\n")
		for j, step := range sc.Steps {
			fmt.Fprintf(&b, "%d. %s\n", j+1, step)
		}
		fmt.Fprintf(&b, "\n**Success criteria:** %s\n\n", sc.SuccessCriteria)
		fmt.Fprintf(&b, "**Outcome:** %s after %d turn(s)\n\n", res.Outcome, res.Turns)
		if res.Summary != "" {
			b.WriteString(res.Summary + "\n\n")
		}
		if res.Recording != "" {
			fmt.Fprintf(&b, "**Recording:** [%s](../%s)\n\n", filepath.Base(res.Recording), res.Recording)
		}

		b.WriteString("#### Hindrances\n\n")
		if len(res.Findings) == 0 {
			b.WriteString("None reported.\n\n")
		}
		for j, f := range res.Findings {
			fmt.Fprintf(&b, "##### %d.%d — %s · %s\n\n", i+1, j+1, f.Severity, f.Category)
			fmt.Fprintf(&b, "- **Step:** %d (turn %d)\n", f.Step, f.Turn)
			if f.Url != "" {
				fmt.Fprintf(&b, "- **Where:** %s\n", f.Url)
			}
			if f.Wcag != nil && *f.Wcag != "" {
				fmt.Fprintf(&b, "- **WCAG:** %s\n", *f.Wcag)
			}
			fmt.Fprintf(&b, "- **What happened:** %s\n", f.Description)
			if f.Element != nil && *f.Element != "" {
				fmt.Fprintf(&b, "- **Element:** %s\n", *f.Element)
			}
			fmt.Fprintf(&b, "- **Recommendation:** %s\n", f.Recommendation)
			if f.Screenshot != "" {
				fmt.Fprintf(&b, "\n![Page state when the hindrance was reported](../%s)\n", f.Screenshot)
			}
			b.WriteString("\n")
		}

		if len(res.Narration) > 0 {
			b.WriteString("#### Transcript\n\n")
			fmt.Fprintf(&b, "<details><summary>%d turn(s)</summary>\n\n", res.Turns)
			for _, line := range res.Narration {
				b.WriteString("- " + line + "\n")
			}
			b.WriteString("\n</details>\n\n")
		}
	}

	// Scenarios planned but never reached, on an interrupted run.
	if len(r.Results) < len(r.Scenarios) {
		b.WriteString("## Not performed\n\n")
		for i := len(r.Results); i < len(r.Scenarios); i++ {
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, r.Scenarios[i].Title, r.Scenarios[i].StartUrl)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// countBySeverity tallies findings per severity.
func countBySeverity(findings []audit.Finding) map[string]int {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	return counts
}

// hostsOf lists the distinct hosts of the URLs, in first-seen order — the
// report's title.
func hostsOf(urls []string) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		if !seen[u.Host] {
			seen[u.Host] = true
			hosts = append(hosts, u.Host)
		}
	}
	return hosts
}
