// Package audit holds the two agents behind the schedule command's
// accessibility audit and the record types their results are reported in.
//
// The explorer is sighted and unrestricted: it drives the browser with the
// full action set, reads the screenshot + DOM each turn, maps the site, and
// finally returns a list of Scenarios — user journeys written for someone who
// cannot see the page. The auditor is a screen-reader persona: keyboard only,
// no screenshot, no DOM, just the focused element each turn. One fresh auditor
// conversation performs one Scenario and reports every Hindrance it meets.
//
// Neither agent touches the browser itself — the schedule command captures
// state and applies actions between turns, exactly as with the interaction
// package the other commands use.
package audit

import (
	"time"

	"axeq/internal/playwright"
)

// Scenario is one user journey the explorer distilled from the site, written
// for a screen-reader user: steps describe intent ("open the main menu and go
// to Pricing"), never pixels or selectors, because the auditor that performs
// them has neither a screenshot nor the DOM.
type Scenario struct {
	ID              string   `json:"id" description:"short unique slug, e.g. main-navigation"`
	Title           string   `json:"title"`
	Goal            string   `json:"goal" description:"what the user is trying to accomplish, in one or two sentences"`
	StartUrl        string   `json:"startUrl" description:"absolute URL the scenario begins on, loaded fresh with nothing focused"`
	Steps           []string `json:"steps" description:"ordered steps as user intent, each achievable with a keyboard and a screen reader; no selectors, coordinates, or visual cues"`
	SuccessCriteria string   `json:"successCriteria" description:"how the auditor can tell, from focused elements and tab titles alone, that the goal was reached"`
	Focus           []string `json:"focus,omitempty" description:"accessibility aspects exercised, e.g. landmarks, forms, modal, menu, skip-link, live-region, media"`
}

// ExplorerResponse is the structured output required from the explorer each
// turn. Scenarios is set only on the final turn, when ExplorationComplete is
// true; until then PerformActions carries the next batch of browser actions.
type ExplorerResponse struct {
	ExplorationComplete bool `json:"explorationComplete"`
	// Progress is the explorer's one-line note of what it has covered and what
	// is next — logged for the person running the audit.
	Progress       *string             `json:"progress,omitempty"`
	PerformActions []playwright.Action `json:"performActions,omitempty"`
	Scenarios      []Scenario          `json:"scenarios,omitempty"`
}

// Hindrance is one obstacle the auditor met while performing a scenario step
// as a screen-reader user. Severity and Category are closed sets (enforced by
// the schema) so the report can aggregate them.
type Hindrance struct {
	Step           int     `json:"step" description:"1-based index of the scenario step being attempted when this was met"`
	Severity       string  `json:"severity" enum:"critical,serious,moderate,minor" description:"critical: the step is impossible; serious: possible only with a workaround or guesswork; moderate: confusing or slow; minor: rough edge"`
	Category       string  `json:"category" enum:"missing-name,unreachable,focus-trap,focus-order,focus-lost,no-feedback,unclear-purpose,missing-landmark,unexpected-behavior,timing,other" description:"missing-name: a control announces no usable name; unreachable: cannot be focused by keyboard; focus-trap: focus cannot leave a region; focus-order: focus moves in an illogical sequence; focus-lost: focus disappears or resets after an action; no-feedback: an action gives no perceivable result; unclear-purpose: announced but meaningless out of context; missing-landmark: no way to skip to or identify a region; unexpected-behavior: something happens the announcement did not prepare for; timing: content changes or disappears before it can be reached"`
	Description    string  `json:"description" description:"what happened, in plain words, from the screen-reader user's point of view"`
	Element        *string `json:"element,omitempty" description:"the offending element as it was announced, when one can be identified"`
	Wcag           *string `json:"wcag,omitempty" description:"the WCAG success criterion believed to apply, e.g. 2.4.3 Focus Order"`
	Recommendation string  `json:"recommendation" description:"a concrete change the developers can make"`
}

// Outcome values the auditor may report for a scenario.
const (
	OutcomeCompleted               = "completed"
	OutcomeCompletedWithDifficulty = "completed-with-difficulty"
	OutcomeBlocked                 = "blocked"
	OutcomeAbandoned               = "abandoned"
)

