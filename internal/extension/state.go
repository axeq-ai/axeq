package extension

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"axeq/internal/playwright"

	"github.com/yosssi/gohtml"
)

// snapshotResult is the extension's reply to a "snapshot" command.
type snapshotResult struct {
	Screenshot string    `json:"screenshot"` // viewport PNG data URL
	Dom        string    `json:"dom"`
	Url        string    `json:"url"`
	Title      string    `json:"title"`
	Tabs       []wireTab `json:"tabs"`
	Error      string    `json:"error"`
}

// wireTab is the extension's tab summary (1-based index, active flag).
type wireTab struct {
	Index  int    `json:"index"`
	Url    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// Snapshot asks the extension for the current page state and writes it into
// dir as a screenshot (.png) and the full formatted DOM (.html), sharing one
// UTC-timestamp base name — same layout as the playwright backend.
func Snapshot(dir string) (screenshotPath, domPath string, err error) {
	res, err := send(map[string]any{"kind": "snapshot"}, 2*time.Minute)
	if err != nil {
		return "", "", err
	}

	var sr snapshotResult
	if err := json.Unmarshal(res, &sr); err != nil {
		return "", "", fmt.Errorf("decoding snapshot reply: %w", err)
	}
	if sr.Error != "" {
		return "", "", fmt.Errorf("extension snapshot: %s", sr.Error)
	}

	base := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000Z"))
	screenshotPath = base + ".png"
	domPath = base + ".html"

	comma := strings.Index(sr.Screenshot, ",")
	if comma < 0 {
		return "", "", fmt.Errorf("extension snapshot: malformed screenshot data URL")
	}
	png, err := base64.StdEncoding.DecodeString(sr.Screenshot[comma+1:])
	if err != nil {
		return "", "", fmt.Errorf("extension snapshot: decoding screenshot: %w", err)
	}
	if err := os.WriteFile(screenshotPath, png, 0o644); err != nil {
		return "", "", fmt.Errorf("writing screenshot: %w", err)
	}
	if err := os.WriteFile(domPath, []byte(gohtml.Format(sr.Dom)), 0o644); err != nil {
		return "", "", fmt.Errorf("writing DOM: %w", err)
	}
	return screenshotPath, domPath, nil
}

// Focused returns the screen-reader view of the currently focused element (for
// the blind preset), evaluated in the page via the shared FocusedElementJS.
func Focused() (*playwright.FocusedElement, error) {
	code := "(" + playwright.FocusedElementJS + ")()"
	res, err := send(map[string]any{"kind": "eval", "code": code}, time.Minute)
	if err != nil {
		return nil, err
	}

	var er struct {
		Value json.RawMessage `json:"value"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(res, &er); err != nil {
		return nil, fmt.Errorf("decoding eval reply: %w", err)
	}
	if er.Error != "" {
		return nil, fmt.Errorf("evaluating focused element: %s", er.Error)
	}
	if len(er.Value) == 0 || string(er.Value) == "null" {
		return nil, nil
	}

	var fe playwright.FocusedElement
	if err := json.Unmarshal(er.Value, &fe); err != nil {
		return nil, fmt.Errorf("unmarshalling focused element: %w", err)
	}
	return &fe, nil
}

// Tabs summarises the extension's controlled tabs. Best-effort like the
// playwright backend (whose Tabs also cannot fail): a bridge error yields an
// empty list rather than aborting the turn.
func Tabs() []playwright.Tab {
	res, err := send(map[string]any{"kind": "tabs"}, time.Minute)
	if err != nil {
		fmt.Printf("extension: listing tabs: %v\n", err)
		return nil
	}

	var tr struct {
		Tabs  []wireTab `json:"tabs"`
		Error string    `json:"error"`
	}
	if err := json.Unmarshal(res, &tr); err != nil || tr.Error != "" {
		return nil
	}

	tabs := make([]playwright.Tab, len(tr.Tabs))
	for i, t := range tr.Tabs {
		tabs[i] = playwright.Tab{Title: t.Title, Url: t.Url, Active: t.Active}
	}
	return tabs
}

// OpenInitialTabs sets up the starting tabs from the requested URLs. With no
// URLs it leaves the extension driving whatever tab the user adopted — for
// sensitive sites that is the point: the session, history, and fingerprint are
// the user's own. Multiple URLs open one tab each, first tab left active.
func OpenInitialTabs(urls []string) error {
	if len(urls) == 0 {
		return nil
	}

	actions := []playwright.Action{
		{Type: playwright.ActionGoTo, Data: playwright.GoToAction{Url: urls[0]}},
	}
	for _, u := range urls[1:] {
		actions = append(actions,
			playwright.Action{Type: playwright.ActionTabOpen, Data: playwright.TabOpenAction{}},
			playwright.Action{Type: playwright.ActionGoTo, Data: playwright.GoToAction{Url: u}},
		)
	}
	if len(urls) > 1 {
		actions = append(actions, playwright.Action{Type: playwright.ActionTabSwitch, Data: playwright.TabSwitchAction{Index: 1}})
	}

	for _, p := range PerformActions(actions) {
		if p.Attempted && !p.Succeeded {
			reason := "unknown"
			if p.Reason != nil {
				reason = *p.Reason
			}
			return fmt.Errorf("opening initial tabs (%s): %s", p.Type, reason)
		}
	}
	return nil
}
