package pull_request

import (
	"strings"
	"testing"

	"axeq/internal/claude/audit"
)

func TestApplyVerdicts(t *testing.T) {
	f := func(desc string) audit.Finding {
		return audit.Finding{Hindrance: audit.Hindrance{Severity: "serious", Description: desc}}
	}
	report := audit.Report{
		Scenarios: []audit.Scenario{{ID: "cart"}, {ID: "nav"}, {ID: "later"}},
		Results: []audit.ScenarioResult{
			{Scenario: audit.Scenario{ID: "cart", Title: "Cart"}, Outcome: audit.OutcomeBlocked, Findings: []audit.Finding{f("drawer has no dialog role"), f("footer contrast"), f("no verdict here")}},
			{Scenario: audit.Scenario{ID: "nav", Title: "Nav", Goal: "Use the menu"}, Outcome: audit.OutcomeCompleted, Findings: []audit.Finding{f("menu items are divs")}},
		},
	}
	verdicts := audit.FilterResponse{
		Summary: "kept the drawer",
		Scenarios: []audit.ScenarioVerdict{
			{ID: "cart", Related: true, Reason: "the PR adds the drawer"},
			{ID: "nav", Related: false, Reason: "the PR does not touch the header"},
		},
		Findings: []audit.FindingVerdict{
			{ScenarioID: "cart", Finding: 1, Related: true, Reason: "CartDrawer.jsx is new"},
			{ScenarioID: "cart", Finding: 2, Related: false, Reason: "footer untouched"},
			{ScenarioID: "nav", Finding: 1, Related: true, Reason: "irrelevant: scenario dropped"},
		},
	}

	filtered, excluded := applyVerdicts(report, verdicts)

	if len(filtered.Results) != 1 || filtered.Results[0].Scenario.ID != "cart" {
		t.Fatalf("want only the cart scenario kept, got %+v", filtered.Results)
	}
	got := filtered.Results[0].Findings
	if len(got) != 2 || got[0].Description != "drawer has no dialog role" || got[1].Description != "no verdict here" {
		t.Errorf("want the related finding and the verdict-less one kept, got %+v", got)
	}
	if filtered.Results[0].Outcome != audit.OutcomeBlocked {
		t.Errorf("outcome should survive filtering")
	}
	// Performed-and-kept first, then the never-performed tail.
	if len(filtered.Scenarios) != 2 || filtered.Scenarios[0].ID != "cart" || filtered.Scenarios[1].ID != "later" {
		t.Errorf("scenarios = %+v", filtered.Scenarios)
	}
	if len(excluded) != 2 {
		t.Fatalf("want 2 exclusions, got %+v", excluded)
	}
	if excluded[0].Finding != 2 || excluded[0].Reason != "footer untouched" {
		t.Errorf("first exclusion = %+v", excluded[0])
	}
	if excluded[1].Finding != 0 || excluded[1].Scenario != "Nav" {
		t.Errorf("whole-scenario exclusion = %+v", excluded[1])
	}

	md := renderExcluded(excluded, verdicts.Summary)
	for _, want := range []string{"kept the drawer", "**Cart**, hindrance 2 (serious) — footer contrast", "_Why:_ footer untouched", "**Nav** — whole scenario: Use the menu"} {
		if !strings.Contains(md, want) {
			t.Errorf("excluded section missing %q:\n%s", want, md)
		}
	}
	if !strings.Contains(renderExcluded(nil, ""), "Nothing was set aside") {
		t.Error("empty exclusion section should say so")
	}
}
