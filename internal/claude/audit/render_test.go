package audit

import (
	"encoding/json"
	"testing"

	"axeq/internal/playwright"

	"github.com/agenticcliorchestra/aclio-go/prompt"
)

// The param sets below mirror the four agent turns. If a placeholder is
// renamed in a template without updating the code (or vice versa), rendering
// fails here instead of aborting a live run. All four templates are rendered
// strictly — none of them mentions a {placeholder}-shaped token in prose.

func TestExplorerInitialPromptRenders(t *testing.T) {
	_, err := prompt.RenderStrict(explorerInitialPrompt, map[string]prompt.Param{
		"page_state":       prompt.Text("state"),
		"site_scope":       prompt.Text(renderScope([]string{"https://example.com/"})),
		"user_focus":       prompt.Text(renderUserFocus("")),
		"turn_budget":      prompt.Text("30"),
		"json_schema":      prompt.JSON(json.RawMessage(explorerSchema)),
		"scenario_example": prompt.JSON(exampleScenario),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExplorerSubsequentPromptRenders(t *testing.T) {
	_, err := prompt.RenderStrict(explorerSubsequentPrompt, map[string]prompt.Param{
		"last_action_set": prompt.JSON([]playwright.PerformedAction{}),
		"page_state":      prompt.Text("state"),
		"budget_note":     prompt.Text(explorerBudgetNote(5)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditorInitialPromptRenders(t *testing.T) {
	_, err := prompt.RenderStrict(auditorInitialPrompt, map[string]prompt.Param{
		"focused_state": prompt.Text(renderFocusedState(nil, []playwright.Tab{})),
		"scenario":      prompt.JSON(exampleScenario),
		"turn_budget":   prompt.Text("40"),
		"json_schema":   prompt.JSON(json.RawMessage(auditorSchema)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditorSubsequentPromptRenders(t *testing.T) {
	_, err := prompt.RenderStrict(auditorSubsequentPrompt, map[string]prompt.Param{
		"last_action_set": prompt.JSON([]playwright.PerformedAction{}),
		"focused_state":   prompt.Text("state"),
		"budget_note":     prompt.Text(auditorBudgetNote(1)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The schemas are what keeps each agent inside its persona, so pin the parts
// that matter: the auditor never gets a mouse, go-to, or selector targeting;
// the explorer does.
func TestSchemasEncodePersonas(t *testing.T) {
	var explorer, auditor struct {
		Properties struct {
			PerformActions struct {
				Items struct {
					Properties struct {
						Type struct {
							Enum []string `json:"enum"`
						} `json:"type"`
						Data struct {
							Properties map[string]any `json:"properties"`
						} `json:"data"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"performActions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(explorerSchema), &explorer); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(auditorSchema), &auditor); err != nil {
		t.Fatal(err)
	}

	has := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}

	if !has(explorer.Properties.PerformActions.Items.Properties.Type.Enum, "mouse") {
		t.Error("explorer schema should allow mouse actions")
	}
	if _, ok := explorer.Properties.PerformActions.Items.Properties.Data.Properties["selector"]; !ok {
		t.Error("explorer schema should allow selector targeting")
	}

	auditorTypes := auditor.Properties.PerformActions.Items.Properties.Type.Enum
	for _, forbidden := range []string{"mouse", "go-to", "javascript", "screenshot", "write-file", "read-file"} {
		if has(auditorTypes, forbidden) {
			t.Errorf("auditor schema must not allow %q actions", forbidden)
		}
	}
	if !has(auditorTypes, "keyboard") {
		t.Error("auditor schema should allow keyboard actions")
	}
	if _, ok := auditor.Properties.PerformActions.Items.Properties.Data.Properties["selector"]; ok {
		t.Error("auditor schema must not allow selector targeting")
	}
}
