package agent

// Claude Code reports each turn's cost itself (total_cost_usd); Codex only
// reports tokens, so its turns are priced here.

// codexRate is a price in USD per million tokens.
type codexRate struct{ in, cached, write, out float64 }

// codexPrices: OpenAI's Standard-tier list prices, short context (up to
// 272K input tokens) then long context. Cache writes at the 30-minute rate.
// Source: developers.openai.com/api/docs/pricing, checked 2026-10-31.
var codexPrices = map[string][2]codexRate{
	"gpt-6-astra":   {{10, 1, 12.5, 50}, {20, 2, 25, 75}},
	"gpt-6.1-sol":   {{2, 0.10, 2.5, 10}, {4, 0.20, 5, 15}},
	"gpt-6.1-terra": {{1, 0.05, 1.25, 5}, {2, 0.10, 2.5, 7.5}},
	"gpt-6-sol":     {{2, 0.20, 2.5, 10}, {4, 0.40, 5, 15}},
	"gpt-6-luna":    {{0.10, 0.01, 0.125, 0.5}, {0.20, 0.02, 0.25, 0.75}},
	"gpt-5.6-sol":   {{4, 0.40, 5, 20}, {8, 0.80, 10, 30}},
	"gpt-5.6-terra": {{2, 0.20, 2.5, 12}, {4, 0.40, 5, 18}},
	"gpt-5.6-luna":  {{0.20, 0.02, 0.25, 1.2}, {0.40, 0.04, 0.5, 1.8}},
}

const codexLongContext = 272_000

// codexUsage is the token count of one model response, as Codex reports it:
// Input includes the cached and cache-written tokens, Output the reasoning.
type codexUsage struct {
	Input, Cached, Write, Output int64
}

// codexCost prices one model response (0 for a model without a known price).
// The Fast tier ("priority") costs twice the Standard rates.
func codexCost(model, tier string, u codexUsage) float64 {
	p, ok := codexPrices[model]
	if !ok {
		return 0
	}
	r := p[0]
	if u.Input > codexLongContext {
		r = p[1]
	}
	plain := max(u.Input-u.Cached-u.Write, 0)
	usd := (float64(plain)*r.in + float64(u.Cached)*r.cached + float64(u.Write)*r.write + float64(u.Output)*r.out) / 1e6
	if tier == "priority" {
		usd *= 2
	}
	return usd
}
