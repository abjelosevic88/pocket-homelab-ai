package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/google/uuid"
)

// Rewind trims the existing Pocket conversation. The provider history is
// snapshotted at the boundary so another terminal cannot restore discarded context.
func (s *Server) handleRewind(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.thread(w, r)
	if t == nil {
		return
	}
	unlock := s.lockThreadEdit(t.ID)
	defer unlock()
	// A send may have completed while this request waited for the edit lock.
	t, err := s.store.GetThreadAny(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "read thread")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if decode(r, &in) != nil || in.Key == "" {
		writeErr(w, 400, "select a message to rewind to")
		return
	}
	s.rt.decorate(t)
	subs, _ := t.Subagents.([]agent.Subagent)
	if t.Status == "running" || t.Status == "waiting" || t.External || t.Queued > 0 || t.Pending > 0 || len(subs) > 0 {
		writeErr(w, 409, "stop the agent and clear queued messages before rewinding")
		return
	}
	s.branchBefore(w, r, t, in.Key, false)
}

// nextSentPrompt is the first sent user message after a turn footer: forking
// at that turn is a branch before it. "" = the turn is the latest one.
func nextSentPrompt(rows []*store.Item, turnKey string) string {
	seen := false
	for _, row := range rows {
		if !seen {
			seen = row.Key == turnKey && row.Kind == "turn"
			continue
		}
		if row.Kind != "user" {
			continue
		}
		var it agent.Item
		if json.Unmarshal(row.Data, &it) == nil && !it.Queued && !it.Unsent {
			return it.Key
		}
	}
	return ""
}

