package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type deferredPrompt struct {
	ID            string   `json:"id"`
	UserID        int64    `json:"-"`
	ThreadID      string   `json:"thread_id"`
	AfterThreadID string   `json:"after_thread_id"`
	AfterTitle    string   `json:"after_title"`
	AfterTurns    int      `json:"-"`
	Text          string   `json:"text"`
	UploadIDs     []string `json:"upload_ids"`
	Status        string   `json:"status"`
	Error         string   `json:"error"`
	CreatedAt     string   `json:"created_at"`
}

func (s *Server) deferredRoutes(r chi.Router) {
	r.Get("/threads/{id}/after", s.handleListDeferred)
	r.Post("/threads/{id}/after", s.messageOnce(s.handleCreateDeferred))
	r.Delete("/threads/{id}/after/{job}", s.handleCancelDeferred)
	r.Post("/threads/{id}/after/{job}/now", s.handleRunDeferred)
}

const deferredColumns = `id,user_id,thread_id,after_thread_id,after_title,after_turns,text,upload_ids,status,error,created_at`

func scanDeferred(row interface{ Scan(...any) error }) (deferredPrompt, error) {
	var d deferredPrompt
	var uploads string
	err := row.Scan(&d.ID, &d.UserID, &d.ThreadID, &d.AfterThreadID, &d.AfterTitle, &d.AfterTurns, &d.Text, &uploads, &d.Status, &d.Error, &d.CreatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(uploads), &d.UploadIDs)
	}
	return d, err
}

func (s *Server) handleListDeferred(w http.ResponseWriter, r *http.Request) {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	rows, err := s.store.DB.QueryContext(r.Context(), `SELECT `+deferredColumns+` FROM deferred_prompts WHERE thread_id=? AND user_id=? AND status NOT IN ('sent','cancelled') ORDER BY created_at,id`, t.ID, me(r))
	if err != nil {
		s.fail(w, err, "waiting prompts")
		return
	}
	defer rows.Close()
	out := []deferredPrompt{}
	for rows.Next() {
		d, err := scanDeferred(rows)
		if err != nil {
			s.fail(w, err, "waiting prompt")
			return
		}
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		s.fail(w, err, "waiting prompts")
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCreateDeferred(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	var in struct {
		Text          string   `json:"text"`
		UploadIDs     []string `json:"upload_ids"`
		AfterThreadID string   `json:"after_thread_id"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if len(in.Text) > 250000 || (in.Text == "" && len(in.UploadIDs) == 0) || len(in.UploadIDs) > 10 {
		writeErr(w, 400, "Provide a prompt with at most 10 attachments.")
		return
	}
	if in.AfterThreadID == t.ID {
		writeErr(w, 400, "Use Queue message to wait for this chat's current reply.")
		return
	}
	if t.Archived || s.rt.isExternal(t.ID) {
		writeErr(w, 400, "Open an active Pocket AI chat to save this prompt.")
		return
	}
	after, err := s.store.GetThread(r.Context(), me(r), in.AfterThreadID)
	if err != nil {
		writeErr(w, 404, "Waiting chat not found.")
		return
	}
	if after.Archived || s.rt.isExternal(after.ID) || (after.Status != "running" && after.Status != "waiting") {
		writeErr(w, 400, "That chat has already finished. Your draft is unchanged; choose another chat or send normally.")
		return
	}
	// Validate every attachment before associating any of them with the saved job.
	for _, id := range in.UploadIDs {
		up, err := s.store.GetUpload(r.Context(), me(r), id)
		if err != nil || (up.ThreadID != nil && *up.ThreadID != t.ID) {
			writeErr(w, 400, "An attached file is gone or belongs to another chat. Attach it again.")
			return
		}
	}
	uploads := in.UploadIDs
	if uploads == nil {
		uploads = []string{}
	}
	encoded, _ := json.Marshal(uploads)
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err, "save waiting prompt")
		return
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM deferred_prompts WHERE user_id=? AND status NOT IN ('sent','cancelled')`, me(r)).Scan(&pending); err != nil {
		s.fail(w, err, "waiting prompts")
		return
	}
	if pending >= 50 {
		writeErr(w, 400, "You already have 50 waiting prompts. Cancel or send some before adding more.")
		return
	}
	id := uuid.NewString()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO deferred_prompts(id,user_id,thread_id,after_thread_id,after_title,after_turns,text,upload_ids) VALUES(?,?,?,?,?,?,?,?)`, id, me(r), t.ID, after.ID, oneLine(after.Title, 200), after.Usage.Turns, in.Text, string(encoded))
	if err == nil {
		for _, upload := range uploads {
			var result sql.Result
			if result, err = tx.ExecContext(r.Context(), `UPDATE uploads SET thread_id=? WHERE id=? AND user_id=? AND (thread_id IS NULL OR thread_id=?)`, t.ID, upload, me(r), t.ID); err != nil {
				break
			}
			if n, _ := result.RowsAffected(); n != 1 {
				err = badRequest("An attachment belongs to another chat.")
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.fail(w, err, "save waiting prompt")
		return
	}
	writeJSON(w, 201, map[string]any{"queued": true, "thread": t})
}

func (s *Server) deferredForRequest(w http.ResponseWriter, r *http.Request) *deferredPrompt {
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return nil
	}
	d, err := scanDeferred(s.store.DB.QueryRowContext(r.Context(), `SELECT `+deferredColumns+` FROM deferred_prompts WHERE id=? AND thread_id=? AND user_id=?`, chi.URLParam(r, "job"), t.ID, me(r)))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "Waiting prompt not found.")
		return nil
	}
	if err != nil {
		s.fail(w, err, "waiting prompt")
		return nil
	}
	return &d
}

func (s *Server) handleCancelDeferred(w http.ResponseWriter, r *http.Request) {
	d := s.deferredForRequest(w, r)
	if d == nil {
		return
	}
	result, err := s.store.DB.ExecContext(r.Context(), `UPDATE deferred_prompts SET status='cancelled' WHERE id=? AND status IN ('waiting','blocked','uncertain')`, d.ID)
	if err != nil {
		s.fail(w, err, "cancel waiting prompt")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		writeErr(w, 409, "This prompt is already being sent. Check the conversation.")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleRunDeferred(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	d := s.deferredForRequest(w, r)
	if d == nil {
		return
	}
	if d.Status != "waiting" && d.Status != "blocked" {
		writeErr(w, 409, "Check this chat before sending the prompt again; its delivery may already have started.")
		return
	}
	// Send sooner still joins the destination's normal queue. It never interrupts it.
	if err := s.dispatchDeferred(r.Context(), *d); err != nil {
		s.log.Warn("send waiting prompt", "id", d.ID, "err", err)
		writeErr(w, 409, "Could not confirm delivery. Check the saved prompt and this chat before trying again.")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) blockDeferred(ctx context.Context, id, message string) {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE deferred_prompts SET status='blocked',error=? WHERE id=? AND status='waiting'`, message, id)
	if err != nil {
		s.log.Warn("pause waiting prompt", "err", err)
	}
}

func (s *Server) pauseDeferredSource(ctx context.Context, threadID, message string) {
	_, err := s.store.DB.ExecContext(ctx, `UPDATE deferred_prompts SET status='blocked',error=? WHERE after_thread_id=? AND status='waiting'`, message, threadID)
	if err != nil {
		s.log.Warn("pause dependent prompts", "err", err)
	}
}

func (s *Server) deferredLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deferredTick(ctx)
		}
	}
}

