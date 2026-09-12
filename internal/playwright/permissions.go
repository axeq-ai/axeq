package playwright

// Preset selects the agent persona for a run: it drives which schema and prompt
// the agent is given, and how page state is captured. It is not enforced at this
// layer — the agent is kept aligned with the allowed actions by the per-preset
// schema it must produce structured output against.
type Preset string

const (
	// PresetDefault: the full action set.
	PresetDefault Preset = "default"
	// PresetBlind is the screen-reader persona: keyboard-only, for testing
	// tab/keyboard navigation, and given no screenshot — just the focused element.
	PresetBlind Preset = "blind"
	// PresetMotor is the sighted keyboard-only persona: it keeps the screenshot +
	// DOM but, like blind, is restricted to keyboard actions by its schema.
	PresetMotor Preset = "motor"
)
