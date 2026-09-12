package harness

import "testing"

func TestTurns(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultTurns},  // unset
		{-3, DefaultTurns}, // below 1
		{1, 1},             // low, warned, kept
		{4, 4},             // low, warned, kept
		{5, 5},
		{15, 15},
		{25, 25},
		{26, MaxTurnsCap}, // clamped
		{30, MaxTurnsCap}, // the committed example configs' explorer budget
		{40, MaxTurnsCap}, // the committed example configs' scenario budget
		{200, MaxTurnsCap},
	}
	for _, key := range []string{"schedule.maxExploreTurns", "schedule.maxScenarioTurns"} {
		for _, c := range cases {
			if got := Turns(key, c.in); got != c.want {
				t.Errorf("Turns(%q, %d) = %d, want %d", key, c.in, got, c.want)
			}
		}
	}
}
