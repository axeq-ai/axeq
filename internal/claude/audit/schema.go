package audit

import (
	"encoding/json"
	"fmt"

	"axeq/internal/playwright"

	"github.com/agenticcliorchestra/aclio-go/jsonschema"
)

// The two structured-output schemas are generated from the playwright action
// structs and the record types in types.go (their json/enum/description
// tags), so neither can drift from what the Go decoders actually accept. Each
// action stays a {type, data} envelope; data is the union of every allowed
// payload's fields (all optional) — which field is valid is determined by type
// and enforced by Action.UnmarshalJSON and the prompt.

// actionData pairs each action type an agent here may emit with the payload
// struct its data decodes into — the same pairing Action.UnmarshalJSON
// switches on.
var actionData = map[playwright.ActionType]any{
	playwright.ActionMouse:      playwright.MouseAction{},
	playwright.ActionKeyboard:   playwright.KeyboardAction{},
	playwright.ActionWait:       playwright.WaitAction{},
	playwright.ActionGoTo:       playwright.GoToAction{},
	playwright.ActionTabOpen:    playwright.TabOpenAction{},
	playwright.ActionTabSwitch:  playwright.TabSwitchAction{},
	playwright.ActionTabClose:   playwright.TabCloseAction{},
	playwright.ActionJavascript: playwright.JavascriptAction{},
}

// explorerActionTypes is the sighted, unrestricted set: the explorer needs to
// get everywhere quickly. Deliverable actions (screenshot, write-file,
// read-file) are left out — its only deliverable is the scenario list, which
// travels in its structured output.
var explorerActionTypes = []playwright.ActionType{
	playwright.ActionMouse, playwright.ActionKeyboard, playwright.ActionWait,
	playwright.ActionGoTo, playwright.ActionTabOpen, playwright.ActionTabSwitch,
	playwright.ActionTabClose, playwright.ActionJavascript,
}

// auditorActionTypes is the screen-reader set: keys and tabs only. No mouse,
// no go-to (a screen-reader user reaches pages through the page), no
// JavaScript, and no screenshots — the orchestrator takes evidence screenshots
// on the auditor's behalf when it reports a hindrance.
var auditorActionTypes = []playwright.ActionType{
	playwright.ActionKeyboard, playwright.ActionWait, playwright.ActionTabOpen,
	playwright.ActionTabSwitch, playwright.ActionTabClose,
}

// auditorExcludedDataFields removes targeting the persona must not use even
// though an allowed payload struct carries it: a screen reader never types
// into an element by selector — it types into whatever has focus.
var auditorExcludedDataFields = []string{"selector"}

// The schemas are built once at startup; a broken schema is a programming
// error (a struct/tag/table mismatch), so init panics rather than letting runs
// proceed with a silently wrong schema.
var explorerSchema, auditorSchema, plannerSchema, filterSchema string

func init() {
	var err error
	if explorerSchema, err = buildExplorerSchema(); err != nil {
		panic(fmt.Sprintf("building explorer schema: %v", err))
	}
	if auditorSchema, err = buildAuditorSchema(); err != nil {
		panic(fmt.Sprintf("building auditor schema: %v", err))
	}
	if plannerSchema, err = buildPlannerSchema(); err != nil {
		panic(fmt.Sprintf("building planner schema: %v", err))
	}
	if filterSchema, err = buildFilterSchema(); err != nil {
		panic(fmt.Sprintf("building filter schema: %v", err))
	}
}

// actionsSchema assembles the performActions array schema for one agent: the
// type enum from types, and data as the union of the allowed payload structs
// minus the excluded fields.
func actionsSchema(types []playwright.ActionType, excluded []string) (map[string]any, error) {
	payloads := make([]any, 0, len(types))
	typeEnum := make([]any, 0, len(types))
	for _, t := range types {
		payload, ok := actionData[t]
		if !ok {
			return nil, fmt.Errorf("no payload struct registered for action type %q", t)
		}
		payloads = append(payloads, payload)
		typeEnum = append(typeEnum, string(t))
	}

	data, err := jsonschema.UnionOf(payloads...)
	if err != nil {
		return nil, err
	}
	dataProps := data["properties"].(map[string]any)
	for _, field := range excluded {
		delete(dataProps, field)
	}

	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "enum": typeEnum},
				"data": data,
			},
			"required": []string{"type", "data"},
		},
	}, nil
}

