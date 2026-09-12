package audit

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed PLANNER_PROMPT.md
var plannerPrompt string

// PullRequest is what the pull_request command's agents are told about the
// change under review. Diff is already trimmed of noise and capped; Notes
// says what was left out of it.
type PullRequest struct {
	Number       int
	Title        string
	Body         string
	Url          string
	HeadRef      string
	BaseRef      string
	ChangedFiles []string
	Diff         string
	Notes        []string
}

// repoTools lets the planner and the filter read and search the repository
// they run from. Neither drives the browser, so neither gets actions.
var repoTools = []string{"Read", "Grep", "Glob"}

// Plan runs the planner: one turn, no browser, that turns the pull request's
// diff into the scenarios worth auditing. It runs the claude CLI from repoDir
// — the checked-out repository at the pull request's head — so the agent can
// read and grep the code the diff touches. urls are the configured entry
// points; userFocus is the optional guidance from the config; maxScenarios
// bounds how many scenarios it may write.
func Plan(repoDir string, pr PullRequest, urls []string, userFocus string, maxScenarios int) (PlannerResponse, error) {
	res, err := prompt.RenderStrict(plannerPrompt, map[string]prompt.Param{
		"repo_dir":         prompt.Text(repoDir),
		"pull_request":     prompt.Text(renderPullRequest(pr)),
		"changed_files":    prompt.Text(renderChangedFiles(pr)),
		"diff":             prompt.Text(pr.Diff),
		"site_scope":       prompt.Text(renderScope(urls)),
		"user_focus":       prompt.Text(renderUserFocus(userFocus)),
		"max_scenarios":    prompt.Text(fmt.Sprintf("%d", maxScenarios)),
		"scenario_example": prompt.JSON(exampleScenario),
		"json_schema":      prompt.JSON(json.RawMessage(plannerSchema)),
	})
	if err != nil {
		return PlannerResponse{}, fmt.Errorf("rendering planner prompt: %w", err)
	}

	req := request("planner", repoDir, res.LLM, plannerSchema, "", "", repoTools)

	resp, _, err := aclio.RunStructured[PlannerResponse](req)
	if err != nil {
		return PlannerResponse{}, fmt.Errorf("planner interaction: %w", err)
	}
	return resp, nil
}

// renderPullRequest fills the {pull_request} block: number, title, branches,
// URL, and the description as the author wrote it.
func renderPullRequest(pr PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- #%d: %s\n", pr.Number, pr.Title)
	if pr.Url != "" {
		fmt.Fprintf(&b, "- URL: %s\n", pr.Url)
	}
	if pr.HeadRef != "" || pr.BaseRef != "" {
		fmt.Fprintf(&b, "- Branch: %s into %s\n", pr.HeadRef, pr.BaseRef)
	}
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		body = "(no description)"
	}
	b.WriteString("\nDescription:\n\n" + body + "\n")
	return b.String()
}

// renderChangedFiles fills the {changed_files} list, with the notes about
// what the trimmed diff leaves out.
func renderChangedFiles(pr PullRequest) string {
	var b strings.Builder
	for _, f := range pr.ChangedFiles {
		b.WriteString("- " + f + "\n")
	}
	if len(pr.ChangedFiles) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, n := range pr.Notes {
		b.WriteString("\nNote: " + n + "\n")
	}
	return b.String()
}
