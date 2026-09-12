package audit

import (
	"axeq/internal/playwright"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed EXPLORER_INITIAL_PROMPT.md
var explorerInitialPrompt string

//go:embed EXPLORER_SUBSEQUENT_PROMPT.md
var explorerSubsequentPrompt string

// exampleScenario is rendered into the explorer's initial prompt so it sees
// the level of detail — and the intent-only phrasing — a scenario needs.
var exampleScenario = Scenario{
	ID:       "newsletter-signup",
	Title:    "Subscribe to the newsletter from the footer",
	Goal:     "A visitor wants to receive the newsletter and signs up with their email address from the site footer.",
	StartUrl: "https://example.com/",
	Steps: []string{
		"From the top of the home page, get to the footer without tabbing through every link in the main content (a skip link or landmark navigation should help).",
		"Find the newsletter sign-up field in the footer and move focus into it.",
		"Type the address qa+newsletter@example.com into the field.",
		"Submit the form with the keyboard and find out whether it was accepted.",
	},
	SuccessCriteria: "Focus lands on, or the announcement includes, a confirmation message that the subscription was received; the field is identified by name before typing.",
	Focus:           []string{"skip-link", "landmarks", "forms", "live-region"},
}

// ExploreInitial starts the explorer's conversation. It runs the claude CLI
// from agentDir (where the captured screenshot + DOM live) and returns the
// conversation id for follow-up turns plus the parsed structured response.
// urls are the entry points the orchestrator opened as tabs — they define the
// site's scope; userFocus is the optional guidance from --prompt; turnBudget
// is the total number of turns the explorer gets.
func ExploreInitial(agentDir, systemPromptFile string, urls []string, userFocus, screenshotPath, domPath string, tabs []playwright.Tab, turnBudget int) (convoId string, response ExplorerResponse, err error) {
	res, err := prompt.RenderStrict(explorerInitialPrompt, map[string]prompt.Param{
		"page_state":       prompt.Text(renderPageState(screenshotPath, domPath, tabs)),
		"site_scope":       prompt.Text(renderScope(urls)),
		"user_focus":       prompt.Text(renderUserFocus(userFocus)),
		"turn_budget":      prompt.Text(fmt.Sprintf("%d", turnBudget)),
		"json_schema":      prompt.JSON(json.RawMessage(explorerSchema)),
		"scenario_example": prompt.JSON(exampleScenario),
	})
	if err != nil {
		return "", ExplorerResponse{}, fmt.Errorf("rendering explorer initial prompt: %w", err)
	}

	req := request("explorer-initial", agentDir, res.LLM, explorerSchema, systemPromptFile, "", explorerTools)

	resp, result, err := aclio.RunStructured[ExplorerResponse](req)
	if err != nil {
		return "", ExplorerResponse{}, fmt.Errorf("explorer interaction: %w", err)
	}

	return result.SessionID, resp, nil
}

// ExploreSubsequent continues the explorer's conversation (resumed via
// convoId). It reports which actions were just performed (compact JSON, in
// order) and the resulting page state, and tells the explorer how many turns
// it has left including this one — at 1 it must return the scenarios.
func ExploreSubsequent(convoId, agentDir string, performed []playwright.PerformedAction, systemPromptFile, screenshotPath, domPath string, tabs []playwright.Tab, turnsLeft int) (ExplorerResponse, error) {
	res, err := prompt.RenderStrict(explorerSubsequentPrompt, map[string]prompt.Param{
		"last_action_set": prompt.JSON(performed),
		"page_state":      prompt.Text(renderPageState(screenshotPath, domPath, tabs)),
		"budget_note":     prompt.Text(explorerBudgetNote(turnsLeft)),
	})
	if err != nil {
		return ExplorerResponse{}, fmt.Errorf("rendering explorer subsequent prompt: %w", err)
	}

	req := request("explorer-subsequent", agentDir, res.LLM, explorerSchema, systemPromptFile, convoId, explorerTools)

	resp, _, err := aclio.RunStructured[ExplorerResponse](req)
	if err != nil {
		return ExplorerResponse{}, fmt.Errorf("explorer interaction: %w", err)
	}

	return resp, nil
}

// renderPageState is the sighted view: the screenshot + DOM paths and the open
// tabs.
func renderPageState(screenshotPath, domPath string, tabs []playwright.Tab) string {
	return fmt.Sprintf("- Screenshot (PNG): %s\n- Full DOM (HTML): %s", screenshotPath, domPath) + "\n\n" + renderTabs(tabs)
}

// renderTabs gives an agent the open tabs as compact JSON (one object per tab,
// in order). Everything the agent does targets the tab with "active":true; it
// manages tabs with tab-open / tab-switch / tab-close (1-based index).
func renderTabs(tabs []playwright.Tab) string {
	b, _ := json.Marshal(tabs)
	return "Open tabs (you act on the one with \"active\":true; tab-switch/tab-close take a 1-based index):\n" + string(b)
}

// renderScope fills the {site_scope} line: the entry URLs and the hosts they
// share, which is as far as the explorer may roam.
func renderScope(urls []string) string {
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
	return fmt.Sprintf("entry points %s; stay on %s (and subdomains of them).",
		strings.Join(urls, ", "), strings.Join(hosts, ", "))
}

// renderUserFocus fills the {user_focus} block: empty when the user gave no
// guidance, otherwise their notes framed as steering for the exploration and
// the scenarios.
func renderUserFocus(focus string) string {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return ""
	}
	return "The person running the audit added these notes. Follow them when\n" +
		"choosing what to explore and which scenarios to write, and put any test\n" +
		"data or credentials they give into the scenario steps that need them:\n\n" +
		focus + "\n"
}

// explorerBudgetNote tells the explorer where it stands in its turn budget.
// turnsLeft counts the turn being prompted for, so 1 is the final turn.
func explorerBudgetNote(turnsLeft int) string {
	switch {
	case turnsLeft <= 1:
		return "This is your final turn: set `explorationComplete` to true and return the scenarios now, based on what you have seen."
	case turnsLeft <= 3:
		return fmt.Sprintf("Only %d turns remain including this one — start wrapping up, and return the scenarios no later than the final turn.", turnsLeft)
	default:
		return fmt.Sprintf("Turns remaining including this one: %d.", turnsLeft)
	}
}
