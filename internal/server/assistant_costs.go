package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Match only comparable minor-unit amounts. A date/amount match by itself is
// not proof: confirmed pairs also require the recorded Money link and owner.
func costMatch(garage, money map[string]any) string {
	cost, ok := garage["cost"].(float64)
	amount, aok := money["amount"].(float64)
	currency, _ := garage["currency"].(string)
	mc, _ := money["currency"].(string)
	if !ok || !aok || cost <= 0 || amount != cost || currency == "" || !strings.EqualFold(currency, mc) || garage["date"] != money["date"] || money["kind"] != "expense" {
		return ""
	}
	if garage["money_tx_id"] != nil && garage["money_tx_id"] == money["id"] && garage["money_user_id"] != nil && garage["money_user_id"] == money["user_id"] {
		return "confirmed"
	}
	return "possible"
}
func appPayload(raw json.RawMessage) any { v, _ := toolData(raw).(map[string]any); return v["result"] }
func records(v any) []map[string]any {
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"items", "transactions", "records", "fuel", "data"} {
			if a, ok := m[k].([]any); ok {
				v = a
				break
			}
		}
	}
	a, _ := v.([]any)
	out := []map[string]any{}
	for _, x := range a {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
func (s *Server) vehicleCosts(ctx context.Context, uid int64, tid string, cat map[string]askTool, id int64, query string) json.RawMessage {
	for _, name := range []string{"pocket_garage_summary", "pocket_garage_records", "pocket_garage_fuel"} {
		if t, ok := cat[name]; !ok || !t.Annotations.ReadOnly {
			return toolFailure("Garage cost tools are unavailable; reconnect the app")
		}
	}
	summary := s.readApp(ctx, uid, tid, "pocket_garage_summary", map[string]any{"id": id})
	service := s.readApp(ctx, uid, tid, "pocket_garage_records", map[string]any{"vehicle_id": id, "query": map[string]any{"limit": 100}})
	fuel := s.readApp(ctx, uid, tid, "pocket_garage_fuel", map[string]any{"vehicle_id": id})
	rows := append(records(appPayload(service)), records(appPayload(fuel))...)
	if len(rows) > 200 {
		rows = rows[:200]
	}
	candidates := []map[string]any{}
	sources := []any{toolData(summary), toolData(service), toolData(fuel)}
	if t, ok := cat["pocket_money_transactions"]; ok && t.Annotations.ReadOnly && query != "" {
		raw := s.readApp(ctx, uid, tid, "pocket_money_transactions", map[string]any{"query": map[string]any{"q": query, "kind": "expense", "limit": 100}})
		candidates = records(appPayload(raw))
		sources = append(sources, toolData(raw))
	}
	linked := map[string]bool{}
	checks := 0
	if t, ok := cat["pocket_money_get_transaction"]; ok && t.Annotations.ReadOnly {
		for _, r := range rows {
			link := r["money_tx_id"]
			if link == nil {
				continue
			}
			key := fmt.Sprint(link)
			if linked[key] || checks >= 30 {
				continue
			}
			linked[key] = true
			checks++
			raw := s.readApp(ctx, uid, tid, "pocket_money_get_transaction", map[string]any{"id": link})
			if m, ok := appPayload(raw).(map[string]any); ok && !toolFailed(raw) {
				candidates = append(candidates, m)
			}
		}
	}
	confirmed, possible := []any{}, []any{}
	used := map[string]bool{}
	for _, g := range rows {
		for _, m := range candidates {
			kind := costMatch(g, m)
			if kind == "" {
				continue
			}
			key := fmt.Sprint(g["id"]) + ":" + fmt.Sprint(g["volume"]) + ":" + fmt.Sprint(m["id"])
			if used[key] {
				continue
			}
			used[key] = true
			pair := map[string]any{"garage_record": g, "money_transaction": m, "reason": "Same date, original currency and minor-unit amount"}
			if kind == "confirmed" {
				pair["reason"] = "Same stored transaction ID and Money owner, with matching date, currency and amount"
				confirmed = append(confirmed, pair)
			} else {
				possible = append(possible, pair)
			}
		}
	}
	return toolResult(map[string]any{"vehicle_id": id, "sources": sources, "confirmed_duplicates": confirmed, "possible_duplicates": possible, "combined_total": nil, "coverage": "Garage summary provides its own totals. Duplicate check examines at most 100 service records and 200 combined fuel/service records, 30 linked Money records, and 100 text-matched Money transactions. Possible duplicates are never subtracted. No combined total is produced because coverage and uncertain matches may be incomplete. Keep app totals separate and cite their ranges/currencies.", "linked_records_checked": checks}, false)
}
