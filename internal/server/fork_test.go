package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func historyOfFork(t *testing.T, s *Server, fork store.Thread) *store.Upload {
	t.Helper()
	row, err := s.store.GetItem(t.Context(), fork.ID, forkHistoryKey)
	if err != nil {
		t.Fatal(err)
	}
	var it agent.Item
	if json.Unmarshal(row.Data, &it) != nil || it.Kind != "notice" || len(it.Files) != 1 {
		t.Fatal("missing history attachment", string(row.Data))
	}
	up, err := s.store.GetUploadAny(t.Context(), it.Files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(up.Path)
	if err != nil || string(data) != up.ExtractedText {
		t.Fatal("history attachment differs from agent context", err)
	}
	return up
}

func TestForkFreezesHistoryWithoutProviderState(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, state := range []string{"idle", "running"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				s, h, root := testServer(t)
				owner, u := person(t, s, h, "snapshot@example.test", "admin", true)
				ctx := t.Context()
				parent := thread(t, s, u.ID, root)
				parent.Model, parent.Effort, parent.Mode, parent.Speed, parent.Status = "original-model", "high", "full-access", "fast", state
				if err := s.store.UpdateThread(ctx, parent); err != nil {
					t.Fatal(err)
				}
				if _, err := s.store.DB.ExecContext(ctx, `UPDATE threads SET provider=?, no_memory=1 WHERE id=?`, provider, parent.ID); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(root, "file.txt")
				if err := os.WriteFile(file, []byte("independent document"), 0600); err != nil {
					t.Fatal(err)
				}
				up := &store.Upload{ID: "source-file", UserID: u.ID, Name: "file.txt", Mime: "text/plain", Path: file, ExtractedText: "independent document"}
				if err := s.store.CreateUpload(ctx, up); err != nil {
					t.Fatal(err)
				}
				if err := s.store.AttachUpload(ctx, up.ID, parent.ID); err != nil {
					t.Fatal(err)
				}
				items := []agent.Item{
					{Key: "u1", Kind: "user", Text: "how full is the zpool", Files: []agent.Image{{ID: up.ID, Name: up.Name}}},
					{Key: "finished", Kind: "turn", Turn: &agent.TurnInfo{OK: true, CostUSD: 45}},
					{Key: "queued", Kind: "user", Text: "QUEUED_PARENT", Queued: true},
					{Key: "unsent", Kind: "user", Text: "UNSENT_PARENT", Unsent: true},
					{Key: "permission", Kind: "request", Text: "PERMISSION_PARENT", Request: &agent.Request{Status: "pending"}},
					{Key: "thinking", Kind: "thinking", Text: "HIDDEN_THINKING"},
					{Key: "todos", Kind: "todos", Todos: []agent.Todo{{Text: "PENDING_TODO"}}},
					{Key: "active", Kind: "tool", Tool: &agent.Tool{Name: "bash", Title: "RUNNING_JOB", Status: "running"}},
					{Key: "done", Kind: "tool", Tool: &agent.Tool{Name: "bash", Title: "Earlier test", Output: "RAW_TOOL_OUTPUT", Status: "done"}},
				}
				if state == "running" {
					items = append(items, agent.Item{Key: "partial", Kind: "assistant", Text: "PARTIAL_RESPONSE"})
				}
				for _, it := range items {
					data, _ := json.Marshal(it)
					if _, err := s.store.UpsertItem(ctx, parent.ID, it.Key, it.Kind, data, it.Text); err != nil {
						t.Fatal(err)
					}
				}
				// No native log exists and both CLI binaries are deliberately absent.
				var fork store.Thread
				owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", map[string]any{}, 201, &fork)
				if fork.ProviderSessionID != "" || fork.SourceRef != parent.ID || fork.Status != "idle" || fork.Live || fork.External || fork.Pending != 0 || fork.Queued != 0 || s.rt.get(fork.ID) != nil {
					t.Fatalf("copied or started provider state: %+v", fork)
				}
				if fork.Provider != provider || fork.Model != parent.Model || fork.Effort != parent.Effort || fork.Mode != parent.Mode || fork.Speed != parent.Speed || !fork.NoMemory || fork.Cwd != parent.Cwd {
					t.Fatalf("lost settings: %+v", fork)
				}
				o := s.rt.baseOptions(ctx, &fork)
				if o.Fork || o.ResumeID != "" || !strings.Contains(o.SystemPrompt, forkScopePrompt) {
					t.Fatal("fork would resume a provider session")
				}
				rows, _ := s.store.ListItems(ctx, fork.ID)
				if len(rows) != 1 || rows[0].Kind != "notice" || fork.LastPrompt != "" || fork.Preview != "" || fork.Usage.Turns != 0 {
					t.Fatal("copied native-looking history or usage into fresh chat")
				}
				history := historyOfFork(t, s, fork)
				for _, want := range []string{"how full is the zpool", "61% full", "independent document", "Earlier test"} {
					if !strings.Contains(history.ExtractedText, want) {
						t.Fatal("missing reference context", want)
					}
				}
				for _, forbidden := range []string{"QUEUED_PARENT", "UNSENT_PARENT", "PERMISSION_PARENT", "HIDDEN_THINKING", "PENDING_TODO", "RUNNING_JOB", "RAW_TOOL_OUTPUT"} {
					if strings.Contains(history.ExtractedText, forbidden) {
						t.Fatal("runtime state leaked into reference", forbidden)
					}
				}
				if state == "running" && !strings.Contains(history.ExtractedText, "PARTIAL_RESPONSE") {
					t.Fatal("mid-run fork lost the current response")
				}
				parent.Model, parent.Status = "changed-model", "idle"
				_ = s.store.UpdateThread(ctx, parent)
				data, _ := json.Marshal(agent.Item{Key: "future", Kind: "assistant", Text: "FUTURE"})
				_, _ = s.store.UpsertItem(ctx, parent.ID, "future", "assistant", data, "FUTURE")
				current, _ := s.store.GetThread(ctx, u.ID, fork.ID)
				if current.Model != "original-model" || strings.Contains(historyOfFork(t, s, fork).ExtractedText, "FUTURE") {
					t.Fatal("parent mutation leaked into fork")
				}
				owner.want("DELETE", "/api/v1/threads/"+parent.ID, nil, 204, nil)
				if _, err := os.Stat(history.Path); err != nil || historyOfFork(t, s, fork).ExtractedText != history.ExtractedText {
					t.Fatal("deleting parent broke fork reference", err)
				}
				owner.want("POST", "/api/v1/threads/"+fork.ID+"/fork", map[string]any{"after": "not-a-reply"}, 400, nil)
			})
		}
	}
}

