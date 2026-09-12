package interaction

import (
	"axeq/internal/playwright"
)

// InteractionResponse is the structured output we require back from the agent
// each turn.
type InteractionResponse struct {
	TaskComplete   bool                `json:"taskComplete"`
	PerformActions []playwright.Action `json:"performActions"`
	// UserRequestedInformation is the agent's answer/output when the user's
	// request asks for information. Set only on the final turn (TaskComplete).
	UserRequestedInformation *string `json:"userRequestedInformation,omitempty"`
}
