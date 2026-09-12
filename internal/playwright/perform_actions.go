package playwright

import (
	"fmt"
	"net/url"

	"github.com/mxschmitt/playwright-go"
)

// PerformActions applies each action to the page in order, waiting for the page
// to load after every one so the resulting state has settled before the next
// action (and before the next snapshot). It returns the outcome of every action.
//
// The first failure stops the run, so actions after it stay Attempted=false.
// There is no error return — everything is reported to the agent via the result.
func PerformActions(actions []Action) []PerformedAction {
	performed := make([]PerformedAction, len(actions))
	for i := range actions {
		performed[i].Action = &actions[i]
	}

	for i := range performed {
		performed[i].Attempted = true

		// read-file is the one action that returns data to the agent; it is
		// local-only (never touches the page), so no load wait afterwards.
		if actions[i].Type == ActionReadFile {
			if err := doReadFileInto(&performed[i], actions[i]); err != nil {
				msg := err.Error()
				performed[i].Reason = &msg
				break
			}
			performed[i].Succeeded = true
			continue
		}

		if err := do(actions[i]); err != nil {
			msg := err.Error()
			performed[i].Reason = &msg
			break
		}

		if err := WaitForLoad(); err != nil {
			msg := err.Error()
			performed[i].Reason = &msg
			break
		}

		performed[i].Succeeded = true
	}

	return performed
}

// WaitForLoad blocks until the page reaches the "load" state. We deliberately
// avoid "networkidle": apps with telemetry, analytics, or websockets keep
// connections open, so the network never goes idle and the wait times out. The
// "load" event fires reliably, and on an in-page (SPA) update with no navigation
// it simply resolves immediately.
func WaitForLoad() error {
	if err := active().WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateLoad,
	}); err != nil {
		return fmt.Errorf("waiting for page load: %w", err)
	}
	return nil
}

func do(a Action) error {
	switch a.Type {
	case ActionMouse:
		d, ok := a.Data.(MouseAction)
		if !ok {
			return fmt.Errorf("mouse action carried %T data", a.Data)
		}
		return doMouse(active(), d)
	case ActionKeyboard:
		d, ok := a.Data.(KeyboardAction)
		if !ok {
			return fmt.Errorf("keyboard action carried %T data", a.Data)
		}
		return doKeyboard(active(), d)
	case ActionWait:
		d, ok := a.Data.(WaitAction)
		if !ok {
			return fmt.Errorf("wait action carried %T data", a.Data)
		}
		return doWait(active(), d)
	case ActionGoTo:
		d, ok := a.Data.(GoToAction)
		if !ok {
			return fmt.Errorf("go-to action carried %T data", a.Data)
		}
		return doGoTo(active(), d)
	case ActionTabOpen:
		return openTab()
	case ActionTabSwitch:
		d, ok := a.Data.(TabSwitchAction)
		if !ok {
			return fmt.Errorf("tab-switch action carried %T data", a.Data)
		}
		return switchTab(d.Index)
	case ActionTabClose:
		d, ok := a.Data.(TabCloseAction)
		if !ok {
			return fmt.Errorf("tab-close action carried %T data", a.Data)
		}
		return closeTab(d.Index)
	case ActionJavascript:
		d, ok := a.Data.(JavascriptAction)
		if !ok {
			return fmt.Errorf("javascript action carried %T data", a.Data)
		}
		return doJavascript(active(), d)
	case ActionScreenshot:
		d, ok := a.Data.(ScreenshotAction)
		if !ok {
			return fmt.Errorf("screenshot action carried %T data", a.Data)
		}
		return doScreenshot(active(), d)
	case ActionWriteFile:
		d, ok := a.Data.(WriteFileAction)
		if !ok {
			return fmt.Errorf("write-file action carried %T data", a.Data)
		}
		return DoWriteFile(d)
	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
}

// doReadFileInto executes a read-file action and stores the file's content in
// the performed record's Result, where the agent picks it up next turn.
func doReadFileInto(p *PerformedAction, a Action) error {
	d, ok := a.Data.(ReadFileAction)
	if !ok {
		return fmt.Errorf("read-file action carried %T data", a.Data)
	}
	content, err := DoReadFile(d)
	if err != nil {
		return err
	}
	p.Result = &content
	return nil
}

func doMouse(page playwright.Page, a MouseAction) error {
	switch a.Action {
	case "click":
		// Prefer a selector when given; otherwise click at coordinates.
		if a.Selector != nil {
			return page.Click(*a.Selector)
		}
		if a.X == nil || a.Y == nil {
			return fmt.Errorf("click needs a selector or both x and y")
		}
		return page.Mouse().Click(*a.X, *a.Y)
	case "double-click":
		if a.Selector != nil {
			return page.Dblclick(*a.Selector)
		}
		if a.X == nil || a.Y == nil {
			return fmt.Errorf("double-click needs a selector or both x and y")
		}
		return page.Mouse().Dblclick(*a.X, *a.Y)
	case "move":
		if a.X == nil || a.Y == nil {
			return fmt.Errorf("move needs both x and y")
		}
		return page.Mouse().Move(*a.X, *a.Y)
	case "scroll":
		if a.X == nil || a.Y == nil {
			return fmt.Errorf("scroll needs both x and y")
		}
		return page.Mouse().Wheel(*a.X, *a.Y)
	case "down":
		if err := positionMouse(page, a); err != nil {
			return err
		}
		return page.Mouse().Down()
	case "up":
		if err := positionMouse(page, a); err != nil {
			return err
		}
		return page.Mouse().Up()
	default:
		return fmt.Errorf("unknown mouse action %q", a.Action)
	}
}

// positionMouse moves the pointer to the target before a press/release: it
// hovers a selector when given, else moves to x/y when given, else leaves the
// pointer where it is.
func positionMouse(page playwright.Page, a MouseAction) error {
	if a.Selector != nil {
		return page.Hover(*a.Selector)
	}
	if a.X != nil && a.Y != nil {
		return page.Mouse().Move(*a.X, *a.Y)
	}
	return nil
}

func doKeyboard(page playwright.Page, a KeyboardAction) error {
	switch a.Action {
	case "type":
		// Fill the selector's field when given; otherwise type wherever focus is.
		if a.Selector != "" {
			return page.Fill(a.Selector, a.Text)
		}
		return page.Keyboard().Type(a.Text)
	case "press":
		return page.Keyboard().Press(a.Key)
	default:
		return fmt.Errorf("unknown keyboard action %q", a.Action)
	}
}

func doWait(page playwright.Page, a WaitAction) error {
	page.WaitForTimeout(float64(a.Ms))
	return nil
}

func doJavascript(page playwright.Page, a JavascriptAction) error {
	_, err := page.Evaluate(a.Code)
	return err
}

func doGoTo(page playwright.Page, a GoToAction) error {
	target, err := resolveURL(page.URL(), a.Url)
	if err != nil {
		return err
	}
	_, err = page.Goto(target)
	return err
}

// resolveURL validates and resolves a go-to target. An absolute URL (with a
// scheme) is used as-is; a relative ("../x") or root-relative ("/x") reference
// is resolved against the current page URL. An empty or unparseable target is an
// error.
func resolveURL(current, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("go-to url must not be empty")
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid go-to url %q: %w", raw, err)
	}
	if ref.IsAbs() {
		return raw, nil
	}
	base, err := url.Parse(current)
	if err != nil {
		return "", fmt.Errorf("resolving go-to url %q against current page %q: %w", raw, current, err)
	}
	return base.ResolveReference(ref).String(), nil
}
