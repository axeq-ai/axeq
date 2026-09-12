package interaction

import (
	"axeq/internal/playwright"
	"encoding/json"
	"fmt"
	"strings"
)

// Example is a hardcoded situation -> actions illustration shown to the agent in
// the initial prompt. Reason is optional.
type Example struct {
	Situation string
	Actions   []playwright.Action
	Reason    *string
}

func ptr[T any](v T) *T { return &v }

// examples are rendered into the {examples} placeholder of the initial prompt.
var examples = []Example{
	{
		Situation: "Waiting for animation to complete",
		Actions:   []playwright.Action{},
		Reason:    ptr("One response => playwright => prompt cycle takes time, if an animation is in progress or any other waiting is needed that will take less than 500ms, no actions is perfect, cycle takes ~500ms"),
	},
	{
		Situation: "Waiting needed, unsure about duration",
		Actions: []playwright.Action{
			{Type: playwright.ActionWait, Data: playwright.WaitAction{Ms: 2000}},
		},
		Reason: ptr("Wait 2s + cycle time is 2.5s, perfect for first time wait, not too long, not too short. If screenshot comes back with same state, increase to e.g. 5s. Use best judgement"),
	},
	{
		Situation: "Clicking the login button after filling in credentials",
		Actions: []playwright.Action{
			{Type: playwright.ActionMouse, Data: playwright.MouseAction{Action: "click", Selector: ptr("button[type=submit]")}},
		},
		Reason: ptr("Prefer a precise CSS selector grepped from the DOM over raw x/y coordinates — it survives layout shifts and is unambiguous"),
	},
	{
		Situation: "The user asked for a screenshot of the pricing table",
		Actions: []playwright.Action{
			{Type: playwright.ActionScreenshot, Data: playwright.ScreenshotAction{Name: "pricing-table", Selector: ptr("table.pricing")}},
		},
		Reason: ptr("A screenshot saves {name}.png for the user; target the whole page with full-page, one element with a selector, the element at a point with x/y, or the viewport with none of those — never combined. A taken name fails the action; resend with override true only to deliberately replace the file"),
	},
}

// renderExamples renders examples to prose (not a JSON array), each separated by
// a blank line. The actions of each example are inline compact JSON.
func renderExamples(exs []Example) string {
	parts := make([]string, 0, len(exs))
	for _, e := range exs {
		actionsJSON, _ := json.Marshal(e.Actions)
		s := fmt.Sprintf("For a situation like '%s', actions could be something like '%s'.", e.Situation, string(actionsJSON))
		if e.Reason != nil {
			s += fmt.Sprintf(" The reason for this is, %s", *e.Reason)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}