// branchBefore prepares provider context before the selected user message.
// Rewind replaces the existing conversation; fork copies the prefix into a
// private chat and can run while the original continues working.
func (s *Server) branchBefore(w http.ResponseWriter, r *http.Request, t *store.Thread, key string, fork bool) {
	ctx := r.Context()
	rows, err := s.store.ListItems(ctx, t.ID)
	if err != nil {
		s.fail(w, err, "read history")
		return
	}
	var prefix []agent.Item
	var users []agent.Item
	var target *agent.Item
	for _, row := range rows {
		var it agent.Item
		if err := json.Unmarshal(row.Data, &it); err != nil {
			s.fail(w, err, "read message")
			return
		}
		if it.Key == key {
			if it.Kind != "user" || it.Queued || it.Unsent {
				writeErr(w, 400, "select a sent user message")
				return
			}
			target = &it
			users = append(users, it)
			break
		}
		if it.Queued || it.Unsent || it.Kind == "request" || (fork && it.Tool != nil && it.Tool.Status == "running") {
			continue
		}
		prefix = append(prefix, it)
		if it.Kind == "user" {
			users = append(users, it)
		}
	}
	wholeFork := fork && key == ""
	if target == nil && !wholeFork {
		writeErr(w, 404, "no such message")
		return
	}
	// a thread on another machine runs on that machine's own login (account 0)
	if t.HostID == 0 {
		if err := s.checkAccount(ctx, userFrom(ctx), t.Provider, t.AccountID); err != nil {
			s.fail(w, err, "account")
			return
		}
	}
	var sid, sessionPath string
	checkpoint := agent.RewindCheckpoint{TurnID: codexReplyCheckpoint(rows, key), UnconfirmedPrompts: codexUnconfirmedPrompts(prefix)}
	if len(users) > 1 || wholeFork {
		if t.ProviderSessionID == "" {
			writeErr(w, 409, "the agent history is unavailable for this message")
			return
		}
		if t.HostID != 0 {
			switch t.Provider {
			case "claude":
				sid, err = s.copyRemoteClaude(ctx, t, users, wholeFork)
			case "codex":
				var h *store.Host
				h, err = s.host(ctx, t.HostID)
				if err == nil {
					o := codexRewindOptions(t, hostBin(h, "codex"), nil, remoteOf(h))
					if wholeFork && checkpoint.TurnID == "" {
						sid, err = agent.ForkCodex(ctx, o)
					} else {
						sid, err = agent.RewindCodex(ctx, o, users, checkpoint)
					}
				}
			default:
				err = fmt.Errorf("this provider does not support rewind")
			}
		} else if err = s.ensureSession(ctx, t, t.ProviderSessionID); err == nil {
			home, env := s.accountHome(ctx, t.Provider, t.AccountID)
			switch t.Provider {
			case "claude":
				if wholeFork {
					sid, sessionPath, err = agent.ForkClaude(home, t.ProviderSessionID)
				} else {
					sid, sessionPath, err = agent.RewindClaude(home, t.ProviderSessionID, users)
				}
			case "codex":
				o := codexRewindOptions(t, s.cfg.CodexBin, env, nil)
				if wholeFork && checkpoint.TurnID == "" {
					sid, err = agent.ForkCodex(ctx, o)
				} else {
					sid, err = agent.RewindCodex(ctx, o, users, checkpoint)
				}
			default:
				err = fmt.Errorf("this provider does not support rewind")
			}
		}
		if err != nil {
			writeErr(w, 409, map[bool]string{false: "Cannot rewind: ", true: "Cannot fork here: "}[fork]+err.Error())
			return
		}
	}
	if !fork {
		s.rewindInPlace(w, r, t, rows, prefix, target, sid, sessionPath)
		return
	}
	n := &store.Thread{ID: uuid.NewString(), UserID: t.UserID, Title: "Fork of " + t.Title,
		Provider: t.Provider, AccountID: t.AccountID, Model: t.Model, Effort: t.Effort, Mode: t.Mode, Speed: t.Speed,
		Cwd: t.Cwd, WorkspaceID: t.WorkspaceID, WorktreeID: t.WorktreeID, NoMemory: t.NoMemory, ProviderSessionID: sid,
		Source: "fork", SourceRef: t.ID, HostID: t.HostID, LogOffset: -1}
	if t.Source == "ask" {
		n.Source, n.SourceRef = t.Source, t.SourceRef
	}
	if sessionPath != "" {
		n.LogOffset = agent.FileSize(sessionPath)
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		cleanup := context.WithoutCancel(ctx)
		for _, up := range s.store.UploadsOfThread(cleanup, n.ID) {
			s.removeUploadFile(up.Path)
			_ = s.store.DeleteUpload(cleanup, n.UserID, up.ID)
		}
		_ = s.store.DeleteThread(cleanup, n.UserID, n.ID)
		if sessionPath != "" {
			_ = os.Remove(sessionPath)
		}
	}()
	if err := s.store.CreateThread(ctx, n); err != nil {
		s.fail(w, err, "rewind")
		return
	}
	// Make independent attachment files: deleting either conversation must not
	// break the other. Re-map URLs in the copied timeline and in the draft.
	copied := map[string]uploadOut{}
	copyItem := func(it *agent.Item) error {
		for _, images := range [][]agent.Image{it.Images, it.Files} {
			for i := range images {
				old := images[i].ID
				up, ok := copied[old]
				if !ok {
					original, err := s.store.GetUploadAny(ctx, old)
					if err != nil || original.ThreadID == nil || *original.ThreadID != t.ID {
						return fmt.Errorf("an attachment is unavailable")
					}
					up, err = s.copyRewindUpload(ctx, n, original)
					if err != nil {
						return err
					}
					copied[old] = up
				}
				images[i] = agent.Image{ID: up.ID, Name: up.Name, URL: up.URL}
			}
		}
		return nil
	}
	for _, it := range prefix {
		if err := copyItem(&it); err != nil {
			s.fail(w, err, "copy attachment")
			return
		}
		data, _ := json.Marshal(it)
		if _, err := s.store.UpsertItem(ctx, n.ID, it.Key, it.Kind, data, itemBody(it)); err != nil {
			s.fail(w, err, "copy history")
			return
		}
		if it.Kind == "assistant" {
			n.Preview = oneLine(it.Text, 200)
		}
		if it.Kind == "user" {
			n.LastPrompt = promptLine(it.Text, len(it.Images))
		}
	}
	if err := s.store.UpdateThread(ctx, n); err != nil {
		s.fail(w, err, "fork")
		return
	}
	complete = true
	s.inheritLink(ctx, t, n)
	s.rt.publishThread(n)
	writeJSON(w, http.StatusCreated, n)
}

func codexRewindOptions(t *store.Thread, bin string, env []string, remote *agent.Remote) agent.Options {
	return agent.Options{Bin: bin, Cwd: t.Cwd, Env: env, Remote: remote, ResumeID: t.ProviderSessionID,
		Model: t.Model, Effort: t.Effort, Mode: t.Mode, Speed: t.Speed}
}

// A synthetic exit footer has no provider turn ID. Its preceding send may
// have failed before Codex recorded it; matching must allow either outcome.
func codexUnconfirmedPrompts(items []agent.Item) map[string]bool {
	unconfirmed := map[string]bool{}
	prompt := ""
	for _, it := range items {
		if it.Kind == "user" && !it.Queued && !it.Unsent {
			prompt = it.Key
		}
		if it.Kind == "turn" && it.Turn != nil {
			if prompt != "" && it.Turn.ID == "" && !it.Turn.OK && !it.Turn.Interrupted && it.Turn.Error != "" {
				unconfirmed[prompt] = true
			}
			prompt = ""
		}
	}
	return unconfirmed
}