// buildExplorerSchema is the ExplorerResponse schema: the sighted action set
// plus the Scenario list, generated from the Scenario struct.
func buildExplorerSchema() (string, error) {
	actions, err := actionsSchema(explorerActionTypes, nil)
	if err != nil {
		return "", err
	}
	scenario, err := jsonschema.FromType(Scenario{})
	if err != nil {
		return "", err
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"explorationComplete": map[string]any{"type": "boolean"},
			"progress": map[string]any{
				"type":        "string",
				"description": "one line: what has been covered so far and what is next",
			},
			"performActions": actions,
			"scenarios": map[string]any{
				"type":        "array",
				"items":       scenario,
				"description": "set only when explorationComplete is true; ordered from most to least important for a visitor",
			},
		},
		"required": []string{"explorationComplete"},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildAuditorSchema is the AuditorResponse schema: the screen-reader action
// set plus the Hindrance list, generated from the Hindrance struct.
func buildAuditorSchema() (string, error) {
	actions, err := actionsSchema(auditorActionTypes, auditorExcludedDataFields)
	if err != nil {
		return "", err
	}
	hindrance, err := jsonschema.FromType(Hindrance{})
	if err != nil {
		return "", err
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scenarioComplete": map[string]any{"type": "boolean"},
			"outcome": map[string]any{
				"type":        "string",
				"enum":        []any{OutcomeCompleted, OutcomeCompletedWithDifficulty, OutcomeBlocked, OutcomeAbandoned},
				"description": "set only when scenarioComplete is true",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "set only when scenarioComplete is true: a short account of the experience for the report",
			},
			"narration": map[string]any{
				"type":        "string",
				"description": "one line: what the screen reader announced and what you are trying next",
			},
			"performActions": actions,
			"hindrances": map[string]any{
				"type":        "array",
				"items":       hindrance,
				"description": "hindrances newly noticed this turn; never repeat one already reported",
			},
		},
		"required": []string{"scenarioComplete"},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildPlannerSchema is the PlannerResponse schema: a summary, the Scenario
// list (generated from the Scenario struct, as for the explorer), and the
// changes left uncovered. No actions — the planner has no browser.
func buildPlannerSchema() (string, error) {
	scenario, err := jsonschema.FromType(Scenario{})
	if err != nil {
		return "", err
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "one paragraph: what the pull request changes in the user interface",
			},
			"scenarios": map[string]any{
				"type":        "array",
				"items":       scenario,
				"description": "ordered from the change most likely to affect a screen-reader user to the least",
			},
			"notCovered": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "changes no scenario exercises, each with the reason",
			},
		},
		"required": []string{"summary", "scenarios"},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildFilterSchema is the FilterResponse schema: a summary plus one verdict
// per scenario and per finding, generated from the verdict structs.
func buildFilterSchema() (string, error) {
	scenarioVerdict, err := jsonschema.FromType(ScenarioVerdict{})
	if err != nil {
		return "", err
	}
	findingVerdict, err := jsonschema.FromType(FindingVerdict{})
	if err != nil {
		return "", err
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "one paragraph: what was kept as related to the pull request and what was set aside as pre-existing",
			},
			"scenarios": map[string]any{
				"type":        "array",
				"items":       scenarioVerdict,
				"description": "exactly one verdict per performed scenario",
			},
			"findings": map[string]any{
				"type":        "array",
				"items":       findingVerdict,
				"description": "exactly one verdict per finding across all scenarios",
			},
		},
		"required": []string{"summary", "scenarios", "findings"},
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
