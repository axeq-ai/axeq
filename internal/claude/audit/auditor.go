package audit

import (
	"axeq/internal/playwright"
	_ "embed"
	"encoding/json"
	"fmt"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed AUDITOR_INITIAL_PROMPT.md
var auditorInitialPrompt string

//go:embed AUDITOR_SUBSEQUENT_PROMPT.md
var auditorSubsequentPrompt string

// AuditInitial starts a fresh auditor conversation for one scenario. The
// auditor is the screen-reader persona: it is handed the scenario and the
// focused element (nil right after a page load — nothing focused), never a
// screenshot or DOM. It runs the claude CLI from agentDir and returns the
// conversation id for follow-up turns plus the parsed structured response.
// turnBudget is the total number of turns the auditor gets for the scenario.
func AuditInitial(agentDir, systemPromptFile string, scenario Scenario, focused *playwright.FocusedElement, tabs []playwright.Tab, turnBudget int) (convoId string, response AuditorResponse, err error) {
	res, err := prompt.RenderStrict(auditorInitialPrompt, map[string]prompt.Param{
		"focused_state": prompt.Text(renderFocusedState(focused, tabs)),
		"scenario":      prompt.JSON(scenario),
		"turn_budget":   prompt.Text(fmt.Sprintf("%d", turnBudget)),
		"json_schema":   prompt.JSON(json.RawMessage(auditorSchema)),
	})
	if err != nil {
		return "", AuditorResponse{}, fmt.Errorf("rendering auditor initial prompt: %w", err)
	}

	req := request("auditor-initial", agentDir, res.LLM, auditorSchema, systemPromptFile, "", nil)

	resp, result, err := aclio.RunStructured[AuditorResponse](req)
	if err != nil {
		return "", AuditorResponse{}, fmt.Errorf("auditor interaction: %w", err)
	}

	return result.SessionID, resp, nil
}

// AuditSubsequent continues an auditor conversation (resumed via convoId). It
// reports which actions were just performed (compact JSON, in order), the
// element now focused, and how many turns are left including this one — at 1
// the auditor must finish the scenario one way or the other.
func AuditSubsequent(convoId, agentDir string, performed []playwright.PerformedAction, systemPromptFile string, focused *playwright.FocusedElement, tabs []playwright.Tab, turnsLeft int) (AuditorResponse, error) {
	res, err := prompt.RenderStrict(auditorSubsequentPrompt, map[string]prompt.Param{
		"last_action_set": prompt.JSON(performed),
		"focused_state":   prompt.Text(renderFocusedState(focused, tabs)),
		"budget_note":     prompt.Text(auditorBudgetNote(turnsLeft)),
	})
	if err != nil {
		return AuditorResponse{}, fmt.Errorf("rendering auditor subsequent prompt: %w", err)
	}

	req := request("auditor-subsequent", agentDir, res.LLM, auditorSchema, systemPromptFile, convoId, nil)

	resp, _, err := aclio.RunStructured[AuditorResponse](req)
	if err != nil {
		return AuditorResponse{}, fmt.Errorf("auditor interaction: %w", err)
	}

	return resp, nil
}

// renderFocusedState is the screen-reader view: the focused element as compact
// JSON (or a note that nothing is focused) and the open tabs, whose titles
// stand in for the page-title announcement.
func renderFocusedState(focused *playwright.FocusedElement, tabs []playwright.Tab) string {
	var state string
	if focused == nil {
		state = "No element is currently focused."
	} else {
		b, _ := json.Marshal(focused)
		state = "Current focused element: " + string(b)
	}
	return state + "\n\n" + renderTabs(tabs)
}

// auditorBudgetNote tells the auditor where it stands in its turn budget.
// turnsLeft counts the turn being prompted for, so 1 is the final turn.
func auditorBudgetNote(turnsLeft int) string {
	switch {
	case turnsLeft <= 1:
		return "This is your final turn: set `scenarioComplete` to true, choose an `outcome` (abandoned if the goal was not reached), report any last hindrance, and write the summary."
	case turnsLeft <= 3:
		return fmt.Sprintf("Only %d turns remain including this one. If the goal is not within reach, record what is blocking you and finish.", turnsLeft)
	default:
		return fmt.Sprintf("Turns remaining including this one: %d.", turnsLeft)
	}
}
