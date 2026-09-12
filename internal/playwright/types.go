package playwright

import (
	"encoding/json"
	"fmt"
)

// ActionType discriminates which concrete ActionData an Action carries.
type ActionType string

const (
	ActionMouse      ActionType = "mouse"
	ActionKeyboard   ActionType = "keyboard"
	ActionWait       ActionType = "wait"
	ActionGoTo       ActionType = "go-to"
	ActionTabOpen    ActionType = "tab-open"
	ActionTabSwitch  ActionType = "tab-switch"
	ActionTabClose   ActionType = "tab-close"
	ActionJavascript ActionType = "javascript"
	ActionScreenshot ActionType = "screenshot"
	ActionWriteFile  ActionType = "write-file"
	ActionReadFile   ActionType = "read-file"
)

// ActionData is the payload of an Action. It is a closed set — only the types in
// this file implement it (via the unexported marker). Use "Find implementations
// of ActionData" to see every variant.
type ActionData interface {
	isActionData()
}

// Action is a single action the agent wants applied to the page: a discriminated
// union of Type plus the matching ActionData payload. It serialises as
// {"type": "...", "data": { ... }}.
type Action struct {
	Type ActionType `json:"type"`
	Data ActionData `json:"data"`
}

// MouseAction is a pointer interaction. Action is required; the rest are
// optional and modelled as pointers so they can be omitted entirely when unused.
// For "click"/"double-click"/"down"/"up", provide a Selector or both X and Y (or
// neither, for down/up, to act at the current position); "move"/"scroll" need X
// and Y.
type MouseAction struct {
	Action   string   `json:"action" enum:"click,double-click,move,scroll,down,up"`
	Selector *string  `json:"selector,omitempty"` // CSS selector target (alternative to X/Y)
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
}

// KeyboardAction is a typing/key interaction.
type KeyboardAction struct {
	Action   string `json:"action" enum:"type,press"`
	Selector string `json:"selector"` // optional field to focus/fill before typing
	Text     string `json:"text"`     // text to type (for "type")
	Key      string `json:"key"`      // key to press, e.g. "Enter" (for "press")
}

// WaitAction pauses for a fixed duration, for pages that settle asynchronously
// (e.g. lazy-loaded / module-federated React content).
type WaitAction struct {
	Ms int `json:"ms"`
}

// GoToAction navigates the page to a URL. Url may be absolute (scheme + domain +
// path), root-relative (/some/path), or relative to the current page
// (../some/path); the relative forms are resolved against the current page URL.
type GoToAction struct {
	Url string `json:"url" description:"go-to: navigation target (absolute, root-relative, or relative to the current page)"`
}

// TabOpenAction opens a new blank tab and makes it active. It carries no fields.
type TabOpenAction struct{}

// TabSwitchAction makes the 1-based Index the active tab.
type TabSwitchAction struct {
	Index int `json:"index"`
}

// TabCloseAction closes the 1-based Index (refused if it's the only tab).
type TabCloseAction struct {
	Index int `json:"index"`
}

// JavascriptAction evaluates arbitrary JavaScript in the active page. Code is the
// script to run.
type JavascriptAction struct {
	Code string `json:"code"`
}

// ScreenshotAction saves a named PNG of the active page for the user. Name is
// required (a plain file name, no extension). At most one targeting option may
// be set: FullPage (whole scrollable page), Selector (a single element), or X+Y
// (the element at that viewport point); with none set, the visible viewport is
// captured. FullPage must stay unset when Selector or coordinates are given.
// The file is {name}.png in the run's screenshots dir; if it already exists the
// action fails unless Override is true.
type ScreenshotAction struct {
	Name     string   `json:"name" description:"screenshot: file name for the saved PNG, without extension"`
	FullPage *bool    `json:"full-page,omitempty" description:"screenshot: capture the whole scrollable page; omit when selector or x/y are given"`
	Selector *string  `json:"selector,omitempty"`
	X        *float64 `json:"x,omitempty" description:"screenshot: viewport x of the element to capture"`
	Y        *float64 `json:"y,omitempty" description:"screenshot: viewport y of the element to capture"`
	Override bool     `json:"override,omitempty" description:"screenshot: replace an existing screenshot with the same name"`
}

// WriteFileAction writes a file for the user as a deliverable. Name is the file
// name including its extension; Content is the file's contents. The file is
// written into the run's outputs dir; if it already exists the action fails
// unless Override is true.
type WriteFileAction struct {
	Name     string `json:"name" description:"write-file: file name including the extension"`
	Content  string `json:"content" description:"write-file: the file's contents"`
	Override bool   `json:"override,omitempty" description:"write-file: replace an existing file with the same name"`
}

// ReadFileAction reads back a file previously saved with write-file, so the
// agent can keep state across turns. Name is the file name including its
// extension. Reading is restricted to the run's outputs (files) dir — the same
// dir write-file writes into. The content is returned to the agent in the
// performed action's Result.
type ReadFileAction struct {
	Name string `json:"name" description:"read-file: file name including the extension"`
}

// PerformedAction is the outcome of attempting an Action, reported back to the
// agent. Attempted is true once we started the action; Succeeded is true only if
// it (and the following load wait) completed; Reason carries the failure message
// when it didn't. Result carries data an action returns to the agent (read-file:
// the file's content).
type PerformedAction struct {
	*Action
	Attempted bool    `json:"attempted"`
	Succeeded bool    `json:"succeeded"`
	Reason    *string `json:"reason,omitempty"`
	Result    *string `json:"result,omitempty"`
}

func (MouseAction) isActionData()      {}
func (KeyboardAction) isActionData()   {}
func (WaitAction) isActionData()       {}
func (GoToAction) isActionData()       {}
func (TabOpenAction) isActionData()    {}
func (TabSwitchAction) isActionData()  {}
func (TabCloseAction) isActionData()   {}
func (JavascriptAction) isActionData() {}
func (ScreenshotAction) isActionData() {}
func (WriteFileAction) isActionData()  {}
func (ReadFileAction) isActionData()   {}

// UnmarshalJSON decodes the {"type": ..., "data": {...}} envelope into the
// concrete payload that matches Type. Default marshalling is the inverse and
// needs no custom MarshalJSON.
func (a *Action) UnmarshalJSON(b []byte) error {
	var env struct {
		Type ActionType      `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	a.Type = env.Type

	switch env.Type {
	case ActionMouse:
		var d MouseAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionKeyboard:
		var d KeyboardAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionWait:
		var d WaitAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionGoTo:
		var d GoToAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionTabOpen:
		var d TabOpenAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionTabSwitch:
		var d TabSwitchAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionTabClose:
		var d TabCloseAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionJavascript:
		var d JavascriptAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionScreenshot:
		var d ScreenshotAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionWriteFile:
		var d WriteFileAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	case ActionReadFile:
		var d ReadFileAction
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return err
		}
		a.Data = d
	default:
		return fmt.Errorf("unknown action type %q", env.Type)
	}
	return nil
}
