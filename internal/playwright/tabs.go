package playwright

import (
	"fmt"
	"sync"

	"github.com/mxschmitt/playwright-go"
)

// activePage is the tab the connector currently drives. New tabs opened by the
// page itself (e.g. a click that spawns a window) are added to the context but
// do NOT become active automatically — the agent switches to them explicitly via
// tab actions. It's read by the recorder goroutine and written by tab actions,
// so it's mutex-guarded.
var (
	activeMu   sync.Mutex
	activePage playwright.Page
)

func setActive(p playwright.Page) {
	activeMu.Lock()
	activePage = p
	activeMu.Unlock()
}

// active returns the tab the connector is currently driving.
func active() playwright.Page {
	activeMu.Lock()
	defer activeMu.Unlock()
	return activePage
}

// Tab is a per-tab summary reported to the agent each turn.
type Tab struct {
	Title  string `json:"title"`
	Url    string `json:"url"`
	Active bool   `json:"active"`
}

// Tabs summarises every open tab, in browser order, with the active one flagged.
// Titles and URLs are read best-effort.
func Tabs() []Tab {
	pages := browserContext.Pages()
	act := active()
	tabs := make([]Tab, len(pages))
	for i, p := range pages {
		title, _ := p.Title()
		tabs[i] = Tab{Title: title, Url: p.URL(), Active: p == act}
	}
	return tabs
}

// OpenInitialTabs sets up the starting tabs from the requested URLs: none -> a
// single about:blank tab; one -> that URL in the initial tab; multiple -> one
// URL per tab, in order, with the first tab left active.
func OpenInitialTabs(urls []string) error {
	if len(urls) == 0 {
		return Goto("about:blank")
	}

	if err := Goto(urls[0]); err != nil {
		return err
	}
	first := active()

	for _, u := range urls[1:] {
		if err := openTab(); err != nil {
			return err
		}
		if err := Goto(u); err != nil {
			return err
		}
	}

	// Leave the first tab active.
	if err := first.BringToFront(); err != nil {
		return fmt.Errorf("activating first tab: %w", err)
	}
	setActive(first)
	return nil
}

// openTab opens a new blank tab and makes it active.
func openTab() error {
	p, err := browserContext.NewPage()
	if err != nil {
		return fmt.Errorf("tab-open: %w", err)
	}
	if err := p.BringToFront(); err != nil {
		return fmt.Errorf("tab-open: %w", err)
	}
	setActive(p)
	return nil
}

// switchTab makes the 1-based index the active tab.
func switchTab(index int) error {
	pages := browserContext.Pages()
	if index < 1 || index > len(pages) {
		return fmt.Errorf("tab-switch: index %d out of range (1..%d)", index, len(pages))
	}
	p := pages[index-1]
	if err := p.BringToFront(); err != nil {
		return fmt.Errorf("tab-switch: %w", err)
	}
	setActive(p)
	return nil
}

// closeTab closes the 1-based index. It refuses to close the only open tab; when
// the closed tab was active, a neighbour becomes active.
func closeTab(index int) error {
	pages := browserContext.Pages()
	if len(pages) <= 1 {
		return fmt.Errorf("tab-close: cannot close the only open tab")
	}
	if index < 1 || index > len(pages) {
		return fmt.Errorf("tab-close: index %d out of range (1..%d)", index, len(pages))
	}

	target := pages[index-1]
	wasActive := target == active()
	if err := target.Close(); err != nil {
		return fmt.Errorf("tab-close: %w", err)
	}

	if wasActive {
		remaining := browserContext.Pages()
		next := index - 1
		if next >= len(remaining) {
			next = len(remaining) - 1
		}
		p := remaining[next]
		if err := p.BringToFront(); err != nil {
			return fmt.Errorf("tab-close: %w", err)
		}
		setActive(p)
	}
	return nil
}
