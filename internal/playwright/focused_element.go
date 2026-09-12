package playwright

import (
	"encoding/json"
	"fmt"
)

// FocusedElement is a screen-reader-ish view of the focused element, read
// straight off the DOM node. Optional fields are pointers so a present-but-empty
// value ("") stays distinct from an absent one (omitted). nil overall when
// nothing meaningful is focused.
type FocusedElement struct {
	Tag         string            `json:"tag"`                   // "button", "input", …
	Role        *string           `json:"role,omitempty"`        // explicit role attribute
	Type        *string           `json:"type,omitempty"`        // input type
	Title       *string           `json:"title,omitempty"`       // title attribute
	Text        *string           `json:"text,omitempty"`        // trimmed textContent
	Value       *string           `json:"value,omitempty"`       // current value (inputs)
	Placeholder *string           `json:"placeholder,omitempty"` // placeholder attribute
	Aria        map[string]string `json:"aria,omitempty"`        // every aria-* attribute
}

// FocusedElementJS reads document.activeElement. It includes a field only when
// the source is actually present (getAttribute returns null when absent), so ""
// is preserved and absence is omitted. textContent is included only when
// non-empty after trimming (nothing to announce otherwise). It is exported so
// the extension backend can evaluate the same script over its bridge.
const FocusedElementJS = `() => {
  const el = document.activeElement;
  if (!el || el === document.body) return null;
  const out = { tag: el.tagName.toLowerCase() };
  const setAttr = (k, n) => { const v = el.getAttribute(n); if (v !== null) out[k] = v; };
  setAttr('role', 'role');
  setAttr('type', 'type');
  setAttr('title', 'title');
  setAttr('placeholder', 'placeholder');
  if (el.value !== undefined && el.value !== null) out.value = el.value;
  const text = (el.textContent || '').trim();
  if (text) out.text = text.slice(0, 500);
  const aria = {};
  for (const a of el.attributes) if (a.name.startsWith('aria-')) aria[a.name] = a.value;
  if (Object.keys(aria).length) out.aria = aria;
  return out;
}`

// Focused returns the screen-reader view of the currently focused element, or
// nil when nothing is focused. It does not touch the page beyond reading it.
func Focused() (*FocusedElement, error) {
	raw, err := active().Evaluate(FocusedElementJS)
	if err != nil {
		return nil, fmt.Errorf("evaluating focused element: %w", err)
	}
	if raw == nil {
		return nil, nil
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshalling focused element: %w", err)
	}

	var fe FocusedElement
	if err := json.Unmarshal(b, &fe); err != nil {
		return nil, fmt.Errorf("unmarshalling focused element: %w", err)
	}
	return &fe, nil
}
