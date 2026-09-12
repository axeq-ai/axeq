package harness

import (
	"strings"
	"testing"
	"time"

	"axeq/internal/claude/audit"
)

func ptr[T any](v T) *T { return &v }

func TestRenderMarkdownCoversEverySection(t *testing.T) {
	sc := audit.Scenario{
		ID: "menu", Title: "Reach Pricing through the main menu", Goal: "Find the price list.",
		StartUrl: "https://example.com/", Steps: []string{"Open the main navigation.", "Activate Pricing."},
		SuccessCriteria: "The tab title mentions Pricing.", Focus: []string{"menu"},
	}
	r := audit.Report{
		Urls:      []string{"https://example.com/"},
		StartedAt: time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC),
		Partial:   true,
		Scenarios: []audit.Scenario{sc, {ID: "later", Title: "Never reached", StartUrl: "https://example.com/x", Steps: []string{"a"}}},
		Results: []audit.ScenarioResult{{
			Scenario: sc, Outcome: audit.OutcomeBlocked, Summary: "The menu button has no name.", Turns: 4,
			Narration: []string{"Turn 1: Nothing focused; pressing Tab."},
			Findings: []audit.Finding{{
				Hindrance: audit.Hindrance{Step: 1, Severity: "critical", Category: "missing-name", Description: "The menu toggle announces as 'button'.", Element: ptr(`{"tag":"button"}`), Wcag: ptr("4.1.2 Name, Role, Value"), Recommendation: "Give the toggle an aria-label."},
				Turn:      2, Url: "https://example.com/", Screenshot: "screenshots/scenario-01-hindrance-01.png",
			}},
			Recording: "recordings/scenario-01.mp4",
		}},
	}

	md := RenderMarkdown(r)
	for _, want := range []string{
		"# Accessibility audit: example.com",
		"**Partial:** the run was interrupted after 1 of 2 planned scenarios",
		"| 1 | Reach Pricing through the main menu | blocked | 1 | 0 | 0 | 0 |",
		"- **critical** · missing-name · scenario 1 step 1 — The menu toggle announces as 'button'. _(WCAG 4.1.2 Name, Role, Value)_",
		"### 1. Reach Pricing through the main menu",
		"**Recording:** [scenario-01.mp4](../recordings/scenario-01.mp4)",
		"##### 1.1 — critical · missing-name",
		"![Page state when the hindrance was reported](../screenshots/scenario-01-hindrance-01.png)",
		"- Turn 1: Nothing focused; pressing Tab.",
		"## Not performed",
		"2. Never reached — https://example.com/x",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report is missing %q\n\n%s", want, md)
		}
	}
}

func TestNormalizeScenarios(t *testing.T) {
	urls := []string{"https://example.com/"}
	in := []audit.Scenario{
		{ID: "a", Title: "Valid", StartUrl: "https://example.com/a", Steps: []string{"x"}},
		{ID: "a", Title: "Duplicate id, relative url", StartUrl: "/b", Steps: []string{"x"}},
		{Title: "No steps", StartUrl: "https://example.com/c"},
		{Title: "", StartUrl: "https://example.com/d", Steps: []string{"x"}},
		{ID: "e", Title: "Cut by max", StartUrl: "https://example.com/e", Steps: []string{"x"}},
	}

	out := NormalizeScenarios(in, urls, 2)
	if len(out) != 2 {
		t.Fatalf("want 2 scenarios, got %d: %+v", len(out), out)
	}
	if out[0].ID != "a" || out[0].StartUrl != "https://example.com/a" {
		t.Errorf("first scenario altered: %+v", out[0])
	}
	if out[1].ID != "scenario-2" {
		t.Errorf("duplicate id should become positional, got %q", out[1].ID)
	}
	if out[1].StartUrl != urls[0] {
		t.Errorf("relative start URL should fall back to the entry URL, got %q", out[1].StartUrl)
	}
}
