package schedule

import "testing"

func TestTurns(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, defaultTurns},  // unset
		{-3, defaultTurns}, // below 1
		{1, 1},             // low, warned, kept
		{4, 4},             // low, warned, kept
		{5, 5},
		{15, 15},
		{25, 25},
		{26, maxTurnsCap}, // clamped
		{30, maxTurnsCap}, // the committed example configs' explorer budget
		{40, maxTurnsCap}, // the committed example configs' scenario budget
		{200, maxTurnsCap},
	}
	for _, key := range []string{"schedule.maxExploreTurns", "schedule.maxScenarioTurns"} {
		for _, c := range cases {
			if got := turns(key, c.in); got != c.want {
				t.Errorf("turns(%q, %d) = %d, want %d", key, c.in, got, c.want)
			}
		}
	}
}
