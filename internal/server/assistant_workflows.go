package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"sort"
	"strings"
	"time"
)

type workflow struct {
	ID      int64  `json:"id"`
	UserID  int64  `json:"-"`
	Name    string `json:"name"`
	Prompt  string `json:"prompt"`
	Model   string `json:"model"`
	Trigger string `json:"trigger_kind"`
	Spec    string `json:"spec"`
	Enabled bool   `json:"enabled"`
	Next    string `json:"next_run_at"`
	Thread  string `json:"last_thread_id"`
	Last    string `json:"last_run_at"`
	Status  string `json:"last_status"`
	Seen    string `json:"-"`
	Notify  bool   `json:"notify"`
}

const workflowCols = `id,user_id,name,prompt,model,trigger_kind,spec,enabled,next_run_at,last_thread_id,last_run_at,last_status,seen,notify`

func scanWorkflow(row interface{ Scan(...any) error }) (*workflow, error) {
	var x workflow
	err := row.Scan(&x.ID, &x.UserID, &x.Name, &x.Prompt, &x.Model, &x.Trigger, &x.Spec, &x.Enabled, &x.Next, &x.Thread, &x.Last, &x.Status, &x.Seen, &x.Notify)
	return &x, err
}
func (s *Server) workflowRoutes(r chi.Router) {
	r.Get("/workflows", s.handleWorkflows)
	r.Post("/workflows", s.handleSaveWorkflow)
	r.Put("/workflows/{wid}", s.handleSaveWorkflow)
	r.Delete("/workflows/{wid}", s.handleDeleteWorkflow)
	r.Post("/workflows/{wid}/run", s.handleRunWorkflow)
}
func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.DB.QueryContext(r.Context(), `SELECT `+workflowCols+` FROM workflows WHERE user_id=? ORDER BY id DESC`, me(r))
	if err != nil {
		s.fail(w, err, "workflows")
		return
	}
	defer rows.Close()
	out := []*workflow{}
	for rows.Next() {
		x, err := scanWorkflow(rows)
		if err != nil {
			s.fail(w, err, "workflows")
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleSaveWorkflow(w http.ResponseWriter, r *http.Request) {
	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()
	var x workflow
	x.Enabled = true
	x.Notify = true
	x.Model = "sonnet"
	x.Trigger = "manual"
	if decode(r, &x) != nil || strings.TrimSpace(x.Name) == "" || strings.TrimSpace(x.Prompt) == "" || len(x.Name) > 100 || len(x.Prompt) > 20000 {
		writeErr(w, 400, "Add a name and a prompt (up to 20,000 characters)")
		return
	}
	x.UserID = me(r)
	x.ID = idParam(r, "wid")
	if chi.URLParam(r, "wid") != "" && x.ID <= 0 {
		writeErr(w, 404, "Workflow not found")
		return
	}
	if x.Model != "sonnet" && x.Model != "haiku" && x.Model != "opus" {
		writeErr(w, 400, "Choose Sonnet, Haiku or Opus")
		return
	}
	if _, err := s.askToken(r.Context(), x.UserID); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	switch x.Trigger {
	case "manual":
		x.Next = ""
	case "schedule":
		sp, err := ParseSpec(x.Spec)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		x.Next = store.FormatTime(sp.Next(time.Now(), s.loc(r.Context(), x.UserID)))
	case "documents", "tasks":
		x.Next = store.FormatTime(time.Now().Add(time.Minute))
	default:
		writeErr(w, 400, "Choose manual, schedule, documents or tasks")
		return
	}
	var err error
	if x.ID > 0 {
		old, e := scanWorkflow(s.store.DB.QueryRowContext(r.Context(), `SELECT `+workflowCols+` FROM workflows WHERE id=? AND user_id=?`, x.ID, x.UserID))
		if e != nil {
			writeErr(w, 404, "Workflow not found")
			return
		}
		seen := old.Seen
		if old.Trigger != x.Trigger || (!old.Enabled && x.Enabled) {
			seen = ""
		}
		_, err = s.store.DB.ExecContext(r.Context(), `UPDATE workflows SET name=?,prompt=?,model=?,trigger_kind=?,spec=?,enabled=?,next_run_at=?,seen=?,notify=?,updated_at=? WHERE id=? AND user_id=?`, x.Name, x.Prompt, x.Model, x.Trigger, x.Spec, x.Enabled, x.Next, seen, x.Notify, store.Now(), x.ID, x.UserID)
	} else {
		var count int
		_ = s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM workflows WHERE user_id=?`, x.UserID).Scan(&count)
		if count >= 30 {
			writeErr(w, 400, "At most 30 workflows are supported")
			return
		}
		var res sql.Result
		res, err = s.store.DB.ExecContext(r.Context(), `INSERT INTO workflows(user_id,name,prompt,model,trigger_kind,spec,enabled,next_run_at,notify) VALUES(?,?,?,?,?,?,?,?,?)`, x.UserID, x.Name, x.Prompt, x.Model, x.Trigger, x.Spec, x.Enabled, x.Next, x.Notify)
		if err == nil {
			x.ID, _ = res.LastInsertId()
		}
	}
	if err != nil {
		s.fail(w, err, "save workflow")
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()
	res, err := s.store.DB.ExecContext(r.Context(), `DELETE FROM workflows WHERE id=? AND user_id=?`, idParam(r, "wid"), me(r))
	if err != nil {
		s.fail(w, err, "delete workflow")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeErr(w, 404, "Workflow not found")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()
	x, err := scanWorkflow(s.store.DB.QueryRowContext(r.Context(), `SELECT `+workflowCols+` FROM workflows WHERE id=? AND user_id=?`, idParam(r, "wid"), me(r)))
	if err != nil {
		writeErr(w, 404, "Workflow not found")
		return
	}
	t, err := s.runWorkflow(r.Context(), x, "manual:"+uuid.NewString(), "")
	if err != nil {
		s.fail(w, err, "run workflow")
		return
	}
	writeJSON(w, 201, t)
}
func (s *Server) runWorkflow(ctx context.Context, x *workflow, key, extra string) (*store.Thread, error) {
	if x.Thread != "" {
		if t, err := s.store.GetThread(ctx, x.UserID, x.Thread); err == nil && (t.Status == "running" || t.Status == "waiting" || t.Status == "queued") {
			return nil, badRequest("The previous run is still working or awaiting approval")
		}
	}
	u, err := s.store.GetUser(ctx, x.UserID)
	if err != nil {
		return nil, err
	}
	res, err := s.store.DB.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_runs(workflow_id,trigger_key) VALUES(?,?)`, x.ID, key)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, badRequest("This occurrence has already run")
	}
	t, err := s.createAsk(ctx, u, askCreate{Text: x.Prompt + extra + "\n\nThis is a saved Pocket workflow. Fetch fresh app data. Any change still requires the user's approval. If a source is unavailable, report that explicitly.", Model: x.Model})
	status := "running"
	tid := ""
	if t != nil {
		tid = t.ID
		t.Title = x.Name + " · " + time.Now().In(s.loc(ctx, x.UserID)).Format("Jan 2 15:04")
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE threads SET title=? WHERE id=? AND user_id=?`, t.Title, t.ID, x.UserID)
	}
	if err != nil {
		status = "error"
	}
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflow_runs SET thread_id=? WHERE workflow_id=? AND trigger_key=?`, tid, x.ID, key)
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET last_thread_id=?,last_run_at=?,last_status=? WHERE id=?`, tid, store.Now(), status, x.ID)
	return t, err
}
func (s *Server) workflowLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.pollWorkflows(ctx)
	}
}
func (s *Server) pollWorkflows(ctx context.Context) {
	s.workflowMu.Lock()
	defer s.workflowMu.Unlock()
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+workflowCols+` FROM workflows WHERE enabled=1 AND trigger_kind!='manual' AND next_run_at<=?`, store.Now())
	if err != nil {
		return
	}
	all := []*workflow{}
	for rows.Next() {
		x, e := scanWorkflow(rows)
		if e == nil {
			all = append(all, x)
		}
	}
	rows.Close()
	for _, x := range all {
		if ctx.Err() != nil {
			return
		}
		next := time.Now().Add(time.Minute)
		key := "schedule:" + x.Next
		extra := ""
		if x.Trigger == "schedule" {
			sp, e := ParseSpec(x.Spec)
			if e != nil {
				continue
			}
			next = sp.Next(time.Now(), s.loc(ctx, x.UserID))
		} else {
			ids, e := s.workflowEventIDs(ctx, x)
			if e != nil {
				_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET last_status='source unavailable',next_run_at=? WHERE id=?`, store.FormatTime(next), x.ID)
				continue
			}
			seen := map[string]bool{}
			_ = json.Unmarshal([]byte(x.Seen), &seen)
			fresh := []string{}
			for _, id := range ids {
				if !seen[id] {
					fresh = append(fresh, id)
				}
				seen[id] = true
			}
			if len(seen) > 5000 {
				seen = map[string]bool{}
				for _, id := range ids {
					seen[id] = true
				}
			}
			if x.Seen == "" || len(fresh) == 0 {
				raw, _ := json.Marshal(seen)
				_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET seen=?,next_run_at=? WHERE id=?`, string(raw), store.FormatTime(next), x.ID)
				continue
			}
			// Keep the event pending while an earlier run awaits approval; no dropped work.
			if t, e := s.store.GetThread(ctx, x.UserID, x.Thread); e == nil && (t.Status == "running" || t.Status == "waiting") {
				continue
			}
			sort.Strings(fresh)
			key = x.Trigger + ":" + strings.Join(fresh, ",")
			extra = "\nNew " + x.Trigger + " record IDs observed: " + strings.Join(fresh, ", ") + ". Read these records before acting."
			raw, _ := json.Marshal(seen)
			_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET seen=? WHERE id=?`, string(raw), x.ID)
		}
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE workflows SET next_run_at=? WHERE id=?`, store.FormatTime(next), x.ID)
		if _, err := s.runWorkflow(ctx, x, key, extra); err != nil {
			s.log.Warn("workflow run", "id", x.ID, "err", err)
		}
	}
}
func (s *Server) workflowEventIDs(ctx context.Context, x *workflow) ([]string, error) {
	name := "pocket_" + x.Trigger + "_list"
	query := map[string]any{"limit": 100, "sort": "-id"}
	if x.Trigger == "tasks" {
		name = "pocket_tasks_activity"
		query = map[string]any{"limit": 100}
	}
	cat, err := s.catalog(ctx, x.UserID)
	if err != nil {
		return nil, err
	}
	if t, ok := cat[name]; !ok || !t.Annotations.ReadOnly {
		return nil, fmt.Errorf("source unavailable")
	}
	token, err := s.askToken(ctx, x.UserID)
	if err != nil {
		return nil, err
	}
	raw, err := s.askReadRPC(ctx, token, "tools/call", map[string]any{"name": name, "arguments": map[string]any{"query": query}})
	if err != nil {
		return nil, err
	}
	if toolFailed(raw) {
		return nil, fmt.Errorf("source unavailable")
	}
	v, _ := toolData(raw).(map[string]any)
	data := v["result"]
	if m, ok := data.(map[string]any); ok {
		for _, k := range []string{x.Trigger, "items", "results"} {
			if a, ok := m[k].([]any); ok {
				data = a
				break
			}
		}
	}
	arr, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("unrecognized source response")
	}
	ids := []string{}
	for _, a := range arr {
		if m, ok := a.(map[string]any); ok && m["id"] != nil {
			if x.Trigger == "tasks" {
				if m["kind"] == "created" && m["task_id"] != nil {
					ids = append(ids, fmt.Sprint(m["task_id"]))
				}
			} else {
				ids = append(ids, fmt.Sprint(m["id"]))
			}
		}
	}
	return ids, nil
}
func (s *Server) workflowFinished(t *store.Thread, ti agent.TurnInfo, text string) bool {
	var name string
	var notify bool
	err := s.store.DB.QueryRowContext(s.rt.ctx, `SELECT name,notify FROM workflows WHERE last_thread_id=? AND last_status='running' AND user_id=?`, t.ID, t.UserID).Scan(&name, &notify)
	if err != nil {
		return false
	}
	status := "done"
	if !ti.OK {
		status = "error"
	}
	_, _ = s.store.DB.ExecContext(s.rt.ctx, `UPDATE workflows SET last_status=? WHERE last_thread_id=? AND user_id=?`, status, t.ID, t.UserID)
	if notify {
		s.notifyUser(s.rt.ctx, t.UserID, "schedule", name, oneLine(firstNonEmpty(text, status), 200), s.threadURL(t.ID))
	}
	return true
}
