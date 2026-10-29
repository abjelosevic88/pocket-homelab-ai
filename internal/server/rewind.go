package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	s.branchBefore(w, r, t, in.Key)
}

// branchBefore prepares provider context before the selected user message.
func (s *Server) branchBefore(w http.ResponseWriter, r *http.Request, t *store.Thread, key string) {
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
		if it.Queued || it.Unsent || it.Kind == "request" {
			continue
		}
		prefix = append(prefix, it)
		if it.Kind == "user" {
			users = append(users, it)
		}
	}
	if target == nil {
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
	if len(users) > 1 {
		if t.ProviderSessionID == "" {
			writeErr(w, 409, "the agent history is unavailable for this message")
			return
		}
		if t.HostID != 0 {
			switch t.Provider {
			case "claude":
				sid, err = s.rewindRemoteClaude(ctx, t, users)
			case "codex":
				var h *store.Host
				h, err = s.host(ctx, t.HostID)
				if err == nil {
					o := codexRewindOptions(t, hostBin(h, "codex"), agent.HomeEnv("codex", t.HostHome), remoteOf(h))
					sid, err = agent.RewindCodex(ctx, o, users, checkpoint)
				}
			default:
				err = fmt.Errorf("this provider does not support rewind")
			}
		} else if err = s.ensureSession(ctx, t, t.ProviderSessionID); err == nil {
			home, env := s.accountHome(ctx, t.Provider, t.AccountID)
			switch t.Provider {
			case "claude":
				sid, sessionPath, err = agent.RewindClaude(home, t.ProviderSessionID, users)
			case "codex":
				o := codexRewindOptions(t, s.cfg.CodexBin, env, nil)
				sid, err = agent.RewindCodex(ctx, o, users, checkpoint)
			default:
				err = fmt.Errorf("this provider does not support rewind")
			}
		}
		if err != nil {
			writeErr(w, 409, "Cannot rewind: "+err.Error())
			return
		}
	}
	s.rewindInPlace(w, r, t, rows, prefix, target, sid, sessionPath)
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
	h, err := s.host(ctx, t.HostID)
	if err != nil || h == nil {
		return "", fmt.Errorf("the server this conversation runs on is gone")
	}
	rm := remoteOf(h)
	out, errOut, err := rm.Run(ctx, 30*time.Second, inLogin("claude", t.HostHome, `for f in "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"/projects/*/"$1".jsonl; do [ -f "$f" ] && { printf '%s\n' "$f"; exit 0; }; done; exit 3`), t.ProviderSessionID)
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
	sid, cut, err := agent.RewindClaude(scratch, t.ProviderSessionID, users)
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
