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

func TestForkFreezesHistoryRuntimeSettingsAndAttachments(t *testing.T) {
	for _, name := range []string{"idle", "working"} {
		t.Run(name, func(t *testing.T) {
			busy := name == "working"
			s, h, root := testServer(t)
			owner, u := person(t, s, h, "snapshot@example.test", "admin", true)
			ctx := t.Context()
			parent := thread(t, s, u.ID, root)
			parent.Model = "original-model"
			parent.Effort = "high"
			parent.Mode = "full-access"
			parent.Speed = "fast"
			if busy {
				parent.Status = "running"
			}
			if err := s.store.UpdateThread(ctx, parent); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.DB.ExecContext(ctx, `UPDATE threads SET no_memory=1 WHERE id=?`, parent.ID); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "file.txt")
			if err := os.WriteFile(file, []byte("independent attachment"), 0600); err != nil {
				t.Fatal(err)
			}
			up := &store.Upload{ID: "source-file", UserID: u.ID, Name: "file.txt", Mime: "text/plain", Path: file}
			if err := s.store.CreateUpload(ctx, up); err != nil {
				t.Fatal(err)
			}
			if err := s.store.AttachUpload(ctx, up.ID, parent.ID); err != nil {
				t.Fatal(err)
			}
			items := []agent.Item{
				{Key: "u1", Kind: "user", Text: "how full is the zpool", Files: []agent.Image{{ID: up.ID, Name: up.Name, URL: "/api/v1/uploads/" + up.ID}}},
				{Key: "finished", Kind: "turn", Turn: &agent.TurnInfo{OK: true}},
				{Key: "queued", Kind: "user", Text: "queued only for parent", Queued: true},
				{Key: "unsent", Kind: "user", Text: "unsent only for parent", Unsent: true},
				{Key: "permission", Kind: "request", Request: &agent.Request{ID: "permission", Status: "pending"}},
			}
			if busy {
				items = append(items, agent.Item{Key: "u2", Kind: "user", Text: "continue working"})
			}
			items = append(items, agent.Item{Key: "active-tool", Kind: "tool", Tool: &agent.Tool{Name: "bash", Status: "running"}})
			for _, it := range items {
				data, _ := json.Marshal(it)
				if _, err := s.store.UpsertItem(ctx, parent.ID, it.Key, it.Kind, data, it.Text); err != nil {
					t.Fatal(err)
				}
			}
			src := filepath.Join(s.cfg.ClaudeHome, "projects", "test", parent.ProviderSessionID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(src), 0700); err != nil {
				t.Fatal(err)
			}
			log := `{"type":"user","uuid":"one","message":{"content":"how full is the zpool"}}
{"type":"assistant","uuid":"a1","parentUuid":"one","message":{"content":[{"type":"text","text":"The zpool is 61% full."}]}}
`
			if busy {
				log += `{"type":"user","uuid":"two","parentUuid":"a1","message":{"content":"continue working"}}` + "\n"
			}
			if err := os.WriteFile(src, []byte(log), 0600); err != nil {
				t.Fatal(err)
			}
			var fork store.Thread
			endpoint := "/api/v1/threads/" + parent.ID + "/fork"
			owner.want("POST", endpoint, map[string]any{}, 201, &fork)
			if fork.ProviderSessionID == "" || fork.ProviderSessionID == parent.ProviderSessionID || fork.SourceRef != parent.ID || fork.Status != "idle" || fork.Live || fork.External || fork.Pending != 0 || fork.Queued != 0 {
				t.Fatalf("shared live state: %+v", fork)
			}
			if fork.Model != parent.Model || fork.Effort != parent.Effort || fork.Mode != parent.Mode || fork.Speed != parent.Speed || !fork.NoMemory || fork.Cwd != parent.Cwd {
				t.Fatalf("lost settings: %+v", fork)
			}
			options := s.rt.baseOptions(ctx, &fork)
			if options.Fork || options.ResumeID != fork.ProviderSessionID {
				t.Fatal("first send would fork parent again")
			}
			if !strings.Contains(options.SystemPrompt, forkScopePrompt) {
				t.Fatal("fork would resume without an independent work scope")
			}
			rows, err := s.store.ListItems(ctx, fork.ID)
			if err != nil || len(rows) != 3 {
				t.Fatal("copied active/queued state", rows, err)
			}
			copiedPath, err := agent.FindClaude(s.cfg.ClaudeHome, fork.ProviderSessionID)
			if err != nil {
				t.Fatal(err)
			}
			frozen, _ := os.ReadFile(copiedPath)
			if busy && strings.Contains(string(frozen), "continue working") {
				t.Fatal("copied in-progress prompt")
			}
			uploads := s.store.UploadsOfThread(ctx, fork.ID)
			if len(uploads) != 1 || uploads[0].ID == up.ID || uploads[0].Path == file {
				t.Fatal("shared attachments")
			}
			parent.Model = "changed-model"
			parent.Status = "idle"
			_ = s.store.UpdateThread(ctx, parent)
			f, _ := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0600)
			f.WriteString(`{"type":"user","message":{"content":"FUTURE"}}` + "\n")
			f.Close()
			newer := agent.Item{Key: "future", Kind: "assistant", Text: "FUTURE"}
			data, _ := json.Marshal(newer)
			_, _ = s.store.UpsertItem(ctx, parent.ID, newer.Key, newer.Kind, data, newer.Text)
			current, _ := s.store.GetThread(ctx, u.ID, fork.ID)
			copied, _ := os.ReadFile(copiedPath)
			after, _ := s.store.ListItems(ctx, fork.ID)
			if string(frozen) != string(copied) || current.Model != "original-model" || len(after) != len(rows) {
				t.Fatal("parent mutation leaked into fork")
			}
			owner.want("DELETE", "/api/v1/threads/"+parent.ID, nil, 204, nil)
			if _, err := os.Stat(uploads[0].Path); err != nil {
				t.Fatal("deleting parent broke fork attachment", err)
			}
			owner.want("POST", "/api/v1/threads/"+fork.ID+"/fork", map[string]any{"after": "not-a-reply"}, 400, nil)
		})
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

func TestForkFirstActiveReplyDoesNotCopyLiveState(t *testing.T) {
	s, h, root := testServer(t)
	owner, u := person(t, s, h, "first@example.test", "admin", true)
	parent := thread(t, s, u.ID, root)
	parent.Status = "running"
	_ = s.store.UpdateThread(t.Context(), parent)
	owner.want("POST", "/api/v1/threads/"+parent.ID+"/fork", map[string]any{}, 409, nil)
	rows, _ := s.store.ListThreads(t.Context(), u.ID, store.ThreadFilter{})
	if len(rows) != 1 {
		t.Fatal("left a partial fork")
	}
}