// A prompt immediately following a completed reply can be cut at that reply's
// stable provider turn ID. Clear the anchor on every sent prompt so a message
// steered into a turn cannot silently discard another prompt in the same turn.
func codexReplyCheckpoint(rows []*store.Item, key string) string {
	checkpoint := ""
	for _, row := range rows {
		if row.Key == key {
			return checkpoint
		}
		var it agent.Item
		if json.Unmarshal(row.Data, &it) != nil {
			return ""
		}
		if it.Kind == "user" && !it.Queued && !it.Unsent {
			checkpoint = ""
		}
		if it.Kind == "turn" && it.Turn != nil && it.Turn.ID != "" {
			checkpoint = it.Turn.ID
		}
	}
	return ""
}

func (s *Server) copyRewindUpload(ctx context.Context, t *store.Thread, original *store.Upload) (uploadOut, error) {
	return s.writeRewindUpload(ctx, t, original.Name, original.Mime, original.ExtractedText, func(out *os.File) error {
		in, err := agent.OpenConfined(original.Path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func (s *Server) writeRewindUpload(ctx context.Context, t *store.Thread, name, mime, extracted string, write func(*os.File) error) (uploadOut, error) {
	id := uuid.NewString()
	dir := filepath.Join(s.cfg.DataDir, "uploads", fmt.Sprint(t.UserID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return uploadOut{}, err
	}
	path := filepath.Join(dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return uploadOut{}, err
	}
	err = write(f)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return uploadOut{}, err
	}
	up := &store.Upload{ID: id, UserID: t.UserID, ThreadID: &t.ID, Name: name, Mime: mime, Size: agent.FileSize(path), Path: path, ExtractedText: extracted}
	if err := s.store.CreateUpload(ctx, up); err != nil {
		os.Remove(path)
		return uploadOut{}, err
	}
	if err := s.store.AttachUpload(ctx, id, t.ID); err != nil {
		os.Remove(path)
		_ = s.store.DeleteUpload(context.WithoutCancel(ctx), t.UserID, id)
		return uploadOut{}, err
	}
	return uploadOut{Upload: up, URL: "/api/v1/uploads/" + id}, nil
}

// rewindRemoteClaude makes the cut copy of a Claude session that lives on
// another machine: the file comes here over ssh, RewindClaude cuts it in a
// scratch folder, and the copy goes back next to the original. "" when the
// copy starts with no earlier context (a fresh session).
func (s *Server) rewindRemoteClaude(ctx context.Context, t *store.Thread, users []agent.Item) (string, error) {
	return s.copyRemoteClaude(ctx, t, users, false)
}

func (s *Server) copyRemoteClaude(ctx context.Context, t *store.Thread, users []agent.Item, wholeFork bool) (string, error) {
	h, err := s.host(ctx, t.HostID)
	if err != nil || h == nil {
		return "", fmt.Errorf("the server this conversation runs on is gone")
	}
	rm := remoteOf(h)
	out, errOut, err := rm.Run(ctx, 30*time.Second, `for f in "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$1".jsonl; do [ -f "$f" ] && { printf '%s\n' "$f"; exit 0; }; done; exit 3`, t.ProviderSessionID)
	src := strings.TrimSpace(out)
	if err != nil || src == "" {
		return "", fmt.Errorf("the session file is not on %s (%s)", h.Name, sshError(errOut, err))
	}
	data, errOut, err := rm.Run(ctx, 2*time.Minute, `cat -- "$1"`, src)
	if err != nil {
		return "", fmt.Errorf("could not read the session on %s: %s", h.Name, sshError(errOut, err))
	}
	scratch, err := os.MkdirTemp(s.cfg.DataDir, "remote-rewind-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	dir := filepath.Join(scratch, "projects", "remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := agent.WriteConfined(filepath.Join(dir, t.ProviderSessionID+".jsonl"), []byte(data), 0o600); err != nil {
		return "", err
	}
	var sid, cut string
	if wholeFork {
		sid, cut, err = agent.ForkClaude(scratch, t.ProviderSessionID)
	} else {
		sid, cut, err = agent.RewindClaude(scratch, t.ProviderSessionID, users)
	}
	if err != nil || sid == "" {
		return sid, err
	}
	copied, err := agent.ReadConfined(cut)
	if err != nil {
		return "", err
	}
	if err := rm.Put(ctx, 2*time.Minute, path.Join(path.Dir(src), sid+".jsonl"), copied); err != nil {
		return "", fmt.Errorf("could not write the copy on %s: %v", h.Name, err)
	}
	return sid, nil
}
