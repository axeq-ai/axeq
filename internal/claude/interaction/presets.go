package interaction

import (
	"axeq/internal/playwright"
	"encoding/json"
	"fmt"
)

// Presets are capability/perception profiles applied above the agent. Planned set:
//
//   - default       Most effective at completing the task: full mouse + keyboard,
//                    selectors, coordinates — no restrictions.
//   - slow          Like default, but actions are slowed down for clearer video.
//   - realistic     Closest to a real human: no coordinate clicks, no selector
//                    targeting, cannot type into an element unless it was manually
//                    focused first, etc.
//   - low-vision    Sighted but magnified/zoomed viewport (only part of the page
//                    visible); mouse + keyboard allowed. Tests reflow/zoom.
//   - motor         Sighted but keyboard-only (no mouse); keeps screenshot + DOM.
//                    Tests keyboard navigation and focus order.
//   - blind         No screenshot, no DOM; keyboard-only; perceives the page via
//                    screen-reader info (the focused element). The strictest.
//   - voice-control Targets elements only by accessible name (no coordinates, no
//                    brittle selectors). Tests labeling / accessible names.
//
// Only `default`, `blind`, and `motor` are implemented; the rest are planned.

// presetSpecific renders the {preset_specific_information} block describing the
// captured page state. In the blind preset it's a screen-reader view of the
// focused element (no screenshot/DOM); otherwise it's the screenshot + DOM paths.
func presetSpecific(preset playwright.Preset, screenshotPath, domPath string, focused *playwright.FocusedElement, tabs []playwright.Tab) string {
	var state string
	if preset == playwright.PresetBlind {
		if focused == nil {
			state = "No element is currently focused."
		} else {
			b, _ := json.Marshal(focused)
			state = "Current focused element: " + string(b)
		}
	} else {
		state = fmt.Sprintf("- Screenshot (PNG): %s\n- Full DOM (HTML): %s", screenshotPath, domPath)
	}

	return state + "\n\n" + renderTabs(tabs)
}

// renderTabs gives the agent the open tabs as compact JSON (one object per tab,
// in order). Everything the agent does targets the tab with "active":true; it
// manages tabs with tab-open / tab-switch / tab-close (1-based index).
func renderTabs(tabs []playwright.Tab) string {
	b, _ := json.Marshal(tabs)
	return "Open tabs (you act on the one with \"active\":true; tab-switch/tab-close take a 1-based index):\n" + string(b)
}
