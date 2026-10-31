package agent

import (
	"math"
	"testing"
)

func TestCodexCost(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	// A real gpt-6-astra response from a rollout: 155 uncached, 49,280 cached.
	u := codexUsage{Input: 49435, Cached: 49280, Output: 407}
	want := (155*10 + 49280*1.0 + 407*50) / 1e6
	if got := codexCost("gpt-6-astra", "", u); !near(got, want) {
		t.Fatalf("astra standard: %v, want %v", got, want)
	}
	if got := codexCost("gpt-6-astra", "priority", u); !near(got, 2*want) {
		t.Fatalf("astra fast: %v, want %v", got, 2*want)
	}

	// Cache writes and the cheaper Sol cache rate.
	u = codexUsage{Input: 30000, Cached: 20000, Write: 5000, Output: 1000}
	want = (5000*2 + 20000*0.10 + 5000*2.5 + 1000*10) / 1e6
	if got := codexCost("gpt-6.1-sol", "", u); !near(got, want) {
		t.Fatalf("sol: %v, want %v", got, want)
	}

	// Over 272K input tokens the whole request pays long-context rates.
	u = codexUsage{Input: 300000, Cached: 290000, Output: 100}
	want = (10000*4 + 290000*0.20 + 100*15) / 1e6
	if got := codexCost("gpt-6.1-sol", "", u); !near(got, want) {
		t.Fatalf("sol long: %v, want %v", got, want)
	}

	if got := codexCost("gpt-unknown", "", u); got != 0 {
		t.Fatalf("unknown model priced: %v", got)
	}
}
