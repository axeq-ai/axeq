package interaction

import (
	"axeq/internal/playwright"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/prompt"
)

//go:embed INITIAL_PROMPT.md
var initialPrompt string

// Initial starts a new agent conversation. It runs the claude CLI from
// screenshotsDir (where the screenshots live), and returns the conversation id
// for follow-up turns plus the parsed structured response. It never touches the
// browser/page — the caller is responsible for capturing screenshots and
// applying actions. attachments names user-attached files already copied into
// screenshotsDir/attachments; they are announced in this initial prompt only.
func Initial(screenshotsDir, userPrompt, systemPromptFile string, preset playwright.Preset, screenshotPath, domPath string, focused *playwright.FocusedElement, tabs []playwright.Tab, attachments []string) (convoId string, response InteractionResponse, err error) {
	schema := presetSchema(preset)

	res, err := prompt.Render(initialPrompt, map[string]prompt.Param{
		"preset_specific_information": prompt.Text(presetSpecific(preset, screenshotPath, domPath, focused, tabs)),
		"attachments":                 prompt.Text(renderAttachments(attachments)),
		"user_prompt":                 prompt.Text(userPrompt),
		"json_schema":                 prompt.JSON(json.RawMessage(schema)),
		"examples":                    prompt.Text(renderExamples(examples)),
	})
	if err != nil {
		return "", InteractionResponse{}, fmt.Errorf("rendering initial prompt: %w", err)
	}
	// The template legitimately mentions {name} in prose (the screenshot/file
	// naming docs), so it is the one leftover allowed to survive rendering.
	for _, ph := range res.Unreplaced {
		if ph != "{name}" {
			return "", InteractionResponse{}, fmt.Errorf("initial prompt: unbound placeholder %s", ph)
		}
	}

	req := request("interaction-initial", screenshotsDir, res.LLM, schema, systemPromptFile, "")

	resp, result, err := aclio.RunStructured[InteractionResponse](req)
	if err != nil {
		return "", InteractionResponse{}, fmt.Errorf("agent interaction: %w", err)
	}

	return result.SessionID, resp, nil
}

// renderAttachments fills the {attachments} block of the initial prompt: empty
// when the user attached nothing, otherwise a note listing each file by the
// path it is readable at, relative to the agent's working directory.
func renderAttachments(attachments []string) string {
	if len(attachments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("The user attached files as inputs for this task. They are in the\n")
	b.WriteString("`attachments/` directory of your working directory — Read them when the\n")
	b.WriteString("task calls for their content:\n\n")
	for _, name := range attachments {
		b.WriteString("- attachments/" + name + "\n")
	}
	return b.String()
}
