package interaction

import (
	"encoding/json"
	"testing"

	"axeq/internal/playwright"

	"github.com/agenticcliorchestra/aclio-go/prompt"
)

// The param sets below mirror Initial and Subsequent. If a placeholder is
// renamed in a template without updating the code (or vice versa), rendering
// fails here instead of aborting a live run.

func TestInitialPromptRenders(t *testing.T) {
	res, err := prompt.Render(initialPrompt, map[string]prompt.Param{
		"preset_specific_information": prompt.Text("state"),
		"attachments":                 prompt.Text(""),
		"user_prompt":                 prompt.Text("do the thing"),
		"json_schema":                 prompt.JSON(json.RawMessage(presetSchema(playwright.PresetDefault))),
		"examples":                    prompt.Text(renderExamples(examples)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// {name} is prose in the template (screenshot/file naming docs), the one
	// leftover Initial tolerates.
	for _, ph := range res.Unreplaced {
		if ph != "{name}" {
			t.Errorf("unbound placeholder %s", ph)
		}
	}
}

func TestSubsequentPromptRenders(t *testing.T) {
	_, err := prompt.RenderStrict(subsequentPrompt, map[string]prompt.Param{
		"last_action_set":             prompt.JSON([]playwright.PerformedAction{}),
		"preset_specific_information": prompt.Text("state"),
	})
	if err != nil {
		t.Fatal(err)
	}
}
