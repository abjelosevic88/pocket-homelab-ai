package server

import (
	"math"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

func TestRepairSessionTakesOutRestoredTotals(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 30, 10, m, 0, 0, time.UTC) }
	turns := []repairTurn{
		{at: at(1), old: 2},    // first process
		{at: at(2), old: 1},    //
		{at: at(10), old: 3.5}, // resumed: restored 3 + 0.5
		{at: at(11), old: 0.2},
		{at: at(20), old: 3.9}, // a held process taken back: its whole 3.9 again (+0.2 new)
		{at: at(30), old: 1.4}, // after a rewind to an earlier total of 1: 1 + 0.4
		{at: at(31), old: 0.1},
	}
	restores := []agent.Restore{{At: at(9), USD: 3}, {At: at(29), USD: 1}}
	repairSession(turns, restores, nil)
	want := []float64{2, 1, 0.5, 0.2, 0.2, 0.4, 0.1}
	for i, tr := range turns {
		if math.Abs(tr.new-want[i]) > 1e-9 {
			t.Fatalf("turn %d: %v, want %v", i, tr.new, want[i])
		}
	}
}

// A big turn of a process that has ended and saved its total is its own.
func TestRepairSessionTrustsTheSavedTotal(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 29, 21, m, 0, 0, time.UTC) }
	turns := []repairTurn{{at: at(1), old: 7.68}, {at: at(30), old: 8.0}, {at: at(48), old: 8.11}}
	end := 16.1
	repairSession(turns, []agent.Restore{{At: at(29), USD: 7.68}}, &end)
	if math.Abs(turns[1].new-0.32) > 1e-9 || math.Abs(turns[2].new-8.11) > 1e-9 {
		t.Fatalf("got %v and %v, want 0.32 and 8.11", turns[1].new, turns[2].new)
	}
}