func (s *Server) deferredTick(ctx context.Context) {
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+deferredColumns+` FROM deferred_prompts WHERE status='waiting' ORDER BY created_at,id`)
	if err != nil {
		return
	}
	jobs := []deferredPrompt{}
	for rows.Next() {
		d, err := scanDeferred(rows)
		if err == nil {
			jobs = append(jobs, d)
		}
	}
	rows.Close()
	for _, d := range jobs {
		if ctx.Err() != nil {
			return
		}
		after, err := s.store.GetThread(ctx, d.UserID, d.AfterThreadID)
		if err != nil || after.Archived {
			s.blockDeferred(ctx, d.ID, "The waiting chat was removed or archived. Review this prompt or send it sooner.")
			continue
		}
		if after.Status == "error" {
			s.blockDeferred(ctx, d.ID, "The other chat ended with an error. Review it before sending this prompt.")
			continue
		}
		if after.Status != "idle" || after.Usage.Turns <= d.AfterTurns {
			continue
		}
		if run := s.rt.get(after.ID); run != nil {
			run.mu.Lock()
			busy := run.running || len(run.queue) > 0 || len(run.pending) > 0
			run.mu.Unlock()
			if busy {
				continue
			}
		}
		var data []byte
		err = s.store.DB.QueryRowContext(ctx, `SELECT data FROM items WHERE thread_id=? AND kind='turn' ORDER BY id DESC LIMIT 1`, after.ID).Scan(&data)
		var it agent.Item
		if err != nil || json.Unmarshal(data, &it) != nil || it.Turn == nil || !it.Turn.OK || it.Turn.Interrupted {
			s.blockDeferred(ctx, d.ID, "The other chat was interrupted or did not finish successfully. Review it before sending this prompt.")
			continue
		}
		if err = s.dispatchDeferred(ctx, d); err != nil {
			s.log.Debug("waiting prompt not dispatched", "id", d.ID, "err", err)
		}
	}
}

func (s *Server) dispatchDeferred(ctx context.Context, d deferredPrompt) error {
	u, err := s.store.GetUser(ctx, d.UserID)
	if err != nil {
		s.blockDeferred(ctx, d.ID, "Your account is no longer available.")
		return err
	}
	t, err := s.store.GetThread(ctx, d.UserID, d.ThreadID)
	if err != nil {
		return err
	}
	if (t.Source != "ask" && !u.MayRun()) || t.Archived || s.rt.isExternal(t.ID) {
		s.blockDeferred(ctx, d.ID, "This chat is archived, read-only, or no longer allowed to run. Review it before sending.")
		return badRequest("This chat cannot run right now.")
	}
	// Claim BEFORE dispatch. A crash or ambiguous send is never automatically
	// repeated. Concurrent ticks, cancellation and Send sooner use the same CAS.
	result, err := s.store.DB.ExecContext(ctx, `UPDATE deferred_prompts SET status='dispatching',error='' WHERE id=? AND status=?`, d.ID, d.Status)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return badRequest("This prompt has already been handled.")
	}
	_, err = s.postMessage(ctx, u, t, d.Text, d.UploadIDs, false)
	status, message := "sent", ""
	if err != nil {
		status, message = "uncertain", "Could not confirm delivery. Check this chat before retrying; the prompt may already be on its timeline."
	}
	// The phone disconnecting must not leave a successful dispatch unrecorded.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, saveErr := s.store.DB.ExecContext(finish, `UPDATE deferred_prompts SET status=?,error=? WHERE id=? AND status='dispatching'`, status, message, d.ID)
	if saveErr != nil {
		return saveErr
	}
	return err
}
