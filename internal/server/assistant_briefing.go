package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type searchInput struct {
	Query string   `json:"query"`
	Apps  []string `json:"apps"`
}
type appRead struct {
	Name   string
	Args   map[string]any
	Filter string
}
type appSection struct {
	Tool     string `json:"tool"`
	Data     any    `json:"data,omitempty"`
	Error    string `json:"error,omitempty"`
	Checked  string `json:"checked_at"`
	Duration int64  `json:"duration_ms"`
	Bounded  bool   `json:"bounded"`
}

func (s *Server) parallelReads(ctx context.Context, uid int64, tid string, reads []appRead) []appSection {
	out := make([]appSection, len(reads))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, r := range reads {
		wg.Add(1)
		go func(i int, r appRead) {
			defer wg.Done()
			start := time.Now()
			out[i] = appSection{Tool: r.Name, Checked: start.UTC().Format(time.RFC3339), Bounded: true}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i].Error = "Request cancelled"
				return
			}
			raw := s.readApp(ctx, uid, tid, r.Name, r.Args)
			out[i].Duration = time.Since(start).Milliseconds()
			if toolFailed(raw) {
				out[i].Error = "This source could not be read. Open the app or reconnect Ask."
			} else {
				data := toolData(raw)
				if r.Filter != "" {
					data = filterMetadata(data, r.Filter)
				}
				out[i].Data = boundData(data, 0)
			}
		}(i, r)
	}
	wg.Wait()
	return out
}

// Keep an overview small without silently claiming a partial collection is complete.
func boundData(v any, depth int) any {
	if depth > 12 {
		return "[nested data omitted]"
	}
	switch x := v.(type) {
	case []any:
		out := []any{}
		for i, a := range x {
			if i >= 20 {
				out = append(out, map[string]any{"omitted": len(x) - 20})
				break
			}
			out = append(out, boundData(a, depth+1))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, a := range x {
			out[k] = boundData(a, depth+1)
		}
		return out
	case string:
		if len(x) > 4000 {
			return x[:4000] + "… [text shortened]"
		}
	}
	return v
}
func (s *Server) appSearch(ctx context.Context, uid int64, tid string, cat map[string]askTool, in searchInput) any {
	choices := map[string]askTool{}
	names := []string{}
	for name := range cat {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := cat[name]
		if !t.Annotations.ReadOnly {
			continue
		}
		parts := strings.Split(name, "_")
		if len(parts) < 3 {
			continue
		}
		app := parts[1]
		if len(in.Apps) > 0 {
			found := false
			for _, a := range in.Apps {
				found = found || a == app
			}
			if !found {
				continue
			}
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if json.Unmarshal(t.InputSchema, &schema) != nil || len(schema.Required) > 0 {
			continue
		}
		var query struct {
			Properties map[string]any `json:"properties"`
		}
		if json.Unmarshal(schema.Properties["query"], &query) != nil || query.Properties["q"] == nil {
			continue
		}
		choices[name] = t
	}
	reads := []appRead{}
	for _, t := range choices {
		query := map[string]any{"q": in.Query}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(t.InputSchema, &schema)
		var qs struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(schema.Properties["query"], &qs)
		if qs.Properties["limit"] != nil {
			query["limit"] = 20
		}
		reads = append(reads, appRead{Name: t.Name, Args: map[string]any{"query": query}})
	}
	sort.Slice(reads, func(i, j int) bool { return reads[i].Name < reads[j].Name })
	for _, name := range []string{"pocket_garage_vehicles", "pocket_split_groups", "pocket_habits_list", "pocket_office_recent"} {
		parts := strings.Split(name, "_")
		allowed := len(in.Apps) == 0
		for _, app := range in.Apps {
			allowed = allowed || app == parts[1]
		}
		if t, ok := cat[name]; ok && t.Annotations.ReadOnly && allowed {
			reads = append(reads, appRead{Name: name, Args: map[string]any{}, Filter: in.Query})
		}
	}
	sections := s.parallelReads(ctx, uid, tid, reads)
	return map[string]any{"query": in.Query, "coverage": "Searches available app search endpoints; Garage vehicles, Split groups, Habits and recent Office documents use metadata matching. At most 20 matches per collection are shown; this is not a full export or a search of every attachment.", "sections": sections}
}
func (s *Server) briefing(ctx context.Context, uid int64, tid string, cat map[string]askTool) any {
	now := time.Now().In(s.loc(ctx, uid))
	day := now.Format("2006-01-02")
	next := now.AddDate(0, 0, 1).Format("2006-01-02")
	planned := []appRead{{Name: "pocket_calendar_events", Args: map[string]any{"query": map[string]any{"from": day, "to": next, "tz": now.Location().String()}}}, {Name: "pocket_tasks_summary", Args: map[string]any{}}, {Name: "pocket_money_bills", Args: map[string]any{}}, {Name: "pocket_review_summary", Args: map[string]any{}}, {Name: "pocket_garage_reminders", Args: map[string]any{}}, {Name: "pocket_bookshelf_reading", Args: map[string]any{}}}
	reads := []appRead{}
	unavailable := []string{}
	for _, r := range planned {
		if t, ok := cat[r.Name]; ok && t.Annotations.ReadOnly {
			reads = append(reads, r)
		} else {
			unavailable = append(unavailable, r.Name)
		}
	}
	return map[string]any{"date": day, "timezone": now.Location().String(), "coverage": "Calendar for today; task/review dashboards; recurring bills and maintenance reminders; books being read with today's pages and finish estimates. Collections are limited to 20 entries, and bill dates may be estimates.", "unavailable": unavailable, "sections": s.parallelReads(ctx, uid, tid, reads)}
}
func (s *Server) handleBriefing(w http.ResponseWriter, r *http.Request) {
	cat, err := s.catalog(r.Context(), me(r))
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, s.briefing(r.Context(), me(r), "", cat))
}
func (s *Server) handleAppSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		writeErr(w, 400, "Write a search query up to 200 characters")
		return
	}
	cat, err := s.catalog(r.Context(), me(r))
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, s.appSearch(r.Context(), me(r), "", cat, searchInput{Query: q}))
}

func filterMetadata(data any, query string) any {
	envelope, ok := data.(map[string]any)
	if !ok {
		return data
	}
	value := envelope["result"]
	rows, ok := value.([]any)
	if !ok {
		if object, ok := value.(map[string]any); ok {
			for _, key := range []string{"items", "results", "vehicles", "groups", "habits", "documents", "recent"} {
				if a, ok := object[key].([]any); ok {
					rows = append(rows, a...)
				}
			}
		}
	}
	matches := []any{}
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(query)) {
			matches = append(matches, row)
		}
	}
	envelope["result"] = matches
	envelope["search_scope"] = "Metadata from the returned overview only; not full document contents"
	return envelope
}
