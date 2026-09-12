package harness

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"axeq/internal/claude/audit"
	browser "axeq/internal/playwright"

	aclio "github.com/agenticcliorchestra/aclio-go"
)

// framesFPS is the capture and playback frame rate for the recordings.
const framesFPS = 15

// RunScenario performs one scenario in a fresh auditor conversation: it
// resets the browser to the scenario's start page, then alternates between the
// auditor's turns and the key presses it asks for, collecting hindrances as
// findings until the auditor declares the scenario complete or its turn budget
// runs out (then the outcome is abandoned). The result is returned even on
// interruption — with exit code 130 — so the report can include what was
// observed so far. name is the run-local file-name stem for this scenario's
// evidence screenshots and recording.
func RunScenario(env Env, name string, sc audit.Scenario, maxTurns int, rec *browser.Recorder) (audit.ScenarioResult, int) {
	be := env.Backend
	result := audit.ScenarioResult{Scenario: sc, Outcome: audit.OutcomeAbandoned, Findings: []audit.Finding{}}

	// A scenario whose start page won't open is the planner's mistake, not a
	// hindrance: record it and move on to the next scenario.
	if reason := resetBrowser(be, sc.StartUrl); reason != "" {
		result.Summary = fmt.Sprintf("Not attempted: the start page %s could not be opened (%s).", sc.StartUrl, reason)
		fmt.Println(result.Summary)
		return result, 0
	}

	// First turn: the auditor is blind — no screenshot, no DOM — so its view of
	// the page is the focused element alone (nothing, right after a load).
	focused, err := be.Focused()
	if err != nil {
		panic(err)
	}

	turn := 1
	rec.Pause()
	convoId, resp, err := audit.AuditInitial(env.AgentDir, "", sc, focused, be.Tabs(), maxTurns)
	rec.Resume()
	if code := AgentFailure(err); code != 0 {
		result.Summary = "Interrupted before the auditor's first turn completed."
		return result, code
	}

	for {
		result.Turns = turn
		observe(&result, be, name, turn, resp)

		if resp.ScenarioComplete {
			if resp.Outcome != nil && *resp.Outcome != "" {
				result.Outcome = *resp.Outcome
			}
			result.Summary = "The auditor finished without a summary."
			if resp.Summary != nil && *resp.Summary != "" {
				result.Summary = *resp.Summary
			}
			break
		}
		// The final turn's prompt demanded a verdict; an auditor that still
		// hasn't finished is cut off, and the scenario counts as abandoned.
		if turn >= maxTurns {
			result.Summary = fmt.Sprintf("Turn budget of %d exhausted before the scenario completed.", maxTurns)
			break
		}

		performed := be.PerformActions(resp.PerformActions)

		focused, err = be.Focused()
		if err != nil {
			panic(err)
		}

		turn++
		rec.Pause()
		resp, err = audit.AuditSubsequent(convoId, env.AgentDir, performed, "", focused, be.Tabs(), maxTurns-turn+1)
		rec.Resume()
		if code := AgentFailure(err); code != 0 {
			result.Turns = turn
			result.Summary = fmt.Sprintf("Interrupted during turn %d.", turn)
			return result, code
		}
	}

	fmt.Printf("[%s] %s after %d turn(s), %d hindrance(s): %s\n", name, result.Outcome, result.Turns, len(result.Findings), result.Summary)
	return result, 0
}

// observe records one auditor turn into the result: its narration line and
// each newly reported hindrance as a finding. The page is, at this point,
// exactly as the auditor perceived it when it wrote the response (its next
// actions have not run yet), so an evidence screenshot taken now shows what it
// was describing. Screenshots go through the backend's own action path.
func observe(result *audit.ScenarioResult, be Backend, name string, turn int, resp audit.AuditorResponse) {
	if resp.Narration != nil && *resp.Narration != "" {
		result.Narration = append(result.Narration, fmt.Sprintf("Turn %d: %s", turn, *resp.Narration))
		fmt.Printf("[%s] turn %d: %s\n", name, turn, *resp.Narration)
	}

	if len(resp.Hindrances) == 0 {
		return
	}
	pageUrl := activeUrl(be.Tabs())
	for _, h := range resp.Hindrances {
		finding := audit.Finding{Hindrance: h, Turn: turn, Url: pageUrl}

		shot := fmt.Sprintf("%s-hindrance-%02d", name, len(result.Findings)+1)
		performed := be.PerformActions([]browser.Action{
			{Type: browser.ActionScreenshot, Data: browser.ScreenshotAction{Name: shot}},
		})
		if len(performed) == 1 && performed[0].Succeeded {
			finding.Screenshot = "screenshots/" + shot + ".png"
		} else {
			fmt.Printf("note: evidence screenshot %s failed: %s\n", shot, failureReason(performed))
		}

		result.Findings = append(result.Findings, finding)
		fmt.Printf("  hindrance [%s/%s] step %d: %s\n", h.Severity, h.Category, h.Step, h.Description)
	}
}

