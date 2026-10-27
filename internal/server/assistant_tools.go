package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/google/uuid"
	"sort"
	"strings"
	"time"
)

func toolResult(v any, failed bool) json.RawMessage {
	b, _ := json.Marshal(v)
	out, _ := json.Marshal(map[string]any{"isError": failed, "structuredContent": v, "content": []map[string]string{{"type": "text", "text": string(b)}}})
	return out
}
func toolFailure(message string) json.RawMessage {
	return toolResult(map[string]string{"error": message}, true)
}
func toolFailed(raw json.RawMessage) bool {
	var v struct {
		Error bool `json:"isError"`
	}
	return json.Unmarshal(raw, &v) != nil || v.Error
}
func toolData(raw json.RawMessage) any {
	var v struct {
		Data    any `json:"structuredContent"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.Data != nil {
		return v.Data
	}
	if len(v.Content) > 0 {
		var x any
		if json.Unmarshal([]byte(v.Content[0].Text), &x) == nil {
			return x
		}
		return v.Content[0].Text
	}
	return nil
}
func extraTools() []askTool {
	defs := []struct{ name, description, schema string }{
		{"pocket_vehicle_costs", "Read a vehicle cost summary and check Garage/Money overlaps. Confirmed duplicates require matching stored transaction ID, owner, date, currency and amount. Similar unlinked records stay possible duplicates. Never fabricate a combined total.", `{"type":"object","properties":{"vehicle_id":{"type":"integer","minimum":1},"money_query":{"type":"string","maxLength":100}},"required":["vehicle_id"]}`},
		{"pocket_find_tools", "Find available Pocket operations by app or intent; returns exact schemas to use with pocket_call_tool.", `{"type":"object","properties":{"query":{"type":"string","maxLength":200}},"required":["query"]}`},
		{"pocket_call_tool", "Call an operation discovered with pocket_find_tools. Every write requires separate user approval. Do not invent operation names or arguments.", `{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object"}},"required":["name","arguments"]}`},
		{"pocket_search", "Search across connected Pocket apps concurrently. Results are bounded per app; errors and coverage are explicit. Use apps to narrow a search.", `{"type":"object","properties":{"query":{"type":"string","maxLength":200},"apps":{"type":"array","items":{"type":"string"},"maxItems":25}},"required":["query"]}`},
		{"pocket_briefing", "Read today's appointments, tasks, recurring bills, reviews and maintenance in one call, with each source and any partial failures.", `{"type":"object","properties":{}}`},
		{"pocket_undo_action", "Undo a recorded Notes update only if it has not changed since. Shows a new approval with the exact restored fields. Other actions cannot currently be undone.", `{"type":"object","properties":{"action_id":{"type":"string"}},"required":["action_id"]}`},
	}
	out := []askTool{}
	for _, d := range defs {
		t := askTool{Name: d.name, Description: d.description, InputSchema: json.RawMessage(d.schema)}
		t.Annotations.ReadOnly = d.name != "pocket_call_tool" && d.name != "pocket_undo_action"
		out = append(out, t)
	}
	return out
}
func findTools(all []askTool, q string, limit int) []askTool {
	words := strings.Fields(strings.ToLower(q))
	type scored struct {
		t askTool
		n int
	}
	scores := []scored{}
	for _, t := range all {
		n := 0
		for _, w := range words {
			if strings.Contains(strings.ToLower(t.Name), w) {
				n += 5
			} else if strings.Contains(strings.ToLower(t.Description), w) {
				n++
			}
		}
		if n > 0 {
			scores = append(scores, scored{t, n})
		}
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].n > scores[j].n })
	out := []askTool{}
	for i, x := range scores {
		if i >= limit {
			break
		}
		out = append(out, x.t)
	}
	return out
}
func scopedCatalog(all []askTool, prompt string) any {
	// Small fixtures/catalogs remain complete. Large catalogs expose a small relevant
	// set and explicit discovery, so a later turn can still use any approved app.
	selected := all
	if len(all) > 30 {
		selected = findTools(all, prompt, 12)
		for _, t := range all {
			if t.Name == "pocket_money_summary" || t.Name == "pocket_notes_folders" {
				found := false
				for _, x := range selected {
					found = found || x.Name == t.Name
				}
				if !found {
					selected = append(selected, t)
				}
			}
		}
	}
	return map[string]any{"tools": append(append([]askTool{}, selected...), extraTools()...)}
}
func (s *Server) catalog(ctx context.Context, uid int64) (map[string]askTool, error) {
	token, err := s.askToken(ctx, uid)
	if err != nil {
		return nil, err
	}
	raw, err := s.askReadRPC(ctx, token, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var v struct {
		Tools []askTool `json:"tools"`
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	out := map[string]askTool{}
	for _, t := range v.Tools {
		out[t.Name] = t
	}
	return out, nil
}
func (s *Server) readApp(ctx context.Context, uid int64, tid, name string, args any) json.RawMessage {
	raw, _ := json.Marshal(args)
	id := s.startAppCall(ctx, uid, tid, name, raw, false)
	start := time.Now()
	token, err := s.askToken(ctx, uid)
	var result json.RawMessage
	if err == nil {
		result, err = s.askReadRPC(ctx, token, "tools/call", map[string]any{"name": name, "arguments": args})
	}
	if err != nil {
		result = toolFailure(err.Error())
	}
	status := "done"
	if toolFailed(result) {
		status = "error"
	}
	s.finishAppCall(id, status, result, time.Since(start))
	return result
}
func (s *Server) callPocketTool(ctx, session context.Context, t *store.Thread, sink *runSink, catalog map[string]askTool, raw json.RawMessage) json.RawMessage {
	// whose apps: the person whose message this turn answers (in a shared
	// thread that need not be the owner); approvals are theirs too
	uid := s.rt.turnSender(t)
	byName := ""
	if _, shared := s.rt.sharesOf(t.ID); shared || uid != t.UserID {
		if u, err := s.store.GetUser(ctx, uid); err == nil {
			byName = displayName(u)
		}
	}
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &call) != nil {
		return toolFailure("Invalid tool call")
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	if call.Name == "pocket_vehicle_costs" {
		var in struct {
			ID    int64  `json:"vehicle_id"`
			Query string `json:"money_query"`
		}
		if json.Unmarshal(call.Arguments, &in) != nil || in.ID < 1 || len(in.Query) > 100 {
			return toolFailure("Choose a vehicle ID and optional short Money search")
		}
		return s.vehicleCosts(ctx, uid, t.ID, catalog, in.ID, in.Query)
	}
	if call.Name == "pocket_find_tools" {
		var in struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(call.Arguments, &in) != nil || len(in.Query) > 200 {
			return toolFailure("Use a query up to 200 characters")
		}
		all := []askTool{}
		for _, v := range catalog {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
		return toolResult(map[string]any{"tools": findTools(all, in.Query, 20)}, false)
	}
	if call.Name == "pocket_search" {
		var in searchInput
		if json.Unmarshal(call.Arguments, &in) != nil || strings.TrimSpace(in.Query) == "" || len(in.Query) > 200 {
			return toolFailure("Write a short search query")
		}
		return toolResult(s.appSearch(ctx, uid, t.ID, catalog, in), false)
	}
	if call.Name == "pocket_briefing" {
		return toolResult(s.briefing(ctx, uid, t.ID, catalog), false)
	}
	if call.Name == "pocket_call_tool" {
		var nested struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(call.Arguments, &nested) != nil {
			return toolFailure("Invalid operation")
		}
		call.Name, call.Arguments = nested.Name, nested.Arguments
	}
	undoID := ""
	if call.Name == "pocket_undo_action" {
		var in struct {
			ID string `json:"action_id"`
		}
		_ = json.Unmarshal(call.Arguments, &in)
		args, err := s.undoNoteArgs(ctx, uid, in.ID)
		if err != nil {
			return toolFailure(err.Error())
		}
		undoID = in.ID
		call.Name = "pocket_notes_update"
		call.Arguments = args
	}
	tool, ok := catalog[call.Name]
	if !ok {
		return toolFailure("Unknown or unavailable Pocket tool. Discover it with pocket_find_tools.")
	}
	var args map[string]any
	if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
		return toolFailure("Arguments must be an object")
	}
	if tool.Annotations.ReadOnly {
		return s.readApp(ctx, uid, t.ID, call.Name, args)
	}
	callID := s.startAppCall(ctx, uid, t.ID, call.Name, call.Arguments, true)
	if callID == "" {
		return toolFailure("Could not save the approval record; no change was sent")
	}
	start := time.Now()
	status := "denied"
	result := toolFailure("Change was not approved. Nothing was sent to the application.")
	defer func() { s.finishAppCall(callID, status, result, time.Since(start)) }()
	preview := askReviewInput(call.Arguments)
	// Capture a Notes version for a conflict-safe undo; failure does not fabricate an undo.
	if call.Name == "pocket_notes_update" {
		if data, ok := args["data"].(map[string]any); ok && data["etag"] != nil {
			before := s.readApp(ctx, uid, t.ID, "pocket_notes_get", map[string]any{"id": args["id"]})
			if !toolFailed(before) {
				_, _ = s.store.DB.ExecContext(ctx, `UPDATE app_calls SET before_state=? WHERE id=?`, string(before), callID)
				prior := map[string]any{}
				old := noteObject(before)
				for k := range data {
					if k != "etag" {
						if v, exists := old[k]; exists {
							prior[k] = v
						}
					}
				}
				if len(prior) > 0 {
					preview, _ = json.Marshal(map[string]any{"previous": prior, "proposed": json.RawMessage(preview)})
				}
			}
		}
	}
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE app_calls SET status='pending' WHERE id=?`, callID)
	id := "ask-" + uuid.NewString()
	answer := make(chan bool, 1)
	key := struct {
		S          *Server
		Thread, ID string
	}{s, t.ID, id}
	req := agent.Request{ID: id, Type: "permission", ToolName: call.Name, Title: askActionTitle(call.Name), Input: preview, Status: "pending", ForID: uid, For: byName, App: tool.Meta.App, Kind: tool.Meta.Kind}
	approved := false
	if why := s.appAutoRules(ctx, uid).allows(tool); why != "" {
		// the person's own rule: shown in the timeline as allowed, nobody waits
		req.Status, req.Auto = "allowed", why
		sink.rt.saveItem(sink.r.threadID, agent.Item{Key: "req-" + id, Kind: "request", Request: &req})
		approved = true
	} else {
		askApprovals.Store(key, askApproval{Thread: t.ID, Answer: answer, UserID: uid})
		defer askApprovals.Delete(key)
		sink.Request(req)
		select {
		case approved = <-answer:
		case <-ctx.Done():
		case <-session.Done():
		case <-time.After(10 * time.Minute):
		}
	}
	if !approved {
		_ = s.rt.respond(t, id, agent.Response{Decision: "deny"})
		return result
	}
	_, err := s.store.DB.ExecContext(ctx, `UPDATE app_calls SET status='running' WHERE id=?`, callID)
	if err != nil {
		result = toolFailure("Could not record approval; nothing was sent")
		return result
	}
	token, err := s.askToken(ctx, uid)
	if err != nil {
		result = toolFailure(err.Error())
		status = "error"
		return result
	}
	result, err = s.askReadRPC(ctx, token, "tools/call", map[string]any{"name": call.Name, "arguments": args})
	status = "done"
	if err != nil {
		result = toolFailure(err.Error())
		status = "unknown"
	} else if toolFailed(result) {
		status = "error"
	}
	if undoID != "" && status == "done" {
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE app_calls SET status='undone' WHERE id=? AND user_id=?`, undoID, uid)
	}
	return result
}
func noteObject(raw json.RawMessage) map[string]any {
	v, _ := toolData(raw).(map[string]any)
	if r, ok := v["result"].(map[string]any); ok {
		v = r
	}
	if r, ok := v["note"].(map[string]any); ok {
		v = r
	}
	return v
}
func (s *Server) undoNoteArgs(ctx context.Context, uid int64, id string) (json.RawMessage, error) {
	var tool, status, input, before, after string
	err := s.store.DB.QueryRowContext(ctx, `SELECT tool,status,input,before_state,result FROM app_calls WHERE id=? AND user_id=?`, id, uid).Scan(&tool, &status, &input, &before, &after)
	if err != nil || tool != "pocket_notes_update" || status != "done" {
		return nil, fmt.Errorf("This action cannot be undone")
	}
	old, new := noteObject(json.RawMessage(before)), noteObject(json.RawMessage(after))
	etag, _ := new["etag"].(string)
	if old == nil || etag == "" {
		return nil, fmt.Errorf("No verified before/after versions were returned; undo is unavailable")
	}
	var original map[string]any
	_ = json.Unmarshal([]byte(input), &original)
	current := s.readApp(ctx, uid, "", "pocket_notes_get", map[string]any{"id": original["id"]})
	if toolFailed(current) || noteObject(current)["etag"] != etag {
		return nil, fmt.Errorf("The note changed since this action. Review it in Notes; automatic undo is unavailable")
	}
	data := map[string]any{"etag": etag}
	changed, _ := original["data"].(map[string]any)
	for _, k := range []string{"title", "content", "category", "favorite", "pinned"} {
		if _, ok := changed[k]; ok {
			if v, exists := old[k]; exists {
				data[k] = v
			} else {
				return nil, fmt.Errorf("The previous value for %s was not recorded", k)
			}
		}
	}
	return json.Marshal(map[string]any{"id": original["id"], "request_id": uuid.NewString(), "data": data})
}
