package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAppMentionNote(t *testing.T) {
	s := &Server{}
	mentionCache.Lock()
	mentionCache.m[mentionKey{s, 7}] = mentionEntry{time.Now(), []mentionApp{
		{Slug: "money", Name: "Pocket Money", Tools: []string{"pocket_money_summary", "pocket_money_transactions"}},
		{Slug: "tasks", Name: "Pocket Tasks", Tools: []string{"pocket_tasks_list"}},
	}}
	mentionCache.Unlock()
	ctx := context.Background()

	note := s.appMentionNote(ctx, 7, "how much did I spend? @Money and again @money, mail me@tasks.org")
	if !strings.Contains(note, "Pocket Money (@money): pocket_money_summary, pocket_money_transactions") {
		t.Fatalf("money missing: %q", note)
	}
	if strings.Count(note, "@money") != 1 || strings.Contains(note, "Pocket Tasks") {
		t.Fatalf("want money once and no tasks (email is not a mention): %q", note)
	}
	if got := s.appMentionNote(ctx, 7, "@nosuchapp hello"); got != "" {
		t.Fatalf("unknown app gave a note: %q", got)
	}
	if got := s.appMentionNote(ctx, 7, "no mentions here"); got != "" {
		t.Fatalf("plain text gave a note: %q", got)
	}
}

func TestMentionName(t *testing.T) {
	for slug, want := range map[string]string{"pr-review": "Pocket PR Review", "money": "Pocket Money", "ai": "Pocket AI"} {
		if got := mentionName(slug); got != want {
			t.Errorf("%s: got %q, want %q", slug, got, want)
		}
	}
}
