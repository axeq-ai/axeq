package interaction

import (
	"encoding/json"
	"fmt"

	"axeq/internal/playwright"

	"github.com/agenticcliorchestra/aclio-go/jsonschema"
)

// The per-preset structured-output schemas are generated from the playwright
// action structs (their json/enum/description tags), so the schema can never
// drift from what Action.UnmarshalJSON actually decodes. Each action stays a
// {type, data} envelope; data is the union of every allowed payload's fields
// (all optional) — which field is valid is determined by type and enforced by
// the Go decoder and the prompt, as before.

// actionData pairs each action type with the payload struct its data decodes
// into — the same pairing Action.UnmarshalJSON switches on.
var actionData = map[playwright.ActionType]any{
	playwright.ActionMouse:      playwright.MouseAction{},
	playwright.ActionKeyboard:   playwright.KeyboardAction{},
	playwright.ActionWait:       playwright.WaitAction{},
	playwright.ActionGoTo:       playwright.GoToAction{},
	playwright.ActionTabOpen:    playwright.TabOpenAction{},
	playwright.ActionTabSwitch:  playwright.TabSwitchAction{},
	playwright.ActionTabClose:   playwright.TabCloseAction{},
	playwright.ActionJavascript: playwright.JavascriptAction{},
	playwright.ActionScreenshot: playwright.ScreenshotAction{},
	playwright.ActionWriteFile:  playwright.WriteFileAction{},
	playwright.ActionReadFile:   playwright.ReadFileAction{},
}

// presetActionTypes lists the action types each preset may emit, in the order
// they appear in the schema's type enum.
var presetActionTypes = map[playwright.Preset][]playwright.ActionType{
	playwright.PresetDefault: {
		playwright.ActionMouse, playwright.ActionKeyboard, playwright.ActionWait,
		playwright.ActionGoTo, playwright.ActionTabOpen, playwright.ActionTabSwitch,
		playwright.ActionTabClose, playwright.ActionJavascript, playwright.ActionScreenshot,
		playwright.ActionWriteFile, playwright.ActionReadFile,
	},
	playwright.PresetBlind: {
		playwright.ActionKeyboard, playwright.ActionWait, playwright.ActionTabOpen,
		playwright.ActionTabSwitch, playwright.ActionTabClose, playwright.ActionScreenshot,
	},
	playwright.PresetMotor: {
		playwright.ActionKeyboard, playwright.ActionWait, playwright.ActionTabOpen,
		playwright.ActionTabSwitch, playwright.ActionTabClose, playwright.ActionGoTo,
		playwright.ActionScreenshot, playwright.ActionWriteFile, playwright.ActionReadFile,
	},
}

// presetExcludedDataFields removes targeting fields a preset's persona must
// not use even though an allowed payload struct carries them: blind (a screen
// reader) can never target by selector or coordinates; motor (sighted,
// keyboard-only) sees the page but never targets by selector.
var presetExcludedDataFields = map[playwright.Preset][]string{
	playwright.PresetBlind: {"selector", "x", "y"},
	playwright.PresetMotor: {"selector"},
}

// presetSchemas is built once at startup; a broken schema is a programming
// error (a struct/tag/preset-table mismatch), so init panics rather than
// letting runs proceed with a silently wrong schema.
var presetSchemas = map[playwright.Preset]string{}

func init() {
	for preset := range presetActionTypes {
		schema, err := buildPresetSchema(preset)
		if err != nil {
			panic(fmt.Sprintf("building %s schema: %v", preset, err))
		}
		presetSchemas[preset] = schema
	}
}

// presetSchema returns the structured-output JSON schema for the preset.
func presetSchema(preset playwright.Preset) string {
	if s, ok := presetSchemas[preset]; ok {
		return s
	}
	return presetSchemas[playwright.PresetDefault]
}

// buildPresetSchema assembles the InteractionResponse schema for one preset:
// the action-type enum from presetActionTypes, and data as the union of the
// allowed payload structs minus the preset's excluded fields.
func buildPresetSchema(preset playwright.Preset) (string, error) {
	types := presetActionTypes[preset]

	payloads := make([]any, 0, len(types))
	typeEnum := make([]any, 0, len(types))
	for _, t := range types {
		payload, ok := actionData[t]
		if !ok {
			return "", fmt.Errorf("no payload struct registered for action type %q", t)
		}
		payloads = append(payloads, payload)
		typeEnum = append(typeEnum, string(t))
	}

	data, err := jsonschema.UnionOf(payloads...)
	if err != nil {
		return "", err
	}
	dataProps := data["properties"].(map[string]any)
	for _, field := range presetExcludedDataFields[preset] {
		delete(dataProps, field)
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"taskComplete":             map[string]any{"type": "boolean"},
			"userRequestedInformation": map[string]any{"type": "string"},
			"performActions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": typeEnum},
						"data": data,
					},
					"required": []string{"type", "data"},
				},
			},
		},
		"required": []string{"taskComplete"},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