func TestForkScopeSurvivesResumeForBothProviders(t *testing.T) {
	s, h, root := testServer(t)
	_, u := person(t, s, h, "fork-scope@example.test", "admin", true)
	for _, provider := range []string{"claude", "codex"} {
		for _, resumed := range []bool{false, true} {
			name := provider + "/fresh"
			sid := ""
			if resumed {
				name, sid = provider+"/resumed", "independent-child-session"
			}
			t.Run(name, func(t *testing.T) {
				child := &store.Thread{ID: name, UserID: u.ID, Provider: provider,
					Source: "fork", SourceRef: "parent-thread", ProviderSessionID: sid,
					Cwd: root, Model: "selected-model", Effort: "high", Mode: "auto", Speed: "fast"}
				if err := s.store.CreateThread(t.Context(), child); err != nil {
					t.Fatal(err)
				}
				// Reload the persisted metadata, as a server restart does. No live
				// in-memory fork marker or first-message flag should be required.
				loaded, err := s.store.GetThread(t.Context(), u.ID, child.ID)
				if err != nil {
					t.Fatal(err)
				}
				o := s.rt.baseOptions(t.Context(), loaded)
				if !strings.Contains(o.SystemPrompt, forkScopePrompt) {
					t.Fatal("persisted fork lost its work scope")
				}
				if o.ResumeID != sid || o.Fork || o.Model != child.Model || o.Effort != child.Effort || o.Mode != child.Mode || o.Speed != child.Speed || o.Cwd != root {
					t.Fatalf("fork scope changed session or user settings: %+v", o)
				}
			})
		}
	}
	parent := &store.Thread{UserID: u.ID, Source: "web", ProviderSessionID: "parent-session"}
	if strings.Contains(s.rt.baseOptions(t.Context(), parent).SystemPrompt, forkScopePrompt) {
		t.Fatal("original conversation was given fork instructions")
	}
	legacy := &store.Thread{UserID: u.ID, SourceRef: "fork:legacy-parent-session"}
	o := s.rt.baseOptions(t.Context(), legacy)
	if !strings.Contains(o.SystemPrompt, forkScopePrompt) || o.ResumeID != "legacy-parent-session" || !o.Fork {
		t.Fatal("legacy fork lost its work scope or native fork options")
	}
}

func TestForkBeforeProviderSessionExists(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "first@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	parent.Status, parent.ProviderSessionID = "running", ""
	_ = s.store.UpdateThread(t.Context(), parent)
	var fork store.Thread
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", map[string]any{}, 201, &fork)
	if fork.ProviderSessionID != "" || !strings.Contains(historyOfFork(t, s, fork).ExtractedText, "zpool") {
		t.Fatal("fork requires provider session state")
	}
	var nested store.Thread
	owner.want("POST", "/api/v1/threads/"+fork.ID+"/fork", map[string]any{}, 201, &nested)
	if nested.SourceRef != fork.ID || !strings.Contains(historyOfFork(t, s, nested).ExtractedText, "61% full") {
		t.Fatal("fork of unopened fork lost history")
	}
}