// resetBrowser puts the browser in the state every scenario starts from: the
// extra tabs a previous phase left open are closed (highest index first — the
// first tab is the one the backend refuses to close) and the remaining tab is
// navigated to startUrl. It returns the failure reason, or "" on success.
func resetBrowser(be Backend, startUrl string) string {
	var actions []browser.Action
	for i := len(be.Tabs()); i > 1; i-- {
		actions = append(actions, browser.Action{Type: browser.ActionTabClose, Data: browser.TabCloseAction{Index: i}})
	}
	actions = append(actions, browser.Action{Type: browser.ActionGoTo, Data: browser.GoToAction{Url: startUrl}})

	performed := be.PerformActions(actions)
	for _, p := range performed {
		if !p.Succeeded {
			return failureReason(performed)
		}
	}
	return ""
}

// Recorded runs one phase of the audit — an exploration, or one scenario —
// under its own constant-rate frame recording into framesDir, encoded to
// outPath once the phase returns, so each phase gets its own video. The phase
// pauses the recorder while waiting on the agent so those gaps don't appear
// in the output.
func Recorded(framesDir, outPath string, phase func(rec *browser.Recorder) int) int {
	rec, err := browser.StartRecording(framesDir, framesFPS)
	if err != nil {
		panic(fmt.Errorf("starting recorder: %w", err))
	}
	code := phase(rec)
	framesDir = rec.Stop()

	// An interrupted phase may end before a single frame landed, and ffmpeg
	// fails on an empty frame pattern — there is simply nothing to encode.
	if entries, err := os.ReadDir(framesDir); err != nil || len(entries) == 0 {
		return code
	}
	if err := browser.EncodeVideo(framesDir, outPath, framesFPS); err != nil {
		panic(fmt.Errorf("encoding recording: %w", err))
	}
	fmt.Printf("Recording: %s\n", outPath)
	return code
}

// NormalizeScenarios makes an agent's (or a file's) scenarios safe to run:
// entries without a title or steps are dropped with a note, a missing or
// non-absolute start URL falls back to the first entry URL, blank or duplicate
// ids get positional ones, and the list is cut to max entries (they are
// ordered by importance, so the tail goes).
func NormalizeScenarios(in []audit.Scenario, urls []string, max int) []audit.Scenario {
	out := make([]audit.Scenario, 0, len(in))
	seenIds := map[string]bool{}
	for i, sc := range in {
		if strings.TrimSpace(sc.Title) == "" || len(sc.Steps) == 0 {
			fmt.Printf("note: dropping scenario %d (%q): it needs a title and at least one step\n", i+1, sc.Title)
			continue
		}
		if u, err := url.Parse(sc.StartUrl); err != nil || u.Scheme == "" || u.Host == "" {
			fmt.Printf("note: scenario %q has no usable start URL (%q); starting it at %s\n", sc.Title, sc.StartUrl, urls[0])
			sc.StartUrl = urls[0]
		}
		if strings.TrimSpace(sc.ID) == "" || seenIds[sc.ID] {
			sc.ID = fmt.Sprintf("scenario-%d", len(out)+1)
		}
		seenIds[sc.ID] = true
		out = append(out, sc)
	}
	if len(out) > max {
		fmt.Printf("note: %d scenario(s) proposed, performing the first %d (raise the scenario budget in the config for more)\n", len(out), max)
		out = out[:max]
	}
	return out
}

// AgentFailure maps an agent-turn error to an exit code: 0 for none, 130 for
// an interrupt (reported, not panicked — ctrl+c is the user's choice), and a
// panic for anything else, where the stack trace is the debugging aid.
func AgentFailure(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, aclio.ErrInterrupted) {
		fmt.Fprintln(os.Stderr, "interrupted")
		return 130
	}
	panic(fmt.Errorf("agent interaction: %w", err))
}

// failureReason describes the first failed action of a performed set — its
// reason when one was given, else which action type stalled.
func failureReason(performed []browser.PerformedAction) string {
	for _, p := range performed {
		if p.Succeeded {
			continue
		}
		if p.Reason != nil {
			return *p.Reason
		}
		if p.Action != nil {
			return fmt.Sprintf("%s action did not complete", p.Type)
		}
		return "action did not complete"
	}
	return "unknown"
}

// activeUrl is the URL of the active tab, or "" when none is flagged active.
func activeUrl(tabs []browser.Tab) string {
	for _, t := range tabs {
		if t.Active {
			return t.Url
		}
	}
	return ""
}