// AuditorResponse is the structured output required from the auditor each
// turn. Hindrances lists the ones newly noticed this turn (they are reported
// as they happen, not collected until the end); Outcome and Summary are set
// only on the final turn, when ScenarioComplete is true.
type AuditorResponse struct {
	ScenarioComplete bool    `json:"scenarioComplete"`
	Outcome          *string `json:"outcome,omitempty" enum:"completed,completed-with-difficulty,blocked,abandoned"`
	Summary          *string `json:"summary,omitempty"`
	// Narration is the auditor's one-line account of what the screen reader
	// announced and what it is trying next — the transcript in the report.
	Narration      *string             `json:"narration,omitempty"`
	PerformActions []playwright.Action `json:"performActions,omitempty"`
	Hindrances     []Hindrance         `json:"hindrances,omitempty"`
}

// Finding is a Hindrance annotated by the orchestrator with where and when it
// was reported: the turn, the active tab's URL, and the evidence screenshot
// taken of the page state the auditor was perceiving (a path relative to the
// run's output folder, empty when no screenshot could be taken).
type Finding struct {
	Hindrance
	Turn       int    `json:"turn"`
	Url        string `json:"url"`
	Screenshot string `json:"screenshot,omitempty"`
}

// ScenarioResult is one scenario's audit: the outcome the auditor reported
// (or the orchestrator imposed, e.g. abandoned on an exhausted turn budget),
// its summary, the turn-by-turn narration, every finding, and the recording
// of the attempt (relative to the run's output folder, empty without video).
type ScenarioResult struct {
	Scenario  Scenario  `json:"scenario"`
	Outcome   string    `json:"outcome"`
	Summary   string    `json:"summary"`
	Turns     int       `json:"turns"`
	Narration []string  `json:"narration,omitempty"`
	Findings  []Finding `json:"findings"`
	Recording string    `json:"recording,omitempty"`
}

// Report is the whole run: what was audited, with what, the scenarios planned,
// and the result of each one performed. Partial is true when the run stopped
// (interrupted) before every planned scenario was performed.
type Report struct {
	// Title and Notes let a command frame the report: the heading (default:
	// "Accessibility audit: <hosts>") and extra header lines, e.g. the pull
	// request under review.
	Title      string           `json:"title,omitempty"`
	Notes      []string         `json:"notes,omitempty"`
	Urls       []string         `json:"urls"`
	StartedAt  time.Time        `json:"startedAt"`
	FinishedAt time.Time        `json:"finishedAt"`
	Partial    bool             `json:"partial"`
	Scenarios  []Scenario       `json:"scenarios"`
	Results    []ScenarioResult `json:"results"`
}

// PlannerResponse is the structured output of the pull_request command's
// planner: the scenarios that exercise what a pull request changed. Unlike the
// explorer it has no browser — it reads the diff and the repository — so it
// answers in one turn.
type PlannerResponse struct {
	// Summary is the planner's one-paragraph reading of what the pull request
	// changes in the interface, for the report.
	Summary   string     `json:"summary"`
	Scenarios []Scenario `json:"scenarios"`
	// NotCovered lists changes no scenario exercises and why — backend-only
	// code, pages outside the configured URLs, changes with no user-facing
	// effect — so the reader knows what the audit did not look at.
	NotCovered []string `json:"notCovered,omitempty"`
}

// ScenarioVerdict is the filter's ruling on one performed scenario: whether
// the journey it exercises is affected by the pull request at all.
type ScenarioVerdict struct {
	ID      string `json:"id" description:"the scenario's id"`
	Related bool   `json:"related" description:"true when the pull request's changes affect what this scenario exercises"`
	Reason  string `json:"reason" description:"one sentence: which change relates it, or why it is unrelated"`
}

// FindingVerdict is the filter's ruling on one finding: whether the hindrance
// was caused, changed, or left in place by the pull request's changes.
type FindingVerdict struct {
	ScenarioID string `json:"scenarioId" description:"id of the scenario the finding belongs to"`
	Finding    int    `json:"finding" description:"1-based position of the finding in that scenario's findings list"`
	Related    bool   `json:"related" description:"true when the pull request introduced, altered, or touched the code behind this hindrance"`
	Reason     string `json:"reason" description:"one sentence naming the changed file or hunk, or why the hindrance is pre-existing"`
}

// FilterResponse is the structured output of the pull_request command's
// filter: verdicts, not a rewritten report — the orchestrator applies them.
type FilterResponse struct {
	// Summary is the filter's one-paragraph account of what it kept and cut.
	Summary   string            `json:"summary"`
	Scenarios []ScenarioVerdict `json:"scenarios"`
	Findings  []FindingVerdict  `json:"findings"`
}
