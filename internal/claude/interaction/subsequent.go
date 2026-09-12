package interaction

import (
	"axeq/internal/playwright"
	_ "embed"
	"fmt"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed SUBSEQUENT_PROMPT.md
var subsequentPrompt string

// Subsequent continues an existing conversation (resumed via convoId). It tells
// the agent which actions were just performed (compact JSON, in order) and that
// the resulting screenshot + DOM have been added to screenshotsDir. Like
// Initial, it never touches the browser/page.
func Subsequent(convoId, screenshotsDir string, performed []playwright.PerformedAction, model, systemPromptFile string, preset playwright.Preset, screenshotPath, domPath string, focused *playwright.FocusedElement, tabs []playwright.Tab) (InteractionResponse, error) {
	res, err := prompt.RenderStrict(subsequentPrompt, map[string]prompt.Param{
		"last_action_set":             prompt.JSON(performed),
		"preset_specific_information": prompt.Text(presetSpecific(preset, screenshotPath, domPath, focused, tabs)),
	})
	if err != nil {
		return InteractionResponse{}, fmt.Errorf("rendering subsequent prompt: %w", err)
	}

	req := request("interaction-subsequent", screenshotsDir, res.LLM, model, presetSchema(preset), systemPromptFile, convoId)

	resp, _, err := aclio.RunStructured[InteractionResponse](req)
	if err != nil {
		return InteractionResponse{}, fmt.Errorf("agent interaction: %w", err)
	}

	return resp, nil
}
