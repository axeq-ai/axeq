package audit

import (
	_ "embed"
	"encoding/json"
	"fmt"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed FILTER_PROMPT.md
var filterPrompt string

// Filter runs the filter: one turn, no browser, that rules on every performed
// scenario and every finding — related to the pull request, or pre-existing.
// It runs the claude CLI from repoDir so it can trace each hindrance to code.
// The orchestrator applies the verdicts; the agent never rewrites the report.
func Filter(repoDir string, pr PullRequest, results []ScenarioResult) (FilterResponse, error) {
	res, err := prompt.RenderStrict(filterPrompt, map[string]prompt.Param{
		"repo_dir":     prompt.Text(repoDir),
		"pull_request": prompt.Text(renderPullRequest(pr)),
		"diff":         prompt.Text(pr.Diff),
		"results":      prompt.JSON(filterView(results)),
		"json_schema":  prompt.JSON(json.RawMessage(filterSchema)),
	})
	if err != nil {
		return FilterResponse{}, fmt.Errorf("rendering filter prompt: %w", err)
	}

	req := request("filter", repoDir, res.LLM, filterSchema, "", "", repoTools)

	resp, _, err := aclio.RunStructured[FilterResponse](req)
	if err != nil {
		return FilterResponse{}, fmt.Errorf("filter interaction: %w", err)
	}
	return resp, nil
}

// The filter's view of the results: what it needs to rule and nothing that
// would bloat the prompt — no narration, no turn counts, no recording paths.
// Findings carry their 1-based position so verdicts can point back at them.
type filterScenarioView struct {
	ID       string              `json:"id"`
	Title    string              `json:"title"`
	Goal     string              `json:"goal"`
	StartUrl string              `json:"startUrl"`
	Steps    []string            `json:"steps"`
	Outcome  string              `json:"outcome"`
	Summary  string              `json:"summary"`
	Findings []filterFindingView `json:"findings"`
}

type filterFindingView struct {
	Finding        int     `json:"finding"`
	Step           int     `json:"step"`
	Severity       string  `json:"severity"`
	Category       string  `json:"category"`
	Description    string  `json:"description"`
	Element        *string `json:"element,omitempty"`
	Recommendation string  `json:"recommendation"`
	Url            string  `json:"url,omitempty"`
}

func filterView(results []ScenarioResult) []filterScenarioView {
	out := make([]filterScenarioView, 0, len(results))
	for _, r := range results {
		v := filterScenarioView{
			ID: r.Scenario.ID, Title: r.Scenario.Title, Goal: r.Scenario.Goal,
			StartUrl: r.Scenario.StartUrl, Steps: r.Scenario.Steps,
			Outcome: r.Outcome, Summary: r.Summary, Findings: []filterFindingView{},
		}
		for i, f := range r.Findings {
			v.Findings = append(v.Findings, filterFindingView{
				Finding: i + 1, Step: f.Step, Severity: f.Severity, Category: f.Category,
				Description: f.Description, Element: f.Element, Recommendation: f.Recommendation, Url: f.Url,
			})
		}
		out = append(out, v)
	}
	return out
}
